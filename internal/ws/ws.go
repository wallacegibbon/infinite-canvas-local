// Package ws is a WebSocket server, hand-rolled.
//
// This repository has no dependencies and is not going to get one for a transport: `internal/comfy`
// already speaks WebSocket to ComfyUI as a client, with the same frame layout. What differs on this
// side is the direction — a server reads masked frames and writes unmasked ones, and the handshake
// answers instead of asking.
//
// Only what this gateway needs is here: one connection, text messages, the close handshake and ping.
// No extensions, no subprotocols, no compression. A fragmented message is reassembled because a
// browser is allowed to send one; nothing here fragments on the way out.
package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxMessage bounds one reassembled message. The socket carries text — prompts and frames — and a
// prompt may carry reference images as data URLs, so the bound is generous on purpose.
const maxMessage = 8 << 20

// pingInterval is how often the server pokes the client. A canvas tab that has been asleep for hours
// holds a connection whose state nothing on this side can see, and a dead peer is only discovered by
// writing to it.
const pingInterval = 30 * time.Second

// Opcodes, as the framing spec numbers them.
const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa
)

// handshakeGUID is the constant every WebSocket handshake is salted with.
const handshakeGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// ErrClosed is a peer that said goodbye, by closing the connection or by dropping it.
var ErrClosed = errors.New("ws: the connection is closed")

// Conn is one accepted connection.
type Conn struct {
	net   net.Conn
	read  *bufio.Reader
	write sync.Mutex // one frame at a time: two writers interleave halves of two frames

	gone  chan struct{}
	once  sync.Once
	close error
}

// Accept upgrades a request and takes over the connection.
//
// The caller decides whether this request may open a socket at all; this function only checks that it
// is a well-formed WebSocket handshake.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !hasToken(r.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("ws: not a websocket handshake")
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, errors.New("ws: only version 13 is supported")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("ws: the handshake carries no key")
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("ws: this response writer cannot be taken over")
	}
	netConn, buffered, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}

	// The response is written by hand: after a hijack there is no ResponseWriter left to describe it.
	sum := sha1.Sum([]byte(key + handshakeGUID))
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n"
	if _, err := netConn.Write([]byte(response)); err != nil {
		_ = netConn.Close()
		return nil, err
	}

	// The buffered reader is kept, not replaced: a client may send frames in the same packet as the
	// handshake, and those bytes are already in this buffer.
	return &Conn{net: netConn, read: buffered.Reader, gone: make(chan struct{})}, nil
}

// ReadMessage returns the next complete message. Control frames are answered here, so a caller only
// ever sees text or binary.
func (c *Conn) ReadMessage() ([]byte, error) {
	var message []byte
	continued := false
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
			_ = c.writeFrame(opClose, nil)
			c.shutdown(ErrClosed)
			return nil, ErrClosed
		case opContinuation:
			if !continued {
				return nil, errors.New("ws: a continuation frame with nothing to continue")
			}
			message = append(message, payload...)
		case opText, opBinary:
			if continued {
				return nil, errors.New("ws: a new message inside a fragmented one")
			}
			message = payload
		default:
			return nil, fmt.Errorf("ws: unknown opcode %#x", opcode)
		}
		if len(message) > maxMessage {
			return nil, fmt.Errorf("ws: a message over %d bytes", maxMessage)
		}
		continued = !fin
		if fin {
			return message, nil
		}
	}
}

// WriteText sends one message. A write that fails closes the connection: a half-written frame leaves
// the peer unable to find the next one, so there is nothing to recover.
func (c *Conn) WriteText(payload []byte) error {
	return c.writeFrame(opText, payload)
}

// KeepAlive pings until the connection closes, so a peer that vanished is noticed rather than assumed
// to still be listening.
func (c *Conn) KeepAlive() {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.gone:
			return
		case <-ticker.C:
			if err := c.writeFrame(opPing, nil); err != nil {
				c.shutdown(err)
				return
			}
		}
	}
}

// Close closes the connection. Safe to call from any goroutine and more than once.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

func (c *Conn) shutdown(err error) {
	c.once.Do(func() {
		c.close = err
		close(c.gone)
		_ = c.net.Close()
	})
}

func (c *Conn) writeFrame(opcode byte, payload []byte) error {
	c.write.Lock()
	defer c.write.Unlock()

	select {
	case <-c.gone:
		return ErrClosed
	default:
	}

	// A server never masks, and a payload length is spelled three ways depending on how big it is.
	frame := make([]byte, 0, len(payload)+10)
	frame = append(frame, 0x80|opcode)
	switch {
	case len(payload) < 126:
		frame = append(frame, byte(len(payload)))
	case len(payload) <= 0xffff:
		frame = append(frame, 126, 0, 0)
		binary.BigEndian.PutUint16(frame[2:], uint16(len(payload)))
	default:
		frame = append(frame, 127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(frame[2:], uint64(len(payload)))
	}
	if _, err := c.net.Write(append(frame, payload...)); err != nil {
		c.shutdown(err)
		return err
	}
	return nil
}

// readFrame reads one frame. Every frame from a client must be masked; the standard says a server has
// to refuse an unmasked one, and refusing is honest — accepting it would mean the two sides disagree
// about the framing.
func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	var header [2]byte
	if _, err = io.ReadFull(c.read, header[:]); err != nil {
		c.shutdown(err)
		return false, 0, nil, err
	}
	fin = header[0]&0x80 != 0
	opcode = header[0] & 0x0f
	if header[1]&0x80 == 0 {
		err = errors.New("ws: an unmasked frame from a client")
		c.shutdown(err)
		return false, 0, nil, err
	}
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err = io.ReadFull(c.read, extended[:]); err != nil {
			c.shutdown(err)
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint16(extended[:]))
	case 127:
		var extended [8]byte
		if _, err = io.ReadFull(c.read, extended[:]); err != nil {
			c.shutdown(err)
			return false, 0, nil, err
		}
		length = int64(binary.BigEndian.Uint64(extended[:]))
	}
	if length < 0 || length > maxMessage {
		err = fmt.Errorf("ws: a frame of %d bytes", length)
		c.shutdown(err)
		return false, 0, nil, err
	}
	var mask [4]byte
	if _, err = io.ReadFull(c.read, mask[:]); err != nil {
		c.shutdown(err)
		return false, 0, nil, err
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.read, payload); err != nil {
		c.shutdown(err)
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

// hasToken reports whether a comma-separated header names this token. `Connection: keep-alive,
// Upgrade` is legal and common, so a substring test would be wrong in both directions.
func hasToken(header, token string) bool {
	for _, part := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}
