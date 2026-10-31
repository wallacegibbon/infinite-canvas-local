package server

// A video run takes its media by role and its knobs as typed inputs, exactly like every other run.
// Nothing here is about a graph: the fixture is a first/last-frame workflow, and the handler never
// learns that. The one input the gateway keeps for itself is `interpolation`, which is applied to the
// finished artifact.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// videoRunGateway serves the frame workflow over a fake ComfyUI that produces an mp4.
func videoRunGateway(t *testing.T) (*fakeComfy, *httptest.Server, *Server) {
	t.Helper()
	fake := newFakeComfy(t)
	fake.outputs = map[string]any{
		"90": map[string]any{"gifs": []any{map[string]any{"filename": "out.mp4", "subfolder": "", "type": "output"}}},
	}
	fake.viewMime = "video/mp4"
	fake.viewBody = "MP4DATA"

	file, graph, entry := videoFixture()
	srv := newTestGatewayWith(t, fake, file, graph, entry)
	gateway := httptest.NewServer(srv.Handler())
	t.Cleanup(gateway.Close)
	return fake, gateway, srv
}

// waitForTerminalRun polls one run until the queue has finished with it.
func waitForTerminalRun(t *testing.T, gateway *httptest.Server, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/runs/"+id, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /api/runs/%s: %d %v", id, status, payload)
		}
		switch payload["status"] {
		case "succeeded", "failed", "cancelled":
			return payload
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never finished: %v", id, payload)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunVideoTakesFramesByRole(t *testing.T) {
	fake, gateway, _ := videoRunGateway(t)
	last := uploadID(t, gateway, "last.png", "image/png", []byte("PNG"))

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-test",
		"inputs": map[string]any{
			"prompt": "a cat", "mode": "frames", "frameMode": "last",
			"seconds": 5, "size": "1024x576",
		},
		"media": map[string]any{"last_frame": last},
	})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %v", status, payload)
	}

	// One frame in `last` mode fills the last_frame socket, not the first one — the rule is the
	// workflow's, the role in the request is the caller's.
	run := waitForTerminalRun(t, gateway, payload["id"].(string))
	if run["status"] != "succeeded" {
		t.Fatalf("run = %v", run)
	}

	node, _ := fake.submittedGraph()["76"].(map[string]any)
	inputs, _ := node["inputs"].(map[string]any)
	if _, wired := inputs["last_frame"]; !wired {
		t.Errorf("last_frame was not wired: %v", inputs)
	}
	if _, wired := inputs["first_frame"]; wired {
		t.Errorf("first_frame was wired for a last-frame request: %v", inputs)
	}

	outputs, _ := run["outputs"].([]any)
	if len(outputs) != 1 {
		t.Fatalf("outputs = %v", outputs)
	}
	output, _ := outputs[0].(map[string]any)
	if output["mime"] != "video/mp4" {
		t.Errorf("output mime = %v", output["mime"])
	}
	if output["url"] != "/api/runs/"+payload["id"].(string)+"/outputs/0" {
		t.Errorf("output url = %v", output["url"])
	}
}

func TestRunVideoRejectsBadMediaMix(t *testing.T) {
	_, gateway, _ := videoRunGateway(t)
	frame := uploadID(t, gateway, "frame.png", "image/png", []byte("PNG"))

	// `both` is two frames, and the workflow says so.
	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-test",
		"inputs":   map[string]any{"prompt": "a cat", "frameMode": "both"},
		"media":    map[string]any{"first_frame": frame},
	})
	if status != http.StatusBadRequest || payload["error"].(map[string]any)["code"] != "missing_reference_image" {
		t.Fatalf("both frames with one image: %d %v", status, payload)
	}

	status, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-test",
		"inputs":   map[string]any{"prompt": "a cat", "frameMode": "middle"},
		"media":    map[string]any{"first_frame": frame},
	})
	if status != http.StatusBadRequest || payload["error"].(map[string]any)["code"] != "invalid_frame_mode" {
		t.Fatalf("unknown frame mode: %d %v", status, payload)
	}
}

func TestRunVideoRejectsUndeclaredInputs(t *testing.T) {
	_, gateway, _ := videoRunGateway(t)

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-test",
		"inputs":   map[string]any{"prompt": "a cat", "interpolation": "2x"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", status, payload)
	}
	message, _ := payload["error"].(map[string]any)["message"].(string)
	// Frame-rate raising is a workflow of its own now; the key must be refused rather than ignored,
	// so a client still sending it learns immediately.
	if !strings.Contains(message, `unknown input "interpolation"`) {
		t.Errorf("message = %s", message)
	}
}

// Raising a clip's frame rate is a workflow like any other: one video in, one video out, no
// special case in the engine. The output frame rate is derived from the multiplier by a binding.
func TestRunRifeInterpolationWorkflow(t *testing.T) {
	fake := newFakeComfy(t)
	fake.outputs = map[string]any{
		"3": map[string]any{"gifs": []any{map[string]any{"filename": "rife.mp4", "subfolder": "rife", "type": "output"}}},
	}
	fake.viewMime = "video/mp4"
	fake.viewBody = "RIFE"
	graph := map[string]any{
		"1": map[string]any{"class_type": "VHS_LoadVideo", "inputs": map[string]any{"video": "input.mp4"}},
		"2": map[string]any{"class_type": "RIFE VFI", "inputs": map[string]any{"frames": []any{"1", 0}, "multiplier": 2.0}},
		"3": map[string]any{"class_type": "VHS_VideoCombine", "inputs": map[string]any{"images": []any{"2", 0}, "frame_rate": 48.0}},
	}
	entry := map[string]any{
		"id":         "video-rife-test",
		"name":       "video-rife-test",
		"type":       "video",
		"file":       "test-rife.json",
		"parameters": []any{map[string]any{"key": "multiplier", "type": "number", "min": 2, "max": 3, "default": 2}},
		"bindings": map[string]any{
			"video":      map[string]any{"node": "1", "input": "video", "from": "video", "videoIndex": 0, "required": true},
			"multiplier": map[string]any{"node": "2", "input": "multiplier", "from": "params.multiplier", "transform": "int", "default": 2},
			"frame_rate": map[string]any{"node": "3", "input": "frame_rate", "from": "params.multiplier", "transform": "multiply", "factor": 24, "default": 48},
		},
	}
	srv := newTestGatewayWith(t, fake, "test-rife.json", graph, entry)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-rife-test",
		"inputs":   map[string]any{"multiplier": 3},
		"media":    map[string]any{"video": uploadID(t, gateway, "clip.mp4", "video/mp4", []byte("ftyp"))},
	})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %v", status, payload)
	}
	run := waitForTerminalRun(t, gateway, payload["id"].(string))
	if run["status"] != "succeeded" {
		t.Fatalf("run = %v", run)
	}

	submitted := fake.submittedGraph()
	loader, _ := submitted["1"].(map[string]any)
	if inputs, _ := loader["inputs"].(map[string]any); inputs["video"] != "uploaded.png" {
		t.Errorf("the clip was not wired into the loader: %v", inputs)
	}
	vfi, _ := submitted["2"].(map[string]any)
	if inputs, _ := vfi["inputs"].(map[string]any); inputs["multiplier"] != 3.0 {
		t.Errorf("multiplier = %v, want 3", inputs["multiplier"])
	}
	combine, _ := submitted["3"].(map[string]any)
	if inputs, _ := combine["inputs"].(map[string]any); inputs["frame_rate"] != 72.0 {
		t.Errorf("frame_rate = %v, want 72 (24 x 3)", inputs["frame_rate"])
	}
}

// `frameMode` belongs to video workflows; on an image run it is a typo, not a silent no-op.
func TestRunRejectsFrameModeOnImageWorkflow(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat", "frameMode": "first"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", status, payload)
	}
	message, _ := payload["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "unknown input") {
		t.Errorf("message = %s", message)
	}
}
