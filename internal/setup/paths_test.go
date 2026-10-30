package setup

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
)

// The layers of InstallRoot, most trustworthy first. The case that matters is the third
// one: a model folder outside the ComfyUI tree must not decide where custom nodes go.
func TestInstallRootPrefersWhatComfyUIReports(t *testing.T) {
	cases := []struct {
		name     string
		comfyDir string
		paths    map[string]any
		want     string
	}{
		{
			name:     "operator wins over everything",
			comfyDir: "/opt/ComfyUI",
			paths:    map[string]any{"custom_nodes": []any{"/elsewhere/custom_nodes"}},
			want:     "/opt/ComfyUI",
		},
		{
			name:  "custom_nodes is used when reported",
			paths: map[string]any{"custom_nodes": []any{"/opt/ComfyUI/custom_nodes"}},
			want:  "/opt/ComfyUI",
		},
		{
			name: "a model folder outside the tree does not move the root",
			paths: map[string]any{
				"custom_nodes": []any{"/opt/ComfyUI/custom_nodes"},
				"checkpoints":  []any{"/mnt/models/checkpoints"},
			},
			want: "/opt/ComfyUI",
		},
		{
			name:  "older builds only report model folders",
			paths: map[string]any{"checkpoints": []any{"/opt/ComfyUI/models/checkpoints"}},
			want:  "/opt/ComfyUI",
		},
		{
			name:  "the []string shape is accepted too",
			paths: map[string]any{"custom_nodes": []string{"/opt/ComfyUI/custom_nodes"}},
			want:  "/opt/ComfyUI",
		},
		{
			name: "nothing reported and nothing configured",
			want: "",
		},
		{
			name:  "empty entries are skipped",
			paths: map[string]any{"custom_nodes": []any{"", "  "}, "checkpoints": []any{""}},
			want:  "",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := InstallRoot(testCase.comfyDir, testCase.paths); got != testCase.want {
				t.Errorf("InstallRoot = %q, want %q", got, testCase.want)
			}
		})
	}
}

// The version ComfyUI reports is recorded and logged once per change, so a bug report does
// not start with "which ComfyUI are you running".
func TestComfyVersionIsRecordedAndLoggedOnce(t *testing.T) {
	var out bytes.Buffer
	manager := New(Options{
		Config: &config.Config{ComfyURL: "http://127.0.0.1:8188"},
		Logger: logx.NewWithWriter("test", &out),
	})

	manager.noteComfyVersion("0.3.45")
	if got := manager.Status().ComfyVersion; got != "0.3.45" {
		t.Fatalf("ComfyVersion = %q, want 0.3.45", got)
	}
	manager.noteComfyVersion("0.3.45") // same scan again: no second line
	if got := strings.Count(out.String(), "ComfyUI 0.3.45 at http://127.0.0.1:8188"); got != 1 {
		t.Errorf("logged %d times, want 1:\n%s", got, out.String())
	}

	manager.noteComfyVersion("0.4.0")
	if got := manager.Status().ComfyVersion; got != "0.4.0" {
		t.Errorf("ComfyVersion = %q, want the upgrade to be recorded", got)
	}
	if !strings.Contains(out.String(), "ComfyUI 0.4.0") {
		t.Errorf("an upgrade was not logged:\n%s", out.String())
	}
}

func TestComfyVersionParsing(t *testing.T) {
	if got := comfyVersion(map[string]any{"system": map[string]any{"comfyui_version": " 0.3.45 "}}); got != "0.3.45" {
		t.Errorf("comfyVersion = %q", got)
	}
	if got := comfyVersion(map[string]any{"system": map[string]any{}}); got != "" {
		t.Errorf("comfyVersion = %q, want empty when absent", got)
	}
	if got := comfyVersion(map[string]any{}); got != "" {
		t.Errorf("comfyVersion = %q, want empty for a malformed payload", got)
	}
}

// A build with no /internal/folder_paths must not look like a build with no models: the
// difference decides whether an install can be attempted at all.
func TestFolderPathsUnavailableLeavesTheRootUnknown(t *testing.T) {
	manager := New(Options{Config: &config.Config{}, Logger: logx.NewWithWriter("test", io.Discard)})
	if got := manager.comfyRootLocked(); got != "" {
		t.Errorf("comfyRootLocked = %q, want empty", got)
	}
}
