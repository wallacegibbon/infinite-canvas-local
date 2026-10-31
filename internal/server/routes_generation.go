package server

import (
	"encoding/base64"
	"net/http"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/prompt"
	"comfyui-gateway/internal/util"
)

// routesGeneration registers the prompt services and the in-flight helpers.
//
// Media is produced by `POST /api/runs` and read back off the run (docs/protocol.md); there are no
// `/v1` generation routes left for them.
func (s *Server) routesGeneration(mux *http.ServeMux) {
	s.route(mux, "POST /api/prompts/optimize", s.handlePromptOptimize)
	s.route(mux, "POST /api/prompts/optimize-image", s.handlePromptOptimizeImage)
	s.route(mux, "POST /api/prompts/caption", s.handlePromptCaption)
	s.route(mux, "POST /api/prompts/chat", s.handlePromptChat)
	s.route(mux, "GET /api/previews/{promptId}", s.handlePreview)
	s.route(mux, "GET /api/progress/{requestId}", s.handleProgress)
	s.route(mux, "GET /files/{name}", s.handleFile)
}

// handlePromptOptimize implements POST /api/prompts/optimize.
func (s *Server) handlePromptOptimize(ctx *httpx.Ctx) error {
	return s.runPromptRoute(ctx, false)
}

// handlePromptOptimizeImage implements POST /api/prompts/optimize-image.
func (s *Server) handlePromptOptimizeImage(ctx *httpx.Ctx) error {
	return s.runPromptRoute(ctx, true)
}

// runPromptRoute shares the plumbing of the two optimize endpoints.
func (s *Server) runPromptRoute(ctx *httpx.Ctx, imageOnly bool) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	requestCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	cloud := promptCloud(body)

	var result map[string]any

	if imageOnly {
		result, err = s.optimizer.OptimizeImage(requestCtx, prompt.OptimizeRequest{
			Prompt:     httpx.String(body, "prompt"),
			Mode:       httpx.String(body, "mode"),
			Language:   httpx.String(body, "language"),
			ImageCount: int(httpx.Number(body, "imageCount", 0)),
			Cloud:      cloud,
		})
	} else {
		files := orderedPromptImages(ctx.Files())
		result, err = s.optimizer.Optimize(requestCtx, prompt.OptimizeRequest{
			Prompt:        httpx.String(body, "prompt"),
			Mode:          httpx.String(body, "mode"),
			Duration:      httpx.Number(body, "duration", httpx.Number(body, "seconds", 0)),
			ReferenceNote: defaultStringOf(httpx.String(body, "referenceNote"), httpx.String(body, "reference_note")),
			Language:      httpx.String(body, "language"),
			CloudImages:   promptImageURLs(files),
			ImageCount:    len(files),
			Cloud:         cloud,
		})
	}
	if err != nil {
		if requestCtx.Err() != nil {
			return nil
		}
		return err
	}
	okJSON(ctx.W, result)
	return nil
}

// handlePromptCaption implements POST /api/prompts/caption.
func (s *Server) handlePromptCaption(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	files := orderedPromptImages(ctx.Files())
	if len(files) == 0 {
		return httpx.NewError(http.StatusBadRequest, "missing_reference_image", "caption request is missing an image")
	}

	images := promptImageURLs(files[:1])

	requestCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	result, err := s.optimizer.Caption(requestCtx,
		images,
		httpx.String(body, "language"),
		httpx.String(body, "instruction"),
		promptCloud(body))
	if err != nil {
		if requestCtx.Err() != nil {
			return nil
		}
		return err
	}
	okJSON(ctx.W, result)
	return nil
}

// handlePromptChat implements POST /api/prompts/chat.
//
// One prompt in, one answer out. The gateway owns no wording for this route — no skill text, no
// sanitiser, no H3 field check — because the caller is the one who knows what it wants asked; the
// configured endpoint and its credentials are the only thing it borrows. Images are optional and
// are attached as ordinary form files, so a vision model can describe them.
func (s *Server) handlePromptChat(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	requestCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	result, err := s.optimizer.Complete(requestCtx, prompt.ChatRequest{
		Prompt: httpx.String(body, "prompt"),
		System: httpx.String(body, "system"),
		Images: promptImageURLs(orderedPromptImages(ctx.Files())),
		Cloud:  promptCloud(body),
	})
	if err != nil {
		if requestCtx.Err() != nil {
			return nil
		}
		return err
	}
	okJSON(ctx.W, result)
	return nil
}

// promptCloud reads the prompt endpoint's address, key and model from a request body.
//
// They travel per request, not as gateway configuration: the endpoint belongs to the browser's
// settings, and the gateway is only the party that sends the request (the browser never contacts
// llama-server directly).
func promptCloud(body map[string]any) prompt.CloudConfig {
	return prompt.CloudConfig{
		BaseURL: httpx.String(body, "cloudBaseUrl"),
		APIKey:  httpx.String(body, "cloudApiKey"),
		Model:   httpx.String(body, "cloudModel"),
	}
}

// promptImageURLs turns uploaded files into the data URLs the prompt endpoint accepts.
func promptImageURLs(files []httpx.File) []string {
	images := make([]string, 0, len(files))
	for _, file := range files {
		images = append(images, "data:"+defaultMime(file.Mimetype)+";base64,"+base64.StdEncoding.EncodeToString(file.Bytes))
	}
	return images
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
