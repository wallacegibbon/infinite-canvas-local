package resource

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBakeResourceReportsTheRealCommand(t *testing.T) {
	root := t.TempDir()
	comfyRoot := filepath.Join(root, "ComfyUI")
	manager := quietManagerAt(t, comfyRoot)
	manager.cfg.RootDir = root
	manager.cfg.ComfyPython = "/venv/bin/python"
	manager.index = &Index{Resources: []*Resource{
		{ID: "moss_image_base_bf16", Type: "model", Target: "diffusion_models/moss_image_base_bf16.safetensors"},
		{ID: "moss_image_sft_bf16", Type: "model", Target: "diffusion_models/moss_image_sft_bf16.safetensors"},
	}}

	bake := &Resource{
		ID:       "moss_balanced_s100",
		Type:     "bake",
		Target:   "diffusion_models/moss_balanced_s100.safetensors",
		Requires: []string{"moss_image_base_bf16", "moss_image_sft_bf16"},
		Builder:  map[string]any{"preset": "Balanced", "strength": float64(1)},
	}

	err := manager.bakeRequiredError(bake, manager.ComfyRoot())
	if err == nil {
		t.Fatal("bake resource produced no error")
	}

	// The panel prints the summary next to the failed item and offers the command for
	// copying, so both halves matter: a vague reason and a pasteable command.
	var manual *manualStepError
	if !errors.As(err, &manual) {
		t.Fatalf("error is %T, want *manualStepError so the command reaches the panel", err)
	}
	if manual.summary != "produced by a local merge, not by a download" {
		t.Errorf("summary = %q", manual.summary)
	}

	for _, want := range []string{
		"/venv/bin/python",
		"tools/bake-moss-preset.py",
		"--preset Balanced",
		"--strength 1",
		"--base " + comfyRoot + "/models/diffusion_models/moss_image_base_bf16.safetensors",
		"--sft " + comfyRoot + "/models/diffusion_models/moss_image_sft_bf16.safetensors",
		"--out " + comfyRoot + "/models/diffusion_models/moss_balanced_s100.safetensors",
	} {
		if !strings.Contains(manual.command, want) {
			t.Errorf("command is missing %q:\n%s", want, manual.command)
		}
	}
	// Inputs must keep the order the script expects: base first, then sft.
	if strings.Index(manual.command, "--base") > strings.Index(manual.command, "--sft") {
		t.Errorf("--base must precede --sft:\n%s", manual.command)
	}
}

func TestBakeResourceWithoutPythonStillHasACommand(t *testing.T) {
	manager := quietManager(t)
	manager.cfg.ComfyPython = ""
	manager.index = &Index{Resources: []*Resource{
		{ID: "base", Type: "model", Target: "diffusion_models/base.safetensors"},
	}}
	bake := &Resource{ID: "merged", Type: "bake", Target: "diffusion_models/merged.safetensors", Requires: []string{"base"}}

	err := manager.bakeRequiredError(bake, manager.ComfyRoot())
	var manual *manualStepError
	if !errors.As(err, &manual) {
		t.Fatalf("error is %T, want *manualStepError", err)
	}
	if !strings.HasPrefix(manual.command, "python3 ") {
		t.Errorf("command should fall back to python3, got:\n%s", manual.command)
	}
	// A single requirement fills --base only; --sft is left out rather than guessed.
	if strings.Contains(manual.command, "--sft") {
		t.Errorf("--sft should be omitted when there is only one input:\n%s", manual.command)
	}
}

func TestInstallNeverStartsWithABakeItem(t *testing.T) {
	manager := quietManager(t)
	manager.index = &Index{
		Resources: []*Resource{
			{ID: "base", Type: "model", Target: "diffusion_models/base.safetensors"},
			{ID: "merged", Type: "bake", Target: "diffusion_models/merged.safetensors", Requires: []string{"base"}},
		},
		Packs: []*Pack{{ID: "moss", Resources: []string{}, OptionalResources: []string{"base", "merged"}}},
	}

	job, err := manager.Install(nil, []string{"moss"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, item := range job.Items {
		if item.ID == "merged" {
			t.Fatalf("bake resource became a job item; a merge can never succeed inside a job")
		}
	}
	if len(job.Items) == 0 {
		t.Fatal("install selected nothing at all")
	}

	// Asking for the bake resource alone must explain itself rather than 400 with
	// "nothing selected".
	if _, err := manager.Install([]string{"merged"}, nil); err == nil {
		t.Fatal("installing a bake resource on its own returned no error")
	} else if !strings.Contains(err.Error(), "local merge") {
		t.Errorf("error should name the manual step, got: %v", err)
	}

	// The job runs in a goroutine that writes into the installation directory, so let it
	// reach a terminal state before the test ends: t.TempDir cleanup otherwise races with
	// it and fails with "directory not empty".
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if snapshot := manager.GetJob(job.ID); snapshot == nil || snapshot.Status != "running" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("the install job did not finish within 5s")
}
