package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

// ErrGone is the process ending: there is nothing left to write to, and the canvas has to be told
// rather than left waiting on a socket that cannot speak.
var ErrGone = errors.New("agent: the process ended")

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

// turnStep applies one frame and reports whether it ended the turn. It is the whole of what reading an
// agent means: a frame is a frame, whatever asked for it.
func turnStep(state *startup, frame *Frame, hasPrompt bool, emit func(Event)) (ended bool) {
	switch frame.Tag {
	case TagUserX:
		// The agent echoing what the canvas sent. A user message comes from here and nowhere else, so
		// the panel has nothing of its own to draw — and nothing to keep in step with the transcript.
		if text, ok := userContent(frame.Value); ok {
			emit(Event{Type: "user", Text: text})
		}
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
		case "steering":
			// A message sent while the agent was working: it says when it queued it and when it used it.
			var payload struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal(data, &payload) == nil && payload.Phase != "" {
				emit(Event{Type: "steering", Phase: payload.Phase})
			}
		case "error":
			emit(systemError(data))
		default:
			state.collect(kind, data)
		}
	}
	return false
}

// commandID tags the command this request carries, so its CO is told apart from anything else.
const commandID = "1"
