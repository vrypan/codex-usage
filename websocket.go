package main

// The local Codex proxy is a byte tunnel to a WebSocket Unix socket. This
// minimal RFC 6455 client supports text messages, fragmentation, ping/pong,
// close, bounded payloads, and client masking. No compression is negotiated.
import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"
)

const maxMessage = 8 * 1024 * 1024

type wsStream struct {
	r          *bufio.Reader
	w          io.WriteCloser
	mu         sync.Mutex
	pending    []byte
	fragment   []byte
	fragmented bool
}

func upgradeWS(r io.Reader, w io.WriteCloser) (*wsStream, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	if _, err := fmt.Fprintf(w, "GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", key); err != nil {
		return nil, err
	}
	br := bufio.NewReaderSize(r, 32*1024)
	response, err := http.ReadResponse(br, &http.Request{Method: "GET"})
	if err != nil {
		return nil, err
	}
	expected := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	connectionOK := false
	for _, v := range strings.Split(response.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "upgrade") {
			connectionOK = true
		}
	}
	if response.StatusCode != 101 || !strings.EqualFold(response.Header.Get("Upgrade"), "websocket") || !connectionOK || response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(expected[:]) {
		return nil, fmt.Errorf("invalid Codex WebSocket handshake (HTTP %d)", response.StatusCode)
	}
	if response.Header.Get("Sec-WebSocket-Extensions") != "" {
		return nil, errors.New("unexpected WebSocket extension")
	}
	return &wsStream{r: br, w: w}, nil
}
func (s *wsStream) frame(op byte, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var frame bytes.Buffer
	frame.WriteByte(0x80 | op)
	n := len(data)
	switch {
	case n < 126:
		frame.WriteByte(0x80 | byte(n))
	case n <= 65535:
		frame.WriteByte(0x80 | 126)
		_ = binary.Write(&frame, binary.BigEndian, uint16(n))
	default:
		frame.WriteByte(0x80 | 127)
		_ = binary.Write(&frame, binary.BigEndian, uint64(n))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	frame.Write(mask[:])
	for i, v := range data {
		frame.WriteByte(v ^ mask[i%4])
	}
	_, err := io.Copy(s.w, &frame)
	return err
}
func (s *wsStream) Write(data []byte) (int, error) {
	// json.Encoder emits exactly one newline-terminated request per Write.
	if len(data) > maxMessage {
		return 0, errors.New("RPC message too large")
	}
	if err := s.frame(1, bytes.TrimSuffix(data, []byte{'\n'})); err != nil {
		return 0, err
	}
	return len(data), nil
}
func (s *wsStream) Close() error { return s.w.Close() }
func (s *wsStream) Read(dst []byte) (int, error) {
	for len(s.pending) == 0 {
		var header [2]byte
		if _, err := io.ReadFull(s.r, header[:]); err != nil {
			return 0, err
		}
		fin := header[0]&0x80 != 0
		op := header[0] & 15
		if header[0]&0x70 != 0 || header[1]&0x80 != 0 {
			return 0, errors.New("invalid server WebSocket frame")
		}
		n := uint64(header[1] & 127)
		switch n {
		case 126:
			var v uint16
			if err := binary.Read(s.r, binary.BigEndian, &v); err != nil {
				return 0, err
			}
			n = uint64(v)
		case 127:
			if err := binary.Read(s.r, binary.BigEndian, &n); err != nil {
				return 0, err
			}
		}
		if n > maxMessage || uint64(len(s.fragment))+n > maxMessage {
			return 0, errors.New("WebSocket message too large")
		}
		if op >= 8 && (!fin || n > 125) {
			return 0, errors.New("invalid WebSocket control frame")
		}
		payload := make([]byte, int(n))
		if _, err := io.ReadFull(s.r, payload); err != nil {
			return 0, err
		}
		switch op {
		case 8:
			return 0, io.EOF
		case 9:
			if err := s.frame(10, payload); err != nil {
				return 0, err
			}
			continue
		case 10:
			continue
		case 1:
			if s.fragmented {
				return 0, errors.New("interleaved WebSocket fragments")
			}
			s.fragment = payload
			s.fragmented = !fin
		case 0:
			if !s.fragmented {
				return 0, errors.New("unexpected WebSocket continuation")
			}
			s.fragment = append(s.fragment, payload...)
			s.fragmented = !fin
		default:
			return 0, errors.New("expected WebSocket text frame")
		}
		if fin {
			if !utf8.Valid(s.fragment) {
				return 0, errors.New("invalid UTF-8 in WebSocket message")
			}
			s.pending = append(s.fragment, '\n')
			s.fragment = nil
		}
	}
	n := copy(dst, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
