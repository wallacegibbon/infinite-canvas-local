package comfy

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebSocket frame opcodes we care about (RFC 6455 §5.2).
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xA
)

// WSConn is a minimal client-side WebSocket connection.
//
// The gateway only needs to read JSON text frames from ComfyUI's /ws and answer
// pings, so this implements exactly that: no extensions, no compression, no
// server role. Staying dependency-free here removes a permanent upgrade burden.
type WSConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex
}

// DialWS opens a WebSocket connection to a ws:// or wss:// URL.
func DialWS(ctx context.Context, rawURL string) (*WSConn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	secure := parsed.Scheme == "wss" || parsed.Scheme == "https"
	host := parsed.Host
	dialHost := host
	if !strings.Contains(dialHost, ":") {
		if secure {
			dialHost += ":443"
		} else {
			dialHost += ":80"
		}
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", dialHost)
	if err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	requestPath := parsed.RequestURI()
	if requestPath == "" {
		requestPath = "/"
	}
	handshake := strings.Join([]string{
		"GET " + requestPath + " HTTP/1.1",
		"Host: " + host,
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Key: " + key,
		"Sec-WebSocket-Version: 13",
		"", "",
	}, "\r\n")

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	}
	if _, err := io.WriteString(conn, handshake); err != nil {
		_ = conn.Close()
		return nil, err
	}

	reader := bufio.NewReader(conn)
	if err := readHandshakeResponse(reader); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})

	return &WSConn{conn: conn, br: reader}, nil
}

// readHandshakeResponse consumes the 101 Switching Protocols response.
func readHandshakeResponse(reader *bufio.Reader) error {
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.Contains(statusLine, "101") {
		return fmt.Errorf("websocket handshake rejected: %s", strings.TrimSpace(statusLine))
	}
	tp := textproto.NewReader(reader)
	if _, err := tp.ReadMIMEHeader(); err != nil {
		return err
	}
	return nil
}

// ReadMessage returns the next text or binary message, transparently handling
// fragmentation, ping and close frames.
func (c *WSConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.conn.SetReadDeadline(deadline)
	}
	var assembled []byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			return nil, io.EOF
		case opText, opBinary:
			assembled = payload
		case opContinuation:
			assembled = append(assembled, payload...)
		default:
			continue
		}
		if fin {
			return assembled, nil
		}
	}
}

// readFrame reads one WebSocket frame. Server frames are never masked.
func (c *WSConn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var header [2]byte
	if _, err = io.ReadFull(c.br, header[:]); err != nil {
		return false, 0, nil, err
	}
	fin = header[0]&0x80 != 0
	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7f)

	switch length {
	case 126:
		var extended [2]byte
		if _, err = io.ReadFull(c.br, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err = io.ReadFull(c.br, extended[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(extended[:]))
	}
	// A preview frame is at most a few hundred kilobytes; guard against a
	// malformed length turning into a huge allocation.
	if length < 0 || length > 32<<20 {
		return false, 0, nil, fmt.Errorf("websocket frame too large: %d", length)
	}

	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, maskKey[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// WriteText sends one text message. Client frames must be masked.
func (c *WSConn) WriteText(payload []byte) error {
	return c.writeFrame(opText, payload)
}

func (c *WSConn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var header []byte
	header = append(header, 0x80|opcode)

	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length)|0x80)
	case length <= 0xffff:
		header = append(header, 126|0x80, byte(length>>8), byte(length))
	default:
		header = append(header, 127|0x80)
		var extended [8]byte
		binary.BigEndian.PutUint64(extended[:], uint64(length))
		header = append(header, extended[:]...)
	}

	var maskKey [4]byte
	if _, err := rand.Read(maskKey[:]); err != nil {
		return err
	}
	header = append(header, maskKey[:]...)

	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ maskKey[i%4]
	}

	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

// Close sends a close frame and closes the socket.
func (c *WSConn) Close() error {
	_ = c.writeFrame(opClose, nil)
	return c.conn.Close()
}
