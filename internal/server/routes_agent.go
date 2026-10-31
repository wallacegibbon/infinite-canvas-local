package server

import (
	"encoding/json"
	"net/http"

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
	request := agent.Request{
		Prompt:  httpx.String(body, "prompt"),
		Images:  promptImageURLs(orderedPromptImages(ctx.Files())),
		Command: httpx.String(body, "command"),
		Args:    httpx.String(body, "args"),
	}
	return agent.Run(ctx.R.Context(), s.agentOptions(), request, startEventStream(ctx.W))
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
