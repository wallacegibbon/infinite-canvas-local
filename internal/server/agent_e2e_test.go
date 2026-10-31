package server

// The agent routes, through the HTTP layer. The fake agent speaks the same wire the real one does, so
// these tests describe the translation: frames in, events out, and nothing invented on the way.

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeAgentScript = `#!/bin/sh
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
    UX*die*)
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

// startAgentGateway is a gateway whose agent is the fake binary.
func startAgentGateway(t *testing.T) *httptest.Server {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(binary, []byte(fakeAgentScript), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	// config.Load reads these; newTestGateway builds the config inside the call below.
	t.Setenv("ALAYACORE_BIN", binary)
	t.Setenv("AGENT_CONFIG_PATH", t.TempDir())

	gateway := httptest.NewServer(newTestGateway(t, newFakeComfy(t)).Handler())
	t.Cleanup(gateway.Close)
	return gateway
}

// postForm sends a multipart form and returns the event stream, decoded.
func postForm(t *testing.T, url string, fields map[string]string) []map[string]any {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type = %q, want an event stream", got)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}

	var events []map[string]any
	for _, block := range strings.Split(string(raw), "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		data, ok := strings.CutPrefix(block, "data: ")
		if !ok {
			t.Fatalf("an event block is not `data: ...`: %q", block)
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			t.Fatalf("event is not JSON: %v (%q)", err, data)
		}
		events = append(events, event)
	}
	return events
}

// postStatus sends a form and returns only the status: for the cases where the answer is that there is
// no answer.
func postStatus(t *testing.T, url string, fields map[string]string) int {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	response, err := http.Post(url, writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode
}

// connect names an agent and returns the session id, which is the name itself: a canvas connects by
// saying who it is.
func connect(t *testing.T, url, name string) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("session", name); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	response, err := http.Post(url+"/api/agent/connect", writer.FormDataContentType(), &body)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer response.Body.Close()
	var connected struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(response.Body).Decode(&connected); err != nil {
		t.Fatalf("decode connect: %v", err)
	}
	if connected.SessionID == "" {
		t.Fatal("connect returned no session")
	}
	return connected.SessionID
}

func TestAgentConnectIsTheSameAgentEveryTime(t *testing.T) {
	gateway := startAgentGateway(t)

	// A canvas asks for its agent twice — the second time is a reloaded page looking for the
	// conversation it left. It has to be the same process, or the history is gone.
	first := connect(t, gateway.URL, "canvas-1")
	second := connect(t, gateway.URL, "canvas-1")
	if first != second {
		t.Fatalf("connect returned %q then %q, want one agent per name", first, second)
	}

	// And a different name is a different agent.
	if other := connect(t, gateway.URL, "canvas-2"); other == first {
		t.Fatalf("two names shared the agent %q", other)
	}
}

func TestAgentChatStreamsTheAnswer(t *testing.T) {
	gateway := startAgentGateway(t)
	session := connect(t, gateway.URL, "canvas-1")
	events := postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "hi", "session": session})

	var text string
	var sawModels, sawDone bool
	for _, event := range events {
		switch event["type"] {
		case "models":
			sawModels = true
			models, _ := event["models"].([]any)
			if len(models) != 2 {
				t.Errorf("models = %v, want the agent's list", models)
			}
		case "delta":
			text += event["text"].(string)
		case "done":
			sawDone = true
			if event["text"] != "hello world" {
				t.Errorf("done text = %v", event["text"])
			}
		}
	}
	if !sawModels || !sawDone || text != "hello world" {
		t.Fatalf("events = %v, want models, two deltas and done", events)
	}

	// The agent's keys travel in SM model_list and must not reach the canvas.
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "secret-key") || strings.Contains(string(encoded), "127.0.0.1:1") {
		t.Errorf("credentials reached the events: %s", encoded)
	}
}

// TestAgentAnswersAgainOnTheSameSession is what the process living means: the second question goes to
// the process the first one used, so the conversation carries on instead of starting over.
func TestAgentAnswersAgainOnTheSameSession(t *testing.T) {
	gateway := startAgentGateway(t)
	session := connect(t, gateway.URL, "canvas-1")

	for turn := 1; turn <= 2; turn++ {
		events := postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "hi", "session": session})
		if !answeredWith(events, "hello world") {
			t.Fatalf("turn %d events = %v, want an answer", turn, events)
		}
	}
}

func TestAgentCommandRelaysTheAgentsAnswer(t *testing.T) {
	gateway := startAgentGateway(t)
	session := connect(t, gateway.URL, "canvas-1")

	events := postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"command": "model_set", "args": "2", "session": session})
	var announced, answered bool
	for _, event := range events {
		switch event["type"] {
		case "model":
			active, _ := event["active"].(map[string]any)
			announced = active["id"] == float64(2)
		case "command":
			answered = event["ok"] == true
		}
	}
	if !announced || !answered {
		t.Fatalf("events = %v, want the agent's SM model and a successful CO", events)
	}

	// A rejection is relayed as the agent worded it; the gateway has no opinion to add. The second
	// command also proves a setting change left the agent connected, as it must.
	events = postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"command": "model_set", "args": "99", "session": session})
	var message string
	for _, event := range events {
		if event["type"] == "command" {
			message, _ = event["message"].(string)
		}
	}
	if !strings.Contains(message, "model not found: 99") {
		t.Fatalf("events = %v, want the agent's rejection", events)
	}
}

// TestAgentChatRefusesANameThatIsNotConnected is the other half of "one agent per canvas": a name that
// was never connected, or was closed, has to be reported before a stream starts.
func TestAgentChatRefusesANameThatIsNotConnected(t *testing.T) {
	gateway := startAgentGateway(t)

	// Once with no session at all, and once with a session that is not connected.
	for _, session := range []string{"", "canvas-never-connected"} {
		fields := map[string]string{"prompt": "hi"}
		if session != "" {
			fields["session"] = session
		}
		if status := postStatus(t, gateway.URL+"/api/agent/chat", fields); status != http.StatusConflict {
			t.Errorf("session %q: status = %d, want 409", session, status)
		}
	}
}

// TestAgentSessionEndsWhenTheProcessDoes: the process outliving the turn is what makes a conversation,
// so the one thing that must not happen is a session that is left filed after its process is gone. A
// write to a dead agent fails before any read would notice, and that is the case this pins down.
func TestAgentSessionEndsWhenTheProcessDoes(t *testing.T) {
	gateway := startAgentGateway(t)
	session := connect(t, gateway.URL, "canvas-1")

	// The fake agent exits on this prompt, which is what a crash looks like from the gateway's side.
	postStatus(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "die", "session": session})

	// The session is no longer filed, so the next question is refused instead of written into a pipe
	// with nothing on the other end.
	if status := postStatus(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "hi", "session": session}); status != http.StatusConflict {
		t.Fatalf("status = %d, want 409 once the process is gone", status)
	}
}

// TestAgentOnceNeverTouchesAConnectedAgent is the reason `/api/agent/once` exists.
//
// A canvas holds one agent for as long as the user keeps it open, and its history is the user's work.
// The buttons that rewrite a prompt or caption an image are single answers, so they must not be able
// to reach that agent — and the shape of the guarantee is that this route has no session at all. Here
// a session is handed to it anyway, which is what a buggy caller would do: the one-shot has to ignore
// it, spawn its own process, and leave the connected one still able to answer.
func TestAgentOnceNeverTouchesAConnectedAgent(t *testing.T) {
	gateway := startAgentGateway(t)

	session := connect(t, gateway.URL, "canvas-1")

	events := postForm(t, gateway.URL+"/api/agent/once", map[string]string{"prompt": "hi", "session": session})
	if !answeredWith(events, "hello world") {
		t.Fatalf("once events = %v, want the one-shot's own answer", events)
	}

	// Still connected, and still answering: a one-shot that had spent the session would fail here
	// with `session_not_connected`.
	events = postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "hi", "session": session})
	if !answeredWith(events, "hello world") {
		t.Fatalf("chat events = %v, want the connected agent to still answer", events)
	}
}

// TestAgentRelaysTheToolItUsed: a built-in tool runs inside AlayaCore, so the call and its result are
// both frames coming back. The gateway neither answers nor interprets them — it draws them, because a
// turn that reads a file is worth seeing.
func TestAgentRelaysTheToolItUsed(t *testing.T) {
	gateway := startAgentGateway(t)
	session := connect(t, gateway.URL, "canvas-1")
	events := postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "read it", "session": session})

	var named, withInput, result bool
	for _, event := range events {
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
		t.Fatalf("events = %v, want the call, its arguments and its result", events)
	}
}

// answeredWith reports whether the stream carried this answer to its end.
func answeredWith(events []map[string]any, want string) bool {
	for _, event := range events {
		if event["type"] == "done" && event["text"] == want {
			return true
		}
	}
	return false
}
