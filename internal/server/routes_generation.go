package server

import (
	"encoding/base64"
	"math"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"comfyui-gateway/internal/generate"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/prompt"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// routesGeneration registers the endpoints that actually produce media.
func (s *Server) routesGeneration(mux *http.ServeMux) {
	s.route(mux, "POST /v1/images/generations", s.handleImageGenerations)
	s.route(mux, "POST /v1/images/upscale", s.handleImageUpscale)
	s.route(mux, "POST /v1/images/edits", s.handleImageEdits)
	s.route(mux, "POST /v1/prompts/optimize", s.handlePromptOptimize)
	s.route(mux, "POST /v1/prompts/optimize-image", s.handlePromptOptimizeImage)
	s.route(mux, "POST /v1/prompts/caption", s.handlePromptCaption)
	s.route(mux, "POST /v1/audio/speech", s.handleAudioSpeech)
	s.route(mux, "POST /v1/videos", s.handleVideoCreate)
	s.route(mux, "POST /v1/videos/upscale", s.handleVideoUpscale)
	s.route(mux, "POST /v1/videos/interpolate", s.handleVideoInterpolate)
	s.route(mux, "GET /v1/videos/{id}", s.handleVideoGet)
	s.route(mux, "GET /v1/videos/{id}/content", s.handleVideoContent)
	s.route(mux, "POST /v1/videos/{id}/cancel", s.handleVideoCancel)
	s.route(mux, "GET /v1/tasks/{id}", s.handleTaskGet)
	s.route(mux, "GET /api/previews/{promptId}", s.handlePreview)
	s.route(mux, "GET /api/progress/{requestId}", s.handleProgress)
	s.route(mux, "GET /files/{name}", s.handleFile)
}

// imageResponse renders the OpenAI-compatible image payload.
func (s *Server) imageResponse(ctx *httpx.Ctx, files []generate.OutputFile, responseFormat string, label string) (map[string]any, error) {
	data := make([]any, 0, len(files))
	for _, file := range files {
		if responseFormat == "url" {
			extension := util.ExtFromMime(file.MimeType, file.Filename, "png")
			saved, err := s.tasks.SaveFile(util.RandomID("img_"), file.Buffer, extension)
			if err != nil {
				return nil, httpx.NewError(http.StatusInternalServerError, "save_failed", "failed to save the image: "+err.Error())
			}
			data = append(data, map[string]any{"url": saved})
			continue
		}
		data = append(data, map[string]any{"b64_json": base64.StdEncoding.EncodeToString(file.Buffer)})
	}
	if len(data) == 0 {
		return nil, httpx.NewError(http.StatusBadGateway, "no_images", label+" returned no images")
	}
	return map[string]any{
		"created": nowUnix(),
		"data":    data,
	}, nil
}

// runImageWorkflow executes one image request through the runner.
func (s *Server) runImageWorkflow(ctx *httpx.Ctx, entry *workflow.Entry, runCtx *workflow.Context, requestID string) (*generate.RunResult, error) {
	requestCtx, cleanup := s.withCancel(ctx.R, requestID)
	defer cleanup()

	result, err := s.runner.RunImage(requestCtx, generate.RunOptions{
		Entry:          entry,
		Context:        runCtx,
		RequestID:      requestID,
		LoraCatalog:    s.setup.LoraNames(),
		ModelAliasFor:  s.setup.ModelAliasFor,
		TimeoutMs:      s.cfg.RequestTimeoutMs,
		PollIntervalMs: s.cfg.PollIntervalMs,
	})
	if err != nil {
		if requestCtx.Err() != nil {
			s.logger.Info("image %s request cancelled by the client %s", entry.Name, requestID)
			return nil, nil
		}
		return nil, err
	}
	return result, nil
}

// handleImageGenerations implements POST /v1/images/generations.
func (s *Server) handleImageGenerations(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	baseEntry, err := s.requireModel(httpx.String(body, "model"), "image")
	if err != nil {
		return err
	}
	mode := strings.TrimSpace(httpx.String(body, "mode"))
	if mode == "" {
		mode = strings.TrimSpace(httpx.String(body, "imageMode"))
	}
	entry := baseEntry
	if mode != "" {
		entry = s.registry.ResolveForMode(baseEntry, mode)
	}

	background := httpx.String(body, "background")
	if background != "" && !hasBackgroundSupport(entry) {
		return httpx.Errorf(http.StatusBadRequest, "unsupported_background",
			"workflow %s has no background binding, so a transparent background cannot be guaranteed.", entry.Name)
	}

	params, err := parseParams(body)
	if err != nil {
		return err
	}
	text := applyBackgroundPrompt(httpx.String(body, "prompt"), background, entry)
	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Prompt:     text,
		Negative:   optionalString(body, "negative"),
		Size:       httpx.String(body, "size"),
		Count:      httpx.Number(body, "n", httpx.Number(body, "count", 0)),
		Quality:    httpx.String(body, "quality"),
		Mode:       mode,
		Background: background,
		Params:     params,
		Presets:    entry.Presets,
	})
	if entry.PromptInject == "moss-layout" {
		applyLayoutPrompt(runCtx)
	}

	result, err := s.runImageWorkflow(ctx, entry, runCtx, ctx.RequestID)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	s.logger.Info("image %s -> %s (%d file(s)) %s", entry.Name, result.PromptID, len(result.Files), ctx.RequestID)

	payload, err := s.imageResponse(ctx, result.Files, strings.ToLower(defaultStringOf(httpx.String(body, "response_format"), "b64_json")), "ComfyUI")
	if err != nil {
		return err
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handleImageUpscale implements POST /v1/images/upscale.
func (s *Server) handleImageUpscale(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	entry, err := s.requireModel(defaultStringOf(httpx.String(body, "model"), "image-esrgan-upscale"), "image")
	if err != nil {
		return err
	}
	if entry.Category != "upscale" {
		return httpx.Errorf(http.StatusBadRequest, "not_upscale_workflow", "model %s is not an upscale workflow", entry.Name)
	}

	files := imageFiles(ctx.Files())
	if len(files) == 0 {
		return httpx.NewError(http.StatusBadRequest, "missing_input_image", "upscale request is missing the input image")
	}

	params, err := parseParams(body)
	if err != nil {
		return err
	}
	uploadCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()
	uploaded, err := s.uploadFiles(uploadCtx, files[:1])
	if err != nil {
		return err
	}

	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Prompt:         httpx.String(body, "prompt"),
		Count:          1,
		Images:         toUploadResults(files[:1]),
		UploadedImages: uploaded,
		Params:         params,
	})

	result, err := s.runImageWorkflow(ctx, entry, runCtx, ctx.RequestID)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	s.logger.Info("upscale %s -> %s (%d file(s)) %s", entry.Name, result.PromptID, len(result.Files), ctx.RequestID)

	payload, err := s.imageResponse(ctx, result.Files, strings.ToLower(defaultStringOf(httpx.String(body, "response_format"), "b64_json")), "the upscale workflow")
	if err != nil {
		return err
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handleImageEdits implements POST /v1/images/edits.
func (s *Server) handleImageEdits(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	entry, err := s.requireModel(httpx.String(body, "model"), "image")
	if err != nil {
		return err
	}

	files := imageFiles(ctx.Files())
	if len(files) == 0 {
		return httpx.NewError(http.StatusBadRequest, "missing_input_image", "image edit request is missing reference images")
	}

	mode := strings.TrimSpace(httpx.String(body, "mode"))
	if mode == "" && entry.Variants.Len() > 0 {
		mode = entry.Variants.Keys[0]
	}
	if entry.Variants.Len() > 0 {
		if _, ok := entry.Variants.Get(mode); !ok {
			return httpx.Errorf(http.StatusBadRequest, "unsupported_image_mode",
				"model %s does not support mode: %s", entry.Name, defaultStringOf(mode, "(empty)"))
		}
	}
	params, err := parseParams(body)
	if err != nil {
		return err
	}

	// The variant is the contract: inpaint wants a source and a mask, outpaint one source, edit up
	// to four references. Those limits live in registry.json, not here.
	resolvedEntry := s.registry.ResolveForMode(entry, mode)
	if _, err := workflow.PlanMedia(resolvedEntry, workflow.MediaCounts{Images: len(files)}, mode, params); err != nil {
		return err
	}

	reorderReferences := resolvedEntry.PromptReorder == "qwen-edit" && len(files) > 1
	orderedFiles := files
	promptText := httpx.String(body, "prompt")
	if reorderReferences {
		orderedFiles = append(append([]httpx.File{}, files[1:]...), files[0])
		promptText = prompt.ReorderQwenEditPrompt(promptText, len(files))
	}

	background := httpx.String(body, "background")
	if background != "" && !hasBackgroundSupport(resolvedEntry) {
		return httpx.Errorf(http.StatusBadRequest, "unsupported_background",
			"workflow %s has no background binding, so a transparent background cannot be guaranteed.", resolvedEntry.Name)
	}
	uploadCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()
	uploaded, err := s.uploadFiles(uploadCtx, orderedFiles)
	if err != nil {
		return err
	}

	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Prompt:         applyBackgroundPrompt(promptText, background, resolvedEntry),
		Negative:       optionalString(body, "negative"),
		Size:           httpx.String(body, "size"),
		Count:          httpx.Number(body, "n", httpx.Number(body, "count", 0)),
		Quality:        httpx.String(body, "quality"),
		Mode:           mode,
		Background:     background,
		Images:         toUploadResults(orderedFiles),
		UploadedImages: uploaded,
		Params:         params,
		Presets:        entry.Presets,
	})
	if entry.PromptInject == "moss-layout" {
		applyLayoutPrompt(runCtx)
	}

	result, err := s.runImageWorkflow(ctx, entry, runCtx, ctx.RequestID)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	s.logger.Info("edit %s -> %s (%d file(s)) %s", entry.Name, result.PromptID, len(result.Files), ctx.RequestID)

	payload, err := s.imageResponse(ctx, result.Files, strings.ToLower(defaultStringOf(httpx.String(body, "response_format"), "b64_json")), "ComfyUI")
	if err != nil {
		return err
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handlePromptOptimize implements POST /v1/prompts/optimize.
func (s *Server) handlePromptOptimize(ctx *httpx.Ctx) error {
	return s.runPromptRoute(ctx, false)
}

// handlePromptOptimizeImage implements POST /v1/prompts/optimize-image.
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

	cloud := prompt.CloudConfig{
		BaseURL: httpx.String(body, "cloudBaseUrl"),
		APIKey:  httpx.String(body, "cloudApiKey"),
		Model:   httpx.String(body, "cloudModel"),
	}

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
		images := make([]string, 0, len(files))
		for _, file := range files {
			images = append(images, "data:"+defaultMime(file.Mimetype)+";base64,"+base64.StdEncoding.EncodeToString(file.Bytes))
		}
		result, err = s.optimizer.Optimize(requestCtx, prompt.OptimizeRequest{
			Prompt:        httpx.String(body, "prompt"),
			Mode:          httpx.String(body, "mode"),
			Duration:      httpx.Number(body, "duration", httpx.Number(body, "seconds", 0)),
			ReferenceNote: defaultStringOf(httpx.String(body, "referenceNote"), httpx.String(body, "reference_note")),
			Language:      httpx.String(body, "language"),
			CloudImages:   images,
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

// handlePromptCaption implements POST /v1/prompts/caption.
func (s *Server) handlePromptCaption(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	files := orderedPromptImages(ctx.Files())
	if len(files) == 0 {
		return httpx.NewError(http.StatusBadRequest, "missing_reference_image", "caption request is missing an image")
	}

	images := []string{
		"data:" + defaultMime(files[0].Mimetype) + ";base64," + base64.StdEncoding.EncodeToString(files[0].Bytes),
	}

	requestCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	result, err := s.optimizer.Caption(requestCtx,
		images,
		httpx.String(body, "language"),
		httpx.String(body, "instruction"),
		prompt.CloudConfig{
			BaseURL: httpx.String(body, "cloudBaseUrl"),
			APIKey:  httpx.String(body, "cloudApiKey"),
			Model:   httpx.String(body, "cloudModel"),
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

// applyAudioFormatParams fills the encoder bindings for the requested container.
//
// The accepted containers and the quality value each one requires come from the workflow's
// `outputs.formats` declaration: SaveAudioAdvanced validates quality against the selected format
// (V0/128k/320k for mp3, 64k-320k for opus, nothing for flac), so the two inputs have to move
// together and a mismatched pair is rejected by ComfyUI, not by us. A container the encoder cannot
// write is refused rather than silently answered with a different one — the canvas names the saved
// file after the response Content-Type, so a lie would surface as a mismatched extension. Values
// the caller supplied win, and a workflow that declares no containers passes the request through.
func applyAudioFormatParams(entry *workflow.Entry, format string, params map[string]any) error {
	if format == "" {
		return nil
	}
	if entry == nil || entry.Outputs == nil || len(entry.Outputs.Formats) == 0 {
		if _, exists := params["format"]; !exists {
			params["format"] = format
		}
		return nil
	}
	spec, ok := entry.Outputs.Formats[format]
	if !ok {
		return httpx.Errorf(http.StatusBadRequest, "unsupported_audio_format",
			"workflow %s writes %s, not %s", entry.Name,
			strings.Join(entry.Outputs.FormatNames(), ", "), format)
	}
	if entry.Outputs.FormatParam != "" {
		if _, exists := params[entry.Outputs.FormatParam]; !exists {
			params[entry.Outputs.FormatParam] = format
		}
	}
	if spec.Quality != "" && entry.Outputs.QualityParam != "" {
		if _, exists := params[entry.Outputs.QualityParam]; !exists {
			params[entry.Outputs.QualityParam] = spec.Quality
		}
	}
	return nil
}

// handleAudioSpeech implements POST /v1/audio/speech.
//
// The canvas audio node speaks the OpenAI speech contract: one text in, audio bytes out.
// Music workflows take two texts, so `input` is the style caption and the optional
// `instructions` field carries the lyrics. Both are plain bindings in registry.json, so a
// prompt-only speech workflow keeps working without a special case here. `speed` and
// `voice` have no meaning for music and are ignored.
func (s *Server) handleAudioSpeech(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	entry, err := s.requireModel(httpx.String(body, "model"), "audio")
	if err != nil {
		return err
	}

	prompt := strings.TrimSpace(httpx.String(body, "input"))
	if prompt == "" {
		return httpx.NewError(http.StatusBadRequest, "missing_prompt", "the audio request is missing `input`")
	}

	params, err := parseParams(body)
	if err != nil {
		return err
	}
	if lyrics := strings.TrimSpace(httpx.String(body, "instructions")); lyrics != "" {
		if _, exists := params["lyrics"]; !exists {
			params["lyrics"] = lyrics
		}
	}
	if err := applyAudioFormatParams(entry, strings.ToLower(strings.TrimSpace(httpx.String(body, "response_format"))), params); err != nil {
		return err
	}

	// Unlike images and videos there is no length parameter in the speech contract, so
	// `duration`/`seconds` are accepted for callers that want to cap the song length.
	seconds := httpx.Number(body, "duration", httpx.Number(body, "seconds", 0))
	if seconds > 0 {
		seconds = math.Min(math.Max(seconds, 4), 360)
	}

	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Prompt:  prompt,
		Count:   1,
		Seconds: seconds,
		Quality: httpx.String(body, "quality"),
		Params:  params,
		Presets: entry.Presets,
	})

	// The audio node expects one blocking call, so this adapter submits the run and waits for
	// it. The run itself is queued: if this request dies, the song keeps rendering and stays
	// readable through GET /api/tasks/{id} — the reason it is no longer generated inline.
	stored := s.submitRun(runSpec{
		Object:    task.ObjectAudio,
		Entry:     entry,
		Context:   runCtx,
		TimeoutMs: s.cfg.AudioTimeoutMs,
		RequestID: ctx.RequestID,
		Request: map[string]any{
			"prompt":         prompt,
			"seconds":        seconds,
			"format":         params["format"],
			"hasLyrics":      params["lyrics"] != nil,
			"steps":          params["steps"],
			"audioQuality":   params["audioQuality"],
			"responseFormat": httpx.String(body, "response_format"),
		},
	}, nil)
	s.logger.Info("audio %s queued %s %s", entry.Name, stored.ID, ctx.RequestID)

	requestCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	finished, err := s.waitRun(requestCtx, stored.ID)
	if err != nil {
		if requestCtx.Err() != nil {
			s.logger.Info("audio %s is still running as run %s after the client left", entry.Name, stored.ID)
			return nil
		}
		return err
	}
	switch finished.Status {
	case "completed":
	case "cancelled":
		return httpx.NewError(http.StatusConflict, "run_cancelled", "the audio run was cancelled")
	default:
		return runFailure(finished)
	}

	path := s.tasks.ContentPath(stored.ID)
	if path == "" {
		return httpx.NewError(http.StatusBadGateway, "no_audio_output", "the audio run finished without an output file")
	}
	mimeType := ""
	if finished.MimeType != nil {
		mimeType = *finished.MimeType
	}
	s.logger.Info("audio %s -> %s (%s) %s", entry.Name, stored.ID, defaultStringOf(mimeType, "audio/mpeg"), ctx.RequestID)

	ctx.W.Header().Set("Content-Type", defaultStringOf(mimeType, util.MimeFromFilename(path)))
	ctx.W.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(path)+"\"")
	http.ServeFile(ctx.W, ctx.R, path)
	return nil
}

// handleVideoGet implements GET /v1/videos/{id}.
func (s *Server) handleVideoGet(ctx *httpx.Ctx) error {
	id := ctx.R.PathValue("id")
	stored := s.tasks.Get(id)
	if stored == nil {
		return httpx.NewError(http.StatusNotFound, "video_not_found", "video task not found")
	}
	data := stored
	payload := map[string]any{
		"id":         data.ID,
		"object":     "video",
		"status":     data.Status,
		"progress":   data.Progress,
		"created_at": data.CreatedAt,
		"model":      data.Model,
	}
	if data.PromptID != nil && *data.PromptID != "" {
		payload["prompt_id"] = *data.PromptID
		if progress := s.hub.GetProgress(*data.PromptID); progress != nil {
			payload["steps"] = map[string]any{"step": progress.Step, "total": progress.Total}
		}
		if preview := s.hub.Get(*data.PromptID); preview != nil {
			payload["preview"] = map[string]any{
				"url":   "/api/previews/" + url.PathEscape(*data.PromptID) + "?t=" + util.Itoa(preview.UpdatedAt.UnixMilli()),
				"mime":  preview.Mime,
				"step":  preview.Step,
				"total": preview.Total,
			}
		}
	}
	if data.URL != nil && *data.URL != "" {
		payload["url"] = *data.URL
	}
	if data.Error != nil && *data.Error != "" {
		payload["error"] = map[string]any{"message": *data.Error}
	}
	if data.CompletedAt != nil && *data.CompletedAt != "" {
		payload["completed_at"] = *data.CompletedAt
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handleVideoContent implements GET /v1/videos/{id}/content.
func (s *Server) handleVideoContent(ctx *httpx.Ctx) error {
	id := ctx.R.PathValue("id")
	if s.tasks.Get(id) == nil {
		return httpx.NewError(http.StatusNotFound, "video_not_found", "video task not found")
	}
	path := s.tasks.ContentPath(id)
	if path == "" {
		return httpx.NewError(http.StatusConflict, "video_not_ready", "the video file is not ready yet")
	}
	http.ServeFile(ctx.W, ctx.R, path)
	return nil
}

// handleVideoCancel implements POST /v1/videos/{id}/cancel.
func (s *Server) handleVideoCancel(ctx *httpx.Ctx) error {
	id := ctx.R.PathValue("id")
	stored := s.tasks.Get(id)
	if stored == nil {
		return httpx.NewError(http.StatusNotFound, "video_not_found", "video task not found")
	}
	if stored.PromptID != nil && *stored.PromptID != "" {
		promptID := *stored.PromptID
		go func() {
			requestCtx, cancel := contextWithTimeout(10000)
			defer cancel()
			s.comfy.Cancel(requestCtx, promptID)
		}()
	}
	updated := s.tasks.Cancel(id)
	httpx.OK(ctx.W, taskPublic(updated))
	return nil
}

// handleTaskGet implements GET /v1/tasks/{id}.
func (s *Server) handleTaskGet(ctx *httpx.Ctx) error {
	stored := s.tasks.Get(ctx.R.PathValue("id"))
	if stored == nil {
		return httpx.NewError(http.StatusNotFound, "task_not_found", "task not found")
	}
	httpx.OK(ctx.W, taskPublic(stored))
	return nil
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
