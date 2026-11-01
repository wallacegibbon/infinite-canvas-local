package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAgent is a stand-in for the real binary that speaks the same wire, so these tests describe the
// gateway's translation rather than a model's behaviour. The protocol facts it encodes (an api key in
// model_list, a CO per command, a task that ends the prompt) were observed on the real agent.
const fakeAgent = `#!/bin/sh
printf 'SM{"type":"version","data":{"message_version":18,"core_version":"fake-1"}}\n'
printf 'SM{"type":"model_list","data":{"models":[{"id":1,"name":"Fake / one","protocol_type":"openai","base_url":"http://127.0.0.1:1/v1","api_key":"secret-key","model_name":"m","context_limit":1000},{"id":2,"name":"Fake / two","protocol_type":"openai","base_url":"http://127.0.0.1:1/v1","api_key":"secret-key-2","model_name":"m2","context_limit":2000}]}}\n'
printf 'SM{"type":"model","data":{"active_id":1,"active_name":"Fake / one","context_limit":1000}}\n'
printf 'SM{"type":"reasoning_level","data":{"level":1}}\n'
printf 'SM{"type":"session","data":{"state":"ready"}}\n'
while IFS= read -r line; do
  [ -n "$FAKE_LOG" ] && printf '%s\n' "$line" >> "$FAKE_LOG"
  case "$line" in
    CI*'"name":"model_set"'*)
      case "$line" in
        *'"args":"2"'*)
          printf 'SM{"type":"model","data":{"active_id":2,"active_name":"Fake / two","context_limit":2000}}\n'
          printf 'CO{"command_id":"1","result":{"active_id":2,"active_name":"Fake / two"}}\n' ;;
        *)
          printf 'CO{"command_id":"1","is_error":true,"result":{"code":"REQUEST_MODEL_UNKNOWN","message":"model_set: model not found: 9"}}\n' ;;
      esac ;;
    CI*'"name":"reasoning_level_set"'*)
      printf 'SM{"type":"reasoning_level","data":{"level":2}}\n'
      printf 'CO{"command_id":"1","result":{"level":2}}\n' ;;
    UE)
      printf 'SM{"type":"task","data":{"in_progress":true,"current_step":1,"context_tokens":5}}\n'
      printf 'Ax{"id":"2","type":"text","text":"hello "}\n'
      printf 'Ax{"id":"2","type":"text","text":"world"}\n'
      printf 'SM{"type":"task","data":{"in_progress":false,"context_tokens":9}}\n' ;;
  esac
done
`

// recorder is the session's watcher in these tests: it keeps every event and lets a test wait for the
// one it is about, because the answer arrives as the agent produces it rather than as a return value.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) sink(event Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func (r *recorder) waitFor(t *testing.T, what string, match func(Event) bool) Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range r.all() {
			if match(event) {
				return event
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; events: %+v", what, r.all())
	return Event{}
}

// fakeSession is one live session plus the pieces a test asks it about.
type fakeSession struct {
	sessions *Sessions
	session  *Session
	watcher  *recorder
	log      string
}

// openFakeSession is the live path a socket takes: one session filed under a name, its startup
// broadcast read before it is ready, and the watcher attached before anything is said.
func openFakeSession(t *testing.T) fakeSession {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte(fakeAgent), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	log := filepath.Join(t.TempDir(), "frames.log")
	t.Setenv("FAKE_LOG", log)

	sessions := NewSessions(nil)
	t.Cleanup(func() { sessions.Close("test", "test over") })
	connected, err := sessions.Connect(context.Background(), "test", SpawnOptions{Binary: path})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	watcher := &recorder{}
	if !connected.Session.Watch(watcher.sink) {
		t.Fatal("the session refused its first watcher")
	}
	return fakeSession{sessions: sessions, session: connected.Session, watcher: watcher, log: log}
}

func TestSessionStreamsTheAnswerAndKeepsKeysHome(t *testing.T) {
	fs := openFakeSession(t)
	session, watcher, log := fs.session, fs.watcher, fs.log
	if err := session.Send(Request{Prompt: "hi"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	done := watcher.waitFor(t, "the end of the turn", func(event Event) bool { return event.Type == "done" })

	var text strings.Builder
	for _, event := range watcher.all() {
		if event.Type == "delta" {
			text.WriteString(event.Text)
		}
	}
	if text.String() != "hello world" {
		t.Errorf("deltas = %q, want %q", text.String(), "hello world")
	}
	// `done` marks the end of the turn; the answer is the deltas, and only the deltas.
	if done.Text != "" {
		t.Errorf("done = %v, want a marker rather than the answer again", done)
	}

	// The prompt went out as one user message, and nothing else was sent.
	frames, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read frames: %v", err)
	}
	if !strings.Contains(string(frames), `"hi"`) {
		t.Errorf("stdin frames = %q, want the prompt", string(frames))
	}
	if !strings.HasSuffix(string(frames), "UE\n") {
		t.Errorf("the prompt was not flushed with UE: %q", string(frames))
	}

	// Every event the canvas can see, as the transport would encode it.
	encoded, _ := json.Marshal(watcher.all())
	if strings.Contains(string(encoded), "secret-key") || strings.Contains(string(encoded), "127.0.0.1:1") {
		t.Errorf("the agent's credentials reached the events: %s", encoded)
	}
}

// A setting change is the same stream as a question: one command, no prompt, and the agent's own
// frames are the whole result.
func TestSessionRelaysACommandAndTheAgentsStatement(t *testing.T) {
	fs := openFakeSession(t)
	session, watcher, log := fs.session, fs.watcher, fs.log
	if err := session.Send(Request{Command: "model_set", Args: "2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	answered := watcher.waitFor(t, "the agent's answer to the command", func(event Event) bool {
		return event.Type == "command"
	})
	if !answered.OK {
		t.Errorf("the command was reported as failed: %+v", answered)
	}
	announced := watcher.waitFor(t, "the agent's own model frame", func(event Event) bool {
		return event.Type == "model" && event.Active != nil && event.Active.ID == 2
	})
	if announced.Active == nil || announced.Active.Name != "Fake / two" {
		t.Errorf("the agent's SM model was not relayed: %+v", announced)
	}

	frames, _ := os.ReadFile(log)
	if !strings.Contains(string(frames), `"name":"model_set"`) || !strings.Contains(string(frames), `"args":"2"`) {
		t.Fatalf("the command was not forwarded: %q", string(frames))
	}
	if strings.Contains(string(frames), "UE") {
		t.Errorf("a bare command must not flush an empty prompt: %q", string(frames))
	}
}

func TestSessionRelaysARejection(t *testing.T) {
	fs := openFakeSession(t)
	session, watcher := fs.session, fs.watcher
	if err := session.Send(Request{Command: "model_set", Args: "99"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	rejected := watcher.waitFor(t, "the agent's rejection", func(event Event) bool {
		return event.Type == "command" && !event.OK
	})
	if !strings.Contains(rejected.Message, "model not found") {
		t.Errorf("the agent's message was not relayed: %+v", rejected)
	}
}

// A watcher that goes away leaves the agent's words in the log rather than in a failed write: the pump
// keeps reading (a goroutine inside `read` cannot be interrupted) and simply has nobody to publish to,
// so the next thing the agent says is dropped instead of detaching the session.
func TestSessionWithoutAWatcherDropsTheEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte(fakeAgent), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	sessions := NewSessions(nil)
	t.Cleanup(func() { sessions.Close("lonely", "test over") })
	connected, err := sessions.Connect(context.Background(), "lonely", SpawnOptions{Binary: path})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	watcher := &recorder{}
	if !connected.Session.Watch(watcher.sink) {
		t.Fatal("the session refused its first watcher")
	}
	connected.Session.Unwatch()

	if err := connected.Session.Send(Request{Prompt: "hi"}); err != nil {
		t.Fatalf("send to a session nobody is watching: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if events := watcher.all(); len(events) != 0 {
		t.Errorf("a detached watcher still received %d event(s): %+v", len(events), events)
	}
}
