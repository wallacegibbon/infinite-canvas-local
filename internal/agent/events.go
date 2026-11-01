package agent

import (
	"context"
	"encoding/json"
	"strings"
)

// Event is a frame the canvas may draw, and nothing more. The names are the protocol's, not ours:
// the agent already decided what happened, this struct only carries it.
type Event struct {
	Type string `json:"type"`

	// delta, reasoning(delta), done
	Text string `json:"text,omitempty"`

	// done
	ElapsedMs int64 `json:"elapsedMs,omitempty"`

	// models: the startup broadcast of the spawn that produced this stream
	Models         []Model `json:"models,omitempty"`
	Active         *Model  `json:"active,omitempty"`
	Level          *int    `json:"level,omitempty"`
	CoreVersion    string  `json:"coreVersion,omitempty"`
	MessageVersion int     `json:"messageVersion,omitempty"`

	// command: the agent's answer to a CI frame
	OK bool `json:"ok,omitempty"`

	// steering: `queued` when a message sent into a running task was taken into the queue, `delivered`
	// when the agent has worked it into the conversation.
	Phase string `json:"phase,omitempty"`

	// tool_call, tool_result: what the agent did between two sentences. A built-in tool runs inside
	// AlayaCore, so both frames come back from it — the gateway relays them and answers neither.
	CallID string `json:"callId,omitempty"`
	Name   string `json:"name,omitempty"`
	Input  string `json:"input,omitempty"`
	Output string `json:"output,omitempty"`

	// tool: one of the gateway's own tools, on its way down to the canvas, which is the only thing that
	// can run it. The canvas answers with `{"toolResult":…}` on the same socket — see Session.CallTool.
	Arguments json.RawMessage `json:"arguments,omitempty"`

	// error: the agent's own words
	Message string `json:"message,omitempty"`

	// history: the conversation the agent loaded from its session file and replayed before it was
	// ready. A reloaded page draws this instead of an empty panel, and replaces whatever it had.
	History []HistoryEntry `json:"history,omitempty"`
}

// HistoryEntry is one thing that was said, as the agent replayed it. Only the words are kept: the
// reasoning blocks and the empty end-of-message markers are AlayaCore's own bookkeeping, and a picture
// is its own content item, which this does not draw yet.
type HistoryEntry struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Model is what `SM model_list` and `SM model` say, minus the two fields that must not travel: the
// frame carries `api_key` and `base_url` in cleartext.
type Model struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ProtocolType string `json:"protocolType,omitempty"`
	ContextLimit int    `json:"contextLimit,omitempty"`
}

// Startup is the model information the agent broadcasts before it accepts input.
type Startup = startup

// startup is the model information the agent broadcasts before it accepts input.
type startup struct {
	models         []Model
	active         *Model
	level          *int
	coreVersion    string
	messageVersion int
}

func (s *startup) collect(kind string, data json.RawMessage) {
	switch kind {
	case "model_list":
		var payload struct {
			Models []struct {
				ID           int    `json:"id"`
				Name         string `json:"name"`
				ProtocolType string `json:"protocol_type"`
				ContextLimit int    `json:"context_limit"`
			} `json:"models"`
		}
		if json.Unmarshal(data, &payload) != nil {
			return
		}
		s.models = make([]Model, 0, len(payload.Models))
		for _, model := range payload.Models {
			s.models = append(s.models, Model{ID: model.ID, Name: model.Name, ProtocolType: model.ProtocolType, ContextLimit: model.ContextLimit})
		}
	case "model":
		var payload struct {
			ActiveID     int    `json:"active_id"`
			ActiveName   string `json:"active_name"`
			ContextLimit int    `json:"context_limit"`
		}
		if json.Unmarshal(data, &payload) == nil {
			s.active = &Model{ID: payload.ActiveID, Name: payload.ActiveName, ContextLimit: payload.ContextLimit}
		}
	case "reasoning_level":
		var payload struct {
			Level int `json:"level"`
		}
		if json.Unmarshal(data, &payload) == nil {
			level := payload.Level
			s.level = &level
		}
	case "version":
		var payload struct {
			MessageVersion int    `json:"message_version"`
			CoreVersion    string `json:"core_version"`
		}
		if json.Unmarshal(data, &payload) == nil {
			s.messageVersion = payload.MessageVersion
			s.coreVersion = payload.CoreVersion
		}
	}
}

func (s *startup) event() Event {
	return Event{Type: "models", Models: s.models, Active: s.active, Level: s.level, CoreVersion: s.coreVersion, MessageVersion: s.messageVersion}
}

// systemMessage is the SM envelope: {"type":...,"data":{...}}.
func systemMessage(value string) (string, json.RawMessage, bool) {
	var envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal([]byte(value), &envelope) != nil {
		return "", nil, false
	}
	return envelope.Type, envelope.Data, true
}

// contentDelta is the Ax body: a content item, `text` or `reasoning`.
func contentDelta(value string) (kind, text string, ok bool) {
	var item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(value), &item) != nil {
		return "", "", false
	}
	return item.Type, item.Text, true
}

// sessionState is the SM session payload.
func sessionState(data json.RawMessage) string {
	var payload struct {
		State string `json:"state"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	return payload.State
}

// taskInProgress is the SM task payload; a task that ends is what ends a prompt.
func taskInProgress(data json.RawMessage) (done bool, known bool) {
	var payload struct {
		InProgress *bool `json:"in_progress"`
	}
	if json.Unmarshal(data, &payload) != nil || payload.InProgress == nil {
		return false, false
	}
	return !*payload.InProgress, true
}

// commandOutput is the CO body.
func commandOutput(value string) (commandID string, ok bool, message string) {
	var payload struct {
		CommandID string `json:"command_id"`
		IsError   bool   `json:"is_error"`
		Result    struct {
			Message string `json:"message"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(value), &payload) != nil {
		return "", false, ""
	}
	return payload.CommandID, !payload.IsError, payload.Result.Message
}

// toolCall is the AF body: one call arrives in two frames, the name first and the arguments in the
// second, sharing a call_id. Both are relayed as they come rather than joined, because the canvas can
// show a step before it knows what the step is doing.
func toolCall(value string) (Event, bool) {
	var payload struct {
		CallID string          `json:"call_id"`
		Name   string          `json:"name"`
		Input  json.RawMessage `json:"input"`
	}
	if json.Unmarshal([]byte(value), &payload) != nil || payload.CallID == "" {
		return Event{}, false
	}
	return Event{Type: "tool_call", CallID: payload.CallID, Name: payload.Name, Input: string(payload.Input)}, true
}

// toolResult is the UF body. The output is a list of content items; the text of the first is what
// there is to show, and a result that carries no text is still a result.
func toolResult(value string) (Event, bool) {
	var payload struct {
		CallID string `json:"call_id"`
		Output []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"output"`
	}
	if json.Unmarshal([]byte(value), &payload) != nil || payload.CallID == "" {
		return Event{}, false
	}
	text := ""
	if len(payload.Output) > 0 {
		text = payload.Output[0].Text
	}
	return Event{Type: "tool_result", CallID: payload.CallID, Output: text}, true
}

// systemError is the SM error payload, which is a plain sentence.
func systemError(data json.RawMessage) Event {
	var payload struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(data, &payload)
	return Event{Type: "error", Message: payload.Text}
}

// withAgent spawns, runs body, and releases. One agent, one action.
func withAgent(ctx context.Context, opts SpawnOptions, body func(*Process, *startup, func(Event)) error, emit func(Event)) error {
	proc, err := Spawn(opts)
	if err != nil {
		return err
	}
	defer proc.Release()

	// A client that goes away is the agent's own `cancel`, not a kill.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			_ = proc.Write(TagCmdInput, `{"command_id":"cancel","name":"cancel"}`)
		case <-stopped:
		}
	}()

	return body(proc, &startup{}, emit)
}

// userContent is a `UX` frame's words: what the canvas sent, as the agent echoes it back.
//
// The tag travels in both directions, and the echo is the only place a user message comes from — live or
// replayed, the panel draws what the agent said was said, and keeps no copy of its own to keep in step.
func userContent(value string) (string, bool) {
	var item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(value), &item) != nil || item.Type != "text" || strings.TrimSpace(item.Text) == "" {
		return "", false
	}
	return item.Text, true
}

// replayed reads a frame that arrived before the agent was ready as part of the conversation it loaded
// from its session file. Live turns send the same tags — `UX` echo and a textless `AX` marker — so this
// is only ever asked about frames from the startup window: a `AX` that carries text is a replayed
// message, and one that carries none is the end of a message already sent as deltas.
func replayed(frame *Frame) (HistoryEntry, bool) {
	role := ""
	switch frame.Tag {
	case TagUserX:
		if text, ok := userContent(frame.Value); ok {
			return HistoryEntry{Role: "user", Text: text}, true
		}
		return HistoryEntry{}, false
	case TagAssistantX:
		role = "assistant"
	default:
		return HistoryEntry{}, false
	}
	var item struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(frame.Value), &item) != nil {
		return HistoryEntry{}, false
	}
	if item.Type != "text" || strings.TrimSpace(item.Text) == "" {
		return HistoryEntry{}, false
	}
	return HistoryEntry{Role: role, Text: item.Text}, true
}

// waitReady reads startup frames until the session accepts input, then hands the collected state to
// onReady. Everything before that is the agent's own startup broadcast and, when it was started with a
// session file, the conversation it replayed from it — which is collected into history.
func waitReady(proc *Process, state *startup, history *[]HistoryEntry, emit func(Event), onReady func() error) error {
	for {
		frame, err := proc.Read()
		if err != nil || frame == nil {
			return nil
		}
		if entry, ok := replayed(frame); ok {
			if history != nil {
				*history = append(*history, entry)
			}
			continue
		}
		if frame.Tag != TagSystemMsg {
			continue
		}
		kind, data, ok := systemMessage(frame.Value)
		if !ok {
			continue
		}
		switch kind {
		case "session":
			if sessionState(data) != "ready" {
				continue
			}
			emit(state.event())
			return onReady()
		case "error":
			var payload struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(data, &payload)
			emit(Event{Type: "error", Message: payload.Text})
		default:
			state.collect(kind, data)
		}
	}
}
