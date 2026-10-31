package agent

import (
	"bufio"
	"strings"
	"testing"
)

// The wire is a contract with another program, so these tests describe the bytes rather than the
// values: a frame is one line, the value begins at byte 2, and anything else ends the stream.

func TestWriteFrameIsOneLineWithoutSeparator(t *testing.T) {
	var out strings.Builder
	if err := WriteFrame(&out, TagUserX, `{"type":"text","text":"hi"}`); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, want := out.String(), "UX{\"type\":\"text\",\"text\":\"hi\"}\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestWriteFrameAllowsTheControlFrames(t *testing.T) {
	var out strings.Builder
	if err := WriteFrame(&out, TagUserEnd, ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, want := out.String(), "UE\n"; got != want {
		t.Errorf("frame = %q, want %q", got, want)
	}
}

func TestWriteFrameRejectsWhatTheAgentWouldReject(t *testing.T) {
	for name, tc := range map[string]struct{ tag, value string }{
		"tag is one letter":       {"U", `{}`},
		"tag is three letters":    {"UXX", `{}`},
		"tag is not letters":      {"U1", `{}`},
		"value is a bare string":  {TagUserX, `"hi"`},
		"value has a raw newline": {TagUserX, "{\"text\":\"a\nb\"}"},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			if err := WriteFrame(&out, tc.tag, tc.value); err == nil {
				t.Fatalf("expected an error, wrote %q", out.String())
			}
			if out.Len() != 0 {
				t.Errorf("a rejected frame must not be written, got %q", out.String())
			}
		})
	}
}

func TestReadFrameSplitsTagAndValue(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("SM{\"type\":\"session\",\"data\":{\"state\":\"ready\"}}\nUE\nAx{\"type\":\"text\",\"text\":\"Hi\"}\n"))
	want := []Frame{
		{Tag: TagSystemMsg, Value: `{"type":"session","data":{"state":"ready"}}`},
		{Tag: TagUserEnd, Value: ""},
		{Tag: TagAssistantXDelta, Value: `{"type":"text","text":"Hi"}`},
	}
	var got []Frame
	for {
		frame, err := ReadFrame(r)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if frame == nil {
			break
		}
		got = append(got, *frame)
	}
	if len(got) != len(want) {
		t.Fatalf("frames = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("frame %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestReadFrameAcceptsATrailingCR(t *testing.T) {
	// A writer on a platform that translates line endings produces CRLF; the CR is the
	// terminator's, never the value's.
	frame, err := ReadFrame(bufio.NewReader(strings.NewReader("UE\r\n")))
	if err != nil || frame == nil || frame.Tag != TagUserEnd || frame.Value != "" {
		t.Fatalf("frame = %v, err = %v", frame, err)
	}
}

func TestReadFrameReportsTheEndOfTheStream(t *testing.T) {
	if frame, err := ReadFrame(bufio.NewReader(strings.NewReader(""))); frame != nil || err != nil {
		t.Errorf("clean end = %v, %v; want nil, nil", frame, err)
	}
	// A line without its terminator is not a frame: a truncated message must not be read as a
	// complete one.
	if _, err := ReadFrame(bufio.NewReader(strings.NewReader(`SM{"type":"session"}`))); err == nil {
		t.Error("expected an error for an unterminated frame")
	}
}

func TestReadFrameEndsTheStreamOnAMalformedLine(t *testing.T) {
	for name, line := range map[string]string{
		"blank line":        "\n",
		"one letter":        "S\n",
		"no tag":            `{"type":"x"}` + "\n",
		"space after tag":   "UX {}\n",
		"value is a string": "UX\"hi\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if frame, err := ReadFrame(bufio.NewReader(strings.NewReader(line))); err == nil {
				t.Fatalf("expected an error, got frame %v", frame)
			}
		})
	}
}

// The transcript is worth keeping; the credentials a frame carries are not.
func TestRedactKeysKeepsTheShape(t *testing.T) {
	frame := `{"models":[{"id":1,"api_key":"sk-secret","name":"A"}],"api_key":"no-key-by-default"}`
	got := redactKeys(frame)
	if strings.Contains(got, "sk-secret") || strings.Contains(got, "no-key-by-default") {
		t.Errorf("a key survived redaction: %s", got)
	}
	if !strings.Contains(got, `"api_key":"***"`) {
		t.Errorf("the field was lost: %s", got)
	}
}
