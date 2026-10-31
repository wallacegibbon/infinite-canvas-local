package agent

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// Request is one prompt: text and, if the canvas attached any, images. Nothing else — the model and
// the reasoning level belong to the agent, and are changed with Send.
type Request struct {
	Prompt string
	Images []string // data URLs, sent before the prompt text
}

// Run sends one prompt and relays what the agent says back.
//
// It decides nothing: a delta is a delta, the answer ends where the agent's task ends, and a failure
// is the agent's own message. The only choice it makes is when to stop reading.
func Run(ctx context.Context, opts SpawnOptions, req Request, emit func(Event)) error {
	return withAgent(ctx, opts, func(proc *Process, state *startup, emit func(Event)) error {
		started := time.Now()
		var answer strings.Builder

		if err := waitReady(proc, state, emit, func() error { return sendPrompt(proc, req) }); err != nil {
			return err
		}

		for {
			frame, err := ReadFrame(proc.Stdout)
			if err != nil || frame == nil {
				return finish(answer.String(), started, emit)
			}
			switch frame.Tag {
			case TagAssistantXDelta:
				kind, text, ok := contentDelta(frame.Value)
				if !ok {
					continue
				}
				switch kind {
				case "text":
					answer.WriteString(text)
					emit(Event{Type: "delta", Text: text})
				case "reasoning":
					emit(Event{Type: "reasoning", Text: text})
				}
			case TagSystemMsg:
				kind, data, ok := systemMessage(frame.Value)
				if !ok {
					continue
				}
				switch kind {
				case "task":
					if over, known := taskInProgress(data); known && over {
						return finish(answer.String(), started, emit)
					}
				case "session":
					if sessionState(data) == "closed" {
						return finish(answer.String(), started, emit)
					}
				case "error":
					emit(systemError(data))
				default:
					state.collect(kind, data)
				}
			}
		}
	}, emit)
}

// Send forwards one command the user asked for — `model_set`, `reasoning_level_set`, whatever the
// protocol defines — and relays the agent's answer.
//
// The gateway does not know which commands exist or what their arguments mean: the canvas sends a name
// and an args string, the agent decides, and its frames come back as they are. A successful change is
// announced by the agent itself (`SM model`, `SM reasoning_level`), which is what the canvas displays.
func Send(ctx context.Context, opts SpawnOptions, name, args string, emit func(Event)) error {
	return withAgent(ctx, opts, func(proc *Process, state *startup, emit func(Event)) error {
		if err := waitReady(proc, state, emit, func() error {
			payload, err := json.Marshal(map[string]any{"command_id": commandID, "name": name, "args": args})
			if err != nil {
				return err
			}
			return proc.Write(TagCmdInput, string(payload))
		}); err != nil {
			return err
		}
		return relayCommand(proc, state, emit)
	}, emit)
}

// commandID tags the one command a Send carries, so its CO is told apart from anything else.
const commandID = "1"

// relayCommand reads until the command is answered or the session ends, passing on the agent's own
// statements about its state.
func relayCommand(proc *Process, state *startup, emit func(Event)) error {
	for {
		frame, err := ReadFrame(proc.Stdout)
		if err != nil || frame == nil {
			return nil
		}
		switch frame.Tag {
		case TagCmdOutput:
			id, ok, message := commandOutput(frame.Value)
			if id != commandID {
				continue
			}
			emit(Event{Type: "command", OK: ok, Message: message})
			return nil
		case TagSystemMsg:
			kind, data, ok := systemMessage(frame.Value)
			if !ok {
				continue
			}
			switch kind {
			case "model", "reasoning_level":
				state.collect(kind, data)
				emit(Event{Type: kind, Active: state.active, Level: state.level})
			case "session":
				if sessionState(data) == "closed" {
					return nil
				}
			case "error":
				emit(systemError(data))
			}
		}
	}
}

// finish closes a prompt stream with what arrived.
func finish(answer string, started time.Time, emit func(Event)) error {
	emit(Event{Type: "done", Text: answer, ElapsedMs: time.Since(started).Milliseconds()})
	return nil
}

func sendPrompt(proc *Process, req Request) error {
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
