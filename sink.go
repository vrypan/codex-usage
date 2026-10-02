package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

type statusbarLine struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
}

const codexLineName = "codex"

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
	// Check support for line listing before creating anything.
	if _, err := s.lines(ctx); err != nil {
		return nil, err
	}
	fifo, err := runStatusbar(ctx, s.binary, "push", codexLineName, "--fifo", "--status", "normal")
	if err != nil {
		return nil, err
	}
	if filepath.Base(strings.TrimSpace(string(fifo))) != codexLineName {
		return nil, errors.New("statusbar push returned an unexpected FIFO path")
	}
	lines, err := s.lines(ctx)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if line.Name == codexLineName {
			s.id = strconv.FormatUint(line.ID, 10)
			return s, nil
		}
	}
	return nil, errors.New("statusbar did not list the new codex line")
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
func (s *sink) lines(ctx context.Context) ([]statusbarLine, error) {
	data, err := runStatusbar(ctx, s.binary, "list", "--short", "--json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Lines []statusbarLine `json:"lines"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil, errors.New("statusbar list returned invalid JSON; update statusbar to a version supporting list --short --json")
	}
	return list.Lines, nil
}
func (s *sink) alive(ctx context.Context) (bool, error) {
	if s.stdout {
		return true, nil
	}
	lines, err := s.lines(ctx)
	if err != nil {
		return false, err
	}
	for _, l := range lines {
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
