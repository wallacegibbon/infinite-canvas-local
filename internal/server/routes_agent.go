package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"comfyui-gateway/internal/agent"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/ws"
)

// routesAgent registers the agent routes.
//
// A session is one agent process with one name, and the name is the client's — the canvas panel calls
// its own `canvas:<canvasId>`, a node's panel calls its own `node:<id>`. The lifetimes differ (the
// canvas's agent lives as long as its socket, a node's while its panel is open) and the client owns both,
// because the client is what knows when a surface went away. The gateway only files processes under
// names, and keeps their conversations in files named after them.
func (s *Server) routesAgent(mux *http.ServeMux) {
	s.route(mux, "GET /api/agent/socket/{session}", s.handleAgentSocket)
	s.route(mux, "DELETE /api/agent/session/{session}", s.handleAgentSessionDelete)
}

// handleAgentSessionDelete implements DELETE /api/agent/session/{session}: forget this conversation.
//
// The canvas owns the name and is the only thing that knows a conversation is over — a canvas that has
// been deleted has nobody left to talk to, and its session file would sit in the data directory forever.
// The process, if one is running, is released first so nothing writes the file back after it is removed.
func (s *Server) handleAgentSessionDelete(ctx *httpx.Ctx) error {
	id := ctx.R.PathValue("session")
	s.agentSessions.Close(id, "forgotten")

	removed := false
	if file, err := s.sessionFile(id); err == nil {
		switch err := os.Remove(file); {
		case err == nil:
			removed = true
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
	}
	_ = os.RemoveAll(filepath.Join(workspacesRoot(s.cfg.DataDir), agentWorkspaceKey(id)))

	httpx.OK(ctx.W, map[string]any{"ok": true, "removed": removed})
	return nil
}

// agentOptions is what every spawn needs. The wording (skills) and the tools are passed through from
// configuration: the gateway has no opinion about either.
//
// Every spawn gets the same options, and `read_file` is the only tool: the H3 prompt rewrite works by
// reading a `SKILL.md`, and an agent that could not read one could not load a skill at all. Nothing
// here can hand itself a shell.
func (s *Server) agentOptions() agent.SpawnOptions {
	return agent.SpawnOptions{
		Binary:     s.cfg.AgentBin,
		ConfigPath: s.cfg.AgentConfigPath,
		Skills:     s.cfg.AgentSkills,
		Tools:      "read_file",
		Log:        s.logger,
	}
}

// socketMessage is what a client may say on the socket. One of them per message: a prompt, a setting,
// a cancel, a tool's answer, or the end of the conversation.
type socketMessage struct {
	Prompt  string   `json:"prompt"`
	Images  []string `json:"images"`
	Command string   `json:"command"`
	Args    string   `json:"args"`
	Cancel  bool     `json:"cancel"`
	// ToolResult is the canvas answering a `tool` event: what running the tool did. It is not the same
	// pipe as the agent's own `tool_call` / `tool_result` events, which run inside AlayaCore and which
	// the gateway only relays.
	ToolResult *toolAnswer `json:"toolResult"`
	// Close ends the conversation: the agent is released, history and all. Closing the tab is not this
	// — a socket that goes away leaves the process running, which is what makes coming back a
	// continuation rather than a fresh start.
	Close bool `json:"close"`
}

// toolAnswer is one tool's result, on its way from the canvas to the agent: the content blocks the page
// produced, in the protocol's own shape, and whether the tool failed rather than did something.
type toolAnswer struct {
	CallID  string          `json:"callId"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"isError"`
}

// handleAgentSocket implements GET /api/agent/socket/{session}: the conversation.
//
// Everything after the upgrade is a message, in both directions. That is why this route reports its
// failures as messages rather than status codes — once the header is out, a status code has nowhere to
// go, and a gateway that pretended otherwise would be lying with a 200.
func (s *Server) handleAgentSocket(ctx *httpx.Ctx) error {
	// Origin is checked before the upgrade by the middleware, which treats an upgrade as the write it
	// becomes: a cross-site page must not be able to talk to a canvas's agent.
	conn, err := ws.Accept(ctx.W, ctx.R)
	if err != nil {
		// Nothing has been hijacked, so this is still an ordinary failed request.
		return httpx.NewError(http.StatusBadRequest, "not_a_websocket", err.Error())
	}
	defer conn.Close()
	go conn.KeepAlive()

	id := ctx.R.PathValue("session")
	send := func(event agent.Event) error {
		payload, err := json.Marshal(event)
		if err != nil {
			return nil // an event that cannot be encoded is not worth closing a conversation over
		}
		return conn.WriteText(payload)
	}

	// Whether this name keeps a conversation is the client's answer, not the gateway's: a canvas panel is a
	// dialogue with a history and asks for one, a node's reverse-prompt is one question and does not.
	// File processes under names — do not teach it which names mean what.
	history := ctx.R.URL.Query().Get("history") == "1"
	options, err := s.agentOptionsFor(id, history)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("agent: socket %q has no workspace: %v", id, err)
		}
		_ = send(agent.Event{Type: "error", Message: err.Error()})
		return nil
	}
	connected, err := s.agentSessions.Connect(ctx.R.Context(), id, options)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("agent: socket %q could not connect: %v", id, err)
		}
		_ = send(agent.Event{Type: "error", Message: err.Error()})
		return nil
	}
	session := connected.Session
	if !session.Watch(send) {
		// A second tab on the same canvas: the agent is already being read, and two readers would split
		// every answer between them.
		_ = send(agent.Event{Type: "error", Message: "这个画布在另一个连接里已经打开了"})
		if s.logger != nil {
			s.logger.Warn("agent: socket %q refused, already watched", id)
		}
		return nil
	}
	defer session.Unwatch()
	if s.logger != nil {
		s.logger.Info("agent: socket open session=%s spawned=%t", session.ID, connected.Spawned)
	}

	// The agent's own startup broadcast first, so the model list is there before anything is asked.
	if err := send(connected.Startup); err != nil {
		return nil
	}
	// Then the conversation it replayed from its session file — always, even when empty: a page that
	// opens this socket is drawing the agent's conversation, and the agent has just said what that is.
	// An empty list is the honest answer for a conversation that has not started yet.
	if err := send(agent.Event{Type: "history", History: connected.History}); err != nil {
		return nil
	}

	// One goroutine, and it is the reading half of the conversation: what the agent says reaches this
	// socket through the pump, and what the user says is applied here until the socket ends.
	s.readSocket(conn, session, send)

	// The socket is gone. The agent has nobody reading it any more, so its current turn is asked to wind
	// down — otherwise it would write into a pipe until it blocked — and then the process goes too.
	//
	// The conversation is not lost with it: AlayaCore keeps it in the session file, and the next connect
	// starts a process that replays it. That is why the process may be this short-lived: a page that was
	// reloaded is a new reader of the same file, not a stranger.
	_ = session.Cancel()
	s.agentSessions.Close(session.ID, "socket closed")
	if s.logger != nil {
		s.logger.Info("agent: socket closed session=%s", session.ID)
	}
	return nil
}

// readSocket applies what the user says, one message at a time, until the socket ends.
func (s *Server) readSocket(conn *ws.Conn, session *agent.Session, send func(agent.Event) error) {
	for {
		raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var message socketMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			_ = send(agent.Event{Type: "error", Message: "无法解析这条消息"})
			continue
		}
		switch {
		case message.ToolResult != nil:
			if s.logger != nil {
				s.logger.Info("agent: tool answered session=%s callId=%s isError=%t", session.ID, message.ToolResult.CallID, message.ToolResult.IsError)
			}
			session.AnswerTool(message.ToolResult.CallID, message.ToolResult.Content, message.ToolResult.IsError)
		case message.Close:
			s.agentSessions.Close(session.ID, "closed by the client")
			_ = conn.Close()
			return
		case message.Cancel:
			if err := session.Cancel(); err != nil {
				return
			}
		default:
			request := agent.Request{Prompt: message.Prompt, Images: message.Images, Command: message.Command, Args: message.Args}
			if s.logger != nil {
				s.logger.Info("agent: socket in session=%s command=%q prompt=%q images=%d", session.ID, request.Command, truncateText(request.Prompt, 120), len(request.Images))
			}
			if err := session.Send(request); err != nil {
				_ = send(agent.Event{Type: "error", Message: "连接已经断开"})
				return
			}
		}
	}
}

// eventCounts renders what was relayed, which is what a reader needs to tell a finished answer from a
// refusal without scrolling the transcript.
func eventCounts(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for _, kind := range []string{"models", "delta", "reasoning", "model", "reasoning_level", "command", "tool_call", "tool_result", "done", "error"} {
		if counts[kind] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", kind, counts[kind]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

// truncateText keeps a prompt's head for the log: a full one can be a page long, and the first line is
// what identifies it.
func truncateText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + fmt.Sprintf("… (%d runes)", len(runes))
}
