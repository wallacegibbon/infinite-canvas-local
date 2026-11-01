package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
)

// fakeComfy is a minimal ComfyUI stand-in: just enough protocol for the generation
// path, with no GPU and no subprocess.
type fakeComfy struct {
	server   *httptest.Server
	promptID string
	hits     atomic.Int32
	// failSubmit makes /prompt return node_errors, to exercise the error path.
	failSubmit bool

	// Optional overrides. Nil/empty keeps the image defaults above, so existing tests
	// describe only what they care about.
	objectInfo  map[string]any
	outputs     map[string]any
	systemStats map[string]any
	viewMime    string
	viewBody    string

	// historyGate, when set, holds /history until it is closed. It lets a test observe a
	// run that outlives the HTTP request that started it.
	historyGate chan struct{}

	mu         sync.Mutex
	lastPrompt map[string]any
}

// submittedGraph returns the graph the last POST /prompt carried.
func (f *fakeComfy) submittedGraph() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastPrompt
}

func newFakeComfy(t *testing.T) *fakeComfy {
	t.Helper()
	fake := &fakeComfy{promptID: "p1"}

	mux := http.NewServeMux()
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, _ *http.Request) {
		if fake.systemStats != nil {
			writeJSON(w, fake.systemStats)
			return
		}
		writeJSON(w, map[string]any{"system": map[string]any{"os": "test"}})
	})
	mux.HandleFunc("/internal/folder_paths", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/object_info", func(w http.ResponseWriter, _ *http.Request) {
		// No model loaders are declared, so the availability check has nothing to
		// complain about. Only the node classes the test graph uses are needed.
		if fake.objectInfo != nil {
			writeJSON(w, fake.objectInfo)
			return
		}
		writeJSON(w, map[string]any{
			"CLIPTextEncode": map[string]any{"input": map[string]any{}},
			"KSampler":       map[string]any{"input": map[string]any{}},
			"SaveImage":      map[string]any{"input": map[string]any{}},
		})
	})
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		fake.hits.Add(1)
		if body, err := io.ReadAll(r.Body); err == nil {
			var decoded map[string]any
			if json.Unmarshal(body, &decoded) == nil {
				if graph, ok := decoded["prompt"].(map[string]any); ok {
					fake.mu.Lock()
					fake.lastPrompt = graph
					fake.mu.Unlock()
				}
			}
		}
		if fake.failSubmit {
			writeJSON(w, map[string]any{
				"node_errors": map[string]any{
					"8": map[string]any{"errors": []any{map[string]any{"message": "bad seed"}}},
				},
			})
			return
		}
		writeJSON(w, map[string]any{"prompt_id": fake.promptID, "number": 1})
	})
	mux.HandleFunc("/history/"+fake.promptID, func(w http.ResponseWriter, r *http.Request) {
		if fake.historyGate != nil {
			select {
			case <-fake.historyGate:
			case <-r.Context().Done():
				return
			}
		}
		outputs := fake.outputs
		if outputs == nil {
			outputs = map[string]any{
				"9": map[string]any{
					"images": []any{
						map[string]any{"filename": "out.png", "subfolder": "", "type": "output"},
					},
				},
			}
		}
		writeJSON(w, map[string]any{
			fake.promptID: map[string]any{
				"status":  map[string]any{"completed": true, "status_str": "success"},
				"outputs": outputs,
			},
		})
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", defaultStringOf(fake.viewMime, "image/png"))
		_, _ = w.Write([]byte(defaultStringOf(fake.viewBody, "PNGDATA")))
	})
	mux.HandleFunc("/upload/image", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"name": "uploaded.png", "subfolder": "", "type": "input"})
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{})
	})
	mux.HandleFunc("/interrupt", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{})
	})

	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// imageFixture is the default workflow a test gateway serves: enough shape to run an
// image, with a prompt binding and a quality preset the tests can observe.
func imageFixture() (string, map[string]any, map[string]any) {
	graph := map[string]any{
		"4": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "placeholder"}},
		"8": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": 1.0, "steps": 4.0, "cfg": 1.0}},
		"9": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{
			"images": []any{"8", 0}, "filename_prefix": "out",
		}},
	}
	entry := map[string]any{
		"id":         "image-test",
		"name":       "image-test",
		"type":       "image",
		"file":       "test-image.json",
		"parameters": []any{},
		"bindings": map[string]any{
			"prompt": map[string]any{"node": "4", "input": "text"},
			"seed":   map[string]any{"node": "8", "input": "seed", "from": "params.seed", "random": true},
			"steps":  map[string]any{"node": "8", "input": "steps", "from": "quality.steps", "default": 4},
		},
	}
	return "test-image.json", graph, entry
}

// newTestGateway builds a gateway backed by a temporary workflow directory and a
// fake ComfyUI.
func newTestGateway(t *testing.T, fake *fakeComfy) *Server {
	t.Helper()
	file, graph, entry := imageFixture()
	return newTestGatewayWith(t, fake, file, graph, entry)
}

// newTestGatewayWith is newTestGateway with the workflow and registry entry supplied by the
// caller, so a test can exercise a non-image run without copying the setup.
func newTestGatewayWith(t *testing.T, fake *fakeComfy, graphFile string, graph, entry map[string]any) *Server {
	t.Helper()

	root := t.TempDir()
	workflowDir := filepath.Join(root, "workflows")
	dataDir := filepath.Join(root, "data")
	outputDir := filepath.Join(dataDir, "outputs")
	for _, dir := range []string{workflowDir, dataDir, outputDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	writeFixture(t, filepath.Join(workflowDir, graphFile), graph)
	writeFixture(t, filepath.Join(workflowDir, "registry.json"), map[string]any{
		"version":   1,
		"workflows": []any{entry},
	})

	cfg, err := config.Load(
		config.WithComfyURL(fake.server.URL),
		config.WithWorkflowDir(workflowDir),
		config.WithDataDir(dataDir),
		config.WithOutputDir(outputDir),
		config.WithGatewayHost("127.0.0.1"),
		config.WithoutScanOnStart(),
		config.WithPollInterval(10),
		config.WithRequestTimeout(5000),
	)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	logger := logx.NewWithWriter("test", io.Discard)
	srv, err := New(cfg, logger)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	if _, err := srv.Setup().Scan(); err != nil {
		t.Fatalf("setup scan: %v", err)
	}
	return srv
}

func writeFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)

	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return response.StatusCode, decoded
}

func TestHealthReportsWorkflowCount(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/health", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if payload["status"] != "ok" {
		t.Errorf("status field = %v", payload["status"])
	}
	workflows, _ := payload["workflows"].(map[string]any)
	if count, _ := workflows["count"].(float64); count != 1 {
		t.Errorf("workflow count = %v, want 1", workflows["count"])
	}
	comfy, _ := payload["comfy"].(map[string]any)
	if reachable, _ := comfy["reachable"].(bool); !reachable {
		t.Error("expected the fake ComfyUI to be reachable")
	}
}

// The workflow list is where a client learns which model ids exist, so it must carry them: an id
// like `img-zit-int8` is a spelling the gateway invents and resolves, not something a client can
// derive from the workflow name.
func TestWorkflowsReportTheirModelIds(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/workflows", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	rows, _ := payload["workflows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("workflows = %d, want 1: %v", len(rows), payload)
	}
	row, _ := rows[0].(map[string]any)
	if row["id"] != "image-test" {
		t.Errorf("id = %v", row["id"])
	}
	if enabled, _ := row["enabled"].(bool); !enabled {
		t.Error("a workflow with no visibility record must report enabled")
	}
	availability, _ := row["availability"].(map[string]any)
	if available, _ := availability["available"].(bool); !available {
		t.Errorf("expected the workflow to be available: %v", availability)
	}

	models, _ := row["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models = %v, want the workflow's own id", models)
	}
	model, _ := models[0].(map[string]any)
	if model["id"] != "image-test" {
		t.Errorf("model id = %v", model["id"])
	}
	if _, present := model["baseModel"]; !present {
		t.Error("baseModel must always be present (null when unused)")
	}
	if inputs, ok := model["imageInputs"].(float64); !ok || inputs != 0 {
		t.Errorf("imageInputs = %v, want 0 for a text-to-image workflow", model["imageInputs"])
	}
}

// A workflow that declares base models answers to one id per installed file. The graph is the same;
// only the checkpoint the loader reads changes, which is why the ids are data on the workflow row
// rather than separate workflows.
func TestWorkflowsReportOneIdPerBaseModel(t *testing.T) {
	fake := newFakeComfy(t)
	// The scan reads the loader's catalogue from ComfyUI: a base model is selectable because the file
	// is installed, which is the same fact the availability check uses.
	fake.objectInfo = map[string]any{
		"UNETLoader": map[string]any{"input": map[string]any{"required": map[string]any{
			"unet_name": []any{[]any{"z_image_bf16.safetensors", "z_image_int8.safetensors"}},
		}}},
	}
	file, graph, entry := imageFixture()
	entry["baseModels"] = map[string]any{
		"param": "unet",
		"variants": []any{
			map[string]any{"file": "z_image_bf16.safetensors", "id": "zit-bf16", "label": "bf16"},
			map[string]any{"file": "z_image_int8.safetensors", "id": "zit-int8", "label": "int8"},
		},
	}
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	_, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/workflows", nil)
	rows, _ := payload["workflows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("workflows = %v", rows)
	}
	row, _ := rows[0].(map[string]any)
	models, _ := row["models"].([]any)
	seen := map[string]string{}
	for _, raw := range models {
		model, _ := raw.(map[string]any)
		seen[asString(model["id"])] = asString(model["baseModel"])
	}
	// The workflow's own id, plus one id per installed base model. The graph's loader has no default
	// value, so the plain id carries none and each variant names its file.
	if len(seen) != 3 {
		t.Fatalf("ids = %v", seen)
	}
	if value, present := seen["image-test"]; !present || value != "" {
		t.Errorf("ids = %v", seen)
	}
	if seen["image-test__zit-bf16"] != "z_image_bf16.safetensors" {
		t.Errorf("ids = %v", seen)
	}
	if seen["image-test__zit-int8"] != "z_image_int8.safetensors" {
		t.Errorf("ids = %v", seen)
	}
}

func TestWorkflowVisibilityToggles(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, _ := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/workflows/image-test/visibility",
		map[string]any{"enabled": false})
	if status != http.StatusOK {
		t.Fatalf("visibility status = %d", status)
	}

	// The gateway reports the fact; dropping a hidden workflow from a model list is the client's
	// decision. The row stays, with `enabled: false`.
	_, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/workflows", nil)
	rows, _ := payload["workflows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("workflows = %v", rows)
	}
	row, _ := rows[0].(map[string]any)
	if enabled, _ := row["enabled"].(bool); enabled {
		t.Error("a hidden workflow must report enabled: false")
	}
}

func TestTaskNotFoundShape(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/tasks/nope", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	errorBody, _ := payload["error"].(map[string]any)
	if errorBody["code"] != "task_not_found" {
		t.Errorf("code = %v", errorBody["code"])
	}
}
