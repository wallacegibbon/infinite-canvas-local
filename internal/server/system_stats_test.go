package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ComfyUI's own numbers are the only ones the panel may show, so these tests check both that they
// arrive and that nothing arrives when ComfyUI has nothing to say.

func TestSystemStatsReportComfysMachine(t *testing.T) {
	fake := newFakeComfy(t)
	const ramTotal = 100551286784
	const ramFree = 52550938624
	const vramTotal = 16707092480
	const vramFree = 1813959112
	fake.systemStats = map[string]any{
		"system": map[string]any{"os": "linux", "ram_total": ramTotal, "ram_free": ramFree},
		"devices": []any{map[string]any{
			"name":       "cuda:0 NVIDIA GeForce RTX 5080 : cudaMallocAsync",
			"type":       "cuda",
			"index":      0,
			"vram_total": vramTotal,
			"vram_free":  vramFree,
		}},
	}
	gateway := httptest.NewServer(newTestGateway(t, fake).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/system/stats", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if payload["available"] != true {
		t.Fatalf("available = %v, want true", payload["available"])
	}
	gpu := payload["gpu"].([]any)[0].(map[string]any)
	if gpu["memoryTotalMb"].(float64) != vramTotal/(1024*1024) {
		t.Fatalf("memoryTotalMb = %v, want %d", gpu["memoryTotalMb"], vramTotal/(1024*1024))
	}
	if gpu["memoryUsedMb"].(float64) != (vramTotal-vramFree)/(1024*1024) {
		t.Fatalf("memoryUsedMb = %v, want %d", gpu["memoryUsedMb"], (vramTotal-vramFree)/(1024*1024))
	}
	ram := payload["ram"].(map[string]any)
	if ram["totalBytes"].(float64) != ramTotal {
		t.Fatalf("ram.totalBytes = %v, want %d", ram["totalBytes"], ramTotal)
	}
	if ram["usedBytes"].(float64) != ramTotal-ramFree {
		t.Fatalf("ram.usedBytes = %v, want %d", ram["usedBytes"], ramTotal-ramFree)
	}
}

func TestSystemStatsAreAbsentWhenComfyReportsNoDevices(t *testing.T) {
	fake := newFakeComfy(t)
	gateway := httptest.NewServer(newTestGateway(t, fake).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/system/stats", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if payload["available"] != false {
		t.Fatalf("available = %v, want false", payload["available"])
	}
	if gpu := payload["gpu"].([]any); len(gpu) != 0 {
		t.Fatalf("gpu = %v, want none", gpu)
	}
	ram := payload["ram"].(map[string]any)
	if ram["totalBytes"].(float64) != 0 || ram["usedBytes"].(float64) != 0 {
		t.Fatalf("ram = %v, want zeros", ram)
	}
}

func TestSystemStatsAreAbsentWhenComfyIsGone(t *testing.T) {
	fake := newFakeComfy(t)
	srv := newTestGateway(t, fake)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()
	// The gateway keeps its ComfyUI client; the server it points at is what disappears.
	fake.server.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/system/stats", nil)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if payload["available"] != false {
		t.Fatalf("available = %v, want false", payload["available"])
	}
}
