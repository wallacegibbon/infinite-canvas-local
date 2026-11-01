package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"comfyui-gateway/internal/agent"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/ws"
)

// routesAgent registers the one agent route.
//
// A session is one agent process with one name, and the name is the client's — the canvas panel calls
// its own `canvas`, a node's panel calls its own `node:<id>`. The lifetimes differ (the canvas's agent
// lives until it is closed, a node's while its panel is open) and the client owns both, because the
// client is what knows when a surface went away. The gateway only files processes under names.
func (s *Server) routesAgent(mux *http.ServeMux) {
	s.route(mux, "GET /api/agent/socket/{session}", s.handleAgentSocket)
}

// agentOptions is what every spawn needs. The wording (skills) and the tools are passed through from
// configuration: the gateway has no opinion about either.
//
// Both routes get the same options. A one-shot question needs `read_file` as much as a conversation
// does — the H3 prompt rewrite works by reading a `SKILL.md` — so the difference between the two is
// lifetime, not capability.
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
// a cancel, or the end of the conversation.
type socketMessage struct {
	Prompt  string   `json:"prompt"`
	Images  []string `json:"images"`
	Command string   `json:"command"`
	Args    string   `json:"args"`
	Cancel  bool     `json:"cancel"`
	// Close ends the conversation: the agent is released, history and all. Closing the tab is not this
	// — a socket that goes away leaves the process running, which is what makes coming back a
	// continuation rather than a fresh start.
	Close bool `json:"close"`
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

	connected, err := s.agentSessions.Connect(ctx.R.Context(), id, s.agentOptions())
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

	// One goroutine, and it is the reading half of the conversation: what the agent says reaches this
	// socket through the pump, and what the user says is applied here until the socket ends.
	s.readSocket(conn, session, send)

	// The socket is gone. The agent has nobody reading it any more, so its current turn is asked to wind
	// down — otherwise it would write into a pipe until it blocked. The process itself stays: closing a
	// tab is not closing the agent.
	_ = session.Cancel()
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
