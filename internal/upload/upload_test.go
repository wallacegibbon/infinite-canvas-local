package upload

import (
	"os"
	"testing"
	"time"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

func newStoreAt(t *testing.T, dataDir string, retentionDays int) *Store {
	t.Helper()
	store, err := New(Options{
		DataDir:       dataDir,
		RetentionDays: retentionDays,
		MaxBytes:      1024,
		Logger:        logx.NewWithWriter("test", os.Stderr),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return store
}

func TestKindOf(t *testing.T) {
	cases := []struct{ mime, name, want string }{
		{"image/png", "a.png", KindImage},
		{"", "a.JPG", KindImage},
		{"video/mp4", "a.mp4", KindVideo},
		{"", "a.mov", KindVideo},
		{"audio/mpeg", "a.mp3", KindAudio},
		{"", "a.flac", KindAudio},
		{"", "a.safetensors", ""},
		{"application/json", "a.json", ""},
	}
	for _, testCase := range cases {
		if got := KindOf(testCase.mime, testCase.name); got != testCase.want {
			t.Errorf("KindOf(%q, %q) = %q, want %q", testCase.mime, testCase.name, got, testCase.want)
		}
	}
}

func TestSaveGetAndComfyReference(t *testing.T) {
	dataDir := t.TempDir()
	store := newStoreAt(t, dataDir, 0)
	file, err := store.Save(KindImage, "ref.png", "image/png", []byte("PNG"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !util.Exists(file.Path) {
		t.Fatalf("bytes were not written: %s", file.Path)
	}
	if got := store.Get(file.ID); got == nil || got.Kind != KindImage {
		t.Fatalf("Get = %+v", got)
	}

	store.SetComfyReference(file.ID, "comfy.png", "refs")

	// A fresh store must reload the index, including the ComfyUI reference.
	reloaded := newStoreAt(t, dataDir, 0)
	reloaded.Load()
	got := reloaded.Get(file.ID)
	if got == nil {
		t.Fatal("the upload did not survive a reload")
	}
	if got.ComfyName != "comfy.png" || got.ComfySubfolder != "refs" {
		t.Errorf("reference = %q/%q", got.ComfyName, got.ComfySubfolder)
	}
}

func TestSaveRejectsOversized(t *testing.T) {
	store := newStoreAt(t, t.TempDir(), 0)
	if _, err := store.Save(KindImage, "big.png", "image/png", make([]byte, 2048)); err != ErrTooLarge {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

// An upload older than the retention window is dropped with its bytes, so an abandoned upload
// cannot grow the data directory forever.
func TestLoadPrunesExpiredUploads(t *testing.T) {
	dataDir := t.TempDir()
	store := newStoreAt(t, dataDir, 0)
	keep, err := store.Save(KindAudio, "keep.mp3", "audio/mpeg", []byte("KEEP"))
	if err != nil {
		t.Fatalf("save keep: %v", err)
	}
	old, err := store.Save(KindAudio, "old.mp3", "audio/mpeg", []byte("OLD"))
	if err != nil {
		t.Fatalf("save old: %v", err)
	}
	// Backdate the record, then let a load with a one-day window prune it.
	old.CreatedAt = time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	store.persist()

	pruned := newStoreAt(t, dataDir, 1)
	pruned.Load()

	if pruned.Get(old.ID) != nil {
		t.Error("an expired upload survived the load")
	}
	if util.Exists(old.Path) {
		t.Error("an expired upload's bytes were left on disk")
	}
	if pruned.Get(keep.ID) == nil {
		t.Error("a fresh upload was pruned")
	}
}
