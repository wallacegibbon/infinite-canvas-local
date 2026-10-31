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
    UE)
      printf 'SM{"type":"task","data":{"in_progress":true,"current_step":1,"context_tokens":5}}\n'
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

func TestAgentChatStreamsTheAnswer(t *testing.T) {
	gateway := startAgentGateway(t)
	events := postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "hi"})

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

// TestAgentOnceNeverTouchesAConnectedAgent is the reason `/api/agent/once` exists.
//
// A canvas holds one agent for as long as the user keeps it open, and its history is the user's work.
// The buttons that rewrite a prompt or caption an image are single answers, so they must not be able
// to reach that agent — and the shape of the guarantee is that this route has no session at all. Here
// a session is handed to it anyway, which is what a buggy caller would do: the one-shot has to ignore
// it, spawn its own process, and leave the connected one still able to answer.
func TestAgentOnceNeverTouchesAConnectedAgent(t *testing.T) {
	gateway := startAgentGateway(t)

	response, err := http.Post(gateway.URL+"/api/agent/connect", "application/json", nil)
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

	events := postForm(t, gateway.URL+"/api/agent/once", map[string]string{"prompt": "hi", "session": connected.SessionID})
	if !answeredWith(events, "hello world") {
		t.Fatalf("once events = %v, want the one-shot's own answer", events)
	}

	// Still connected, and still answering: a one-shot that had spent the session would fail here
	// with `session_not_connected`.
	events = postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"prompt": "hi", "session": connected.SessionID})
	if !answeredWith(events, "hello world") {
		t.Fatalf("chat events = %v, want the connected agent to still answer", events)
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

func TestAgentCommandRelaysTheAgentsAnswer(t *testing.T) {
	gateway := startAgentGateway(t)

	events := postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"command": "model_set", "args": "2"})
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

	// A rejection is relayed as the agent worded it; the gateway has no opinion to add.
	events = postForm(t, gateway.URL+"/api/agent/chat", map[string]string{"command": "model_set", "args": "99"})
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
