package resource

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"comfyui-gateway/internal/config"
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
		"schema": 2,
		"version": "2026.10.29-28",
		"channel": "stable",
		"resources": [
			{"id": "a", "type": "model", "target": "diffusion_models/a.safetensors", "sources": ["https://example.com/a"]},
			{"id": "b", "type": "model", "target": "loras/sub/b.safetensors"}
		],
		"packs": [
			{"id": "core", "workflows": ["image-a"], "alternates": ["b"]}
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
		"old schema":        `{"schema": 1, "resources": [], "packs": []}`,
		"missing id":        `{"schema": 2, "resources": [{"target": "vae/x"}], "packs": []}`,
		"duplicate id":      `{"schema": 2, "resources": [{"id":"a","target":"vae/x"},{"id":"a","target":"vae/y"}], "packs": []}`,
		"bad target":        `{"schema": 2, "resources": [{"id":"a","target":"noslash"}], "packs": []}`,
		"dangling ref":      `{"schema": 2, "resources": [], "packs": [{"id":"p","alternates":["ghost"]}]}`,
		"pack without id":   `{"schema": 2, "resources": [], "packs": [{"alternates":[]}]}`,
		"duplicate pack id": `{"schema": 2, "resources": [], "packs": [{"id":"p"},{"id":"p"}]}`,
	}
	for name, document := range cases {
		index := decodeIndex(t, document)
		if err := validateIndex(index); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

// Sources are plain URLs; anything else in the field is ignored rather than half-honoured.
func TestSourceURLsAcceptsOnlyRealDownloads(t *testing.T) {
	resource := &Resource{Sources: []any{
		"https://example.com/file.bin",
		map[string]any{"url": "https://mirror.example.com/file.bin", "label": "mirror"},
		map[string]any{"url": ""},
		map[string]any{"label": "no destination"},
		42,
	}}

	urls := sourceURLs(resource)
	if len(urls) != 2 || urls[0] != "https://example.com/file.bin" || urls[1] != "https://mirror.example.com/file.bin" {
		t.Fatalf("sourceURLs = %v, want the two usable URLs", urls)
	}
}

// A resource staged under payload/<target> is "bundled" by convention: no field,
// and the panel shows no download source because there is nothing to download.
func TestPayloadPathMirrorsTheTarget(t *testing.T) {
	manager := &Manager{cfg: &config.Config{RootDir: "/gw"}}

	if got := manager.payloadPath("custom_nodes/comfyui-kjnodes"); got != filepath.Join("/gw", "payload", "custom_nodes", "comfyui-kjnodes") {
		t.Errorf("payloadPath = %q", got)
	}
	if got := manager.payloadPath("vae_approx/taeh3.safetensors"); got != filepath.Join("/gw", "payload", "vae_approx", "taeh3.safetensors") {
		t.Errorf("payloadPath = %q", got)
	}

	resource := &Resource{Target: "custom_nodes/comfyui-kjnodes", Sources: []any{}}
	if sources := manager.sourcesFor(resource); len(sources) != 0 {
		t.Errorf("sourcesFor = %v, want none for a payload resource", sources)
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

// A node pack is installed when ComfyUI loaded its classes, not when a directory of that name
// exists: a zip extraction renames the folder, and a missing python dependency leaves a folder with
// nothing in it.
func TestMissingNodeClasses(t *testing.T) {
	loaded := map[string]bool{"VHS_VideoCombine": true, "RIFE VFI": true}
	provides := []string{"VHS_VideoCombine", "RIFE VFI"}
	if missing := missingNodeClasses(provides, func(name string) bool { return loaded[name] }); len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}

	// The directory is there, the import failed, so no class is loaded.
	missing := missingNodeClasses([]string{"QwenMultiangleCameraNode", "QwenImage21Cache"}, func(name string) bool { return loaded[name] })
	if len(missing) != 2 || missing[0] != "QwenMultiangleCameraNode" {
		t.Errorf("missing = %v, want both classes named", missing)
	}
}
