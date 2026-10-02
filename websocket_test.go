package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func serverHandshake(r *bufio.Reader, w io.Writer) error {
	req, err := http.ReadRequest(r)
	if err != nil {
		return err
	}
	hash := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, err = fmt.Fprintf(w, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(hash[:]))
	return err
}
func readClientFrame(r io.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	if h[1]&128 == 0 {
		return 0, nil, fmt.Errorf("client frame not masked")
	}
	n := uint64(h[1] & 127)
	if n == 126 {
		var v uint16
		if err := binary.Read(r, binary.BigEndian, &v); err != nil {
			return 0, nil, err
		}
		n = uint64(v)
	} else if n == 127 {
		if err := binary.Read(r, binary.BigEndian, &n); err != nil {
			return 0, nil, err
		}
	}
	if n > maxMessage {
		return 0, nil, fmt.Errorf("oversized")
	}
	var key [4]byte
	if _, err := io.ReadFull(r, key[:]); err != nil {
		return 0, nil, err
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	for i := range data {
		data[i] ^= key[i%4]
	}
	return h[0] & 15, data, nil
}
func TestWebSocketHandshakeMaskPingAndFragment(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	_ = a.SetDeadline(time.Now().Add(3 * time.Second))
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	errors := make(chan error, 1)
	go func() {
		r := bufio.NewReader(b)
		if err := serverHandshake(r, b); err != nil {
			errors <- err
			return
		}
		op, p, err := readClientFrame(r)
		if err != nil || op != 1 || string(p) != `{"id":1}` {
			errors <- fmt.Errorf("bad client frame %d %s %v", op, p, err)
			return
		}
		_, _ = b.Write([]byte{0x01, 3, 'a', 'b', 'c'})
		_, _ = b.Write([]byte{0x89, 1, '?'})
		op, p, err = readClientFrame(r)
		if err != nil || op != 10 || string(p) != "?" {
			errors <- fmt.Errorf("bad pong")
			return
		}
		_, _ = b.Write([]byte{0x80, 3, 'd', 'e', 'f'})
		errors <- nil
	}()
	s, err := upgradeWS(a, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Write([]byte("{\"id\":1}\n")); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(s).ReadString('\n')
	if err != nil || line != "abcdef\n" {
		t.Fatalf("%q %v", line, err)
	}
	if err = <-errors; err != nil {
		t.Fatal(err)
	}
}
func TestWebSocketRejectsMaskedAndOversizedFrames(t *testing.T) {
	for _, data := range [][]byte{{0x81, 0x80}, {0x81, 127, 0, 0, 0, 0, 1, 0, 0, 0}, {0x80, 0}, {0x83, 0}, {0x09, 0}} {
		s := &wsStream{r: bufio.NewReader(strings.NewReader(string(data)))}
		_, err := s.Read(make([]byte, 64))
		if err == nil {
			t.Fatalf("accepted %v", data)
		}
	}
}
