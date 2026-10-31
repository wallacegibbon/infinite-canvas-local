package server

import (
	"testing"

	"comfyui-gateway/internal/workflow"
)

func audioFormatEntry() *workflow.Entry {
	return &workflow.Entry{
		Name: "audio-test",
		Outputs: &workflow.OutputSpec{
			Kind:         "audio",
			FormatParam:  "format",
			QualityParam: "audioQuality",
			Formats: map[string]workflow.FormatSpec{
				"mp3":  {Quality: "V0"},
				"flac": {},
				"opus": {Quality: "128k"},
			},
		},
	}
}

// Encoder quality is validated against the selected container, so the two bindings have to be
// written together; getting this wrong only shows up as a ComfyUI validation error.
func TestApplyAudioFormatParams(t *testing.T) {
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
		if err := applyAudioFormatParams(audioFormatEntry(), testCase.format, params); err != nil {
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

func TestApplyAudioFormatParamsRejectsUndeclaredContainers(t *testing.T) {
	for _, format := range []string{"wav", "aac", "pcm"} {
		if err := applyAudioFormatParams(audioFormatEntry(), format, map[string]any{}); err == nil {
			t.Errorf("%q was accepted", format)
		}
	}
}

func TestApplyAudioFormatParamsKeepsCallerQuality(t *testing.T) {
	params := map[string]any{"audioQuality": "320k"}
	if err := applyAudioFormatParams(audioFormatEntry(), "mp3", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := params["audioQuality"]; got != "320k" {
		t.Errorf("audioQuality = %v, want the caller's 320k", got)
	}
}

// A workflow that declares no containers keeps the old pass-through behaviour.
func TestApplyAudioFormatParamsUndeclaredWorkflow(t *testing.T) {
	params := map[string]any{}
	if err := applyAudioFormatParams(&workflow.Entry{Name: "tts"}, "wav", params); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := params["format"]; got != "wav" {
		t.Errorf("format = %v, want the requested wav passed through", got)
	}
}
