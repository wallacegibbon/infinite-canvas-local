package workflow

import (
	"errors"
	"testing"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/util"
)

// sampleGraph is a minimal but realistic ComfyUI API graph: a text encoder, a
// latent source, a sampler and a save node.
func sampleGraph() Graph {
	return Graph{
		"4": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs":     map[string]any{"text": "placeholder", "clip": []any{"1", 1}},
		},
		"7": map[string]any{
			"class_type": "EmptyLatentImage",
			"inputs":     map[string]any{"width": 832.0, "height": 1216.0, "batch_size": 1.0},
		},
		"8": map[string]any{
			"class_type": "KSampler",
			"inputs": map[string]any{
				"seed": 1.0, "steps": 10.0, "cfg": 1.0,
				"model": []any{"1", 0}, "latent_image": []any{"7", 0},
			},
		},
		"9": map[string]any{
			"class_type": "SaveImage",
			"inputs":     map[string]any{"images": []any{"8", 0}, "filename_prefix": "out"},
		},
	}
}

func newEntry(graph Graph, bindings ...any) *Entry {
	builder := newBindingBuilder()
	for index := 0; index+1 < len(bindings); index += 2 {
		key := bindings[index].(string)
		value := bindings[index+1].(map[string]any)
		builder.Set(key, kvFromMap(value))
	}
	return &Entry{
		ID:          "image-test",
		Name:        "image-test",
		Type:        "image",
		Workflow:    graph,
		RawBindings: builder.Result(),
		Defaults:    map[string]any{},
		Presets:     map[string]any{},
		Parameters:  []any{},
	}
}

// kvFromMap builds an ordered object from a plain map, keeping the test terse.
func kvFromMap(values map[string]any) OrderedMap[any] {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	// Sort for determinism; binding key order does not affect semantics.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	out := OrderedMap[any]{Values: map[string]any{}}
	for _, key := range keys {
		out.Keys = append(out.Keys, key)
		out.Values[key] = values[key]
	}
	return out
}

func TestApplyBindingsScalarAndSize(t *testing.T) {
	entry := newEntry(sampleGraph(),
		"prompt", map[string]any{"node": "4", "input": "text"},
		"width", map[string]any{"node": "7", "input": "width", "from": "size.width", "transform": "snap16", "default": 832},
		"height", map[string]any{"node": "7", "input": "height", "from": "size.height", "transform": "snap16", "default": 1216},
		"batch", map[string]any{"node": "7", "input": "batch_size", "from": "count"},
	)

	ctx := BuildContext(BuildContextParams{
		Prompt: "a cat",
		Size:   "1000x1000",
		Count:  3,
	})

	bound, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}

	textNode, _ := bound.NodeAt("4")
	if got := textNode.Inputs()["text"]; got != "a cat" {
		t.Errorf("prompt = %v, want %q", got, "a cat")
	}
	latent, _ := bound.NodeAt("7")
	if got := latent.Inputs()["width"]; got != 1008 {
		t.Errorf("width = %v (%T), want 1008 (snap16 of 1000)", got, got)
	}
	if got := latent.Inputs()["height"]; got != 1008 {
		t.Errorf("height = %v, want 1008", got)
	}
	if got := latent.Inputs()["batch_size"]; got != 3 {
		t.Errorf("batch_size = %v, want 3", got)
	}

	// The source graph must be untouched: bindings never mutate workflows on disk.
	original, _ := entry.Workflow.NodeAt("7")
	if got := original.Inputs()["width"]; got != 832.0 {
		t.Errorf("source graph mutated: width = %v", got)
	}
}

func TestApplyBindingsDefaultAndRandom(t *testing.T) {
	entry := newEntry(sampleGraph(),
		"prompt", map[string]any{"node": "4", "input": "text"},
		"steps", map[string]any{"node": "8", "input": "steps", "from": "quality.steps", "default": 8},
		"seed", map[string]any{"node": "8", "input": "seed", "from": "params.seed", "random": true},
	)

	ctx := BuildContext(BuildContextParams{Prompt: "x"})
	bound, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}
	sampler, _ := bound.NodeAt("8")
	if got := sampler.Inputs()["steps"]; got != 8 {
		t.Errorf("steps = %v, want default 8", got)
	}
	seed, ok := sampler.Inputs()["seed"].(int)
	if !ok {
		t.Fatalf("seed type = %T, want int", sampler.Inputs()["seed"])
	}
	if seed < 1 || seed > 2147483646 {
		t.Errorf("seed = %d, want within [1, 2147483646]", seed)
	}
}

func TestApplyBindingsQualityPreset(t *testing.T) {
	entry := newEntry(sampleGraph(),
		"prompt", map[string]any{"node": "4", "input": "text"},
		"steps", map[string]any{"node": "8", "input": "steps", "from": "quality.steps", "default": 8},
	)
	entry.Presets = map[string]any{
		"quality": map[string]any{
			"high": map[string]any{"steps": 16.0},
		},
	}

	ctx := BuildContext(BuildContextParams{Prompt: "x", Quality: "high", Presets: entry.Presets})
	bound, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}
	sampler, _ := bound.NodeAt("8")
	if got := sampler.Inputs()["steps"]; got != 16.0 {
		t.Errorf("steps = %v, want preset value 16", got)
	}
}

func TestApplyBindingsRequiredMissingIsClientError(t *testing.T) {
	entry := newEntry(sampleGraph(),
		"prompt", map[string]any{"node": "4", "input": "text"},
		"seed", map[string]any{"node": "8", "input": "seed", "from": "params.seed", "required": true},
	)

	_, err := ApplyBindings(entry.Workflow, entry, BuildContext(BuildContextParams{Prompt: "x"}))
	if err == nil {
		t.Fatal("expected an error for a missing required parameter")
	}
	var appErr *httpx.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error type = %T, want *httpx.Error", err)
	}
	if appErr.Code != "missing_parameter" || appErr.Status != 400 {
		t.Errorf("code=%q status=%d, want missing_parameter/400", appErr.Code, appErr.Status)
	}
}

func TestApplyBindingsImageArray(t *testing.T) {
	graph := sampleGraph()
	graph["5"] = map[string]any{
		"class_type": "MiniMaxH3Ref2VA",
		"inputs":     map[string]any{"ref_images": map[string]any{}, "prompt": "p"},
	}
	entry := newEntry(graph,
		"prompt", map[string]any{"node": "5", "input": "prompt"},
		"refs", map[string]any{
			"node": "5", "input": "ref_images", "from": "image",
			"array": true, "inputPrefix": "ref_images.ref_image_", "min": 1, "max": 6,
		},
	)

	ctx := BuildContext(BuildContextParams{
		Prompt: "x",
		UploadedImages: []Media{
			{Name: "a.png"},
			{Name: "b.png", Subfolder: "sub"},
		},
	})

	bound, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}
	node, _ := bound.NodeAt("5")
	inputs := node.Inputs()

	first, ok := inputs["ref_images.ref_image_0"].([]any)
	if !ok {
		t.Fatalf("first ref not wired: %v", inputs["ref_images.ref_image_0"])
	}
	loaderID, _ := first[0].(string)
	loader, ok := bound.NodeAt(loaderID)
	if !ok {
		t.Fatalf("loader node %q missing", loaderID)
	}
	if loader.ClassType() != "LoadImage" {
		t.Errorf("loader class = %q, want LoadImage", loader.ClassType())
	}
	if got := loader.Inputs()["image"]; got != "a.png" {
		t.Errorf("first image = %v, want a.png", got)
	}

	second, ok := inputs["ref_images.ref_image_1"].([]any)
	if !ok {
		t.Fatalf("second ref not wired")
	}
	secondLoader, _ := bound.NodeAt(second[0].(string))
	if got := secondLoader.Inputs()["image"]; got != "sub/b.png" {
		t.Errorf("second image = %v, want sub/b.png (subfolder is part of the reference)", got)
	}
}

func TestApplyBindingsImageArrayMinViolation(t *testing.T) {
	graph := sampleGraph()
	graph["5"] = map[string]any{
		"class_type": "MiniMaxH3Ref2VA",
		"inputs":     map[string]any{"ref_images": map[string]any{}, "prompt": "p"},
	}
	entry := newEntry(graph,
		"refs", map[string]any{
			"node": "5", "input": "ref_images", "from": "image",
			"array": true, "inputPrefix": "ref_images.ref_image_", "min": 2,
		},
	)
	ctx := BuildContext(BuildContextParams{UploadedImages: []Media{{Name: "only.png"}}})
	_, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err == nil {
		t.Fatal("expected an error when fewer than min references are supplied")
	}
	var appErr *httpx.Error
	if !errors.As(err, &appErr) || appErr.Code != "missing_reference_media" {
		t.Fatalf("got %v, want missing_reference_media", err)
	}
}

func TestApplyBindingsFrameModeBoth(t *testing.T) {
	graph := sampleGraph()
	graph["76"] = map[string]any{
		"class_type": "MiniMaxH3",
		"inputs":     map[string]any{"prompt": "p"},
	}
	entry := newEntry(graph,
		"frames", map[string]any{
			"node": "76", "input": "first_frame", "from": "image",
			"frameMode": true, "firstInput": "first_frame", "lastInput": "last_frame", "max": 2,
		},
	)

	ctx := BuildContext(BuildContextParams{
		UploadedImages: []Media{{Name: "first.png"}, {Name: "last.png"}},
	})
	bound, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}
	node, _ := bound.NodeAt("76")
	firstLink, ok := node.Inputs()["first_frame"].([]any)
	if !ok {
		t.Fatal("first_frame not wired")
	}
	lastLink, ok := node.Inputs()["last_frame"].([]any)
	if !ok {
		t.Fatal("last_frame not wired")
	}
	firstLoader, _ := bound.NodeAt(firstLink[0].(string))
	lastLoader, _ := bound.NodeAt(lastLink[0].(string))
	if got := firstLoader.Inputs()["image"]; got != "first.png" {
		t.Errorf("first_frame image = %v", got)
	}
	if got := lastLoader.Inputs()["image"]; got != "last.png" {
		t.Errorf("last_frame image = %v", got)
	}
}

func TestApplyBindingsFrameModeTextSkips(t *testing.T) {
	graph := sampleGraph()
	graph["76"] = map[string]any{
		"class_type": "MiniMaxH3",
		"inputs":     map[string]any{"first_frame": []any{"1", 0}},
	}
	entry := newEntry(graph,
		"frames", map[string]any{
			"node": "76", "input": "first_frame", "from": "image",
			"frameMode": true, "firstInput": "first_frame", "lastInput": "last_frame", "max": 2,
		},
	)
	ctx := BuildContext(BuildContextParams{Params: map[string]any{"frameMode": "text"}})
	bound, err := ApplyBindings(entry.Workflow, entry, ctx)
	if err != nil {
		t.Fatalf("ApplyBindings: %v", err)
	}
	node, _ := bound.NodeAt("76")
	if _, exists := node.Inputs()["first_frame"]; exists {
		t.Error("text mode must clear frame inputs")
	}
	if _, exists := node.Inputs()["last_frame"]; exists {
		t.Error("text mode must clear frame inputs")
	}
}

func TestSecondsToFrames(t *testing.T) {
	// Wan/LTX require 4n+1 frames; H3 requires 17k+5.
	if got := secondsToFrames(5, 16); got != 81 {
		t.Errorf("secondsToFrames(5,16) = %d, want 81", got)
	}
	if got := secondsToFrames17(5, 24); got != 124 {
		t.Errorf("secondsToFrames17(5,24) = %d, want 124", got)
	}
	if got := secondsToFrames17(1, 24); got%17 != 5 {
		t.Errorf("secondsToFrames17(1,24) = %d, want 17k+5", got)
	}
}

func TestApplyTransformNumericCoercions(t *testing.T) {
	cases := []struct {
		transform string
		value     any
		want      any
	}{
		{"snap16", 1000.0, 1008},
		{"snap32", 1000.0, 992},
		{"int", 3.9, 3},
		{"round", 3.5, 4},
		{"invert10", 3.0, 7.0},
	}
	for _, testCase := range cases {
		binding := NewBinding(map[string]any{"transform": testCase.transform})
		if got := applyTransform(testCase.value, binding); got != testCase.want {
			t.Errorf("%s(%v) = %v (%T), want %v", testCase.transform, testCase.value, got, got, testCase.want)
		}
	}
}

func TestParseSizeRejectsGarbage(t *testing.T) {
	for _, input := range []string{"", "1024", "1024x", "axb", "0x100"} {
		if got := util.ParseSize(input); got != nil {
			t.Errorf("ParseSize(%q) = %+v, want nil", input, got)
		}
	}
	if got := util.ParseSize(" 1024 x 768 "); got == nil || got.Width != 1024 || got.Height != 768 {
		t.Errorf("ParseSize did not accept a spaced size: %+v", got)
	}
}

func TestInferBindingsFindsCommonNodes(t *testing.T) {
	graph := map[string]any{
		"4": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs":     map[string]any{"text": "p"},
			"_meta":      map[string]any{"title": "Positive"},
		},
		"5": map[string]any{
			"class_type": "CLIPTextEncode",
			"inputs":     map[string]any{"text": ""},
			"_meta":      map[string]any{"title": "Negative"},
		},
		"6": map[string]any{
			"class_type": "EmptyLatentImage",
			"inputs":     map[string]any{"width": 1024.0, "height": 1024.0, "batch_size": 1.0},
		},
		"7": map[string]any{
			"class_type": "KSampler",
			"inputs":     map[string]any{"seed": 1.0, "steps": 20.0, "cfg": 7.0},
		},
	}
	bindings := InferBindings(Graph(graph))

	prompt, ok := bindings.Get("prompt")
	if !ok {
		t.Fatal("prompt binding not inferred")
	}
	if got := prompt.Values[keyNode]; got != "4" {
		t.Errorf("prompt node = %v, want 4 (the Positive encoder)", got)
	}
	negative, ok := bindings.Get("negative")
	if !ok {
		t.Fatal("negative binding not inferred")
	}
	if got := negative.Values[keyNode]; got != "5" {
		t.Errorf("negative node = %v, want 5", got)
	}
	for _, key := range []string{"width", "height", "batch", "seed", "steps", "cfg"} {
		if _, ok := bindings.Get(key); !ok {
			t.Errorf("binding %q not inferred", key)
		}
	}
}
