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

// Request is what one message carries, and the only two kinds of thing a user does: change a setting,
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

// HasPrompt reports whether this request asks something, rather than only changing a setting. It is
// what tells a turn that ended from a command that was answered.
func (r Request) HasPrompt() bool {
	return strings.TrimSpace(r.Prompt) != "" || len(r.Images) > 0
}

// ErrGone is the process ending. A one-shot agent ends by itself and that is the answer; a connected
// one that stops has nothing left to write to, and the canvas has to be told rather than left waiting
// on a socket that cannot speak.
var ErrGone = errors.New("agent: the process ended")

// Run performs one request against one agent process and releases it.
//
// It decides nothing about the content: a delta is a delta, a command's answer is the agent's own
// sentence, and the answer ends where the agent's task ends. It only decides when to stop reading.
func Run(ctx context.Context, opts SpawnOptions, req Request, emit func(Event)) error {
	return withAgent(ctx, opts, func(proc *Process, state *startup, emit func(Event)) error {
		started := time.Now()
		var answer strings.Builder
		if err := waitReady(proc, state, emit, func() error { return writeRequest(proc, req) }); err != nil {
			return err
		}
		if err := readTurn(ctx, proc, state, req.HasPrompt(), func(event Event) {
			collect(&answer, event, emit)
		}); err != nil {
			return err
		}
		return finish(answer.String(), started, emit)
	}, emit)
}

// writeRequest puts one request on the agent's stdin.
//
// Writing to a dead process is the other way a session ends, and it is the one that arrives first: the
// write fails with a broken pipe, before any read would notice the process is gone. So a failed write
// is ErrGone, not a transport error — it is the same fact the caller has to act on either way.
func writeRequest(proc *Process, req Request) error {
	if req.Command != "" {
		payload, err := json.Marshal(map[string]any{"command_id": commandID, "name": req.Command, "args": req.Args})
		if err != nil {
			return err
		}
		if err := proc.Write(TagCmdInput, string(payload)); err != nil {
			return fmt.Errorf("%w: %v", ErrGone, err)
		}
	}
	if req.HasPrompt() {
		for _, image := range req.Images {
			payload, err := json.Marshal(map[string]any{"type": "image", "uri": image})
			if err != nil {
				return err
			}
			if err := proc.Write(TagUserX, string(payload)); err != nil {
				return fmt.Errorf("%w: %v", ErrGone, err)
			}
		}
		for _, part := range splitForPrompt(req.Prompt) {
			payload, err := json.Marshal(map[string]any{"type": "text", "text": part})
			if err != nil {
				return err
			}
			if err := proc.Write(TagUserX, string(payload)); err != nil {
				return fmt.Errorf("%w: %v", ErrGone, err)
			}
		}
		if err := proc.Write(TagUserEnd, ""); err != nil {
			return fmt.Errorf("%w: %v", ErrGone, err)
		}
	}
	return nil
}

// splitForPrompt is one text part, or none. It exists so an empty prompt writes no frame at all rather
// than an empty one.
func splitForPrompt(prompt string) []string {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	return []string{prompt}
}

// readTurn reads until the agent says the turn ended.
//
// There is no timer and no quorum here: the agent decides when it is done, and the one frame that says
// so is `SM task in_progress:false`. A step boundary is not the end of a turn — a hundred-step loop is
// still one turn, and a message the user sends mid-loop is inserted by the agent between those steps,
// which is why nothing in this function may assume one prompt equals one answer.
func readTurn(ctx context.Context, proc *Process, state *startup, hasPrompt bool, emit func(Event)) error {
	// A client that goes away is the agent's own `cancel`, not a kill. The frames that arrive while it
	// winds down are still read and dropped, so nothing is attributed to the turn after this one.
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

	for {
		frame, err := proc.Read()
		if err != nil || frame == nil {
			return ErrGone
		}
		if turnStep(state, frame, hasPrompt, func(event Event) {
			if !cancelled.Load() {
				emit(event)
			}
		}) {
			return nil
		}
	}
}

// turnStep applies one frame and reports whether it ended the turn. It is the whole of what reading an
// agent means, shared by the one-shot loop and the socket's: a frame is a frame whatever is listening.
func turnStep(state *startup, frame *Frame, hasPrompt bool, emit func(Event)) (ended bool) {
	switch frame.Tag {
	case TagAssistantXDelta:
		kind, text, ok := contentDelta(frame.Value)
		if !ok {
			return false
		}
		switch kind {
		case "text":
			emit(Event{Type: "delta", Text: text})
		case "reasoning":
			emit(Event{Type: "reasoning", Text: text})
		}
	case TagAssistantF, TagUserF:
		// A built-in tool runs inside AlayaCore: the call and its result both come back here, and the
		// gateway answers neither. They are drawn, not acted on.
		event, ok := toolCall(frame.Value)
		if frame.Tag == TagUserF {
			event, ok = toolResult(frame.Value)
		}
		if ok {
			emit(event)
		}
	case TagCmdOutput:
		id, ok, message := commandOutput(frame.Value)
		if id != commandID {
			return false
		}
		emit(Event{Type: "command", OK: ok, Message: message})
		return !hasPrompt
	case TagSystemMsg:
		kind, data, ok := systemMessage(frame.Value)
		if !ok {
			return false
		}
		switch kind {
		case "model", "reasoning_level":
			// The agent announcing its own state: this is what the canvas displays.
			state.collect(kind, data)
			emit(Event{Type: kind, Active: state.active, Level: state.level})
		case "task":
			if over, known := taskInProgress(data); known && over && hasPrompt {
				return true
			}
		case "session":
			if sessionState(data) == "closed" {
				return true
			}
		case "error":
			emit(systemError(data))
		default:
			state.collect(kind, data)
		}
	}
	return false
}

// collect accumulates the answer as it arrives, so `done` can carry the whole of it: a client that only
// started reading at the end still gets the text.
func collect(answer *strings.Builder, event Event, emit func(Event)) {
	if event.Type == "delta" {
		answer.WriteString(event.Text)
	}
	emit(event)
}

// commandID tags the command this request carries, so its CO is told apart from anything else.
const commandID = "1"

// finish closes the stream with what arrived.
func finish(answer string, started time.Time, emit func(Event)) error {
	emit(Event{Type: "done", Text: answer, ElapsedMs: time.Since(started).Milliseconds()})
	return nil
}
