package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Re-execute the test binary as a fake Codex proxy. Exercises real pipes,
// interleaved notifications, errors, timeouts, and process teardown.
func TestRPCChild(t *testing.T) {
	if os.Getenv("STATUSBAR_CODEX_TEST_CHILD") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var msg struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &msg)
		switch msg.Method {
		case "initialized":
			continue
		case "hang":
			continue
		case "fail":
			fmt.Printf(`{"id":%d,"error":{"code":-32601,"message":"unsupported"}}`+"\n", msg.ID)
		case "quit":
			os.Exit(0)
		default:
			fmt.Println(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"limitId":"codex"}}}`)
			fmt.Printf(`{"id":%d,"result":{"ok":true}}`+"\n", msg.ID)
		}
	}
	os.Exit(0)
}
func TestRPCRoundTripAndTimeout(t *testing.T) {
	t.Setenv("STATUSBAR_CODEX_TEST_CHILD", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := launchRPC(ctx, os.Args[0], "-test.run=^TestRPCChild$")
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	if err = c.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err = c.call(ctx, "read", nil, &result); err != nil || !result.OK {
		t.Fatalf("%v %+v", err, result)
	}
	select {
	case m := <-c.messages:
		if m.Method != "account/rateLimits/updated" {
			t.Fatal(m)
		}
	default:
		t.Fatal("missing notification")
	}
	if err = c.call(ctx, "fail", nil, nil); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal(err)
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if err = c.call(short, "hang", nil, nil); err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if err = c.call(ctx, "quit", nil, nil); err == nil {
		t.Fatal("EOF should fail pending request")
	}
}
