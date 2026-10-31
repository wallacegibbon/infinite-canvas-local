package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"comfyui-gateway/internal/agent"
	"comfyui-gateway/internal/httpx"
)

// routesAgent registers the agent routes.
//
// They are two kinds of thing, and the route is what says which. The canvas holds one agent per
// canvas for as long as the user keeps it open (`connect` + `chat`): it keeps a history, it can call
// tools, and it grows the more that canvas is worked on. A prompt rewrite or a caption is the other
// kind (`once`): one question, one process, gone when it is answered.
//
// The separation is the point, not an accident of routing. A one-shot question is handed a process of
// its own and no session at all, so a button that rewrites a prompt cannot reach — let alone spend —
// the agent the user is working with.
//
// A prompt and a command are the same kind of thing to AlayaCore — frames on its stdin — so `chat`
// carries both: what the request holds is written to the agent, and what the agent says comes back.
func (s *Server) routesAgent(mux *http.ServeMux) {
	s.route(mux, "POST /api/agent/connect", s.handleAgentConnect)
	s.route(mux, "POST /api/agent/chat", s.handleAgentChat)
	s.route(mux, "POST /api/agent/once", s.handleAgentOnce)
	s.route(mux, "DELETE /api/agent/session/{id}", s.handleAgentDisconnect)
}

// agentOptions is what every spawn needs. The wording (skills) and the tools are passed through from
// configuration: the gateway has no opinion about either.
func (s *Server) agentOptions() agent.SpawnOptions {
	return agent.SpawnOptions{
		Binary:     s.cfg.AgentBin,
		ConfigPath: s.cfg.AgentConfigPath,
		Skills:     s.cfg.AgentSkills,
		Tools:      "read_file",
		Log:        s.logger,
	}
}

// handleAgentConnect implements POST /api/agent/connect: start an agent and keep it, so the canvas can
// show the model list before anyone asks a question.
//
// The response is the agent's own startup broadcast. The process stays until the prompt that follows
// (or an idle timeout, or an explicit disconnect) releases it.
func (s *Server) handleAgentConnect(ctx *httpx.Ctx) error {
	started := time.Now()
	session, err := s.agentSessions.Connect(ctx.R.Context(), s.agentOptions())
	if err != nil {
		s.logger.Warn("agent: connect failed after %dms: %v", time.Since(started).Milliseconds(), err)
		return httpx.NewError(http.StatusBadGateway, "agent_unavailable", err.Error())
	}
	okJSON(ctx.W, map[string]any{"sessionId": session.ID, "state": session.State()})
	return nil
}

// handleAgentDisconnect releases a session the client walked away from.
func (s *Server) handleAgentDisconnect(ctx *httpx.Ctx) error {
	s.agentSessions.Close(ctx.R.PathValue("id"), "disconnect")
	okJSON(ctx.W, map[string]any{})
	return nil
}

// handleAgentChat implements POST /api/agent/chat.
//
// The body is whatever should be written to the agent: `command`/`args` for a setting change,
// `prompt` (and images) for a question, or both. Nothing is interpreted on the way — this is a
// transcription of two streams, not an API with opinions.
func (s *Server) handleAgentChat(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	request := agentRequest(body, ctx.Files())
	sessionID := httpx.String(body, "session")
	started := time.Now()
	s.logger.Info("agent: request command=%q args=%q prompt=%q images=%d", request.Command, request.Args, truncateText(request.Prompt, 120), len(request.Images))

	// A session that is gone has to be reported before the stream starts: once the headers are out, an
	// error can only be a broken event.
	var session *agent.Session
	if sessionID != "" {
		var ok bool
		session, ok = s.agentSessions.Peek(sessionID)
		if !ok {
			s.logger.Warn("agent: session %s is not connected", sessionID)
			return httpx.NewError(http.StatusConflict, "session_not_connected", "this agent session is no longer connected")
		}
	}

	counts := map[string]int{}
	emit := startEventStream(ctx.W)
	relay := func(event agent.Event) {
		counts[event.Type]++
		emit(event)
	}
	var runErr error
	switch {
	case session != nil && hasPrompt(request):
		// The send half of the button: the question spends the connection, and the process with it.
		proc, state, connected := s.agentSessions.Take(sessionID)
		if !connected {
			return httpx.NewError(http.StatusConflict, "session_not_connected", "this agent session is no longer connected")
		}
		runErr = agent.Continue(ctx.R.Context(), proc, state, request, relay)
		s.logger.Info("agent: session=%s answered and released", sessionID)
	case session != nil:
		// A setting change: the same connection answers it and stays connected.
		runErr = session.Command(ctx.R.Context(), request, relay)
		s.agentSessions.Touch(sessionID)
		s.logger.Info("agent: session=%s command=%q stayed connected", sessionID, request.Command)
	default:
		runErr = agent.Run(ctx.R.Context(), s.agentOptions(), request, relay)
	}
	return s.finishAgent(ctx, started, counts, runErr)
}

// handleAgentOnce implements POST /api/agent/once: one question, one process, released when the
// answer ends.
//
// There is no session on this route and no way to name one. What it is for — a rewritten prompt, a
// caption — is a single answer that nothing follows, so the process that produces it belongs to the
// request and dies with it. Naming a session here would be meaningless, and reading one would be
// worse than meaningless: it is the one way a shortcut could take the agent a canvas is holding.
func (s *Server) handleAgentOnce(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	request := agentRequest(body, ctx.Files())
	started := time.Now()
	s.logger.Info("agent: once command=%q args=%q prompt=%q images=%d", request.Command, request.Args, truncateText(request.Prompt, 120), len(request.Images))

	counts := map[string]int{}
	emit := startEventStream(ctx.W)
	runErr := agent.Run(ctx.R.Context(), s.agentOptions(), request, func(event agent.Event) {
		counts[event.Type]++
		emit(event)
	})
	return s.finishAgent(ctx, started, counts, runErr)
}

// agentRequest is the body as one request: the words to send, the images before them, and optionally
// one of AlayaCore's own commands. Both routes take the same shape — only the process they reach
// differs, which is why the shape is read in one place.
func agentRequest(body map[string]any, files []httpx.File) agent.Request {
	return agent.Request{
		Prompt:  httpx.String(body, "prompt"),
		Images:  promptImageURLs(orderedPromptImages(files)),
		Command: httpx.String(body, "command"),
		Args:    httpx.String(body, "args"),
	}
}

// finishAgent logs how a request ended, which is what a reader needs to tell a finished answer from a
// refusal without scrolling the transcript.
func (s *Server) finishAgent(ctx *httpx.Ctx, started time.Time, counts map[string]int, err error) error {
	elapsed := time.Since(started).Milliseconds()
	switch {
	case err != nil:
		s.logger.Warn("agent: request failed after %dms: %v", elapsed, err)
		return err
	case ctx.R.Context().Err() != nil:
		s.logger.Info("agent: request cancelled after %dms (events: %s)", elapsed, eventCounts(counts))
	default:
		s.logger.Info("agent: request done in %dms (events: %s)", elapsed, eventCounts(counts))
	}
	return nil
}

// hasPrompt reports whether this request asks a question rather than only changing a setting.
func hasPrompt(request agent.Request) bool {
	return strings.TrimSpace(request.Prompt) != "" || len(request.Images) > 0
}

// eventCounts renders what was relayed, which is what a reader needs to tell a finished answer from a
// refusal without scrolling the transcript.
func eventCounts(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for _, kind := range []string{"models", "delta", "reasoning", "model", "reasoning_level", "command", "done", "error"} {
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

// startEventStream answers with an event stream and returns the function that writes one event.
//
// A prompt can take a while and says things as it goes, so the response is flushed per event: an
// event that is still in a buffer is an event the user cannot see. The stream is the whole response —
// there is no envelope, no status to return afterwards.
func startEventStream(w http.ResponseWriter) func(agent.Event) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	return func(event agent.Event) {
		payload, err := json.Marshal(event)
		if err != nil {
			return
		}
		_, _ = w.Write(append(append([]byte("data: "), payload...), '\n', '\n'))
		if flusher != nil {
			flusher.Flush()
		}
	}
}
