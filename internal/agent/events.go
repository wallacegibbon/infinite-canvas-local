package agent

import (
	"context"
	"encoding/json"
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

	// error: the agent's own words
	Message string `json:"message,omitempty"`
}

// Model is what `SM model_list` and `SM model` say, minus the two fields that must not travel: the
// frame carries `api_key` and `base_url` in cleartext.
type Model struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ProtocolType string `json:"protocolType,omitempty"`
	ContextLimit int    `json:"contextLimit,omitempty"`
}

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

// waitReady reads startup frames until the session accepts input, then hands the collected state to
// onReady. Everything before that is the agent's own startup broadcast.
func waitReady(proc *Process, state *startup, emit func(Event), onReady func() error) error {
	for {
		frame, err := ReadFrame(proc.Stdout)
		if err != nil || frame == nil {
			return nil
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
