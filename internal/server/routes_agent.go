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

// routesAgent registers the one agent route.
//
// A prompt and a command are the same kind of thing to AlayaCore — frames on its stdin — so they are
// one route and one stream: what the request carries is written to the agent, and what the agent says
// comes back. The old `/api/prompts/*` recipes are untouched and keep running for now.
func (s *Server) routesAgent(mux *http.ServeMux) {
	s.route(mux, "POST /api/agent/chat", s.handleAgentChat)
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
	files := orderedPromptImages(ctx.Files())
	request := agent.Request{
		Prompt:  httpx.String(body, "prompt"),
		Images:  promptImageURLs(files),
		Command: httpx.String(body, "command"),
		Args:    httpx.String(body, "args"),
	}
	started := time.Now()
	s.logger.Info("agent: request command=%q args=%q prompt=%q images=%d", request.Command, request.Args, truncateText(request.Prompt, 120), len(files))

	emit := startEventStream(ctx.W)
	counts := map[string]int{}
	err = agent.Run(ctx.R.Context(), s.agentOptions(), request, func(event agent.Event) {
		counts[event.Type]++
		emit(event)
	})
	if err != nil {
		s.logger.Warn("agent: request failed after %dms: %v", time.Since(started).Milliseconds(), err)
		return err
	}
	if ctx.R.Context().Err() != nil {
		s.logger.Info("agent: request cancelled after %dms (events: %s)", time.Since(started).Milliseconds(), eventCounts(counts))
		return nil
	}
	s.logger.Info("agent: request done in %dms (events: %s)", time.Since(started).Milliseconds(), eventCounts(counts))
	return nil
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
