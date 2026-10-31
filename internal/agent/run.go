package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Request is what one stream carries, and the only two kinds of thing a user does: change a setting,
// or ask something. Both go into the agent's stdin, in this order — the choices first, then the words.
//
// Command is a name from AlayaCore's own vocabulary (`model_set`, `reasoning_level_set`, …) with the
// args it expects; the gateway forwards the pair without knowing what either means. Either half may be
// empty: a setting change carries a command and no prompt, a question carries a prompt and no command.
type Request struct {
	Prompt  string
	Images  []string // data URLs, sent before the prompt text
	Command string
	Args    string
}

// ErrGone is the process ending before the turn did. A one-shot agent ends by itself and that is the
// answer; a connected one that stops mid-sentence has nothing left to write to, and the canvas has to
// be told rather than left waiting on a session that cannot speak.
var ErrGone = errors.New("agent: the process ended before the turn did")

// Run performs one request against one agent process and releases it.
//
// It decides nothing about the content: a delta is a delta, a command's answer is the agent's own
// sentence, and the answer ends where the agent's task ends. It only decides when to stop reading.
func Run(ctx context.Context, opts SpawnOptions, req Request, emit func(Event)) error {
	return withAgent(ctx, opts, func(proc *Process, state *startup, emit func(Event)) error {
		return converse(ctx, proc, state, req, emit, false)
	}, emit)
}

// converse is the loop both paths share: the choices and the prompt go out, the agent's frames come
// back, and the answer ends where the agent says the task ended.
//
// `ready` is the one thing they differ on, twice over. A freshly spawned agent still has to announce
// that it accepts input, while a connected one already did before its connect returned; and a process
// that ends is the end of a one-shot answer but the loss of a connected one.
func converse(ctx context.Context, proc *Process, state *startup, req Request, emit func(Event), ready bool) error {
	started := time.Now()
	var answer strings.Builder
	hasPrompt := strings.TrimSpace(req.Prompt) != "" || len(req.Images) > 0

	// Writing to a dead process is the other way a session ends, and it is the one that arrives first:
	// the write fails with a broken pipe, before any read would notice the process is gone. So a failed
	// write is ErrGone, not a transport error — it is the same fact the caller has to act on either way.
	send := func() error {
		if req.Command != "" {
			payload, err := json.Marshal(map[string]any{"command_id": commandID, "name": req.Command, "args": req.Args})
			if err != nil {
				return err
			}
			if err := proc.Write(TagCmdInput, string(payload)); err != nil {
				return fmt.Errorf("%w: %v", ErrGone, err)
			}
		}
		if hasPrompt {
			if err := sendPrompt(proc, req); err != nil {
				return fmt.Errorf("%w: %v", ErrGone, err)
			}
		}
		return nil
	}
	if ready {
		if err := send(); err != nil {
			return err
		}
	} else if err := waitReady(proc, state, emit, send); err != nil {
		return err
	}

	// A client that goes away is the agent's own `cancel`, not a kill. The process belongs to the
	// canvas now, so it has to be left where the next turn can use it: the frames that arrive while it
	// winds down are still read, and dropped, so nothing is attributed to the turn after this one.
	var cancelled atomic.Bool
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			cancelled.Store(true)
			_ = proc.Write(TagCmdInput, `{"command_id":"cancel","name":"cancel"}`)
		case <-stopped:
		}
	}()
	relay := func(event Event) {
		if cancelled.Load() {
			return
		}
		emit(event)
	}

	for {
		frame, err := proc.Read()
		if err != nil || frame == nil {
			if ready {
				return ErrGone
			}
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
				relay(Event{Type: "delta", Text: text})
			case "reasoning":
				relay(Event{Type: "reasoning", Text: text})
			}
		case TagAssistantF, TagUserF:
			// A built-in tool runs inside AlayaCore: the call and its result both come back here, and
			// the gateway answers neither. They are drawn, not acted on.
			event, ok := toolCall(frame.Value)
			if frame.Tag == TagUserF {
				event, ok = toolResult(frame.Value)
			}
			if ok {
				relay(event)
			}
		case TagCmdOutput:
			id, ok, message := commandOutput(frame.Value)
			if id != commandID {
				continue
			}
			relay(Event{Type: "command", OK: ok, Message: message})
			if !hasPrompt {
				return finish("", started, emit)
			}
		case TagSystemMsg:
			kind, data, ok := systemMessage(frame.Value)
			if !ok {
				continue
			}
			switch kind {
			case "model", "reasoning_level":
				// The agent announcing its own state: this is what the canvas displays.
				state.collect(kind, data)
				relay(Event{Type: kind, Active: state.active, Level: state.level})
			case "task":
				if over, known := taskInProgress(data); known && over && hasPrompt {
					if cancelled.Load() {
						return nil
					}
					return finish(answer.String(), started, emit)
				}
			case "session":
				if sessionState(data) == "closed" {
					return finish(answer.String(), started, emit)
				}
			case "error":
				relay(systemError(data))
			default:
				state.collect(kind, data)
			}
		}
	}
}

// commandID tags the command this request carries, so its CO is told apart from anything else.
const commandID = "1"

// finish closes the stream with what arrived.
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
