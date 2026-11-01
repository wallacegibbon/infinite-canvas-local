package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An agent that dies before `ready` used to be reported as a successful connect: `waitReady` returned nil
// on EOF, `Connect` filed an empty session, and the socket sent `models: []` to a page whose agent was
// already gone. The only account of why is the child's stderr, so that is what the error has to carry.
func TestConnectFailsWithTheAgentsLastWords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dying-agent")
	script := "#!/bin/sh\necho 'model.conf: unknown model \"a/b\"' >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}

	sessions := NewSessions(nil)
	connected, err := sessions.Connect(context.Background(), "dying", SpawnOptions{Binary: path})
	if err == nil {
		sessions.Close("dying", "test over")
		t.Fatalf("connect reported success: %+v", connected)
	}
	if !strings.Contains(err.Error(), "stopped before it was ready") {
		t.Errorf("error = %q, want it to say the agent stopped before ready", err)
	}
	if !strings.Contains(err.Error(), "unknown model") {
		t.Errorf("error = %q, want the agent's stderr in it", err)
	}
	// Nothing is filed: a caller that gets an error has no session, so a later connect must start fresh.
	if list := sessions.List(); len(list) != 0 {
		t.Errorf("a failed connect filed %d session(s)", len(list))
	}
}

// A process that ends without a word still has to produce a sentence rather than an empty one.
func TestConnectFailsWhenTheAgentSaysNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "silent-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	sessions := NewSessions(nil)
	if _, err := sessions.Connect(context.Background(), "silent", SpawnOptions{Binary: path}); err == nil {
		sessions.Close("silent", "test over")
		t.Fatal("connect reported success for an agent that never spoke")
	} else if !strings.Contains(err.Error(), "ready") {
		t.Errorf("error = %q", err)
	}
}
