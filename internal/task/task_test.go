package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"comfyui-gateway/internal/logx"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(Options{
		DataDir:       t.TempDir(),
		PublicBaseURL: "http://127.0.0.1:8190",
		Logger:        logx.NewWithWriter("test", os.Stderr),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Close stops the debounced writer before t.TempDir removal runs; without it the
	// pending persist races with the teardown.
	t.Cleanup(store.Close)
	return store
}

// waitFor polls until the condition holds or the deadline passes.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTaskCompletesAndPersists(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectVideo, "video-test", map[string]any{"prompt": "x"})
	if created.Status != "queued" {
		t.Fatalf("initial status = %q, want queued", created.Status)
	}

	store.Enqueue(created, func(_ *Task, progress func(Update), _ context.Context) (*Result, error) {
		progress(Update{Progress: 0.5, PromptID: "p1"})
		return &Result{Outputs: []ResultFile{{Buffer: []byte("VIDEO"), Ext: "mp4", MimeType: "video/mp4"}}, PromptID: "p1"}, nil
	})

	waitFor(t, "completion", func() bool { return store.Get(created.ID).Status == "completed" })

	done := store.Get(created.ID)
	if done.File == nil || !fileExists(*done.File) {
		t.Fatalf("output file was not written: %v", done.File)
	}
	if done.URL == nil || *done.URL == "" {
		t.Error("expected a public URL")
	}
	if done.Bytes != int64(len("VIDEO")) {
		t.Errorf("bytes = %d, want %d", done.Bytes, len("VIDEO"))
	}

	path := store.ContentPath(created.ID)
	if path == "" {
		t.Fatal("ContentPath returned empty for a completed task")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "VIDEO" {
		t.Errorf("content = %q, err = %v", data, err)
	}

	// A fresh store must reconcile the persisted record rather than mark it failed.
	reloaded, err := New(Options{
		DataDir:       filepath.Dir(filepath.Dir(*done.File)),
		PublicBaseURL: "http://127.0.0.1:8190",
		Logger:        logx.NewWithWriter("test", os.Stderr),
	})
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	t.Cleanup(reloaded.Close)
	reloaded.Load()
	if got := reloaded.Get(created.ID); got == nil || got.Status != "completed" {
		t.Errorf("reloaded status = %v, want completed", got)
	}
}

func TestTaskFailureIsRecorded(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectVideo, "video-test", nil)
	store.Enqueue(created, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return nil, errors.New("boom")
	})

	waitFor(t, "failure", func() bool { return store.Get(created.ID).Status == "failed" })

	failed := store.Get(created.ID)
	if failed.Error == nil || *failed.Error != "boom" {
		t.Errorf("error = %v, want boom", failed.Error)
	}
	if failed.CompletedAt == nil {
		t.Error("completedAt should be set on failure")
	}
}

func TestCancelQueuedTask(t *testing.T) {
	store := newStore(t)

	// Occupy the single worker so the second task stays queued.
	blocker := make(chan struct{})
	first := store.Create(ObjectVideo, "video-test", nil)
	store.Enqueue(first, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		<-blocker
		return &Result{Outputs: []ResultFile{{Buffer: []byte("A"), Ext: "mp4"}}}, nil
	})
	waitFor(t, "first task running", func() bool { return store.Get(first.ID).Status == "running" })

	second := store.Create(ObjectVideo, "video-test", nil)
	store.Enqueue(second, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return &Result{Outputs: []ResultFile{{Buffer: []byte("B"), Ext: "mp4"}}}, nil
	})
	waitFor(t, "second task queued", func() bool { return store.Get(second.ID).Status == "queued" })

	if cancelled := store.Cancel(second.ID); cancelled == nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel result = %v", cancelled)
	}

	close(blocker)
	waitFor(t, "first task completion", func() bool { return store.Get(first.ID).Status == "completed" })
	if got := store.Get(second.ID).Status; got != "cancelled" {
		t.Errorf("second status = %q, want cancelled", got)
	}
}

func TestCancelRunningTaskAbortsContext(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectVideo, "video-test", nil)
	aborted := make(chan struct{})

	store.Enqueue(created, func(_ *Task, _ func(Update), ctx context.Context) (*Result, error) {
		<-ctx.Done()
		close(aborted)
		return nil, ctx.Err()
	})
	waitFor(t, "task running", func() bool { return store.Get(created.ID).Status == "running" })

	// The cancel is reported immediately, not only after the worker unwinds.
	if cancelled := store.Cancel(created.ID); cancelled == nil || cancelled.Status != "cancelled" {
		t.Errorf("immediate status = %v, want cancelled", cancelled)
	}

	select {
	case <-aborted:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelling a running task must cancel its context")
	}
	waitFor(t, "cancelled status", func() bool { return store.Get(created.ID).Status == "cancelled" })
}

func TestCleanupRespectsLimits(t *testing.T) {
	store := newStore(t)

	// Both limits at zero means "keep everything": that is the documented default,
	// so cleanup must be a no-op rather than wiping the history.
	keep := store.Create(ObjectVideo, "video-test", nil)
	store.Enqueue(keep, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return &Result{Outputs: []ResultFile{{Buffer: []byte("KEEP"), Ext: "mp4"}}}, nil
	})
	waitFor(t, "first completion", func() bool { return store.Get(keep.ID).Status == "completed" })

	if removed, files := store.Cleanup(0, 0); removed != 0 || files != 0 {
		t.Fatalf("cleanup(0,0) = %d/%d, want 0/0", removed, files)
	}

	// With maxTasks=1, the oldest terminal task is pruned along with its file.
	extra := store.Create(ObjectVideo, "video-test", nil)
	store.Enqueue(extra, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return &Result{Outputs: []ResultFile{{Buffer: []byte("EXTRA"), Ext: "mp4"}}}, nil
	})
	waitFor(t, "second completion", func() bool { return store.Get(extra.ID).Status == "completed" })
	// Guarantee distinct completion timestamps so "oldest" is unambiguous.
	time.Sleep(2 * time.Millisecond)

	removed, filesDeleted := store.Cleanup(0, 1)
	if removed != 1 || filesDeleted != 1 {
		t.Fatalf("cleanup(0,1) = removed %d, files %d; want 1/1", removed, filesDeleted)
	}
	// Retention keeps the newest task, so the first one is the one that goes.
	if store.Get(keep.ID) != nil {
		t.Error("the oldest task should have been pruned")
	}
	if store.Get(extra.ID) == nil {
		t.Error("the newest task must be retained")
	}
	if len(store.List()) != 1 {
		t.Errorf("remaining tasks = %d, want 1", len(store.List()))
	}
}

func TestContentPathRejectsEscapingFile(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectVideo, "video-test", nil)

	// Simulate a corrupted record pointing outside the output directory.
	outside := filepath.Join(t.TempDir(), "secret.mp4")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	created.File = &outside

	if got := store.ContentPath(created.ID); got != "" {
		t.Errorf("ContentPath = %q, want empty for a path outside the output dir", got)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// The queue used to assume video everywhere: object, fallback MIME type and the file
// extension reconciliation looks for. An audio run has to persist the same way.
func TestAudioTaskStoresMp3AndReconciles(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectAudio, "audio-test", nil)
	store.Enqueue(created, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return &Result{Outputs: []ResultFile{{Buffer: []byte("MP3"), Ext: "mp3", MimeType: "audio/mpeg"}}}, nil
	})

	waitFor(t, "audio completion", func() bool { return store.Get(created.ID).Status == "completed" })
	done := store.Get(created.ID)
	if done.MimeType == nil || *done.MimeType != "audio/mpeg" {
		t.Errorf("mime = %v, want audio/mpeg", done.MimeType)
	}
	if done.File == nil || filepath.Ext(*done.File) != ".mp3" {
		t.Fatalf("file = %v, want a .mp3 path", done.File)
	}
	if done.Object != ObjectAudio {
		t.Errorf("object = %v, want audio", done.Object)
	}

	// A record that never stored its path (older format) must be found by extension.
	orphan := &Task{ID: "task_orphan", Object: ObjectAudio, Status: "queued"}
	if err := os.WriteFile(filepath.Join(store.outputDir, "task_orphan.flac"), []byte("FLAC"), 0o644); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	store.reconcileLocked(orphan)
	if orphan.Status != "completed" {
		t.Errorf("orphan status = %q, want completed", orphan.Status)
	}
	if orphan.MimeType != nil {
		t.Errorf("reconciled record invented a mime type: %v", *orphan.MimeType)
	}
}

// The queue must persist the structured failure, not only the message: an adapter (and
// later a run reader) needs the code, and callers must not have to parse English.
func TestTaskFailureKeepsCodeAndDetails(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectAudio, "audio-test", nil)
	store.Enqueue(created, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return nil, &codedTestError{code: "comfy_execution_failed", message: "boom", details: map[string]any{"node": "35"}}
	})

	waitFor(t, "coded failure", func() bool { return store.Get(created.ID).Status == "failed" })
	done := store.Get(created.ID)
	if done.Error == nil || *done.Error != "boom" {
		t.Fatalf("error = %v", done.Error)
	}
	if done.ErrorCode == nil || *done.ErrorCode != "comfy_execution_failed" {
		t.Errorf("errorCode = %v", done.ErrorCode)
	}
	if details, ok := done.ErrorDetails.(map[string]any); !ok || details["node"] != "35" {
		t.Errorf("errorDetails = %v", done.ErrorDetails)
	}
	if done.ErrorStatus != 400 {
		t.Errorf("errorStatus = %d, want the reporter's 400", done.ErrorStatus)
	}
}

type codedTestError struct {
	code    string
	message string
	details any
}

func (e *codedTestError) Error() string       { return e.message }
func (e *codedTestError) FailureCode() string { return e.code }
func (e *codedTestError) FailureDetails() any { return e.details }
func (e *codedTestError) StatusCode() int     { return 400 }

// A batch image request stores every artifact, in order, and the first one keeps the legacy
// single-file fields so an older client still finds it.
func TestTaskStoresEveryOutput(t *testing.T) {
	store := newStore(t)
	created := store.Create(ObjectImage, "image-test", nil)
	store.Enqueue(created, func(_ *Task, _ func(Update), _ context.Context) (*Result, error) {
		return &Result{Outputs: []ResultFile{
			{Buffer: []byte("ONE"), Ext: "png", MimeType: "image/png"},
			{Buffer: []byte("TWO"), Ext: "png", MimeType: "image/png"},
		}}, nil
	})

	waitFor(t, "batch completion", func() bool { return store.Get(created.ID).Status == "completed" })
	done := store.Get(created.ID)
	if len(done.Outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(done.Outputs))
	}
	for index, want := range []string{"ONE", "TWO"} {
		path := store.OutputPath(created.ID, index)
		if path == "" {
			t.Fatalf("output %d has no path", index)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != want {
			t.Errorf("output %d = %q, err = %v", index, data, err)
		}
	}
	if done.File == nil || *done.File != done.Outputs[0].File {
		t.Errorf("legacy File does not mirror the first output")
	}
	if store.OutputPath(created.ID, 5) != "" {
		t.Error("an out-of-range index returned a path")
	}
	if len(done.Outputs) != 2 {
		t.Errorf("outputs = %v", done.Outputs)
	}
}
