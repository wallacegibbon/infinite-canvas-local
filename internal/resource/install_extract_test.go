package resource

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// GitHub's archive/<sha>.zip wraps everything in one directory named after the repo and the commit.
// Install state is read from custom_nodes/<target name>, so the wrapper has to go: a pack that
// landed in custom_nodes/ComfyUI-KJNodes-d7641533 would look uninstalled for ever.
func TestExtractNodeArchiveDropsTheWrapperDirectory(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "pack.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	for name, body := range map[string]string{
		"ComfyUI-KJNodes-d7641533/__init__.py":     "print(1)\n",
		"ComfyUI-KJNodes-d7641533/nodes/nodes.py":  "print(2)\n",
		"ComfyUI-KJNodes-d7641533/web/js/panel.js": "console.log(3)\n",
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("zip entry %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	destination := filepath.Join(dir, "custom_nodes", "comfyui-kjnodes")
	if err := extractNodeArchive(context.Background(), archive, destination); err != nil {
		t.Fatalf("extract: %v", err)
	}

	for _, want := range []string{"__init__.py", "nodes/nodes.py", "web/js/panel.js"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(want))); err != nil {
			t.Errorf("%s missing after extract: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "ComfyUI-KJNodes-d7641533")); !os.IsNotExist(err) {
		t.Errorf("wrapper directory survived: err=%v", err)
	}
}
