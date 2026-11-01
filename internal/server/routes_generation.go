package server

import (
	"encoding/base64"
	"net/http"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/util"
)

// routesGeneration registers the in-flight helpers and the file route.
//
// Media is produced by `POST /api/runs` (docs/protocol.md); prompts are the agent's, on
// `/api/agent/chat` (routes_agent.go). There is nothing else here to route.
func (s *Server) routesGeneration(mux *http.ServeMux) {
	s.routesAgent(mux)
	s.route(mux, "GET /api/previews/{promptId}", s.handlePreview)
	s.route(mux, "GET /api/progress/{requestId}", s.handleProgress)
	s.route(mux, "GET /files/{name}", s.handleFile)
}

// handlePreview implements GET /api/previews/{promptId}.
func (s *Server) handlePreview(ctx *httpx.Ctx) error {
	preview := s.hub.Get(ctx.R.PathValue("promptId"))
	if preview == nil {
		return httpx.NewError(http.StatusNotFound, "preview_not_ready", "no preview available yet")
	}
	ctx.W.Header().Set("Cache-Control", "no-store")
	if preview.Step != nil {
		ctx.W.Header().Set("X-Preview-Step", util.Stringify(float64(*preview.Step)))
	}
	if preview.Total != nil {
		ctx.W.Header().Set("X-Preview-Total", util.Stringify(float64(*preview.Total)))
	}
	data, err := base64.StdEncoding.DecodeString(preview.Data)
	if err != nil {
		return httpx.NewError(http.StatusInternalServerError, "invalid_preview", "the preview data is corrupt")
	}
	ctx.W.Header().Set("Content-Type", defaultStringOf(preview.Mime, "image/jpeg"))
	ctx.W.WriteHeader(http.StatusOK)
	_, _ = ctx.W.Write(data)
	return nil
}

// handleProgress implements GET /api/progress/{requestId}.
func (s *Server) handleProgress(ctx *httpx.Ctx) error {
	progress := s.hub.GetByRequest(ctx.R.PathValue("requestId"))
	ctx.W.Header().Set("Cache-Control", "no-store")
	payload := map[string]any{"status": "pending", "step": 0, "total": 0}
	if progress != nil {
		payload["status"] = progress.State
		payload["step"] = progress.Step
		payload["total"] = progress.Total
		if progress.PromptID != "" {
			payload["prompt_id"] = progress.PromptID
		}
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handleFile implements GET /files/{name}.
func (s *Server) handleFile(ctx *httpx.Ctx) error {
	return s.serveFile(ctx.W, ctx.R, ctx.R.PathValue("name"))
}
