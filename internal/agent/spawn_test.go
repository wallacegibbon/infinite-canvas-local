package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// These two runs use the real agent, because the things they check are its behaviour and not ours:
// what a startup looks like, and what it does with a model name that no longer exists. They skip
// when the binary is not installed — this repository ships a gateway, not an agent.

func testCore(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("ALAYACORE_BIN"); path != "" {
		return path
	}
	path, err := exec.LookPath("alayacore")
	if err != nil {
		t.Skip("alayacore is not on PATH; set ALAYACORE_BIN to run this")
	}
	return path
}

// readStartup reads frames until the session reports ready, returning the system messages it saw.
func readStartup(t *testing.T, proc *Process) []map[string]any {
	t.Helper()
	var messages []map[string]any
	for {
		frame, err := ReadFrame(proc.Stdout)
		if err != nil {
			t.Fatalf("read: %v (stderr: %s)", err, proc.Stderr.Last())
		}
		if frame == nil {
			t.Fatalf("the agent closed before it was ready (stderr: %s)", proc.Stderr.Last())
		}
		if frame.Tag != TagSystemMsg {
			continue
		}
		var envelope struct {
			Type string         `json:"type"`
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal([]byte(frame.Value), &envelope); err != nil {
			t.Fatalf("system message is not JSON: %v", err)
		}
		messages = append(messages, map[string]any{"type": envelope.Type, "data": envelope.Data})
		if envelope.Type == "session" && envelope.Data["state"] == "ready" {
			return messages
		}
	}
}

func TestSpawnStartsTheAgentAndReportsItsModel(t *testing.T) {
	proc, err := Spawn(SpawnOptions{Binary: testCore(t), ConfigPath: t.TempDir()})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	defer proc.Release()

	messages := readStartup(t, proc)

	var version, model map[string]any
	for _, message := range messages {
		switch message["type"] {
		case "version":
			version = message["data"].(map[string]any)
		case "model":
			model = message["data"].(map[string]any)
		}
	}
	if version == nil {
		t.Fatalf("no version frame; the message protocol cannot be checked at startup: %v", messages)
	}
	if version["message_version"] == nil || version["core_version"] == nil {
		t.Errorf("version frame = %v", version)
	}
	// A fresh config directory comes with the agent's own default, which is what the canvas shows
	// when nothing has been configured yet.
	if model == nil || model["active_name"] == "" {
		t.Errorf("model frame = %v; the canvas needs the active model to display it", model)
	}
}
