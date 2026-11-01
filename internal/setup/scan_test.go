package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"comfyui-gateway/internal/comfy"
	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/workflow"
)

// fakeComfy answers the three calls a scan makes, so the suite can exercise the real path
// without a ComfyUI install.
func fakeComfy(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{"system": map[string]any{"os": "posix"}})
	})
	mux.HandleFunc("/object_info", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{
			"KSampler":               map[string]any{"input": map[string]any{}},
			"SaveImage":              map[string]any{"input": map[string]any{}},
			"CheckpointLoaderSimple": map[string]any{"input": map[string]any{}},
		})
	})
	mux.HandleFunc("/internal/folder_paths", func(w http.ResponseWriter, _ *http.Request) {
		write(w, map[string]any{})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func newScanManager(t *testing.T, workflowsDir string, logger *logx.Logger, baseURL string) (*Manager, *workflow.Registry) {
	t.Helper()
	root := filepath.Dir(workflowsDir)
	cfg := &config.Config{
		RootDir:          root,
		WorkflowDir:      workflowsDir,
		DataDir:          filepath.Join(root, "data"),
		ComfyURL:         baseURL,
		MaxWorkflowBytes: 5 * 1024 * 1024,
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := comfy.New(comfy.Options{BaseURL: baseURL, TimeoutMs: 2000, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	registry := workflow.NewRegistry(workflow.RegistryOptions{Dir: workflowsDir, Logger: logger})
	if err := registry.Load(); err != nil {
		t.Fatal(err)
	}
	return New(Options{Config: cfg, Registry: registry, Comfy: client, Logger: logger}), registry
}

// imageGraph is a minimal graph a scan recognises as an image workflow.
func imageGraph() map[string]any {
	return map[string]any{
		"1": map[string]any{"class_type": "CheckpointLoaderSimple", "inputs": map[string]any{}},
		"2": map[string]any{"class_type": "KSampler", "inputs": map[string]any{}},
		"3": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{}},
	}
}

func writeGraph(t *testing.T, dir, name string, graph map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// The file name is the identity: "<type>-<slug>.json" registers as itself, in place, with
// no copy and no renamed duplicate.
func TestScanRegistersAGraphNamedAfterItsType(t *testing.T) {
	root := t.TempDir()
	workflowsDir := filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGraph(t, workflowsDir, "image-my-flow.json", imageGraph())

	logger := logx.NewWithWriter("test", &bytes.Buffer{})
	manager, registry := newScanManager(t, workflowsDir, logger, fakeComfy(t).URL)

	status, err := manager.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if status.Phase != "ready" {
		t.Fatalf("phase = %q, want ready", status.Phase)
	}

	entry := registry.Resolve("image-my-flow")
	if entry == nil {
		t.Fatalf("the graph was not registered; entries: %v", registry.List())
	}
	if entry.File != "image-my-flow.json" {
		t.Errorf("entry file = %q, want the file itself", entry.File)
	}
	if availability := manager.WorkflowStatus("image-my-flow"); !availability.Available {
		t.Errorf("availability = %+v, want the scan to have cleared it", availability)
	}
	if names := fileNames(t, workflowsDir); len(names) != 2 {
		t.Errorf("workflows directory = %v, want only the graph and registry.json", names)
	}
}

// Files that do not follow the naming rule are skipped, not renamed: the workflow set is
// hand-managed, so a bad name is a mistake worth reporting.
func TestScanSkipsGraphsThatBreakTheNamingRule(t *testing.T) {
	root := t.TempDir()
	workflowsDir := filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGraph(t, workflowsDir, "my-flow.json", imageGraph())          // no type prefix
	writeGraph(t, workflowsDir, "Video Loud.json", imageGraph())       // not a slug
	writeGraph(t, workflowsDir, "video-mislabeled.json", imageGraph()) // prefix disagrees with the graph

	var out bytes.Buffer
	manager, registry := newScanManager(t, workflowsDir, logx.NewWithWriter("test", &out), fakeComfy(t).URL)

	if _, err := manager.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if entries := registry.List(); len(entries) != 0 {
		t.Fatalf("entries = %v, want nothing registered", entries)
	}
	if names := fileNames(t, workflowsDir); len(names) != 4 {
		t.Errorf("workflows directory = %v, want the three graphs plus registry.json only", names)
	}

	log := out.String()
	for _, want := range []string{
		"workflow files left unregistered",
		"must start with its type",
		"must be lowercase letters, digits",
		"the graph looks like image",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("log does not mention %q:\n%s", want, log)
		}
	}
}

// A UI-format file cannot be submitted. The reporting endpoint is gone, so the reason has to
// reach the log instead of disappearing.
func TestScanReportsUnregisteredWorkflowFiles(t *testing.T) {
	root := t.TempDir()
	workflowsDir := filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGraph(t, workflowsDir, "image-ui.json", map[string]any{"nodes": []any{}, "links": []any{}})

	var out bytes.Buffer
	manager, _ := newScanManager(t, workflowsDir, logx.NewWithWriter("test", &out), fakeComfy(t).URL)

	if _, err := manager.Scan(); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	log := out.String()
	if !strings.Contains(log, "workflow files left unregistered") {
		t.Fatalf("log does not report the skipped file:\n%s", log)
	}
	if !strings.Contains(log, "UI-format") {
		t.Errorf("log does not explain why:\n%s", log)
	}
}

// A mode the workflow offers but does not open with must not take the workflow off the model list.
//
// Qwen Image's inpaint mode wants Inpaint-CropAndStitch, and because that node pack was not installed
// the whole model vanished from the picker while edit and text-to-image were perfectly runnable. The
// gap belongs to the mode; the workflow is still the user's to choose.
func TestScanKeepsAWorkflowWhoseVariantModeIsBlocked(t *testing.T) {
	root := t.TempDir()
	workflowsDir := filepath.Join(root, "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGraph(t, workflowsDir, "image-qwen.json", imageGraph())

	// A variant graph that loads a node no fake ComfyUI reports. The file name breaks the
	// `<type>-<slug>.json` rule on purpose: a variant is registered by the entry that names it, not
	// by sitting in the folder.
	inpaintGraph := imageGraph()
	inpaintGraph["4"] = map[string]any{"class_type": "InpaintCropImproved", "inputs": map[string]any{}}
	writeGraph(t, workflowsDir, "variant-inpaint.json", inpaintGraph)

	registryJSON := `{"version": 1, "workflows": [{
		"id": "image-qwen", "name": "image-qwen", "type": "image", "file": "image-qwen.json",
		"variants": {"inpaint": {"file": "variant-inpaint.json", "label": "Inpaint"}}
	}]}`
	if err := os.WriteFile(filepath.Join(workflowsDir, "registry.json"), []byte(registryJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, _ := newScanManager(t, workflowsDir, logx.NewWithWriter("test", &bytes.Buffer{}), fakeComfy(t).URL)
	status, err := manager.Scan()
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	availability := manager.WorkflowStatus("image-qwen")
	if !availability.Available {
		t.Fatalf("availability = %+v, want available: the blocked graph is a mode, not the workflow", availability)
	}
	if len(availability.ModeProblems["inpaint"]) == 0 {
		t.Errorf("modeProblems = %v, want the inpaint gap reported", availability.ModeProblems)
	}
	// Counted, not swallowed: the node is still missing, and the panel's node list has to say so.
	found := false
	for _, node := range status.Missing.Nodes {
		if strings.Contains(node, "InpaintCropImproved") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing nodes = %v, want InpaintCropImproved among them", status.Missing.Nodes)
	}
}
