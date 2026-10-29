package resource

import (
	"os"
	"path/filepath"
	"strings"
)

// probeComfyPython finds the interpreter running ComfyUI.
//
// The "runtime deps" hint tells the user to run `pip install` in the ComfyUI
// environment, so naming the wrong interpreter is worse than naming none. The
// configured value wins; otherwise the running process is inspected, because that
// is the only authoritative answer.
func (m *Manager) probeComfyPython(root string) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}

	bestMatch := ""
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid := entry.Name()
		if pid == "" || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		if int(pid[0]-'0') == 0 && len(pid) > 1 {
			continue
		}

		cmdline, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil {
			continue
		}
		arguments := strings.Split(strings.TrimRight(string(cmdline), "\x00"), "\x00")
		if len(arguments) == 0 || !looksLikeComfyCommand(arguments) {
			continue
		}

		// Prefer a process whose working directory is the ComfyUI root.
		if root != "" {
			if cwd, err := os.Readlink(filepath.Join("/proc", pid, "cwd")); err == nil {
				if !strings.HasPrefix(cwd, root) {
					continue
				}
				bestMatch = interpreterFor(arguments, pid)
				if bestMatch != "" {
					return bestMatch
				}
			}
		}
		if bestMatch == "" {
			bestMatch = interpreterFor(arguments, pid)
		}
	}
	return bestMatch
}

// looksLikeComfyCommand reports whether a command line runs ComfyUI's main.py.
func looksLikeComfyCommand(arguments []string) bool {
	for _, argument := range arguments {
		base := filepath.Base(argument)
		if base == "main.py" || base == "comfyui" {
			return true
		}
	}
	return false
}

// interpreterFor returns the interpreter path as it was invoked.
//
// /proc/<pid>/exe follows symlinks, so a venv interpreter resolves to the base
// system Python. That is the wrong answer here: `pip install` must target the venv,
// and the argv[0] the user actually launched with is the venv path.
func interpreterFor(arguments []string, pid string) string {
	if len(arguments) > 0 {
		invoked := arguments[0]
		if filepath.IsAbs(invoked) && strings.Contains(strings.ToLower(filepath.Base(invoked)), "python") {
			return invoked
		}
	}
	return readExe(pid)
}

// readExe resolves the executable behind a pid.
func readExe(pid string) string {
	exe, err := os.Readlink(filepath.Join("/proc", pid, "exe"))
	if err != nil {
		return ""
	}
	// "(deleted)" appears when the interpreter was replaced in place.
	return strings.TrimSuffix(exe, " (deleted)")
}
