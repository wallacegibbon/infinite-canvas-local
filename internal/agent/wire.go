// Package agent drives AlayaCore's rawio frame protocol: one agent process per prompt request.
//
// The wire is one frame per line — `[2-byte tag][JSON value]` — as specified in AlayaCore's
// adapter guide. This file is the codec only; spawn.go owns the process, run.go the request.
//
// The same codec exists in AlayaCore and in the AlayaFace backend, which is where this one comes
// from. Three copies drift, so message_version is checked at startup (see run.go) rather than
// assumed: a frame shape that moved would otherwise surface as a nonsense parse at the worst moment.
package agent

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Frames the gateway writes to the agent's stdin.
const (
	// TagUserX is user content: a JSON content item (`{"type":"text","text":...}` or an image).
	// It travels in both directions — the agent echoes it back on stdout with a history id.
	TagUserX = "UX"
	// TagUserEnd flushes the staged content as one user message.
	TagUserEnd = "UE"
	// TagCmdInput is the command plane: `{"command_id":...,"name":...,"args":...}`. `cancel` is the
	// one the gateway sends.
	TagCmdInput = "CI"
)

// Frames the gateway reads from the agent's stdout.
const (
	TagAssistantX      = "AX" // complete assistant content
	TagAssistantXDelta = "Ax" // assistant content delta (a JSON content item: text or reasoning)
	TagAssistantF      = "AF" // tool call
	TagAssistantFDelta = "Af" // tool argument delta
	TagUserF           = "UF" // tool result
	TagUserFDelta      = "Uf" // tool result preview (display only, never authoritative)
	TagCmdOutput       = "CO" // command result
	TagSystemMsg       = "SM" // system message: {"type":...,"data":{...}}
)

// MaxFrameSize bounds one frame, tag and terminator included. It matches AlayaCore's own bound: a
// reader that accepted more would let a corrupt or hostile stream grow its buffer without limit.
const MaxFrameSize = 64 << 20

// Frame is one parsed wire frame: a two-letter tag and its JSON value (empty for control frames).
type Frame struct {
	Tag   string
	Value string
}

// WriteFrame writes one frame. A value that is neither empty nor a JSON object is rejected rather
// than sent: the agent would read it as a malformed frame and end the stream.
func WriteFrame(w io.Writer, tag, value string) error {
	if len(tag) != 2 || !isLetter(tag[0]) || !isLetter(tag[1]) {
		return fmt.Errorf("wire: tag must be two ASCII letters, got %q", tag)
	}
	if value != "" && value[0] != '{' {
		return errors.New("wire: a frame's value must be a JSON object, or empty for a control frame")
	}
	// A raw newline inside the value would end the frame early. encoding/json escapes them, so
	// this only catches a value that was assembled by hand.
	if i := strings.IndexAny(value, "\n\r"); i >= 0 {
		return fmt.Errorf("wire: %s value contains a raw line break at offset %d", tag, i)
	}
	if len(tag)+len(value)+1 > MaxFrameSize {
		return fmt.Errorf("wire: frame of %d bytes exceeds the maximum %d", len(tag)+len(value)+1, MaxFrameSize)
	}
	_, err := w.Write([]byte(tag + value + "\n"))
	return err
}

// ReadFrame reads one frame. A clean end of stream between frames is (nil, nil); every other error
// is terminal — a malformed frame means the writer lost its place, so no later line can be trusted.
//
// The reader must be buffered and reused for the whole stream: a frame's boundary is a line, and
// reading one must not consume the bytes of the next.
func ReadFrame(r *bufio.Reader) (*Frame, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
			if len(line) > MaxFrameSize {
				return nil, fmt.Errorf("wire: frame exceeds the maximum of %d bytes", MaxFrameSize)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(line) == 0 {
					return nil, nil
				}
				return nil, errors.New("wire: stream ended with an unterminated frame")
			}
			return nil, err
		}
		break
	}
	return decodeLine(line)
}

// decodeLine validates one line and splits it into tag and value. The value begins at byte 2: there
// is no separator, so `UX {...}` is a malformed frame rather than a frame with a leading space.
func decodeLine(line []byte) (*Frame, error) {
	// A trailing LF, and a CR before it, are the terminator: a value can never contain either.
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	switch {
	case len(line) == 0:
		return nil, errors.New("wire: blank line; every line must be one frame")
	case len(line) < 2:
		return nil, fmt.Errorf("wire: %q is shorter than a two-letter tag", line)
	case !isLetter(line[0]) || !isLetter(line[1]):
		return nil, fmt.Errorf("wire: %q does not start with a two-letter tag", line)
	}
	tag := string(line[:2])
	if !utf8.Valid(line[2:]) {
		return nil, fmt.Errorf("wire: %s value is not valid UTF-8", tag)
	}
	value := string(line[2:])
	if value != "" && value[0] != '{' {
		return nil, fmt.Errorf("wire: %s value must be a JSON object, or empty for a control frame", tag)
	}
	return &Frame{Tag: tag, Value: value}, nil
}

// isLetter reports whether b is an ASCII letter, which is what a tag is made of: that is how a
// reader tells a frame from a fragment of one.
func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
