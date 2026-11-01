package server

import (
	"net/http"

	"comfyui-gateway/internal/httpx"
)

// routesGeneration registers the file route.
//
// Media is produced by `POST /api/runs` (docs/protocol.md); prompts are the agent's, on
// `/api/agent/socket/{session}` (routes_agent.go). A run carries its own preview frame and step count,
// so there is no second endpoint to ask for either — there is nothing else here to route.
func (s *Server) routesGeneration(mux *http.ServeMux) {
	s.routesAgent(mux)
	s.route(mux, "GET /files/{name}", s.handleFile)
}

// handleFile implements GET /files/{name}.
func (s *Server) handleFile(ctx *httpx.Ctx) error {
	return s.serveFile(ctx.W, ctx.R, ctx.R.PathValue("name"))
}
