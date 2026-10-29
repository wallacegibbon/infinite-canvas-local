package resource

import (
	"encoding/json"
	"testing"
)

func TestTargetParts(t *testing.T) {
	cases := []struct{ target, dir, file string }{
		{"diffusion_models/model.safetensors", "diffusion_models", "model.safetensors"},
		{"loras/Qwen-功能2.1/lora.safetensors", "loras/Qwen-功能2.1", "lora.safetensors"},
		{`vae\clip\vae.safetensors`, "vae/clip", "vae.safetensors"},
		{"noSlash", "", "noSlash"},
	}
	for _, testCase := range cases {
		dir, file := targetParts(testCase.target)
		if dir != testCase.dir || file != testCase.file {
			t.Errorf("targetParts(%q) = (%q, %q), want (%q, %q)",
				testCase.target, dir, file, testCase.dir, testCase.file)
		}
	}
}

func decodeIndex(t *testing.T, raw string) *Index {
	t.Helper()
	var index Index
	if err := json.Unmarshal([]byte(raw), &index); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	return &index
}

func TestValidateIndexAcceptsRealisticDocument(t *testing.T) {
	index := decodeIndex(t, `{
		"schema": 1,
		"version": "2026.10.29-28",
		"channel": "stable",
		"resources": [
			{"id": "a", "type": "model", "target": "diffusion_models/a.safetensors", "sources": ["https://example.com/a"]},
			{"id": "b", "type": "model", "target": "loras/sub/b.safetensors"}
		],
		"packs": [
			{"id": "core", "resources": ["a"], "optionalResources": ["b"],
			 "workflowDependencies": {"image-a": ["a"]}}
		]
	}`)

	if err := validateIndex(index); err != nil {
		t.Fatalf("validateIndex rejected a valid index: %v", err)
	}
	if index.Channel != "stable" {
		t.Errorf("channel = %q", index.Channel)
	}
}

func TestValidateIndexRejects(t *testing.T) {
	cases := map[string]string{
		"wrong schema": `{"schema": 2, "resources": [], "packs": []}`,
		"missing id":   `{"schema": 1, "resources": [{"target": "vae/x"}], "packs": []}`,
		"duplicate id": `{"schema": 1, "resources": [{"id":"a","target":"vae/x"},{"id":"a","target":"vae/y"}], "packs": []}`,
		"bad target":   `{"schema": 1, "resources": [{"id":"a","target":"noslash"}], "packs": []}`,
		"dangling ref": `{"schema": 1, "resources": [], "packs": [{"id":"p","resources":["ghost"]}]}`,
		"dangling dep": `{"schema": 1, "resources": [], "packs": [{"id":"p","workflowDependencies":{"w":["ghost"]}}]}`,
		"dangling opt": `{"schema": 1, "resources": [], "packs": [{"id":"p","optionalResources":["ghost"]}]}`,
	}
	for name, document := range cases {
		index := decodeIndex(t, document)
		if err := validateIndex(index); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestNormalizeSourcesFoldsBothShapes(t *testing.T) {
	raw := []any{
		"https://example.com/file.bin",
		map[string]any{"url": "https://mirror.example.com/file.bin", "label": "mirror", "verify": "sha256"},
		map[string]any{"kind": "bundled", "path": "resources/nodes/x"},
		map[string]any{"kind": "bundled", "path": ""},
		map[string]any{"label": "no destination"},
		42,
	}

	sources := normalizeSources(raw)

	if len(sources) != 3 {
		t.Fatalf("sources = %d, want 3 usable entries: %+v", len(sources), sources)
	}
	if sources[0]["url"] != "https://example.com/file.bin" {
		t.Errorf("string source was not converted: %+v", sources[0])
	}
	if sources[1]["label"] != "mirror" || sources[1]["verify"] != "sha256" {
		t.Errorf("extra fields were dropped: %+v", sources[1])
	}
	if sources[2]["kind"] != "bundled" || sources[2]["url"] != "" {
		t.Errorf("bundled source = %+v", sources[2])
	}
}

func TestSourcesForPrependsBundledDir(t *testing.T) {
	manager := &Manager{}
	resource := &Resource{
		ID:        "node",
		BundleDir: "resources/nodes/unpacked/Node",
		Sources:   []any{"https://example.com/node.zip"},
	}

	sources := manager.sourcesFor(resource)
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want the bundled dir plus the download", len(sources))
	}
	if sources[0]["kind"] != "bundled-dir" {
		t.Errorf("first source = %+v, want the bundled directory", sources[0])
	}
	if sources[1]["url"] != "https://example.com/node.zip" {
		t.Errorf("second source = %+v", sources[1])
	}
}

func TestNormalizeRelName(t *testing.T) {
	cases := map[string]string{
		`Sub\Dir\File.SafeTensors`: "sub/dir/file.safetensors",
		"./a/b.safetensors":        "a/b.safetensors",
		"  spaced.safetensors  ":   "spaced.safetensors",
		"":                         "",
	}
	for input, want := range cases {
		if got := normalizeRelName(input); got != want {
			t.Errorf("normalizeRelName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestLockViewAlwaysHasInstalled(t *testing.T) {
	view := lockView(map[string]any{"version": 1})
	installed, ok := view["installed"].(map[string]any)
	if !ok || installed == nil {
		t.Fatalf("installed = %v, want an empty map", view["installed"])
	}

	view = lockView(map[string]any{
		"installed": map[string]any{"a": map[string]any{"bytes": 1.0}},
		"updatedAt": "2026-01-01T00:00:00.000Z",
	})
	if view["updatedAt"] != "2026-01-01T00:00:00.000Z" {
		t.Errorf("updatedAt = %v", view["updatedAt"])
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	if _, err := safeJoin(base, "../../etc/passwd"); err != nil {
		t.Fatalf("safeJoin should contain the entry inside base, got %v", err)
	}
	target, err := safeJoin(base, "sub/file.txt")
	if err != nil {
		t.Fatalf("safeJoin: %v", err)
	}
	if target == "" {
		t.Fatal("safeJoin returned an empty path")
	}
}

func TestWeightFilePattern(t *testing.T) {
	for _, name := range []string{"a.safetensors", "a.SFT", "a.ckpt", "a.pt", "a.pth", "a.bin", "a.gguf", "a.onnx", "a.engine"} {
		if !weightFilePattern.MatchString(name) {
			t.Errorf("%q should be recognized as a weight file", name)
		}
	}
	for _, name := range []string{"a.json", "a.txt", "a.png", "a"} {
		if weightFilePattern.MatchString(name) {
			t.Errorf("%q should not be recognized as a weight file", name)
		}
	}
}
