package server

// A video run takes its media by role and its knobs as typed inputs, exactly like every other run.
// Nothing here is about a graph: the fixture is a first/last-frame workflow, and the handler never
// learns that. The one input the gateway keeps for itself is `interpolation`, which is applied to the
// finished artifact.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
			"seconds": 5, "size": "1024x576", "interpolation": "off",
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

func TestRunVideoValidatesTypedInputs(t *testing.T) {
	_, gateway, _ := videoRunGateway(t)

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-test",
		"inputs":   map[string]any{"prompt": "a cat", "interpolation": "4x"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", status, payload)
	}
	message, _ := payload["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "off, 2x, 3x") {
		t.Errorf("message does not name the accepted multipliers: %s", message)
	}
}

// `interpolation` is the gateway's own step: the run is one request and one artifact, and the
// frame-rate pass happens inside it rather than as a second run over the first one's output.
func TestRunVideoInterpolatesWhenAsked(t *testing.T) {
	fake, gateway, srv := videoRunGateway(t)
	template := filepath.Join(srv.cfg.WorkflowDir, "_internal", "video-rife.json")
	if err := os.MkdirAll(filepath.Dir(template), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFixture(t, template, map[string]any{
		"1": map[string]any{"class_type": "VHS_LoadVideo", "inputs": map[string]any{"video": "input.mp4"}},
		"2": map[string]any{"class_type": "RIFE VFI", "inputs": map[string]any{"multiplier": 2}},
		"3": map[string]any{"class_type": "VHS_VideoCombine", "inputs": map[string]any{"frame_rate": 48}},
	})

	frame := uploadID(t, gateway, "frame.png", "image/png", []byte("PNG"))
	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "video-test",
		"inputs":   map[string]any{"prompt": "a cat", "frameMode": "first", "interpolation": "2x"},
		"media":    map[string]any{"first_frame": frame},
	})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %v", status, payload)
	}

	run := waitForTerminalRun(t, gateway, payload["id"].(string))
	if run["status"] != "succeeded" {
		t.Fatalf("run = %v", run)
	}
	// The generation graph and then the RIFE graph: two submissions, one run.
	if hits := fake.hits.Load(); hits < 2 {
		t.Errorf("ComfyUI received %d submissions, want the generation and the interpolation", hits)
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
