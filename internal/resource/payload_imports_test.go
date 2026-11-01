package resource

// The vendored node packs under `resources/payload/custom_nodes` are installed by copying a directory
// into ComfyUI, so a pack that is missing a file is a pack that fails to import — and ComfyUI then reports
// the node classes as missing, which the panel shows as "this workflow cannot run" with no reason.
//
// That happened: `ComfyUI-SeedVR2_VideoUpscaler` shipped without `src/data/`, which its own
// `src/core/generation_utils.py` imports, so `video-seedvr2-upscale` could never load (known-issues #1
// spent a while blaming the installed copy instead). These tests read the payload the way Python would.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pythonBaseDirs are the directories this check is allowed to leave alone: a pack may import a sibling
// package that the host provides (`ComfyUI`, `folder_paths`, `comfy`, `nodes`).
var pythonHostModules = map[string]bool{
	"ComfyUI": true, "folder_paths": true, "comfy": true, "nodes": true, "server": true, "app": true,
}

// relativeImport matches `from ..pkg.mod import name` — the form that has to resolve inside the pack.
var relativeImport = regexp.MustCompile(`(?m)^\s*from\s+(\.+)([\w.]*)\s+import\b`)

// pythonFiles walks a pack and returns every .py file in it.
func pythonFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".py") {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// resolveRelativeImport maps `from <dots><module> import …` in one file to the path it names, or to "".
//
// Python's rule: one dot is the file's own package, each further dot is one package up, and the module
// parts are directories below that. A package is a `.py` file or a namespace directory (the vendored
// packs use namespace packages, so `__init__.py` is not required).
func resolveRelativeImport(packRoot, file, dots, module string) string {
	dir := filepath.Dir(file)
	for range len(dots) - 1 {
		if dir == packRoot || dir == "." || dir == string(filepath.Separator) {
			return ""
		}
		dir = filepath.Dir(dir)
	}
	if module != "" {
		dir = filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(module, ".", "/")))
	}
	return dir
}

// importExists reports whether a resolved target is a module or a package that actually exists.
func importExists(target string) bool {
	if target == "" {
		return false
	}
	if strings.HasSuffix(target, ".py") {
		return fileExists(target)
	}
	return fileExists(target+".py") || fileExists(filepath.Join(target, "__init__.py")) || dirExists(target)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// Every relative import inside every vendored pack has to name something that is in the pack. This is the
// check that would have caught the missing `src/data/` before an install did.
func TestVendoredPacksImportOnlyWhatTheyShip(t *testing.T) {
	root := filepath.Join("..", "..", "resources", "payload", "custom_nodes")
	if !dirExists(root) {
		t.Skipf("vendored packs not available at %s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read %s: %v", root, err)
	}

	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		packRoot := filepath.Join(root, entry.Name())
		for _, file := range pythonFiles(t, packRoot) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			for _, match := range relativeImport.FindAllStringSubmatch(string(raw), -1) {
				dots, module := match[1], match[2]
				if module == "" {
					// `from . import name` — each name is resolved by Python against the package; there is
					// no module path here to check.
					continue
				}
				if pythonHostModules[strings.Split(module, ".")[0]] {
					continue
				}
				checked++
				target := resolveRelativeImport(packRoot, file, dots, module)
				if importExists(target) {
					continue
				}
				rel, _ := filepath.Rel(root, file)
				relTarget, _ := filepath.Rel(root, target)
				t.Errorf("%s imports %s%s, which is not in the pack (expected %s)", rel, dots, module, relTarget)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no relative imports were checked; the walk is looking in the wrong place")
	}
	t.Logf("checked %d relative imports across the vendored packs", checked)
}
