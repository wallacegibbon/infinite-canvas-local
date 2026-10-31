package server

// Audio runs, without the speech adapter that used to sit in front of them.
//
// The adapter is gone (`POST /v1/audio/speech` was deleted with the rest of `/v1`); what is worth
// keeping from its tests is the part that was never about the adapter: a music graph takes a caption
// and lyrics as two ordinary inputs, and a ComfyUI rejection reaches the caller with ComfyUI's own
// code instead of a flattened "run failed".

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// audioFixture is a music-shaped workflow: one text encoder with a caption and a lyrics input, a
// sampler, and an audio save node.
func audioFixture(t *testing.T) *fakeComfy {
	t.Helper()
	fake := newFakeComfy(t)
	fake.objectInfo = map[string]any{
		"CLIPTextEncode":    map[string]any{"input": map[string]any{}},
		"KSampler":          map[string]any{"input": map[string]any{}},
		"SaveAudioAdvanced": map[string]any{"input": map[string]any{}},
	}
	fake.outputs = map[string]any{
		"35": map[string]any{
			"audio": []any{
				map[string]any{"filename": "out.mp3", "subfolder": "audio", "type": "output"},
			},
		},
	}
	fake.viewMime = "audio/mpeg"
	fake.viewBody = "MP3DATA"
	return fake
}

func audioGraph() map[string]any {
	return map[string]any{
		"4": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "", "lyrics": ""}},
		"8": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": 1.0, "steps": 30.0}},
		"35": map[string]any{"class_type": "SaveAudioAdvanced", "inputs": map[string]any{
			"audio": []any{"8", 0}, "filename_prefix": "out",
		}},
	}
}

func audioEntry() map[string]any {
	return map[string]any{
		"id":         "audio-test",
		"name":       "audio-test",
		"type":       "audio",
		"file":       "test-audio.json",
		"parameters": []any{},
		"bindings": map[string]any{
			"prompt": map[string]any{"node": "4", "input": "text", "required": true},
			"lyrics": map[string]any{"node": "4", "input": "lyrics", "from": "params.lyrics"},
			"seed":   map[string]any{"node": "8", "input": "seed", "from": "params.seed", "random": true},
		},
	}
}

func startAudioGateway(t *testing.T, fake *fakeComfy) *httptest.Server {
	t.Helper()
	srv := newTestGatewayWith(t, fake, "test-audio.json", audioGraph(), audioEntry())
	gateway := httptest.NewServer(srv.Handler())
	t.Cleanup(gateway.Close)
	return gateway
}

// A music graph needs two texts, and both are ordinary declared inputs.
func TestAudioRunTakesCaptionAndLyrics(t *testing.T) {
	fake := audioFixture(t)
	gateway := startAudioGateway(t, fake)

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "audio-test",
		"inputs":   map[string]any{"prompt": "calm piano", "lyrics": "[Verse]\nline one"},
	})
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %v", status, payload)
	}
	finished := waitForRun(t, gateway, asString(payload["id"]))
	if finished["status"] != "succeeded" {
		t.Fatalf("run failed: %v", finished)
	}

	submitted := fake.submittedGraph()
	if submitted == nil {
		t.Fatal("no graph was submitted")
	}
	encoder, _ := submitted["4"].(map[string]any)
	inputs, _ := encoder["inputs"].(map[string]any)
	if inputs["text"] != "calm piano" {
		t.Errorf("caption = %v", inputs["text"])
	}
	if inputs["lyrics"] != "[Verse]\nline one" {
		t.Errorf("lyrics = %v", inputs["lyrics"])
	}

	outputs := runOutputs(t, finished)
	if len(outputs) != 1 {
		t.Fatalf("outputs = %v, want the audio file", outputs)
	}
	if outputs[0]["kind"] != "audio" || outputs[0]["mime"] != "audio/mpeg" {
		t.Errorf("output = %v", outputs[0])
	}
}

// A rejected workflow reaches the caller with ComfyUI's own code and message, not as a flattened
// "run failed": the run record is the transport, not a lossy one.
func TestAudioRunCarriesComfyUIsRejection(t *testing.T) {
	fake := audioFixture(t)
	fake.failSubmit = true
	gateway := startAudioGateway(t, fake)

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "audio-test",
		"inputs":   map[string]any{"prompt": "calm piano"},
	})
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %v", status, payload)
	}
	finished := waitForRun(t, gateway, asString(payload["id"]))
	if finished["status"] != "failed" {
		t.Fatalf("run = %v, want a failure", finished)
	}
	problem, _ := finished["error"].(map[string]any)
	if problem["code"] != "node_errors" {
		t.Errorf("code = %v, want ComfyUI's own code", problem["code"])
	}
	if message := asString(problem["message"]); message == "" {
		t.Error("a failed run must carry ComfyUI's message")
	}
}
