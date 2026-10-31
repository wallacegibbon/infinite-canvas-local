package workflow

import "testing"

// Audio graphs still sample with a KSampler, so type inference has to look for audio
// classes before the image patterns; otherwise a music export is rejected as an image
// graph that carries an audio- prefix.
func TestInferTypePrefersAudioOverImage(t *testing.T) {
	graph := Graph{
		"1": map[string]any{"class_type": "UNETLoader", "inputs": map[string]any{"unet_name": "music3.safetensors"}},
		"2": map[string]any{"class_type": "MiniMaxMusic3TextEncode", "inputs": map[string]any{"caption": "pop"}},
		"3": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"model": []any{"1", 0}}},
		"4": map[string]any{"class_type": "VAEDecodeAudio", "inputs": map[string]any{"samples": []any{"3", 0}}},
		"5": map[string]any{"class_type": "SaveAudioAdvanced", "inputs": map[string]any{"audio": []any{"4", 0}}},
	}
	if got := InferType(graph, ""); got != "audio" {
		t.Fatalf("InferType = %q, want audio", got)
	}
}

func TestInferTypeStillDetectsImageAndVideo(t *testing.T) {
	image := Graph{
		"1": map[string]any{"class_type": "KSampler", "inputs": map[string]any{}},
		"2": map[string]any{"class_type": "EmptyLatentImage", "inputs": map[string]any{}},
		"3": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{}},
	}
	if got := InferType(image, ""); got != "image" {
		t.Fatalf("image graph inferred as %q", got)
	}

	video := Graph{
		"1": map[string]any{"class_type": "WanImageToVideo", "inputs": map[string]any{}},
		"2": map[string]any{"class_type": "VHS_VideoCombine", "inputs": map[string]any{}},
	}
	if got := InferType(video, ""); got != "video" {
		t.Fatalf("video graph inferred as %q", got)
	}

	// An explicit hint always wins, which is how registry entries stay stable when a
	// graph is ambiguous.
	if got := InferType(image, "audio"); got != "audio" {
		t.Fatalf("explicit hint ignored: %q", got)
	}
}
