package workflow

// Output containers, applied from the workflow's `outputs` declaration.
//
// An encoder node writes a fixed set of containers and validates its quality value against the one
// it was given (SaveAudioAdvanced takes mp3/V0, flac, opus/128k — never opus/V0). That knowledge is
// data on the workflow; this is the single place that applies it, so the run route and the `/v1`
// adapter cannot drift apart.

import (
	"net/http"
	"sort"
	"strings"

	"comfyui-gateway/internal/httpx"
)

// ApplyOutputFormat validates the requested container in `params` and fills the quality value the
// encoder expects for it.
//
// A workflow without an `outputs` declaration passes anything through: the graph decides. A caller
// that supplied its own quality keeps it. An undeclared container is refused rather than silently
// replaced, because the response's Content-Type is what names the saved file.
func (e *Entry) ApplyOutputFormat(params map[string]any) error {
	if e == nil || e.Outputs == nil || len(e.Outputs.Formats) == 0 {
		return nil
	}
	outputs := e.Outputs
	if outputs.FormatParam == "" {
		return nil
	}
	raw, ok := params[outputs.FormatParam]
	if !ok || raw == nil {
		return nil
	}
	format, ok := raw.(string)
	if !ok {
		return httpx.Errorf(http.StatusBadRequest, "invalid_output_format",
			"workflow %s needs a container name, not %T", e.Name, raw)
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		return nil
	}
	params[outputs.FormatParam] = format

	spec, declared := outputs.Formats[format]
	if !declared {
		return httpx.Errorf(http.StatusBadRequest, codeForOutputFormat(e),
			"workflow %s writes %s, not %s", e.Name, strings.Join(outputs.FormatNames(), ", "), format)
	}
	if outputs.QualityParam != "" && spec.Quality != "" {
		if _, exists := params[outputs.QualityParam]; !exists {
			params[outputs.QualityParam] = spec.Quality
		}
	}
	return nil
}

// codeForOutputFormat keeps the audio code stable: it was `unsupported_audio_format` before the
// declaration existed, and the canvas is the client that reads it.
func codeForOutputFormat(entry *Entry) string {
	if entry.Type == "audio" {
		return "unsupported_audio_format"
	}
	return "unsupported_output_format"
}

// OutputFormatNames is a stable list for error messages and schema output.
func (e *Entry) OutputFormatNames() []string {
	if e == nil || e.Outputs == nil {
		return nil
	}
	names := make([]string, 0, len(e.Outputs.Formats))
	for name := range e.Outputs.Formats {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
