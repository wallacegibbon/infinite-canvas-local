package generate

import (
	"errors"
	"testing"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/workflow"
)

// loraGraph is a UNETLoader feeding a sampler, which is the shape LoRA injection
// has to handle.
func loraGraph() workflow.Graph {
	return workflow.Graph{
		"1": map[string]any{
			"class_type": "UNETLoader",
			"inputs":     map[string]any{"unet_name": "model.safetensors"},
		},
		"8": map[string]any{
			"class_type": "KSampler",
			"inputs":     map[string]any{"model": []any{"1", 0}, "steps": 8.0},
		},
		"9": map[string]any{
			"class_type": "SaveImage",
			"inputs":     map[string]any{"images": []any{"8", 0}},
		},
	}
}

func TestApplyLoraChainSplicesLoader(t *testing.T) {
	graph := loraGraph()
	params := map[string]any{
		"loras": []any{
			map[string]any{"name": "H3/turbo.safetensors", "strength": 0.8},
		},
	}

	result, applied, err := ApplyLoraChain(graph, params, LoraOptions{})
	if err != nil {
		t.Fatalf("ApplyLoraChain: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("applied = %d, want 1", len(applied))
	}

	// The sampler must now read from the LoRA node, not the raw loader.
	sampler, _ := result.NodeAt("8")
	link, ok := sampler.Inputs()["model"].([]any)
	if !ok || len(link) != 2 {
		t.Fatalf("sampler model input = %v, want a link", sampler.Inputs()["model"])
	}
	loraID, _ := link[0].(string)
	loraNode, ok := result.NodeAt(loraID)
	if !ok {
		t.Fatalf("LoRA node %q missing", loraID)
	}
	if loraNode.ClassType() != "LoraLoaderModelOnly" {
		t.Errorf("class = %q", loraNode.ClassType())
	}
	if got := loraNode.Inputs()["lora_name"]; got != "H3/turbo.safetensors" {
		t.Errorf("lora_name = %v", got)
	}
	if got := loraNode.Inputs()["strength_model"]; got != 0.8 {
		t.Errorf("strength = %v", got)
	}

	// The source graph must be untouched.
	original, _ := graph.NodeAt("8")
	originalLink := original.Inputs()["model"].([]any)
	if originalLink[0] != "1" {
		t.Error("the source graph was mutated")
	}
}

func TestApplyLoraChainChainsMultiple(t *testing.T) {
	params := map[string]any{
		"loras": []any{
			map[string]any{"name": "a.safetensors", "strength": 1.0},
			map[string]any{"name": "b.safetensors", "strength": 0.5},
		},
	}
	result, applied, err := ApplyLoraChain(loraGraph(), params, LoraOptions{})
	if err != nil {
		t.Fatalf("ApplyLoraChain: %v", err)
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %d, want 2", len(applied))
	}

	// The second LoRA must consume the first one's output.
	second, _ := result.NodeAt(applied[1].ID)
	link := second.Inputs()["model"].([]any)
	if link[0] != applied[0].ID {
		t.Errorf("second LoRA reads from %v, want %v", link[0], applied[0].ID)
	}
}

func TestApplyLoraChainSkipsDisabled(t *testing.T) {
	params := map[string]any{
		"loras": []any{
			map[string]any{"name": "off.safetensors", "enabled": false},
			map[string]any{"name": "on.safetensors"},
		},
	}
	_, applied, err := ApplyLoraChain(loraGraph(), params, LoraOptions{})
	if err != nil {
		t.Fatalf("ApplyLoraChain: %v", err)
	}
	if len(applied) != 1 || applied[0].Name != "on.safetensors" {
		t.Errorf("applied = %+v, want only on.safetensors", applied)
	}
}

func TestApplyLoraChainRejectsForeignLora(t *testing.T) {
	params := map[string]any{
		"loras": []any{map[string]any{"name": "krea2/anime.safetensors"}},
	}
	_, _, err := ApplyLoraChain(loraGraph(), params, LoraOptions{Filter: `H3|minimax`})
	if err == nil {
		t.Fatal("expected a rejection for a LoRA outside the workflow's family")
	}
	var appErr *httpx.Error
	if !errors.As(err, &appErr) || appErr.Code != "lora_injection_failed" {
		t.Fatalf("got %v, want lora_injection_failed", err)
	}
}

func TestApplyLoraChainRejectsMissingFile(t *testing.T) {
	params := map[string]any{"loras": []any{map[string]any{"name": "nope.safetensors"}}}
	_, _, err := ApplyLoraChain(loraGraph(), params, LoraOptions{
		Available: []string{"present.safetensors"},
	})
	if err == nil {
		t.Fatal("expected a rejection for an uninstalled LoRA")
	}
}

func TestApplyLoraChainNoLorasIsIdentity(t *testing.T) {
	graph := loraGraph()
	result, applied, err := ApplyLoraChain(graph, map[string]any{}, LoraOptions{})
	if err != nil {
		t.Fatalf("ApplyLoraChain: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("applied = %d, want 0", len(applied))
	}
	if len(result) != len(graph) {
		t.Errorf("graph size changed from %d to %d", len(graph), len(result))
	}
}

func TestApplyLoraChainWithoutModelLoaderFails(t *testing.T) {
	graph := workflow.Graph{
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{}},
	}
	params := map[string]any{"loras": []any{map[string]any{"name": "x.safetensors"}}}
	_, _, err := ApplyLoraChain(graph, params, LoraOptions{})
	if err == nil {
		t.Fatal("expected an error when no model loader exists")
	}
}

func TestFindModelSourcePrefersConsumedUnetLoader(t *testing.T) {
	graph := workflow.Graph{
		"1": map[string]any{"class_type": "CheckpointLoaderSimple", "inputs": map[string]any{"ckpt_name": "c.safetensors"}},
		"2": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": "u.safetensors"}},
		"8": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"model": []any{"2", 0}}},
	}
	if got := findModelSource(graph, ""); got != "2" {
		t.Errorf("source = %q, want 2 (the loader that is actually consumed)", got)
	}
	if got := findModelSource(graph, "1"); got != "1" {
		t.Errorf("explicit source was not honored: %q", got)
	}
}

func TestEstimateStepPlan(t *testing.T) {
	graph := workflow.Graph{
		"8": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"steps": 8.0}},
		"9": map[string]any{"class_type": "KSamplerAdvanced", "inputs": map[string]any{
			"steps": 20.0, "start_at_step": 10.0, "end_at_step": 20.0,
		}},
	}
	plan := EstimateStepPlan(graph)
	if plan.Total != 18 {
		t.Errorf("total = %d, want 8 + 10 = 18", plan.Total)
	}
	if plan.Steps["8"] != 8 || plan.Steps["9"] != 10 {
		t.Errorf("per-node steps = %v", plan.Steps)
	}
}

func TestEstimateStepPlanSelfLiftIsAbsolute(t *testing.T) {
	graph := workflow.Graph{
		"10": map[string]any{"class_type": "BasicScheduler", "inputs": map[string]any{"steps": 4.0}},
		"20": map[string]any{"class_type": "SelfLiftH3Sampler", "inputs": map[string]any{
			"sigmas": []any{"10", 0},
		}},
	}
	plan := EstimateStepPlan(graph)
	if !plan.Absolute["20"] {
		t.Error("SelfLift reports a global step index and must be marked absolute")
	}
	if plan.Total != 4 {
		t.Errorf("total = %d, want 4", plan.Total)
	}
}

// SaveAudio / SaveAudioAdvanced report under "audio", a key the collector used to skip;
// without it an audio workflow looks like it produced nothing at all.
func TestCollectOutputFilesIncludesAudio(t *testing.T) {
	history := map[string]any{
		"outputs": map[string]any{
			"35": map[string]any{
				"audio": []any{
					map[string]any{"filename": "canvas_minimax_music3_00001_.mp3", "subfolder": "audio", "type": "output"},
				},
			},
		},
	}
	files := collectOutputFiles(history, false)
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if files[0].Filename != "canvas_minimax_music3_00001_.mp3" || files[0].Type != "output" {
		t.Errorf("collected %+v", files[0])
	}
}

func TestIsAudioExtension(t *testing.T) {
	cases := map[string]bool{
		"song.mp3":  true,
		"song.FLAC": true,
		"song.opus": true,
		"song.wav":  true,
		"song.mp4":  false,
		"song.png":  false,
	}
	for name, want := range cases {
		if got := isAudioExtension(name); got != want {
			t.Errorf("isAudioExtension(%q) = %v, want %v", name, got, want)
		}
	}
}
