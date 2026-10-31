package server

// The install page reports every registered workflow, tool or model.
//
// A presentation flag used to drop "helper" workflows from the totals and from the missing list,
// which is how the panel could read 14/14 while a toolbar button was missing three node classes.
// There is no such flag any more: a missing node behind a button is still missing.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupReportsToolWorkflowProblems(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	srv := newTestGatewayWith(t, fake, file, graph, entry)

	// A second workflow that needs a node class this ComfyUI does not have.
	toolGraph := map[string]any{
		"1": map[string]any{"class_type": "MissingUpscaleNode", "inputs": map[string]any{}},
	}
	writeFixture(t, filepath.Join(srv.cfg.WorkflowDir, "test-tool.json"), toolGraph)
	writeFixture(t, filepath.Join(srv.cfg.WorkflowDir, "registry.json"), map[string]any{
		"version": 1,
		"workflows": []any{
			entry,
			map[string]any{
				"id":         "video-tool",
				"name":       "video-tool",
				"type":       "video",
				"file":       "test-tool.json",
				"parameters": []any{},
				"bindings":   map[string]any{},
			},
		},
	})
	if _, err := srv.Setup().Scan(); err != nil {
		t.Fatalf("scan: %v", err)
	}

	status := srv.Setup().Status()
	if status.Counts.Total != 2 {
		t.Errorf("total = %d, want both workflows counted: %+v", status.Counts, status.Workflows)
	}
	found := false
	for _, missing := range status.Missing.Nodes {
		if strings.Contains(missing, "MissingUpscaleNode") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the tool workflow's missing node is not reported: %+v", status.Missing)
	}

	// The same fact over HTTP, because that is what the panel reads.
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()
	code, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/setup/status", nil)
	if code != http.StatusOK {
		t.Fatalf("status endpoint = %d", code)
	}
	if _, ok := payload["status"].(map[string]any)["missing"]; !ok {
		t.Errorf("payload has no missing section: %v", payload)
	}
}

// A workflow row no longer carries a presentation flag: the client decides what to offer.
func TestWorkflowsExposeNoHiddenFlag(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	code, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/workflows", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	rows, _ := payload["workflows"].([]any)
	if len(rows) == 0 {
		t.Fatal("no workflows")
	}
	row, _ := rows[0].(map[string]any)
	if _, present := row["hidden"]; present {
		t.Errorf("row still exposes hidden: %v", row)
	}
}
