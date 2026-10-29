package setup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/util"
)

// ModelDirKeys maps a logical model directory to the on-disk names ComfyUI may use
// for it. ComfyUI renamed several of these over time, so both spellings are probed.
var ModelDirKeys = map[string][]string{
	"diffusion_models":      {"diffusion_models", "unet"},
	"text_encoders":         {"text_encoders", "clip"},
	"vae":                   {"vae"},
	"vae_approx":            {"vae_approx"},
	"loras":                 {"loras"},
	"checkpoints":           {"checkpoints"},
	"upscale_models":        {"upscale_models"},
	"latent_upscale_models": {"latent_upscale_models"},
	"vdn":                   {"vdn"},
	"clip_vision":           {"clip_vision"},
	"LLM":                   {"LLM", "llm"},
	"SEEDVR2":               {"SEEDVR2", "seedvr2"},
}

var yamlKeyPattern = regexp.MustCompile(`^([\w.-]+):\s*(.*)$`)

// ParseExtraModelPaths reads the subset of extra_model_paths.yaml that matters:
// section headers, base_path, and plain or block-style path lists.
func ParseExtraModelPaths(file string) map[string][]string {
	text, err := os.ReadFile(file)
	if err != nil {
		return map[string][]string{}
	}
	baseDir := filepath.Dir(file)
	result := map[string][]string{}

	section := ""
	basePath := ""
	multilineKey := ""
	resolve := func(value string) string {
		raw := strings.Trim(strings.TrimSpace(value), `"`)
		if raw == "" {
			return ""
		}
		if filepath.IsAbs(raw) {
			return raw
		}
		base := basePath
		if base == "" {
			base = baseDir
		}
		return filepath.Join(base, raw)
	}

	for _, line := range strings.Split(strings.ReplaceAll(string(text), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent == 0 && strings.HasSuffix(trimmed, ":") && !strings.Contains(strings.TrimSuffix(trimmed, ":"), " ") {
			section = strings.TrimSuffix(trimmed, ":")
			basePath = ""
			multilineKey = ""
			continue
		}
		if section == "" {
			continue
		}
		if match := yamlKeyPattern.FindStringSubmatch(trimmed); match != nil && !strings.HasPrefix(trimmed, "-") {
			key := match[1]
			value := strings.TrimSpace(match[2])
			multilineKey = ""
			switch key {
			case "base_path":
				basePath = resolve(value)
				continue
			case "is_default":
				continue
			}
			if _, ok := result[key]; !ok {
				result[key] = nil
			}
			if value == "|" || value == ">" {
				multilineKey = key
				continue
			}
			if value != "" {
				result[key] = append(result[key], resolve(value))
			}
			continue
		}
		if multilineKey != "" && indent > 0 {
			if resolved := resolve(trimmed); resolved != "" {
				result[multilineKey] = append(result[multilineKey], resolved)
			}
		}
	}
	return result
}

type extraPathsCacheEntry struct {
	at    time.Time
	paths map[string][]string
}

var (
	extraPathsMu    sync.Mutex
	extraPathsCache = map[string]extraPathsCacheEntry{}
)

// ExtraModelPathsFor merges every extra_model_paths.yaml that could apply.
//
// A portable ComfyUI bundle keeps the file either inside the ComfyUI directory or
// one level above it; mapped drives only become visible to ComfyUI through this
// list, so all three candidate locations are read.
func ExtraModelPathsFor(root, comfyDir string) map[string][]string {
	if root == "" && comfyDir == "" {
		return map[string][]string{}
	}
	var candidates []string
	add := func(value string) {
		if value == "" {
			return
		}
		abs, err := filepath.Abs(value)
		if err != nil {
			return
		}
		for _, existing := range candidates {
			if existing == abs {
				return
			}
		}
		candidates = append(candidates, abs)
	}
	if root != "" {
		add(filepath.Join(root, "extra_model_paths.yaml"))
		add(filepath.Join(filepath.Dir(root), "extra_model_paths.yaml"))
	}
	if comfyDir != "" {
		add(filepath.Join(comfyDir, "extra_model_paths.yaml"))
	}

	cacheKey := strings.ToLower(root + "|" + comfyDir)
	extraPathsMu.Lock()
	if entry, ok := extraPathsCache[cacheKey]; ok && time.Since(entry.at) < 10*time.Second {
		extraPathsMu.Unlock()
		return entry.paths
	}
	extraPathsMu.Unlock()

	merged := map[string][]string{}
	for _, file := range candidates {
		if !util.Exists(file) {
			continue
		}
		for name, values := range ParseExtraModelPaths(file) {
			if len(values) == 0 {
				continue
			}
			merged[name] = append(merged[name], values...)
		}
	}

	extraPathsMu.Lock()
	extraPathsCache[cacheKey] = extraPathsCacheEntry{at: time.Now(), paths: merged}
	extraPathsMu.Unlock()
	return merged
}

// CollectModelRoots lists every directory a logical model type may live in.
func CollectModelRoots(dir string, folderPaths map[string]any, comfyDir, comfyRoot string) []string {
	keys, ok := ModelDirKeys[dir]
	if !ok {
		keys = []string{dir}
	}
	var roots []string
	seen := map[string]bool{}
	add := func(value string) {
		if value == "" {
			return
		}
		abs, err := filepath.Abs(value)
		if err != nil {
			return
		}
		lowered := strings.ToLower(abs)
		if seen[lowered] {
			return
		}
		seen[lowered] = true
		roots = append(roots, abs)
	}

	for _, key := range keys {
		if list, ok := folderPaths[key].([]any); ok {
			for _, entry := range list {
				if text, ok := entry.(string); ok {
					add(text)
				}
			}
		}
	}

	base := comfyRoot
	if base == "" {
		base = comfyDir
	}
	if base != "" {
		extra := ExtraModelPathsFor(base, comfyDir)
		for _, key := range keys {
			for _, entry := range extra[key] {
				add(entry)
			}
		}
	}

	for _, baseDir := range []string{comfyRoot, comfyDir} {
		if baseDir == "" {
			continue
		}
		for _, key := range keys {
			add(filepath.Join(baseDir, "models", key))
		}
	}
	return roots
}

// ModelRelativePath turns an absolute selection into the relative path a ComfyUI
// loader can see.
//
// Users pick files through a system dialog, which happily returns paths on a mapped
// drive. ComfyUI resolves loaders relative to its model roots, so a path outside
// every root is invisible to it: this walks symlinks back to the real location
// inside a root so the selection still works.
func ModelRelativePath(roots []string, absolute string) (relative string, root string, ok bool) {
	target, err := filepath.Abs(absolute)
	if err != nil {
		return "", "", false
	}
	for _, candidate := range roots {
		if rel, inside := relativeInside(candidate, target); inside {
			return rel, candidate, true
		}
	}

	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", false
	}
	for _, candidate := range roots {
		if rel, found := mappedRelative(candidate, realTarget, 0); found {
			return rel, candidate, true
		}
	}
	return "", "", false
}

// relativeInside returns the slash-separated path of child within parent.
func relativeInside(parent, child string) (string, bool) {
	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// mappedRelative walks a root looking for the directory a symlink actually points at.
func mappedRelative(root, realTarget string, depth int) (string, bool) {
	realDir, err := filepath.EvalSymlinks(root)
	if err != nil {
		realDir = root
	}
	if rel, found := relativeInside(realDir, realTarget); found {
		prefix, _ := relativeInside(filepath.Dir(root), root)
		if prefix == "" {
			return rel, true
		}
		return prefix + "/" + rel, true
	}
	if depth >= 4 {
		return "", false
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() {
			continue
		}
		if rel, found := mappedRelative(filepath.Join(root, entry.Name()), realTarget, depth+1); found {
			return rel, true
		}
	}
	return "", false
}
