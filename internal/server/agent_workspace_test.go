package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A workspace key ends up as a directory under DATA_DIR and as a path segment in the MCP URL, so its
// length cannot be whatever the client typed. The readable prefix is capped at 40 bytes and the hash
// covers the whole name, which is what keeps two long names apart.
func TestAgentWorkspaceKeyIsBoundedAndUnique(t *testing.T) {
	long := strings.Repeat("canvas-UyrJZS8t68LWjeruICYOR-", 20)
	key := agentWorkspaceKey(long)

	prefix, _, found := strings.Cut(key, "-")
	if !found {
		t.Fatalf("key = %q, want a readable prefix and a hash", key)
	}
	parts := strings.Split(key, "-")
	suffix := parts[len(parts)-1]
	if len(suffix) != 8 {
		t.Errorf("hash = %q, want 8 hex characters", suffix)
	}
	if len(key) > 49 {
		t.Errorf("key = %d bytes (%q), want at most 40 + 1 + 8", len(key), key)
	}
	if len(prefix) > 40 {
		t.Errorf("readable prefix = %d bytes, want at most 40", len(prefix))
	}
	if key != agentWorkspaceKey(long) {
		t.Error("the key is not deterministic")
	}

	// Two names that agree for their first hundred characters are still two names.
	other := long[:len(long)-1] + "X"
	if agentWorkspaceKey(other) == key {
		t.Error("two different session names produced one key")
	}

	// Everything a URL or a file name would object to becomes a dash, and the result stays a key.
	awkward := agentWorkspaceKey("canvas:/../*? 名字")
	if strings.ContainsAny(awkward, "/:*?") {
		t.Errorf("key = %q, want the path-unsafe characters replaced", awkward)
	}
	if strings.HasPrefix(awkward, ".") {
		t.Errorf("key = %q, must not start with a dot", awkward)
	}
}

// Startup clears what per-session config copies are worth clearing: the workspace tree of the current
// layout, and any per-session directory a previous layout left behind. Conversations survive — they are
// the user's words — and nothing else in the data directory is touched.
func TestClearAgentWorkspacesKeepsConversationsAndDropsOldLayouts(t *testing.T) {
	dataDir := t.TempDir()
	agentDir := filepath.Join(dataDir, "agent")
	files := []string{
		filepath.Join(agentDir, "workspaces", "canvas-1-abcd1234", "model.conf"),
		filepath.Join(agentDir, "sessions", "canvas-1-abcd1234.session"),
		filepath.Join(agentDir, "canvas-7aa15835", "mcp.conf"), // the layout from before #100
		filepath.Join(dataDir, "tasks.json"),
	}
	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", file, err)
		}
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}

	clearAgentWorkspaces(dataDir)

	for _, gone := range []string{
		filepath.Join(agentDir, "workspaces"),
		filepath.Join(agentDir, "canvas-7aa15835"),
	} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s survived startup", gone)
		}
	}
	for _, kept := range []string{
		filepath.Join(agentDir, "sessions", "canvas-1-abcd1234.session"),
		filepath.Join(dataDir, "tasks.json"),
	} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s should have been kept: %v", kept, err)
		}
	}
}
