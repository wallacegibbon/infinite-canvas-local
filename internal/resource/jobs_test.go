package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/setup"
)

// quietManager builds a Manager with just enough wiring for the download and job
// paths: a logger, a lock file, and no ComfyUI setup (nil on purpose — the paths
// under test must not depend on a live ComfyUI).
func quietManager(t *testing.T) *Manager {
	t.Helper()
	dataDir := t.TempDir()
	logger := logx.NewWithWriter("test", io.Discard)
	// A scanned-on-demand setup manager over empty directories: the status paths call into
	// it (folder paths, loader options, model aliases), and an unscanned one is harmless.
	cfg := &config.Config{RootDir: t.TempDir(), DataDir: dataDir, ComfyDir: t.TempDir()}
	return &Manager{
		cfg:       cfg,
		setup:     setup.New(setup.Options{Config: cfg, Logger: logger}),
		logger:    logger,
		lockPath:  filepath.Join(dataDir, "resource-lock.json"),
		lock:      map[string]any{"version": 1, "installed": map[string]any{}},
		jobs:      map[string]*Job{},
		inventory: map[string]inventoryEntry{},
	}
}

func TestJobProgressWeightsBytesNotItems(t *testing.T) {
	// The user-visible bug: one 6.2 GB model is one item, so counting finished items
	// left the bar at 0% until the download ended. Progress has to follow bytes.
	half := []*JobItem{{Status: "downloading", Size: 6_201_001_296, Bytes: 3_100_500_648}}
	completed, percent := jobProgress(half)
	if completed != 0 {
		t.Errorf("completed = %d, want 0 while the item is still downloading", completed)
	}
	if percent < 49 || percent > 51 {
		t.Errorf("progress = %v, want ~50 for a half-downloaded single item", percent)
	}

	over := []*JobItem{{Status: "downloading", Size: 100, Bytes: 250}}
	if _, percent := jobProgress(over); percent != 100 {
		t.Errorf("progress = %v, want 100 when bytes exceed the declared size", percent)
	}

	done := []*JobItem{{Status: "done", Size: 100, Bytes: 100}}
	if _, percent := jobProgress(done); percent != 100 {
		t.Errorf("progress = %v, want 100", percent)
	}
}

func TestJobProgressFallsBackToFinishedItems(t *testing.T) {
	// Node archives and runtimes report no size; there the only honest signal is how
	// many items are finished.
	items := []*JobItem{
		{Status: "done", Size: 0},
		{Status: "queued", Size: 0},
	}
	completed, percent := jobProgress(items)
	if completed != 1 || percent != 50 {
		t.Errorf("(completed, progress) = (%d, %v), want (1, 50)", completed, percent)
	}

	if _, percent := jobProgress(nil); percent != 100 {
		t.Errorf("progress of an empty job = %v, want 100", percent)
	}
}

func TestJobProgressIgnoresSkippedItems(t *testing.T) {
	// A pack that is already installed must not read as 0%: skipped items are done
	// work, and counting their size in the denominator would stall the bar.
	items := []*JobItem{
		{Status: "skipped", Size: 1000, Bytes: 1000},
		{Status: "downloading", Size: 1000, Bytes: 500},
	}
	if _, percent := jobProgress(items); percent != 50 {
		t.Errorf("progress = %v, want 50", percent)
	}
}

func TestSnapshotCarriesProgressAndCompleted(t *testing.T) {
	job := &Job{
		ID:     "job_1",
		Status: "running",
		Items: []*JobItem{
			{ID: "a", Status: "done", Size: 100, Bytes: 100},
			{ID: "b", Status: "downloading", Size: 100, Bytes: 50},
		},
	}
	snapshot := job.snapshot()
	if snapshot.Completed != 1 {
		t.Errorf("completed = %d, want 1", snapshot.Completed)
	}
	if snapshot.Progress != 75 {
		t.Errorf("progress = %v, want 75", snapshot.Progress)
	}
	// The snapshot must be a copy: mutating it must not touch the live job.
	snapshot.Items[1].Bytes = 100
	if job.Items[1].Bytes != 50 {
		t.Error("snapshot shares item storage with the live job")
	}
}

func TestDownloadFileReportsPhasesToThePanel(t *testing.T) {
	payload := make([]byte, 3<<20)
	for index := range payload {
		payload[index] = byte(index)
	}
	sum := sha256.Sum256(payload)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "3145728")
		// Two chunks with a pause: enough for the sampler to observe a live download.
		_, _ = writer.Write(payload[:1<<20])
		writer.(http.Flusher).Flush()
		time.Sleep(120 * time.Millisecond)
		_, _ = writer.Write(payload[1<<20:])
	}))
	defer server.Close()

	manager := quietManager(t)
	target := filepath.Join(t.TempDir(), "model.bin")
	item := &JobItem{ID: "a", Label: "model", Status: "queued", Size: int64(len(payload))}

	done := make(chan error, 1)
	go func() {
		done <- manager.downloadFile(context.Background(), server.URL+"/model.bin", target, hex.EncodeToString(sum[:]), item)
	}()

	var sawDownloading bool
	var lastBytes int64
	deadline := time.After(10 * time.Second)
poll:
	for {
		select {
		case <-deadline:
			t.Fatal("download did not finish in time")
		case err := <-done:
			if err != nil {
				t.Fatalf("downloadFile: %v", err)
			}
			break poll
		default:
			manager.mu.Lock()
			status, bytes := item.Status, item.Bytes
			manager.mu.Unlock()
			if status == "downloading" {
				sawDownloading = true
			}
			if bytes < lastBytes {
				t.Fatalf("byte counter went backwards: %d -> %d", lastBytes, bytes)
			}
			lastBytes = bytes
			time.Sleep(2 * time.Millisecond)
		}
	}

	if !sawDownloading {
		t.Error(`item never reported "downloading" — the panel shows no byte counter or speed without it`)
	}
	manager.mu.Lock()
	finalStatus, finalBytes := item.Status, item.Bytes
	manager.mu.Unlock()
	// downloadFile's last phase is the checksum; only the job runner marks "done".
	if finalStatus != "verifying" {
		t.Errorf("final item status = %q, want \"verifying\" (the panel maps it to its verifying copy)", finalStatus)
	}
	if finalBytes != int64(len(payload)) {
		t.Errorf("final byte counter = %d, want %d", finalBytes, len(payload))
	}
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(written) != len(payload) {
		t.Errorf("target size = %d, want %d", len(written), len(payload))
	}
	if _, err := os.Stat(target + ".part"); !os.IsNotExist(err) {
		t.Error("temporary .part file was left behind")
	}
}

func TestJobSnapshotJSONMatchesPanelContract(t *testing.T) {
	// The panel reads progress as an antd percent (0..100) and stops polling only on
	// done/error/canceled, so the wire format itself is worth pinning.
	job := &Job{
		ID:     "job_json",
		Status: "done",
		Items: []*JobItem{
			{ID: "a", Label: "a", Status: "done", Size: 100, Bytes: 100},
			{ID: "b", Label: "b", Status: "skipped", Size: 100, Bytes: 100},
		},
	}
	encoded, err := json.Marshal(job.snapshot())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Status    string  `json:"status"`
		Progress  float64 `json:"progress"`
		Completed int     `json:"completed"`
		Items     []struct {
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Status != "done" {
		t.Errorf("status = %q, want \"done\"", decoded.Status)
	}
	if decoded.Progress != 100 {
		t.Errorf("progress = %v, want 100 (a 0..1 fraction renders as an empty bar)", decoded.Progress)
	}
	if decoded.Completed != 2 {
		t.Errorf("completed = %d, want 2", decoded.Completed)
	}
	if decoded.Items[1].Status != "skipped" {
		t.Errorf("item status = %q, want \"skipped\"", decoded.Items[1].Status)
	}
}

func TestCancelJobUsesPanelVocabulary(t *testing.T) {
	manager := quietManager(t)
	job := &Job{
		ID:     "job_cancel",
		Status: "running",
		Items: []*JobItem{
			{ID: "a", Status: "downloading", Size: 100, Bytes: 40},
			{ID: "b", Status: "queued", Size: 100},
			{ID: "c", Status: "done", Size: 100, Bytes: 100},
		},
	}
	manager.jobs[job.ID] = job

	snapshot := manager.CancelJob(job.ID)
	if snapshot == nil {
		t.Fatal("CancelJob returned nil for a live job")
	}
	if snapshot.Status != "canceled" {
		t.Errorf("job status = %q, want \"canceled\" — the panel stops polling on that value", snapshot.Status)
	}
	for _, item := range snapshot.Items {
		want := "error"
		if item.ID == "c" {
			want = "done"
		}
		if item.Status != want {
			t.Errorf("item %s status = %q, want %q", item.ID, item.Status, want)
		}
		if item.ID != "c" && item.Error != "cancelled" {
			t.Errorf("item %s error = %q, want \"cancelled\"", item.ID, item.Error)
		}
	}
	// A finished item reports 1/3 of the bytes, so a full bar would be a lie.
	if snapshot.Progress >= 100 {
		t.Errorf("progress after cancel = %v, want less than 100", snapshot.Progress)
	}
	if snapshot == job {
		t.Error("CancelJob returned the live job instead of a snapshot")
	}
	if manager.jobs[job.ID] != job {
		t.Error("CancelJob replaced the stored job with the snapshot")
	}
}

func TestInstallJobFinishesWithDoneStatus(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundles", "node-a")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "__init__.py"), []byte("NODE_CLASS_MAPPINGS = {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := quietManager(t)
	manager.cfg.RootDir = root
	manager.cfg.ComfyDir = filepath.Join(root, "ComfyUI")
	manager.cfg.ResourceConcurrency = 2
	manager.index = &Index{Resources: []*Resource{{
		ID:        "node-a",
		Type:      "node",
		Label:     "node a",
		Target:    "custom_nodes/node-a",
		BundleDir: "bundles/node-a",
	}}}

	job, err := manager.Install([]string{"node-a"}, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		snapshot := manager.GetJob(job.ID)
		if snapshot == nil {
			t.Fatal("job disappeared")
		}
		if snapshot.Status != "running" {
			if snapshot.Status != "done" {
				t.Fatalf("job status = %q (%s), want \"done\"", snapshot.Status, snapshot.Message)
			}
			if snapshot.Progress != 100 {
				t.Errorf("progress = %v, want 100", snapshot.Progress)
			}
			if snapshot.Items[0].Status != "done" {
				t.Errorf("item status = %q, want \"done\"", snapshot.Items[0].Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := os.Stat(filepath.Join(manager.cfg.ComfyDir, "custom_nodes", "node-a", "__init__.py")); err != nil {
		t.Errorf("bundled node was not installed: %v", err)
	}
}
