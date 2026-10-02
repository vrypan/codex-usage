package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const version = "0.1.0"

type options struct {
	codex, statusbar, socket, bucket, thread string
	interval, usageInterval, timeout         time.Duration
	color, bars, stdout, keep, json          bool
	width                                    int
}

func parse(args []string) (string, options, error) {
	o := options{}
	if len(args) == 0 {
		return "", o, errors.New("usage: statusbar-codex <snapshot|watch|version> [options]")
	}
	mode := args[0]
	if mode == "--help" || mode == "-h" {
		fmt.Println("Usage: statusbar-codex <snapshot|watch|version> [options]\n\nSnapshot reads Codex usage; watch updates a statusbar line.\nUse snapshot --help or watch --help for options.")
		return "", o, flag.ErrHelp
	}
	if mode == "version" {
		return mode, o, nil
	}
	if mode != "snapshot" && mode != "watch" {
		return "", o, errors.New("expected snapshot, watch, or version")
	}
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	f.StringVar(&o.codex, "codex", "codex", "Codex executable")
	f.StringVar(&o.statusbar, "statusbar", "statusbar", "statusbar executable")
	f.StringVar(&o.socket, "socket", "", "explicit Codex daemon socket")
	f.StringVar(&o.bucket, "bucket", "", "quota bucket (default: codex, otherwise first sorted bucket)")
	f.StringVar(&o.thread, "thread", "", "thread ID for read-only estimated usage")
	f.DurationVar(&o.interval, "interval", time.Minute, "quota and thread usage refresh interval")
	f.DurationVar(&o.usageInterval, "usage-interval", 5*time.Minute, "account token activity refresh interval")
	f.DurationVar(&o.timeout, "timeout", 10*time.Second, "timeout for each RPC call")
	f.IntVar(&o.width, "width", 0, "explicit output width; 0 leaves clipping to statusbar")
	f.BoolVar(&o.bars, "bars", true, "show thin quota meters")
	f.BoolVar(&o.json, "json", false, "snapshot: print structured JSON")
	f.BoolVar(&o.stdout, "stdout", false, "watch: emit changed lines to stdout instead of statusbar")
	f.BoolVar(&o.keep, "keep", false, "watch: keep the owned line on exit")
	var color string
	f.StringVar(&color, "color", "auto", "auto, always, or never")
	if err := f.Parse(args[1:]); err != nil {
		return "", o, err
	}
	if f.NArg() != 0 {
		return "", o, errors.New("unexpected positional arguments")
	}
	if o.interval < 5*time.Second || o.usageInterval < 5*time.Second || o.timeout <= 0 || o.width < 0 {
		return "", o, errors.New("intervals must be >=5s, timeout positive, and width nonnegative")
	}
	if mode == "watch" && o.json {
		return "", o, errors.New("--json is supported by snapshot only")
	}
	switch color {
	case "always":
		o.color = true
	case "never":
		o.color = false
	case "auto":
		info, err := os.Stdout.Stat()
		o.color = (mode == "watch" && !o.stdout) || (err == nil && info.Mode()&os.ModeCharDevice != 0)
	default:
		return "", o, errors.New("color must be auto, always, or never")
	}
	return mode, o, nil
}
func request(ctx context.Context, c *rpcClient, o options, method string, params any, out any) error {
	t, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	return c.call(t, method, params, out)
}
func connect(ctx context.Context, o options) (*rpcClient, error) {
	c, err := startRPC(ctx, o.codex, o.socket, o.timeout)
	if err != nil {
		return nil, err
	}
	t, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	if err = c.initialize(t); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}
func refresh(ctx context.Context, c *rpcClient, o options, s *state, limitsDue, usageDue bool) bool {
	if limitsDue {
		var l limits
		err := request(ctx, c, o, "account/rateLimits/read", map[string]any{"excludeResetCreditDetails": true}, &l)
		if err != nil {
			s.LimitsError = err.Error()
		} else {
			s.replaceLimits(l, time.Now())
		}
	}
	if usageDue {
		var u usage
		err := request(ctx, c, o, "account/usage/read", map[string]any{}, &u)
		if err != nil {
			s.UsageError = err.Error()
		} else {
			now := time.Now()
			s.Usage = &u
			s.UsageAt = &now
			s.UsageError = ""
		}
	}
	if limitsDue && o.thread != "" {
		var u usage
		err := request(ctx, c, o, "account/usage/read", map[string]any{"threadId": o.thread}, &u)
		if err != nil {
			s.ThreadError = err.Error()
		} else if u.Thread != nil && u.Thread.ID != o.thread {
			s.ThreadError = "server returned usage for a different thread"
		} else {
			now := time.Now()
			s.Thread = &u
			s.ThreadAt = &now
			s.ThreadError = ""
		}
	}
	invalidated := false
	// Drain notifications received during polling before publishing. In particular,
	// auth changes invalidate readings gathered across two account identities.
	for {
		select {
		case m := <-c.messages:
			s.notification(m)
			if m.Method == "account/updated" {
				invalidated = true
			}
		default:
			return invalidated
		}
	}
}
func snapshot(ctx context.Context, o options) error {
	c, err := connect(ctx, o)
	if err != nil {
		return err
	}
	defer c.close()
	s := state{Connected: true}
	if refresh(ctx, c, o, &s, true, true) {
		refresh(ctx, c, o, &s, true, true)
	}
	if o.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err = enc.Encode(struct {
			Version int   `json:"version"`
			State   state `json:"state"`
		}{1, s}); err != nil {
			return err
		}
	} else {
		fmt.Println(render(s, o, time.Now()))
	}
	if s.Limits == nil && s.Usage == nil {
		return fmt.Errorf("usage unavailable: %s; %s", s.LimitsError, s.UsageError)
	}
	return nil
}
func acquireLock(o options) (func(), error) {
	if o.stdout {
		return func() {}, nil
	}
	path := os.Getenv("STATUSBAR_STATE")
	if path == "" {
		return nil, errors.New("watch must run inside statusbar; use --stdout for text output")
	}
	// One named codex line can be owned by only one helper in this session.
	f, err := os.OpenFile(path+".codex.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("a codex line helper is already running in this statusbar session")
	}
	// Do not unlink: another process could already hold the same inode open.
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
func watch(ctx context.Context, o options) error {
	unlock, err := acquireLock(o)
	if err != nil {
		return err
	}
	defer unlock()
	output, err := newSink(ctx, o)
	if err != nil {
		return err
	}
	defer output.close()
	s := state{}
	var c *rpcClient
	defer func() {
		if c != nil {
			c.close()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	retryAt := time.Time{}
	delay := time.Second
	var lastLimits, lastUsage, lastAlive time.Time
	dirty := false
	for {
		now := time.Now()
		if ctx.Err() != nil {
			return nil
		}
		if now.Sub(lastAlive) >= 5*time.Second {
			alive, checkErr := output.alive(ctx)
			if checkErr != nil {
				return checkErr
			}
			if !alive {
				return errors.New("statusbar session or owned line is gone")
			}
			lastAlive = now
		}
		if c == nil && !now.Before(retryAt) {
			c, err = connect(ctx, o)
			if err != nil {
				s.Connected = false
				s.LimitsError = "Codex daemon unavailable"
				retryAt = now.Add(delay)
				delay *= 2
				if delay > 30*time.Second {
					delay = 30 * time.Second
				}
			} else {
				// A disconnected observer may have missed an account switch. Discard old
				// values before accepting data from the reconnected daemon.
				s = state{Connected: true}
				lastLimits = time.Time{}
				lastUsage = time.Time{}
			}
		}
		if c != nil {
			limitsDue := now.Sub(lastLimits) >= o.interval || (dirty && now.Sub(lastLimits) >= 5*time.Second)
			usageDue := now.Sub(lastUsage) >= o.usageInterval
			if limitsDue || usageDue {
				invalidated := refresh(ctx, c, o, &s, limitsDue, usageDue)
				if limitsDue {
					lastLimits = time.Now()
					dirty = false
				}
				if usageDue {
					lastUsage = time.Now()
				}
				if invalidated {
					lastLimits = time.Time{}
					lastUsage = time.Time{}
				}
				if s.LimitsAt != nil {
					delay = time.Second
				}
			}
		}
		if err = output.write(ctx, render(s, o, time.Now())); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		var notifications <-chan rpcMessage
		var disconnected <-chan struct{}
		if c != nil {
			notifications = c.messages
			disconnected = c.done
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case m := <-notifications:
			if s.notification(m) {
				dirty = true
				if m.Method == "account/updated" {
					lastUsage = time.Time{}
					lastLimits = time.Time{}
				}
			}
		case <-disconnected:
			c.close()
			c = nil
			s.Connected = false
			s.LimitsError = "Codex disconnected"
			retryAt = time.Now().Add(delay)
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}
func main() {
	mode, o, err := parse(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil && mode == "version" {
		fmt.Println("statusbar-codex " + version)
		return
	}
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if mode == "snapshot" {
			err = snapshot(ctx, o)
		} else {
			err = watch(ctx, o)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "statusbar-codex:", strings.ReplaceAll(err.Error(), "\n", " "))
		os.Exit(1)
	}
}
