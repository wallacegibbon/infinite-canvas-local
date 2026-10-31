package server

import (
	"encoding/json"
	"net/http"

	"comfyui-gateway/internal/agent"
	"comfyui-gateway/internal/httpx"
)

// routesAgent registers the two agent routes: one prompt, and one command.
//
// The old `/api/prompts/*` recipes are untouched and keep running; these sit beside them until the
// canvas is moved over.
func (s *Server) routesAgent(mux *http.ServeMux) {
	s.route(mux, "POST /api/agent/chat", s.handleAgentChat)
	s.route(mux, "POST /api/agent/command", s.handleAgentCommand)
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

// handleAgentChat implements POST /api/agent/chat: a prompt in, the agent's frames out as SSE.
//
// Nothing is interpreted on the way: `delta` is a delta, `models` is the agent's own startup
// broadcast (with the keys stripped by the agent package), and `error` is the agent's own sentence.
func (s *Server) handleAgentChat(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	request := agent.Request{
		Prompt: httpx.String(body, "prompt"),
		Images: promptImageURLs(orderedPromptImages(ctx.Files())),
	}
	emit := startEventStream(ctx.W)
	return agent.Run(ctx.R.Context(), s.agentOptions(), request, emit)
}

// handleAgentCommand implements POST /api/agent/command: the canvas names one of AlayaCore's commands
// (`model_set`, `reasoning_level_set`, …) and the frames answering it come back as they are.
//
// The gateway does not know the command vocabulary, does not check the arguments, and keeps no state:
// the agent decides, announces, and its announcement is what the canvas displays.
func (s *Server) handleAgentCommand(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	emit := startEventStream(ctx.W)
	return agent.Send(ctx.R.Context(), s.agentOptions(), httpx.String(body, "name"), httpx.String(body, "args"), emit)
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
