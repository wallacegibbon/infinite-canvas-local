package workflow

import (
	"strings"
	"testing"
)

func audioOutputEntry() *Entry {
	return &Entry{
		Name: "audio-test",
		Type: "audio",
		Outputs: &OutputSpec{
			Kind:         "audio",
			FormatParam:  "format",
			QualityParam: "audioQuality",
			Formats: map[string]FormatSpec{
				"mp3":  {Quality: "V0"},
				"flac": {},
				"opus": {Quality: "128k"},
			},
		},
	}
}

// Encoder quality is validated against the selected container, so the two bindings have to be
// written together; a mismatched pair is rejected by ComfyUI, not by us.
func TestApplyOutputFormatFillsQualityPerContainer(t *testing.T) {
	cases := []struct {
		format   string
		wantFmt  any
		wantQual any
	}{
		{format: "", wantFmt: nil, wantQual: nil},
		{format: "mp3", wantFmt: "mp3", wantQual: "V0"},
		{format: "opus", wantFmt: "opus", wantQual: "128k"},
		{format: "flac", wantFmt: "flac", wantQual: nil},
	}
	for _, testCase := range cases {
		params := map[string]any{}
		if testCase.format != "" {
			params["format"] = testCase.format
		}
		if err := audioOutputEntry().ApplyOutputFormat(params); err != nil {
			t.Fatalf("%q: unexpected error: %v", testCase.format, err)
		}
		if got := params["format"]; got != testCase.wantFmt {
			t.Errorf("%q: format = %v, want %v", testCase.format, got, testCase.wantFmt)
		}
		if got := params["audioQuality"]; got != testCase.wantQual {
			t.Errorf("%q: audioQuality = %v, want %v", testCase.format, got, testCase.wantQual)
		}
	}
}

func TestApplyOutputFormatRejectsUndeclaredContainers(t *testing.T) {
	for _, format := range []string{"wav", "aac", "pcm"} {
		err := audioOutputEntry().ApplyOutputFormat(map[string]any{"format": format})
		if err == nil {
			t.Errorf("%q was accepted", format)
			continue
		}
		if code := errorCode(t, err); code != "unsupported_audio_format" {
			t.Errorf("%q: code = %q", format, code)
		}
		if !strings.Contains(err.Error(), "writes flac, mp3, opus") {
			t.Errorf("%q: message = %q", format, err.Error())
		}
	}
}

func TestApplyOutputFormatKeepsCallerQuality(t *testing.T) {
	params := map[string]any{"format": "mp3", "audioQuality": "320k"}
	if err := audioOutputEntry().ApplyOutputFormat(params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := params["audioQuality"]; got != "320k" {
		t.Errorf("audioQuality = %v, want the caller's 320k", got)
	}
}

// A workflow without a declaration passes the request through: the graph decides, as before.
func TestApplyOutputFormatUndeclaredWorkflow(t *testing.T) {
	params := map[string]any{"format": "wav"}
	if err := (&Entry{Name: "tts"}).ApplyOutputFormat(params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := params["format"]; got != "wav" {
		t.Errorf("format = %v, want the requested wav passed through", got)
	}
}

// The shipped music workflow is the real declaration: mp3, flac and opus, and nothing else.
func TestShippedMusicWorkflowDeclaresItsContainers(t *testing.T) {
	entry, _ := loadShippedEntry(t, musicWorkflowID)
	names := entry.OutputFormatNames()
	if strings.Join(names, ",") != "flac,mp3,opus" {
		t.Fatalf("containers = %v", names)
	}
	params := map[string]any{"format": "opus"}
	if err := entry.ApplyOutputFormat(params); err != nil {
		t.Fatalf("opus rejected: %v", err)
	}
	if params["audioQuality"] != "128k" {
		t.Errorf("audioQuality = %v, want opus's 128k", params["audioQuality"])
	}
}
