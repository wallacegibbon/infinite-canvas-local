package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	objectInfo map[string]any
	outputs    map[string]any
	viewMime   string
	viewBody   string

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

func TestModelsListsRegisteredWorkflow(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/v1/models", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("models = %d, want 1: %v", len(data), payload)
	}
	model, _ := data[0].(map[string]any)
	if model["id"] != "image-test" {
		t.Errorf("model id = %v", model["id"])
	}
	metadata, _ := model["metadata"].(map[string]any)
	if metadata["workflowId"] != "image-test" {
		t.Errorf("workflowId = %v", metadata["workflowId"])
	}
	if _, present := metadata["baseModel"]; !present {
		t.Error("baseModel must always be present (null when unused)")
	}
	availability, _ := metadata["availability"].(map[string]any)
	if available, _ := availability["available"].(bool); !available {
		t.Errorf("expected the workflow to be available: %v", availability)
	}
}

func TestImageGenerationReturnsBase64(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/v1/images/generations",
		map[string]any{"model": "image-test", "prompt": "a cat"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, payload)
	}

	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %d entries, want 1: %v", len(data), payload)
	}
	entry, _ := data[0].(map[string]any)
	encoded, _ := entry["b64_json"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("b64_json is not valid base64: %v", err)
	}
	if string(decoded) != "PNGDATA" {
		t.Errorf("image bytes = %q, want the fake ComfyUI payload", decoded)
	}
	if fake.hits.Load() != 1 {
		t.Errorf("submit calls = %d, want 1", fake.hits.Load())
	}
}

func TestImageGenerationURLFormatServesFile(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/v1/images/generations",
		map[string]any{"model": "image-test", "prompt": "a cat", "response_format": "url"})
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	data, _ := payload["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("data = %v", data)
	}
	entry, _ := data[0].(map[string]any)
	fileURL, _ := entry["url"].(string)
	if !strings.HasPrefix(fileURL, "http") || !strings.Contains(fileURL, "/files/") {
		t.Fatalf("url = %q, want an absolute /files/ URL", fileURL)
	}

	// The URL is absolute against the configured public base URL; fetch the path
	// from this test server instead.
	parsed, err := url.Parse(fileURL)
	if err != nil {
		t.Fatalf("parse url %q: %v", fileURL, err)
	}
	response, err := gateway.Client().Get(gateway.URL + parsed.Path)
	if err != nil {
		t.Fatalf("fetch %s: %v", fileURL, err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || string(body) != "PNGDATA" {
		t.Errorf("file response = %d %q", response.StatusCode, body)
	}
}

func TestImageGenerationUnknownModelIs404(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/v1/images/generations",
		map[string]any{"model": "does-not-exist", "prompt": "x"})
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %v", status, payload)
	}
	errorBody, _ := payload["error"].(map[string]any)
	if errorBody["code"] != "model_not_found" {
		t.Errorf("code = %v, want model_not_found", errorBody["code"])
	}
}

func TestImageGenerationMissingModelIs400(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/v1/images/generations",
		map[string]any{"prompt": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %v", status, payload)
	}
}

func TestSubmitNodeErrorsBecome400(t *testing.T) {
	fake := newFakeComfy(t)
	fake.failSubmit = true
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/v1/images/generations",
		map[string]any{"model": "image-test", "prompt": "x"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (ComfyUI rejected the graph): %v", status, payload)
	}
	errorBody, _ := payload["error"].(map[string]any)
	message, _ := errorBody["message"].(string)
	if !strings.Contains(message, "bad seed") {
		t.Errorf("message = %q, want the ComfyUI node error surfaced", message)
	}
}

func TestCrossSiteWriteIsRejected(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	request, _ := http.NewRequest(http.MethodPost, gateway.URL+"/v1/images/generations",
		strings.NewReader(`{"model":"image-test","prompt":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	response, err := gateway.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for a cross-site write", response.StatusCode)
	}
}

func TestSessionLifecycle(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/session/open",
		map[string]any{"id": "tab-1"})
	if status != http.StatusOK {
		t.Fatalf("open status = %d", status)
	}
	if count, _ := payload["count"].(float64); count != 1 {
		t.Errorf("count = %v, want 1", payload["count"])
	}

	_, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/session/close",
		map[string]any{"id": "tab-1"})
	if count, _ := payload["count"].(float64); count != 0 {
		t.Errorf("count after close = %v, want 0", payload["count"])
	}
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/nope", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if _, ok := payload["error"]; !ok {
		t.Errorf("body is not the standard error envelope: %v", payload)
	}
}

func TestWorkflowsEndpointExposesShape(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/workflows", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	workflows, _ := payload["workflows"].([]any)
	if len(workflows) != 1 {
		t.Fatalf("workflows = %d, want 1", len(workflows))
	}
	entry, _ := workflows[0].(map[string]any)
	for _, key := range []string{"id", "name", "type", "label", "enabled", "availability", "bindings"} {
		if _, ok := entry[key]; !ok {
			t.Errorf("workflow entry is missing %q: %v", key, entry)
		}
	}
	if enabled, _ := entry["enabled"].(bool); !enabled {
		t.Error("expected the workflow to be enabled")
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

	_, models := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/v1/models", nil)
	if data, _ := models["data"].([]any); len(data) != 0 {
		t.Errorf("hidden workflow must not appear in /v1/models: %v", data)
	}
}

func TestTaskNotFoundShape(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/v1/tasks/nope", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	errorBody, _ := payload["error"].(map[string]any)
	if errorBody["code"] != "task_not_found" {
		t.Errorf("code = %v", errorBody["code"])
	}
}
