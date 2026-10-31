package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Event is one thing the canvas may draw.
//
// The agent's own frames are richer than this on purpose: `SM model_list` carries every api_key from
// `model.conf` verbatim, and theme/tool/command frames mean nothing to a canvas node. What leaves
// here is the answer, the reasoning behind it, and — once, at the start — what models exist.
type Event struct {
	Type string `json:"type"`

	// delta, reasoning, done
	Text string `json:"text,omitempty"`

	// done
	ElapsedMs int64 `json:"elapsedMs,omitempty"`

	// models: the agent's startup broadcast, so a select can be filled without another endpoint
	Models         []Model `json:"models,omitempty"`
	Active         *Model  `json:"active,omitempty"`
	Reasoning      *int    `json:"reasoning,omitempty"`
	CoreVersion    string  `json:"coreVersion,omitempty"`
	MessageVersion int     `json:"messageVersion,omitempty"`

	// error — the agent's own words
	Message string `json:"message,omitempty"`
}

// Model is one entry of the list the agent reports: the id is what its commands take, the name is what
// a user reads.
type Model struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ProtocolType string `json:"protocolType,omitempty"`
	ContextLimit int    `json:"contextLimit,omitempty"`
}

// Request is one prompt, plus the two choices the canvas shows beside it.
//
// The model and the reasoning level are not the gateway's business: they are AlayaCore's own commands,
// written to its stdin exactly like the prompt is. Zero leaves whatever the agent has; the reasoning
// level uses -1 for that because 0 (off) is a real choice.
type Request struct {
	Prompt    string
	System    string
	Images    []string // data URLs, sent before the prompt text
	ModelID   int
	Reasoning int
}

// Run performs one prompt against one agent process and releases it.
//
// No timeout and no retry: cancelling is the agent's own `cancel` command, and a failure is whatever
// the agent says it is.
func Run(ctx context.Context, opts SpawnOptions, req Request, emit func(Event)) error {
	proc, err := Spawn(opts)
	if err != nil {
		return err
	}
	defer proc.Release()

	// A client that goes away is a cancel, sent as the agent's own command rather than a kill: it is
	// the process that knows how to leave a half-finished step in a sane state.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			_ = proc.Write(TagCmdInput, `{"command_id":"cancel","name":"cancel"}`)
		case <-stopped:
		}
	}()

	var answer strings.Builder
	started := time.Now()
	// sent guards the rest: the task frames that matter are the ones after the prompt.
	sent := false
	state := startup{}

	for {
		frame, err := ReadFrame(proc.Stdout)
		if err != nil || frame == nil {
			// A stream that ends after an answer is a finished answer; before one, the agent's last
			// words on stderr are the only explanation there is.
			if answer.Len() > 0 {
				emit(Event{Type: "done", Text: answer.String(), ElapsedMs: time.Since(started).Milliseconds()})
			} else if tail := proc.Stderr.Last(); tail != "" {
				emit(Event{Type: "error", Message: tail})
			} else if err != nil {
				emit(Event{Type: "error", Message: err.Error()})
			}
			return nil
		}

		switch frame.Tag {
		case TagAssistantXDelta:
			var item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal([]byte(frame.Value), &item) != nil {
				continue
			}
			switch item.Type {
			case "text":
				answer.WriteString(item.Text)
				emit(Event{Type: "delta", Text: item.Text})
			case "reasoning":
				emit(Event{Type: "reasoning", Text: item.Text})
			}
		case TagCmdOutput:
			// Only a rejection is worth showing: the agent says why it would not take a choice. The
			// answer still arrives from whatever was in effect, so this is a notice, not the end.
			var output struct {
				IsError bool `json:"is_error"`
				Result  struct {
					Message string `json:"message"`
				} `json:"result"`
			}
			if json.Unmarshal([]byte(frame.Value), &output) == nil && output.IsError {
				emit(Event{Type: "error", Message: output.Result.Message})
			}
		case TagSystemMsg:
			var envelope struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal([]byte(frame.Value), &envelope) != nil {
				continue
			}
			switch envelope.Type {
			case "model_list", "model", "reasoning_level":
				state.collect(envelope.Type, envelope.Data)
			case "session":
				var data struct {
					State string `json:"state"`
				}
				if sent || json.Unmarshal(envelope.Data, &data) != nil || data.State != "ready" {
					continue
				}
				sent = true
				// The list is here, on the way to the answer: one event, no extra endpoint and no
				// gateway-side cache to go stale.
				emit(state.event())
				if err := sendPrompt(proc, req); err != nil {
					emit(Event{Type: "error", Message: err.Error()})
					return nil
				}
			case "task":
				// The task this prompt started is over: answer with what arrived.
				var data struct {
					InProgress bool `json:"in_progress"`
				}
				if sent && json.Unmarshal(envelope.Data, &data) == nil && !data.InProgress {
					emit(Event{Type: "done", Text: answer.String(), ElapsedMs: time.Since(started).Milliseconds()})
					return nil
				}
			case "error":
				var data struct {
					Text string `json:"text"`
				}
				_ = json.Unmarshal(envelope.Data, &data)
				emit(Event{Type: "error", Message: data.Text})
			}
		}
	}
}

// startup is the model information the agent broadcasts before it accepts a prompt.
type startup struct {
	models         []Model
	active         *Model
	reasoning      *int
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
			// Four fields, so the keys that travel in the frame do not travel any further.
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
			s.reasoning = &level
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
	return Event{Type: "models", Models: s.models, Active: s.active, Reasoning: s.reasoning, CoreVersion: s.coreVersion, MessageVersion: s.messageVersion}
}

// sendPrompt hands the user's choices to the agent, then the prompt, in that order and on one stream:
// the agent applies them as it reads them, so nothing has to be acknowledged first.
func sendPrompt(proc *Process, req Request) error {
	if req.ModelID != 0 {
		if err := proc.Write(TagCmdInput, fmt.Sprintf(`{"command_id":"model","name":"model_set","args":"%d"}`, req.ModelID)); err != nil {
			return err
		}
	}
	if req.Reasoning >= 0 {
		if err := proc.Write(TagCmdInput, fmt.Sprintf(`{"command_id":"reasoning","name":"reasoning_level_set","args":"%d"}`, req.Reasoning)); err != nil {
			return err
		}
	}
	for _, image := range req.Images {
		payload, err := json.Marshal(map[string]any{"type": "image", "uri": image})
		if err != nil {
			return err
		}
		if err := proc.Write(TagUserX, string(payload)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.Prompt) != "" {
		payload, err := json.Marshal(map[string]any{"type": "text", "text": req.Prompt})
		if err != nil {
			return err
		}
		if err := proc.Write(TagUserX, string(payload)); err != nil {
			return err
		}
	}
	return proc.Write(TagUserEnd, "")
}
