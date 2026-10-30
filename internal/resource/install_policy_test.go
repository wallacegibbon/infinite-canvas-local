package resource

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comfyui-gateway/internal/httpx"
)

// Without COMFY_MODELS_SHARED=1 no download may start: the dangerous case is not a failed
// download but a successful one into a path that happens to exist on this host.
func TestInstallRefusesUnlessTheDeploymentSaysShared(t *testing.T) {
	manager := quietManager(t)
	manager.cfg.ComfyURL = "http://192.168.3.50:8188"
	manager.cfg.ComfyModelsShared = false
	manager.index = &Index{Resources: []*Resource{{
		ID:      "m1",
		Type:    "model",
		Label:   "Model",
		Target:  "checkpoints/m1.safetensors",
		Sources: []any{map[string]any{"url": "http://127.0.0.1:1/m1.safetensors"}},
	}}}

	job, err := manager.Install([]string{"m1"}, nil)
	if job != nil {
		t.Fatalf("job = %v, want a refusal instead of a job", job)
	}
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an httpx.Error", err)
	}
	if apiErr.Code != "model_install_disabled" || apiErr.Status != 400 {
		t.Errorf("(status, code) = (%d, %q), want (400, model_install_disabled)", apiErr.Status, apiErr.Code)
	}
	// Both fixes and the machine to move to have to be in the message.
	for _, want := range []string{"192.168.3.50:8188", "COMFY_MODELS_SHARED=1"} {
		if !strings.Contains(apiErr.Message, want) {
			t.Errorf("message %q does not mention %q", apiErr.Message, want)
		}
	}
}

func TestInstallPolicyFollowsTheFlagOnly(t *testing.T) {
	manager := quietManager(t)
	manager.cfg.ComfyURL = "http://127.0.0.1:8188"

	manager.cfg.ComfyModelsShared = false
	if policy := manager.InstallPolicy(); policy.Allowed {
		t.Errorf("policy = %+v, want refused while the flag is unset", policy)
	}

	manager.cfg.ComfyModelsShared = true
	policy := manager.InstallPolicy()
	if !policy.Allowed {
		t.Errorf("policy = %+v, want allowed for COMFY_MODELS_SHARED=1", policy)
	}
	if policy.ComfyHost != "127.0.0.1:8188" {
		t.Errorf("ComfyHost = %q, want the COMFY_URL host", policy.ComfyHost)
	}
}

// A refused install must not depend on the local filesystem: the same config on a host with
// and without the folders has to reach the same verdict.
func TestInstallPolicyIgnoresTheLocalFilesystem(t *testing.T) {
	// ComfyUI is installed and reported here; the policy still has the last word.
	manager := quietManagerAt(t, t.TempDir())
	manager.cfg.ComfyModelsShared = false

	if manager.InstallPolicy().Allowed {
		t.Error("a known ComfyUI directory must not enable installs on its own")
	}
}

// The wrong-machine cause is refused before a byte moves, so a write failure now means the
// folder itself is not writable. The hint has to say that, not "disk full or no permission".
func TestWriteFailureHintNamesTheFolderNotTheDisk(t *testing.T) {
	hint := installHint(errors.New("cannot write into ComfyUI's folders: mkdir /comfy/models: permission denied"))
	if !strings.Contains(hint, "mounted writable") || !strings.Contains(hint, "belongs to this user") {
		t.Errorf("hint = %q, want it to name the folder and its ownership", hint)
	}
	// The specific causes keep their own hints, even though they arrive wrapped now.
	if hint := installHint(errors.New("cannot write into ComfyUI's folders: no space left on device")); !strings.Contains(hint, "disk space") {
		t.Errorf("hint = %q, want the disk-space hint to win", hint)
	}
	if hint := installHint(errors.New("cannot write into ComfyUI's folders: disk quota exceeded")); !strings.Contains(hint, "quota") {
		t.Errorf("hint = %q, want the quota hint", hint)
	}
	if hint := installHint(errors.New("this release is missing a bundled file: open bundles/x: no such file")); !strings.Contains(hint, "reinstall") {
		t.Errorf("hint = %q, want the reinstall hint", hint)
	}
}

// A target folder that cannot be created stops the job with a message naming the folder —
// including when the cause is "not there", which used to be neither fatal nor explained.
func TestInstallStopsWhenTheTargetFolderCannotBeCreated(t *testing.T) {
	// A regular file where the ComfyUI directory should be: MkdirAll fails with ENOTDIR.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := quietManagerAt(t, filepath.Join(blocker, "ComfyUI"))
	manager.index = &Index{Resources: []*Resource{{
		ID:      "m1",
		Type:    "model",
		Label:   "Model",
		Target:  "checkpoints/m1.safetensors",
		Sources: []any{map[string]any{"url": "http://127.0.0.1:1/m1.safetensors"}},
	}}}

	job, err := manager.Install([]string{"m1"}, nil)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := manager.GetJob(job.ID)
		if snapshot != nil && snapshot.Status != "running" {
			if len(snapshot.Items) != 1 {
				t.Fatalf("items = %d, want 1", len(snapshot.Items))
			}
			item := snapshot.Items[0]
			if item.Status != "error" {
				t.Fatalf("item status = %q (%s), want error", item.Status, item.Error)
			}
			if !strings.Contains(item.Error, "cannot write into ComfyUI's folders") {
				t.Errorf("item error = %q, want it to name the folder", item.Error)
			}
			if !strings.Contains(item.Hint, "mounted writable") {
				t.Errorf("item hint = %q, want actionable advice", item.Hint)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the install job did not finish within 5s")
}

// Without a reported folder layout there is nowhere to write, and the refusal has to name
// what ComfyUI failed to report.
func TestInstallRefusesWhenTheComfyUIDirectoryIsUnknown(t *testing.T) {
	// No folder layout was reported: nothing knows where ComfyUI lives.
	manager := quietManagerAt(t, "")
	manager.index = &Index{Resources: []*Resource{{
		ID:      "m1",
		Type:    "model",
		Label:   "Model",
		Target:  "checkpoints/m1.safetensors",
		Sources: []any{"http://127.0.0.1:1/m1.safetensors"},
	}}}

	job, err := manager.Install([]string{"m1"}, nil)
	if job != nil {
		t.Fatalf("job = %v, want a refusal", job)
	}
	var apiErr *httpx.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "install_root_unknown" {
		t.Fatalf("err = %v, want install_root_unknown", err)
	}
	for _, want := range []string{"custom_nodes", "/internal/folder_paths"} {
		if !strings.Contains(apiErr.Message, want) {
			t.Errorf("message %q does not mention %q", apiErr.Message, want)
		}
	}
}
