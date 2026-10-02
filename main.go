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

type options struct {
	codex, socket, bucket, thread    string
	interval, usageInterval, timeout time.Duration
	color, bars, json                bool
	width                            int
}

func parse(args []string) (string, options, error) {
	o := options{}
	if len(args) == 0 {
		return "", o, errors.New("usage: codex-usage <snapshot|watch|version> [options]")
	}
	mode := args[0]
	if mode == "--help" || mode == "-h" {
		fmt.Println("Usage: codex-usage <snapshot|watch|version> [options]\n\nSnapshot reads Codex usage; watch prints updates.\nUse snapshot --help or watch --help for options.")
		return "", o, flag.ErrHelp
	}
	if mode == "version" || mode == "--version" || mode == "-v" {
		return "version", o, nil
	}
	if mode != "snapshot" && mode != "watch" {
		return "", o, errors.New("expected snapshot, watch, or version")
	}
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	f.StringVar(&o.codex, "codex", "codex", "Codex executable")
	f.StringVar(&o.socket, "socket", "", "explicit Codex daemon socket")
	f.StringVar(&o.bucket, "bucket", "", "quota bucket (default: codex, otherwise first sorted bucket)")
	f.StringVar(&o.thread, "thread", "", "thread ID for read-only estimated usage")
	f.DurationVar(&o.interval, "interval", time.Minute, "quota and thread usage refresh interval")
	f.DurationVar(&o.usageInterval, "usage-interval", 5*time.Minute, "account token activity refresh interval")
	f.DurationVar(&o.timeout, "timeout", 10*time.Second, "timeout for each RPC call")
	f.IntVar(&o.width, "width", 0, "maximum output width; 0 disables clipping")
	f.BoolVar(&o.bars, "bars", true, "show thin quota meters")
	f.BoolVar(&o.json, "json", false, "snapshot: print structured JSON")
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
		o.color = err == nil && info.Mode()&os.ModeCharDevice != 0
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
func watch(ctx context.Context, o options) error {
	s := state{}
	var c *rpcClient
	var err error
	defer func() {
		if c != nil {
			c.close()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	retryAt := time.Time{}
	delay := time.Second
	var lastLimits, lastUsage time.Time
	dirty := false
	previous := ""
	for {
		now := time.Now()
		if ctx.Err() != nil {
			return nil
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
		text := render(s, o, time.Now())
		if text != previous {
			if _, err = fmt.Fprintln(os.Stdout, text); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			previous = text
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
	resolveVersion()
	mode, o, err := parse(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil && mode == "version" {
		fmt.Println(versionString())
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
		fmt.Fprintln(os.Stderr, "codex-usage:", strings.ReplaceAll(err.Error(), "\n", " "))
		os.Exit(1)
	}
}
