package server

// The agent routes, through the HTTP layer. The fake agent speaks the same wire the real one does, so
// these tests describe the translation: frames in, messages out, and nothing invented on the way.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"comfyui-gateway/internal/comfy"
)

const fakeAgentScript = `#!/bin/sh
printf '%s\n' "$$" >> "$FAKE_SPAWN_LOG"
printf 'SM{"type":"version","data":{"message_version":18,"core_version":"fake-1"}}\n'
printf 'SM{"type":"model_list","data":{"models":[{"id":1,"name":"Fake / one","protocol_type":"openai","base_url":"http://127.0.0.1:1/v1","api_key":"secret-key","model_name":"m","context_limit":1000},{"id":2,"name":"Fake / two","protocol_type":"openai","base_url":"http://127.0.0.1:1/v1","api_key":"secret-key-2","model_name":"m2","context_limit":2000}]}}\n'
printf 'SM{"type":"model","data":{"active_id":1,"active_name":"Fake / one","context_limit":1000}}\n'
printf 'SM{"type":"reasoning_level","data":{"level":1}}\n'
printf 'SM{"type":"session","data":{"state":"ready"}}\n'
while IFS= read -r line; do
  case "$line" in
    CI*'"name":"model_set"'*)
      case "$line" in
        *'"args":"2"'*)
          printf 'SM{"type":"model","data":{"active_id":2,"active_name":"Fake / two","context_limit":2000}}\n'
          printf 'CO{"command_id":"1","result":{"active_id":2,"active_name":"Fake / two"}}\n' ;;
        *)
          printf 'CO{"command_id":"1","is_error":true,"result":{"code":"REQUEST_MODEL_UNKNOWN","message":"model_set: model not found: 99"}}\n' ;;
      esac ;;
    UX*'die'*)
      exit 0 ;;
    UE)
      printf 'SM{"type":"task","data":{"in_progress":true,"current_step":1,"context_tokens":5}}\n'
      printf 'AF{"call_id":"call-1","name":"read_file","id":"3"}\n'
      printf 'AF{"call_id":"call-1","input":{"path":"/skills/SKILL.md"},"id":"3"}\n'
      printf 'UF{"call_id":"call-1","output":[{"type":"text","text":"a file"}],"id":"4"}\n'
      printf 'Ax{"id":"2","type":"text","text":"hello "}\n'
      printf 'Ax{"id":"2","type":"text","text":"world"}\n'
      printf 'SM{"type":"task","data":{"in_progress":false,"context_tokens":9}}\n' ;;
  esac
done
`

// startAgentGateway is a gateway whose agent is the fake binary, plus the file it appends its pid to.
func startAgentGateway(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-agent")
	if err := os.WriteFile(binary, []byte(fakeAgentScript), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	spawnLog := filepath.Join(dir, "spawned")
	t.Setenv("FAKE_SPAWN_LOG", spawnLog)
	// config.Load reads these; newTestGateway builds the config inside the call below.
	t.Setenv("ALAYACORE_BIN", binary)
	t.Setenv("AGENT_CONFIG_PATH", t.TempDir())

	gateway := httptest.NewServer(newTestGateway(t, newFakeComfy(t)).Handler())
	t.Cleanup(gateway.Close)
	return gateway, spawnLog
}

// spawns counts how many agent processes this gateway has started.
func spawns(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(string(raw)), "\n"))
}

// openSocket dials the conversation and drains the startup broadcast, which is what a client does
// before it says anything.
func openSocket(t *testing.T, gateway *httptest.Server, session string) *comfy.WSConn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(gateway.URL, "http") + "/api/agent/socket/" + session
	conn, err := comfy.DialWS(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if first := nextEvent(t, conn); first["type"] != "models" {
		t.Fatalf("first message = %v, want the agent's startup broadcast", first)
	}
	return conn
}

// nextEvent reads one message from the socket.
func nextEvent(t *testing.T, conn *comfy.WSConn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := conn.ReadMessage(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatalf("message is not JSON: %v (%q)", err, raw)
	}
	return event
}

// textOf is the answer as a client assembles it: the deltas, in order.
func textOf(events []map[string]any) string {
	var text string
	for _, event := range events {
		if event["type"] == "delta" {
			text += event["text"].(string)
		}
	}
	return text
}

// until reads messages until one of this type arrives, and returns everything it saw.
func until(t *testing.T, conn *comfy.WSConn, kind string) []map[string]any {
	t.Helper()
	var seen []map[string]any
	for i := 0; i < 200; i++ {
		event := nextEvent(t, conn)
		seen = append(seen, event)
		if event["type"] == kind {
			return seen
		}
	}
	t.Fatalf("no %q among %v", kind, seen)
	return nil
}

func send(t *testing.T, conn *comfy.WSConn, message map[string]any) {
	t.Helper()
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := conn.WriteText(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSocketStartsWithTheModelList(t *testing.T) {
	gateway, _ := startAgentGateway(t)
	url := "ws" + strings.TrimPrefix(gateway.URL, "http") + "/api/agent/socket/canvas-1"
	conn, err := comfy.DialWS(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	event := nextEvent(t, conn)
	if event["type"] != "models" {
		t.Fatalf("first message = %v, want models", event)
	}
	models, _ := event["models"].([]any)
	if len(models) != 2 {
		t.Errorf("models = %v, want the agent's list", models)
	}
	if active, _ := event["active"].(map[string]any); active["name"] != "Fake / one" {
		t.Errorf("active = %v, want the agent's own choice", event["active"])
	}
	// The agent's keys travel in SM model_list and must not reach the canvas.
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "secret-key") || strings.Contains(string(encoded), "127.0.0.1:1") {
		t.Errorf("credentials reached the client: %s", encoded)
	}
}

func TestSocketAnswersAPrompt(t *testing.T) {
	gateway, _ := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	send(t, conn, map[string]any{"prompt": "hi"})
	seen := until(t, conn, "done")

	if text := textOf(seen); text != "hello world" {
		t.Fatalf("deltas = %q, want the answer", text)
	}
	// And the end of the turn carries no second copy of it.
	if last := seen[len(seen)-1]; last["text"] != nil {
		t.Fatalf("done = %v, want a marker rather than the answer again", last)
	}
}

// TestSocketAnswersAgainOnTheSameConnection is what a conversation is: the second message goes to the
// same process, so the agent still has the first in front of it.
func TestSocketAnswersAgainOnTheSameConnection(t *testing.T) {
	gateway, spawnLog := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	for turn := 1; turn <= 2; turn++ {
		send(t, conn, map[string]any{"prompt": "hi"})
		if seen := until(t, conn, "done"); textOf(seen) != "hello world" {
			t.Fatalf("turn %d = %v", turn, seen)
		}
	}
	if got := spawns(t, spawnLog); got != 1 {
		t.Fatalf("spawned %d agents for one conversation, want 1", got)
	}
}

// TestSocketIsOnePerCanvas: a second connection to the same name is refused with a message, because two
// readers would take half the frames each.
func TestSocketIsOnePerCanvas(t *testing.T) {
	gateway, _ := startAgentGateway(t)
	first := openSocket(t, gateway, "canvas-1")

	url := "ws" + strings.TrimPrefix(gateway.URL, "http") + "/api/agent/socket/canvas-1"
	second, err := comfy.DialWS(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer second.Close()

	event := nextEvent(t, second)
	if event["type"] != "error" {
		t.Fatalf("second socket got %v, want a refusal", event)
	}

	// The first connection is unaffected: it is still the one being answered.
	send(t, first, map[string]any{"prompt": "hi"})
	if seen := until(t, first, "done"); textOf(seen) != "hello world" {
		t.Fatalf("first connection stopped working: %v", seen)
	}
}

func TestSocketRelaysASettingChange(t *testing.T) {
	gateway, _ := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	send(t, conn, map[string]any{"command": "model_set", "args": "2"})
	seen := until(t, conn, "command")

	var announced, answered bool
	for _, event := range seen {
		switch event["type"] {
		case "model":
			active, _ := event["active"].(map[string]any)
			announced = active["id"] == float64(2)
		case "command":
			answered = event["ok"] == true
		}
	}
	if !announced || !answered {
		t.Fatalf("messages = %v, want the agent's SM model and a successful CO", seen)
	}

	// A rejection is relayed as the agent worded it; the gateway has no opinion to add.
	send(t, conn, map[string]any{"command": "model_set", "args": "99"})
	seen = until(t, conn, "command")
	message, _ := seen[len(seen)-1]["message"].(string)
	if !strings.Contains(message, "model not found: 99") {
		t.Fatalf("messages = %v, want the agent's rejection", seen)
	}
}

// TestSocketRelaysTheToolItUsed: a built-in tool runs inside AlayaCore, so the call and its result are
// both frames coming back. The gateway neither answers nor interprets them — it draws them.
func TestSocketRelaysTheToolItUsed(t *testing.T) {
	gateway, _ := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	send(t, conn, map[string]any{"prompt": "read it"})
	seen := until(t, conn, "done")

	var named, withInput, result bool
	for _, event := range seen {
		switch event["type"] {
		case "tool_call":
			if event["name"] == "read_file" {
				named = true
			}
			if input, _ := event["input"].(string); strings.Contains(input, "SKILL.md") {
				withInput = true
			}
		case "tool_result":
			result = event["output"] == "a file"
		}
	}
	if !named || !withInput || !result {
		t.Fatalf("messages = %v, want the call, its arguments and its result", seen)
	}
}

// TestSocketCloseReleasesTheAgent: closing the conversation is the one thing that ends an agent, and the
// only way a canvas can say "I am done with this" rather than "I am not looking right now".
func TestSocketCloseReleasesTheAgent(t *testing.T) {
	gateway, spawnLog := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")
	if got := spawns(t, spawnLog); got != 1 {
		t.Fatalf("spawned %d agents, want 1", got)
	}

	send(t, conn, map[string]any{"close": true})
	// The socket ends, and the process with it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ReadMessage(ctx); err == nil {
		t.Fatal("the socket stayed open after close")
	}

	// A new connection to the same name starts a new agent, which is what releasing it means.
	openSocket(t, gateway, "canvas-1")
	if got := spawns(t, spawnLog); got != 2 {
		t.Fatalf("spawned %d agents after closing, want a new one", got)
	}
}

// TestSocketGoingAwayKeepsTheAgent is the other half: a connection that simply drops is not the user
// closing the conversation, so the process — and its history — is still there for the next one.
func TestSocketGoingAwayKeepsTheAgent(t *testing.T) {
	gateway, spawnLog := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")
	if got := spawns(t, spawnLog); got != 1 {
		t.Fatalf("spawned %d agents, want 1", got)
	}

	_ = conn.Close()
	time.Sleep(200 * time.Millisecond)

	openSocket(t, gateway, "canvas-1")
	if got := spawns(t, spawnLog); got != 1 {
		t.Fatalf("spawned %d agents after a dropped connection, want the same one", got)
	}
}

// TestSocketReportsADeadProcess: an agent killed under a live conversation has to be reported, not
// waited on.
func TestSocketReportsADeadProcess(t *testing.T) {
	gateway, _ := startAgentGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	// The fake agent exits on this prompt, which is what a crash looks like from here.
	send(t, conn, map[string]any{"prompt": "die"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, err := conn.ReadMessage(ctx); err != nil {
			return // the socket closed: the conversation is over and the client knows
		}
	}
}
