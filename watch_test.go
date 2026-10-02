package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeServerJSON(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	h := []byte{0x81}
	if len(data) < 126 {
		h = append(h, byte(len(data)))
	} else {
		h = append(h, 126, 0, 0)
		binary.BigEndian.PutUint16(h[2:], uint16(len(data)))
	}
	if _, err = w.Write(h); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}
func TestUsageServerChild(t *testing.T) {
	marker := os.Getenv("STATUSBAR_CODEX_SERVER_MARKER")
	if marker == "" {
		return
	}
	first := false
	if f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600); err == nil {
		first = true
		f.Close()
	}
	r := bufio.NewReader(os.Stdin)
	if serverHandshake(r, os.Stdout) != nil {
		os.Exit(2)
	}
	for {
		_, data, err := readClientFrame(r)
		if err != nil {
			os.Exit(0)
		}
		var m struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Method == "initialized" {
			continue
		}
		var result any = map[string]any{}
		switch m.Method {
		case "initialize":
		case "account/rateLimits/read":
			percent := float64(32)
			if !first {
				percent = 70
			}
			result = limits{AccountID: ptr("fixture"), Legacy: &bucket{ID: ptr("codex"), Primary: &window{UsedPercent: percent, Duration: ptr(int64(300))}, Credits: &credits{Balance: ptr("7")}}}
		case "account/usage/read":
			if _, ok := m.Params["threadId"]; ok {
				result = usage{Thread: &threadUsage{ID: "t", Groups: []tokenGroup{{Total: ptr(int64(123))}}}}
			} else {
				u := usage{}
				u.Summary.Lifetime = ptr(int64(1000))
				result = u
			}
		default:
			os.Exit(3)
		}
		if writeServerJSON(os.Stdout, map[string]any{"id": m.ID, "result": result}) != nil {
			os.Exit(4)
		}
		if first && m.Method == "account/usage/read" {
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}
	}
}
func TestWatchReconnectsAndCancels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STATUSBAR_CODEX_SERVER_MARKER", filepath.Join(dir, "connected"))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "codex")
	content := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "' -test.run='^TestUsageServerChild$'\n"
	if err = os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old; r.Close(); w.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	output := make(chan string, 1)
	go func() {
		var text strings.Builder
		scan := bufio.NewScanner(r)
		for scan.Scan() {
			line := scan.Text()
			text.WriteString(line + "\n")
			if strings.Contains(line, "70% used") {
				cancel()
			}
		}
		output <- text.String()
	}()
	o := options{codex: script, stdout: true, interval: time.Minute, usageInterval: time.Minute, timeout: time.Second}
	err = watch(ctx, o)
	w.Close()
	got := <-output
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"32% used", "disconnected", "70% used", "tokens lifetime 1.0k"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q: %s", want, got)
		}
	}
}
func TestSinkOwnershipAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STATUSBAR_STATE", filepath.Join(dir, "state"))
	t.Setenv("STATUSBAR_SESSION_ID", "test")
	t.Setenv("STATUSBAR_CODEX_TEST_LOG", filepath.Join(dir, "calls"))
	t.Setenv("STATUSBAR_CODEX_TEST_GONE", filepath.Join(dir, "gone"))
	t.Setenv("STATUSBAR_CODEX_TEST_CREATED", filepath.Join(dir, "created"))
	t.Setenv("STATUSBAR_CODEX_TEST_REPLACED", filepath.Join(dir, "replaced"))
	script := filepath.Join(dir, "statusbar")
	text := `#!/bin/sh
printf '%s\n' "$*" >> "$STATUSBAR_CODEX_TEST_LOG"
case "$1" in
push) touch "$STATUSBAR_CODEX_TEST_CREATED"; printf '/tmp/codex\n';;
list) if [ -f "$STATUSBAR_CODEX_TEST_GONE" ] || [ ! -f "$STATUSBAR_CODEX_TEST_CREATED" ]; then printf '{"lines":[]}'; elif [ -f "$STATUSBAR_CODEX_TEST_REPLACED" ]; then printf '{"lines":[{"id":28,"name":"codex"}]}'; else printf '{"lines":[{"id":27,"name":"codex"}]}'; fi;;
esac
`
	if err := os.WriteFile(script, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	o := options{statusbar: script}
	unlock, err := acquireLock(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = acquireLock(o); err == nil {
		t.Fatal("duplicate helper accepted")
	}
	otherThread := o
	otherThread.thread = "another-thread"
	if _, err = acquireLock(otherThread); err == nil {
		t.Fatal("a second helper could claim the same codex line")
	}
	unlock()
	unlock, err = acquireLock(o)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	s, err := newSink(ctx, o)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.write(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	_ = s.write(ctx, "hello")
	if alive, err := s.alive(ctx); !alive || err != nil {
		t.Fatal("line should be present")
	}
	_ = os.WriteFile(filepath.Join(dir, "replaced"), nil, 0600)
	if alive, err := s.alive(ctx); alive || err != nil {
		t.Fatal("a new line using the same name must not be adopted")
	}
	_ = os.WriteFile(filepath.Join(dir, "gone"), nil, 0600)
	if alive, err := s.alive(ctx); alive || err != nil {
		t.Fatal("removed line treated as present")
	}
	s.close()
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if !strings.Contains(string(calls), "push codex --fifo --status normal") || strings.Count(string(calls), "set 27 hello --status normal") != 1 || !strings.Contains(string(calls), "pop 27") {
		t.Fatal(string(calls))
	}
}
func TestFlags(t *testing.T) {
	for _, args := range [][]string{{"watch", "--interval", "0s"}, {"watch", "--json"}, {"snapshot", "--color", "purple"}, {"snapshot", "oops"}} {
		if _, _, err := parse(args); err == nil {
			t.Fatal(fmt.Sprint(args))
		}
	}
}
