package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type sink struct {
	binary   string
	id       string
	previous string
	stdout   bool
	keep     bool
}

func runStatusbar(ctx context.Context, binary string, args ...string) ([]byte, error) {
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := exec.CommandContext(call, binary, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("statusbar %s failed: %w", args[0], err)
	}
	return result, nil
}
func newSink(ctx context.Context, o options) (*sink, error) {
	s := &sink{binary: o.statusbar, stdout: o.stdout, keep: o.keep}
	if s.stdout {
		return s, nil
	}
	if os.Getenv("STATUSBAR_STATE") == "" || os.Getenv("STATUSBAR_SESSION_ID") == "" {
		return nil, errors.New("watch must run inside statusbar; use --stdout for text output")
	}
	// An unnamed line gives us its immutable numeric ID immediately. Avoid name
	// collisions and never adopt or delete a line belonging to another process.
	fifo, err := runStatusbar(ctx, s.binary, "push", "--fifo", "--status", "normal")
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(string(fifo))
	slash := strings.LastIndex(path, "/")
	s.id = path[slash+1:]
	if _, err = strconv.ParseUint(s.id, 10, 64); err != nil {
		return nil, errors.New("statusbar push returned an invalid line ID")
	}
	return s, nil
}
func (s *sink) write(ctx context.Context, text string) error {
	if text == s.previous {
		return nil
	}
	if s.stdout {
		if _, err := fmt.Fprintln(os.Stdout, text); err != nil {
			return err
		}
	} else {
		if _, err := runStatusbar(ctx, s.binary, "set", s.id, text, "--status", "normal"); err != nil {
			return err
		}
	}
	s.previous = text
	return nil
}
func (s *sink) alive(ctx context.Context) (bool, error) {
	if s.stdout {
		return true, nil
	}
	data, err := runStatusbar(ctx, s.binary, "list", "--short", "--json")
	if err != nil {
		return false, err
	}
	var list struct {
		Lines []struct {
			ID uint64 `json:"id"`
		} `json:"lines"`
	}
	if json.Unmarshal(data, &list) != nil {
		return false, errors.New("statusbar list returned invalid JSON; update statusbar to a version supporting list --short --json")
	}
	for _, l := range list.Lines {
		if strconv.FormatUint(l.ID, 10) == s.id {
			return true, nil
		}
	}
	return false, nil
}
func (s *sink) close() {
	if !s.stdout && !s.keep && s.id != "" {
		_, _ = runStatusbar(context.Background(), s.binary, "pop", s.id)
	}
}
