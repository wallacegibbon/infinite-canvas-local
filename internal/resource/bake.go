package resource

import (
	"path/filepath"
	"strconv"

	"comfyui-gateway/internal/util"
)

// manualStepError reports work the user has to run themselves: the summary is the one-line
// reason shown next to the item, the command is what they can paste into a shell.
type manualStepError struct {
	summary string
	command string
}

func (e *manualStepError) Error() string { return e.summary }

// bakeCommand builds the command that produces a "bake" resource.
//
// The gateway never installs these: merging two checkpoints needs torch, which a static
// binary cannot offer, and the result is ~10 GB, which does not belong in an HTTP install
// request. The command carries real paths because "run the merge in ComfyUI" only leaves
// the user guessing.
func (m *Manager) bakeCommand(resource *Resource, root string) string {
	python := m.ComfyPython()
	if python == "" {
		python = "python3"
	}
	script := filepath.Join(m.cfg.RootDir, "tools", "bake-moss-preset.py")
	if !util.Exists(script) {
		script = filepath.Join("tools", "bake-moss-preset.py")
	}

	args := []string{script}
	if preset := util.StringField(resource.Builder, "preset"); preset != "" {
		args = append(args, "--preset", preset)
	}
	if strength := util.NumberField(resource.Builder, "strength", 0); strength > 0 {
		args = append(args, "--strength", strconv.FormatFloat(strength, 'f', -1, 64))
	}
	// `requires` lists the inputs in order: the base checkpoint first, then the SFT one.
	inputs := m.requirePaths(resource, root)
	for index, flag := range []string{"--base", "--sft"} {
		if index >= len(inputs) {
			break
		}
		args = append(args, flag, inputs[index])
	}
	args = append(args, "--out", m.targetPath(resource, root))

	return util.CommandLine(python, args...)
}

// bakeRequiredError is the job-item form of bakeCommand: it is what a caller sees if a bake
// resource still reaches an install job.
func (m *Manager) bakeRequiredError(resource *Resource, root string) error {
	return &manualStepError{
		summary: "produced by a local merge, not by a download",
		command: m.bakeCommand(resource, root),
	}
}

// requirePaths resolves a resource's `requires` entries to the files they point at.
func (m *Manager) requirePaths(resource *Resource, root string) []string {
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()
	if index == nil {
		return nil
	}
	paths := make([]string, 0, len(resource.Requires))
	for _, id := range resource.Requires {
		required := findResource(index, id)
		if required == nil {
			continue
		}
		paths = append(paths, m.targetPath(required, root))
	}
	return paths
}
