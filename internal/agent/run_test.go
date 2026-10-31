package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
          printf 'CO{"command_id":"model","result":{"active_id":2,"active_name":"Fake / two"}}\n' ;;
        *)
          printf 'CO{"command_id":"model","is_error":true,"result":{"code":"REQUEST_MODEL_UNKNOWN","message":"model_set: model not found: 9"}}\n' ;;
      esac ;;
    CI*'"name":"reasoning_level_set"'*)
      printf 'SM{"type":"reasoning_level","data":{"level":2}}\n'
      printf 'CO{"command_id":"reasoning","result":{"level":2}}\n' ;;
    UE)
      printf 'SM{"type":"task","data":{"in_progress":true,"current_step":1,"context_tokens":5}}\n'
      printf 'Ax{"id":"2","type":"text","text":"hello "}\n'
      printf 'Ax{"id":"2","type":"text","text":"world"}\n'
      printf 'SM{"type":"task","data":{"in_progress":false,"context_tokens":9}}\n' ;;
  esac
done
`

func fakeAgentBinary(t *testing.T) (SpawnOptions, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(path, []byte(fakeAgent), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	log := filepath.Join(t.TempDir(), "frames.log")
	t.Setenv("FAKE_LOG", log)
	return SpawnOptions{Binary: path}, log
}

func TestRunStreamsTheAnswerAndKeepsKeysHome(t *testing.T) {
	opts, log := fakeAgentBinary(t)
	var events []Event
	if err := Run(context.Background(), opts, Request{Prompt: "hi", Reasoning: -1}, func(event Event) { events = append(events, event) }); err != nil {
		t.Fatalf("run: %v", err)
	}

	var text strings.Builder
	var done *Event
	for i := range events {
		switch events[i].Type {
		case "delta":
			text.WriteString(events[i].Text)
		case "done":
			done = &events[i]
		}
	}
	if text.String() != "hello world" {
		t.Errorf("deltas = %q, want %q", text.String(), "hello world")
	}
	if done == nil || done.Text != "hello world" {
		t.Errorf("done = %v, want the whole answer", done)
	}

	// The prompt went out as one user message, and nothing else was sent.
	frames, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read frames: %v", err)
	}
	if got := strings.TrimSpace(string(frames)); got != "UX{\"text\":\"hi\",\"type\":\"text\"}\nUE" && !strings.Contains(got, `"hi"`) {
		t.Errorf("stdin frames = %q, want the prompt and UE", got)
	}
	if !strings.HasSuffix(string(frames), "UE\n") {
		t.Errorf("the prompt was not flushed with UE: %q", string(frames))
	}

	// Every event the canvas can see, as the transport would encode it.
	encoded, _ := json.Marshal(events)
	if strings.Contains(string(encoded), "secret-key") || strings.Contains(string(encoded), "127.0.0.1:1") {
		t.Errorf("the agent's credentials reached the events: %s", encoded)
	}
}

// The two choices travel as the agent's own commands, ahead of the prompt on the same stream.
func TestRunSendsTheChoicesBeforeThePrompt(t *testing.T) {
	opts, log := fakeAgentBinary(t)
	var events []Event
	err := Run(context.Background(), opts, Request{Prompt: "hi", ModelID: 2, Reasoning: 2}, func(event Event) { events = append(events, event) })
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	frames, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(frames)), "\n")
	if len(lines) < 3 {
		t.Fatalf("frames = %q, want the two commands and the prompt", string(frames))
	}
	if !strings.Contains(lines[0], `"name":"model_set"`) || !strings.Contains(lines[0], `"args":"2"`) {
		t.Errorf("first frame = %q, want model_set with the chosen id", lines[0])
	}
	if !strings.Contains(lines[1], `"name":"reasoning_level_set"`) {
		t.Errorf("second frame = %q, want reasoning_level_set", lines[1])
	}
	if !strings.HasSuffix(string(frames), "UE\n") {
		t.Errorf("the prompt was not flushed with UE: %q", string(frames))
	}

	// The model list came back with the start of this very request: no separate endpoint, no cache.
	var models *Event
	for i := range events {
		if events[i].Type == "models" {
			models = &events[i]
		}
	}
	if models == nil || len(models.Models) != 2 || models.Active == nil || models.Active.ID != 1 {
		t.Fatalf("models event = %v, want the agent's own list and active model", models)
	}
}
