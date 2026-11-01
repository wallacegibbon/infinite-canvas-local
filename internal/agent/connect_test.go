package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// slowAgent says `ready` only after a second, which is what a real one does while it loads its models.
const slowAgent = `#!/bin/sh
printf 'SM{"type":"model_list","data":{"models":[]}}\n'
sleep 1
printf 'SM{"type":"session","data":{"state":"ready"}}\n'
while IFS= read -r line; do :; done
`

func writeAgent(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write agent: %v", err)
	}
	return path
}

// `List` is how an MCP tool call finds the session it came from, so it must never wait behind a spawn:
// a canvas whose agent is loading its models would otherwise freeze every other panel's tools.
func TestListDoesNotWaitForASpawn(t *testing.T) {
	sessions := NewSessions(nil)
	path := writeAgent(t, slowAgent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		connected, err := sessions.Connect(context.Background(), "slow", SpawnOptions{Binary: path})
		if err == nil {
			sessions.Close(connected.Session.ID, "test over")
		}
	}()

	// Give the spawn a moment to be inside its startup wait, then ask for the list.
	time.Sleep(150 * time.Millisecond)
	started := time.Now()
	_ = sessions.List()
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond {
		t.Errorf("List took %v while a session was spawning; the lock is held across the spawn", elapsed)
	}
	<-done
}

// Two connects for one name: one spawn wins, the other releases what it started, and both callers get
// the same session. Run under -race, this also covers reading the session's startup state while its pump
// is running.
func TestConcurrentConnectsFileOneSession(t *testing.T) {
	sessions := NewSessions(nil)
	t.Cleanup(func() { sessions.Close("same", "test over") })
	path := writeAgent(t, fakeAgent)

	results := make(chan Connected, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			connected, err := sessions.Connect(context.Background(), "same", SpawnOptions{Binary: path})
			if err != nil {
				errs <- err
				return
			}
			results <- connected
		}()
	}

	var first, second Connected
	for range 2 {
		select {
		case connected := <-results:
			if first.Session == nil {
				first = connected
			} else {
				second = connected
			}
		case err := <-errs:
			t.Fatalf("connect: %v", err)
		}
	}
	if first.Session != second.Session {
		t.Fatalf("two connects for one name returned two sessions")
	}
	if list := sessions.List(); len(list) != 1 {
		t.Errorf("filed %d sessions under one name", len(list))
	}
}

// A second connect while a turn is streaming: it answers with the same session and its startup state,
// which is read under the state lock (the pump is writing it at the same time — this is the -race case).
func TestConnectWhileATurnStreamsReadsTheState(t *testing.T) {
	fs := openFakeSession(t)
	if err := fs.session.Send(Request{Prompt: "hi"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	fs.watcher.waitFor(t, "the first delta", func(event Event) bool { return event.Type == "delta" })

	connected, err := fs.sessions.Connect(context.Background(), fs.session.ID, SpawnOptions{})
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if connected.Session != fs.session {
		t.Error("a second connect made a second session for one name")
	}
	if connected.Startup.Type != "models" || len(connected.Startup.Models) == 0 {
		t.Errorf("startup = %+v, want the agent's own model list", connected.Startup)
	}
}
