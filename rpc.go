package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type rpcClient struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	messages  chan rpcMessage
	done      chan struct{}
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[int64]chan rpcMessage
	nextID    int64
	err       error
	closeOnce sync.Once
}

func startRPC(ctx context.Context, binary, socket string, timeout time.Duration) (*rpcClient, error) {
	args := []string{"app-server", "proxy"}
	if socket != "" {
		args = append(args, "--sock", socket)
	}
	return launchTransport(ctx, true, timeout, binary, args...)
}

func launchRPC(ctx context.Context, binary string, args ...string) (*rpcClient, error) {
	return launchTransport(ctx, false, 0, binary, args...)
}

func launchTransport(ctx context.Context, websocket bool, timeout time.Duration, binary string, args ...string) (*rpcClient, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	// Protocol errors are reported through the RPC stream. Keep CLI diagnostics
	// out of the terminal UI and do not expose authentication details in logs.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		input.Close()
		output.Close()
		return nil, err
	}
	var reader io.Reader = output
	if websocket {
		timer := time.AfterFunc(timeout, func() { _ = cmd.Process.Kill() })
		ws, upgradeErr := upgradeWS(output, input)
		timer.Stop()
		if upgradeErr != nil {
			input.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, fmt.Errorf("Codex WebSocket handshake: %w", upgradeErr)
		}
		input = ws
		reader = ws
	}
	c := &rpcClient{cmd: cmd, input: input, messages: make(chan rpcMessage, 128), done: make(chan struct{}), pending: make(map[int64]chan rpcMessage)}
	go c.read(reader)
	return c, nil
}

func (c *rpcClient) read(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	readErr := error(io.EOF)
	for scanner.Scan() {
		var m rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			readErr = fmt.Errorf("invalid RPC JSON: %w", err)
			break
		}
		if len(m.ID) > 0 && m.Method == "" {
			var id int64
			if json.Unmarshal(m.ID, &id) == nil {
				c.mu.Lock()
				ch := c.pending[id]
				c.mu.Unlock()
				if ch != nil {
					select {
					case ch <- m:
					default:
					}
				}
			}
			continue
		}
		// This client never subscribes to approvals or starts inference. Unexpected
		// server requests must not be treated as approvals.
		if len(m.ID) > 0 && m.Method != "" {
			readErr = fmt.Errorf("unexpected server request %q", m.Method)
			break
		}
		select {
		case c.messages <- m:
		default:
			// Losing an auth change or sparse update could misattribute usage. Reconnect
			// for a full snapshot instead of silently dropping messages.
			readErr = errors.New("notification queue overflow")
			goto finished
		}
	}
	if err := scanner.Err(); err != nil {
		readErr = err
	}
finished:
	c.mu.Lock()
	c.err = readErr
	c.mu.Unlock()
	close(c.done)
}

func (c *rpcClient) send(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return json.NewEncoder(c.input).Encode(v)
}

func (c *rpcClient) call(ctx context.Context, method string, params any, result any) error {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	if err := c.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return err
	}
	select {
	case m := <-ch:
		if m.Error != nil {
			return fmt.Errorf("%s: RPC %d: %s", method, m.Error.Code, m.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return fmt.Errorf("Codex connection closed: %w", c.err)
	}
}

func (c *rpcClient) initialize(ctx context.Context) error {
	err := c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "statusbar-codex", "version": version},
		"capabilities": map[string]any{"experimentalApi": false, "requestAttestation": false},
	}, nil)
	if err != nil {
		return err
	}
	return c.send(map[string]any{"method": "initialized"})
}

func (c *rpcClient) close() {
	c.closeOnce.Do(func() { c.input.Close(); _ = c.cmd.Process.Kill(); _ = c.cmd.Wait() })
}
