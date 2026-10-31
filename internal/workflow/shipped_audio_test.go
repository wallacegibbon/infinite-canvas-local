package workflow

// Guards for the shipped audio workflow: the graph must parse as an API export, be
// inferred as audio (not image, even though it samples with a KSampler), and expose the
// caption/lyrics split the /v1/audio/speech route relies on.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const musicWorkflowID = "audio-minimax-music-3"

func loadShippedEntry(t *testing.T, id string) (*Entry, Graph) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(shippedWorkflowsDir, registryFileName))
	if err != nil {
		t.Skipf("shipped workflows not available: %v", err)
	}
	var document registryFile
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode registry.json: %v", err)
	}
	for _, item := range document.Workflows {
		var decoded rawEntry
		if err := json.Unmarshal(item, &decoded); err != nil {
			t.Fatalf("decode registry entry: %v", err)
		}
		if decoded.ID != id {
			continue
		}
		graphData, err := os.ReadFile(filepath.Join(shippedWorkflowsDir, decoded.File))
		if err != nil {
			t.Fatalf("read %s: %v", decoded.File, err)
		}
		graph, err := decodeImportJSON(graphData)
		if err != nil {
			t.Fatalf("%s is not a submittable API graph: %v", decoded.File, err)
		}
		if got := InferType(graph, ""); got != "audio" {
			t.Errorf("%s inferred as %q without a hint, want audio", decoded.File, got)
		}
		entry := &Entry{
			ID:          decoded.ID,
			Name:        decoded.Name,
			Type:        InferType(graph, decoded.Type),
			File:        decoded.File,
			Workflow:    graph,
			Defaults:    decoded.Defaults,
			Presets:     decoded.Presets,
			RawBindings: decoded.Bindings,
		}
		return entry, graph
	}
	t.Fatalf("%s is not registered", id)
	return nil, nil
}

func TestShippedMusicWorkflowBindsCaptionAndLyrics(t *testing.T) {
	entry, graph := loadShippedEntry(t, musicWorkflowID)

	ctx := BuildContext(BuildContextParams{
		Prompt:  "Upbeat disco funk with brass stabs",
		Count:   1,
		Seconds: 45,
		Params: map[string]any{
			"lyrics":       "[Verse]\nhello",
			"format":       "opus",
			"audioQuality": "128k",
			"seed":         "1234",
		},
	})
	bound, err := ApplyBindings(graph, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}

	encode, ok := bound.NodeAt("37:13")
	if !ok {
		t.Fatal("node 37:13 missing")
	}
	if got := encode.Inputs()["caption"]; got != "Upbeat disco funk with brass stabs" {
		t.Errorf("caption = %v", got)
	}
	if got := encode.Inputs()["lyrics"]; got != "[Verse]\nhello" {
		t.Errorf("lyrics = %v", got)
	}
	if got := encode.Inputs()["max_duration"]; got != 45.0 {
		t.Errorf("max_duration = %v", got)
	}

	seedNode, ok := bound.NodeAt("37:38")
	if !ok {
		t.Fatal("seed node 37:38 missing")
	}
	if got := seedNode.Inputs()["seed"]; got != 1234 {
		t.Errorf("seed = %v", got)
	}

	save, ok := bound.NodeAt("35")
	if !ok {
		t.Fatal("save node 35 missing")
	}
	if got := save.Inputs()["format"]; got != "opus" {
		t.Errorf("format = %v", got)
	}
	// Encoder quality is format-specific (V0 for mp3, 64k-320k for opus), so the request
	// has to move both inputs together.
	if got := save.Inputs()["format.quality"]; got != "128k" {
		t.Errorf("format.quality = %v", got)
	}
}

func TestShippedMusicWorkflowDefaultsLyricsEmpty(t *testing.T) {
	entry, graph := loadShippedEntry(t, musicWorkflowID)

	ctx := BuildContext(BuildContextParams{Prompt: "ambient piano"})
	bound, err := ApplyBindings(graph, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}
	encode, _ := bound.NodeAt("37:13")
	if got, _ := encode.Inputs()["lyrics"].(string); got != "" {
		t.Errorf("lyrics = %q, want an empty default", got)
	}
	if got := encode.Inputs()["max_duration"]; got != 60.0 {
		t.Errorf("max_duration = %v, want the 60s default", got)
	}
	save, _ := bound.NodeAt("35")
	if got := save.Inputs()["format"]; got != "mp3" {
		t.Errorf("format = %v, want the mp3 default", got)
	}
	if got := save.Inputs()["format.quality"]; got != "V0" {
		t.Errorf("format.quality = %v, want the mp3 default V0", got)
	}
}
