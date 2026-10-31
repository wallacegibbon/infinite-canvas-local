package server

// The audio endpoint is a thin adapter: it submits a run and waits for that run. These tests
// pin the two properties the split buys us — the request is answered from the run's stored
// artifact, and the run keeps going when the request does not.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// audioFixture is a music-shaped workflow: one text encoder with a caption and a lyrics
// input, a sampler, and an audio save node.
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

func TestAudioSpeechAnswersFromTheRun(t *testing.T) {
	fake := audioFixture(t)
	gateway := startAudioGateway(t, fake)

	response, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"model":"audio-test","input":"calm piano","instructions":"[Verse]\nline one"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("content type = %q, want audio/mpeg", got)
	}
	if string(body) != "MP3DATA" {
		t.Errorf("body = %q, want the fake ComfyUI audio", body)
	}

	// `input` becomes the caption and `instructions` the lyrics: that split is the whole
	// reason a music graph can be driven by a speech-shaped request.
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

	// The artifact is a run, not a one-off response: it is visible in the task list and on
	// disk, so the canvas can collect it again after a reload.
	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/tasks", nil)
	if status != http.StatusOK {
		t.Fatalf("tasks status = %d", status)
	}
	tasks, _ := payload["tasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want 1: %v", len(tasks), payload)
	}
	run, _ := tasks[0].(map[string]any)
	if run["object"] != "audio" {
		t.Errorf("object = %v, want audio", run["object"])
	}
	if run["status"] != "completed" {
		t.Errorf("status = %v, want completed", run["status"])
	}
	if run["mimeType"] != "audio/mpeg" {
		t.Errorf("mimeType = %v", run["mimeType"])
	}
}

// A run must not die with the request that started it. The client gives up while ComfyUI is
// still rendering; the run finishes and stays retrievable.
func TestAudioRunOutlivesItsRequest(t *testing.T) {
	fake := audioFixture(t)
	fake.historyGate = make(chan struct{})
	gateway := startAudioGateway(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/audio/speech",
		strings.NewReader(`{"model":"audio-test","input":"calm piano"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go func() {
		defer close(done)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// The request is gone; the run is not.
	close(fake.historyGate)

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/tasks", nil)
		tasks, _ := payload["tasks"].([]any)
		if len(tasks) == 1 {
			if run, _ := tasks[0].(map[string]any); run["status"] == "completed" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the abandoned run never completed: %v", payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A rejected workflow must reach the caller with ComfyUI's own code and details, not as a
// flattened "run failed": the run record is the transport, not a lossy one.
func TestAudioSpeechPropagatesComfyRejection(t *testing.T) {
	fake := audioFixture(t)
	fake.failSubmit = true
	gateway := startAudioGateway(t, fake)

	response, err := http.Post(gateway.URL+"/v1/audio/speech", "application/json",
		strings.NewReader(`{"model":"audio-test","input":"calm piano"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)

	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", response.StatusCode, body)
	}
	text := string(body)
	for _, want := range []string{`"code":"node_errors"`, "bad seed"} {
		if !strings.Contains(text, want) {
			t.Errorf("response %s does not contain %s", text, want)
		}
	}
}
