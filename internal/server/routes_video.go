package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"comfyui-gateway/internal/generate"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/prompt"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// handleVideoCreate implements POST /v1/videos.
//
// This is the most branch-heavy endpoint in the gateway: each H3 workflow accepts a
// different mix of images, video and audio, and the differences are enforced here so
// that ComfyUI never receives a graph it will reject minutes later.
func (s *Server) handleVideoCreate(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	entry, err := s.requireModel(httpx.String(body, "model"), "video")
	if err != nil {
		return err
	}

	allFiles := ctx.Files()
	files := orderedVideoFiles(allFiles)
	videoFiles := orderedVideoRefs(allFiles)
	audioFiles := orderedAudioRefs(allFiles)

	params, err := parseParams(body)
	if err != nil {
		return err
	}
	mergedParams := map[string]any{}
	for key, value := range params {
		mergedParams[key] = value
	}

	interpolation := strings.TrimSpace(util.StringField(params, "interpolation"))
	if interpolation == "" {
		interpolation = "off"
	}
	switch interpolation {
	case "off", "2x", "3x":
	default:
		return httpx.NewError(http.StatusBadRequest, "invalid_interpolation", "interpolation must be off, 2x or 3x")
	}

	videoMode := strings.TrimSpace(httpx.String(body, "mode"))
	switch entry.ID {
	case "video-h3-t2v":
		videoMode = "text"
	case "video-h3-frames":
		videoMode = "frames"
	case "video-h3-fast":
		if videoMode != "text" {
			videoMode = "frames"
		}
	case "video-h3-ref2va":
		videoMode = "reference"
	}

	requiredImages := requiredImageCount(entry)
	totalMedia := len(files) + len(videoFiles) + len(audioFiles)

	switch {
	case entry.ID == "video-h3-t2v":
		if totalMedia > 0 {
			return httpx.NewError(http.StatusBadRequest, "unsupported_video_media",
				"H3 text-to-video accepts no image/video/audio; use the H3 first/last-frame workflow or H3 multi-reference instead")
		}
		requiredImages = 0

	case entry.ID == "video-h3-fast" && videoMode == "text":
		if totalMedia > 0 {
			return httpx.NewError(http.StatusBadRequest, "unsupported_video_media",
				"the H3 fast draft text mode accepts no image/video/audio; switch to first/last frame to supply a first frame")
		}
		requiredImages = 0

	case entry.ID == "video-h3-frames" || entry.ID == "video-h3-fast":
		if len(videoFiles) > 0 || len(audioFiles) > 0 {
			return httpx.NewError(http.StatusBadRequest, "unsupported_video_media",
				"H3 first/last frame accepts only 1-2 images; use H3 multi-reference for audio or video")
		}
		frameMode := util.StringField(params, "frameMode")
		if frameMode == "" {
			if len(files) >= 2 {
				frameMode = "both"
			} else {
				frameMode = "first"
			}
		}
		switch frameMode {
		case "first", "last", "both":
		default:
			return httpx.NewError(http.StatusBadRequest, "invalid_frame_mode", "frameMode must be first, last or both")
		}
		mergedParams["frameMode"] = frameMode
		switch {
		case frameMode == "both":
			requiredImages = 2
		case entry.ID == "video-h3-fast":
			requiredImages = 0
		default:
			requiredImages = 1
		}
	}

	if len(files) < requiredImages {
		hint := ""
		switch entry.ID {
		case "video-h3-ref2va":
			hint = " (H3 multi-reference accepts 1-6 images, 0-2 audio clips and 0-1 video, with at least one media item)"
		case "video-h3-frames", "video-h3-fast":
			hint = " (first/last frame accepts a first frame, a last frame, or both; both needs 2 images)"
		}
		return httpx.Errorf(http.StatusBadRequest, "missing_reference_image",
			"workflow %s needs at least %d reference image(s)%s", entry.Name, requiredImages, hint)
	}
	if entry.MinMedia > 0 && totalMedia < entry.MinMedia {
		return httpx.Errorf(http.StatusBadRequest, "missing_reference_media",
			"workflow %s needs at least %d reference media item(s) (image/audio/video)", entry.Name, entry.MinMedia)
	}

	requestCtx, cancel := context.WithCancel(context.Background())
	_ = requestCtx

	uploadCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	uploadedImages, err := s.uploadFiles(uploadCtx, files)
	if err != nil {
		cancel()
		return err
	}
	uploadedVideos, err := s.uploadFiles(uploadCtx, videoFiles)
	if err != nil {
		cancel()
		return err
	}
	uploadedAudios, err := s.uploadFiles(uploadCtx, audioFiles)
	if err != nil {
		cancel()
		return err
	}

	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Prompt:         httpx.String(body, "prompt"),
		Size:           httpx.String(body, "size"),
		Count:          1,
		Quality:        defaultStringOf(httpx.String(body, "resolution_name"), httpx.String(body, "vquality")),
		Seconds:        httpx.Number(body, "seconds", 0),
		Mode:           videoMode,
		Images:         toUploadResults(files),
		UploadedImages: uploadedImages,
		UploadedVideos: uploadedVideos,
		UploadedAudios: uploadedAudios,
		Params: map[string]any{
			"ratio":           httpx.String(body, "ratio"),
			"resolution_name": httpx.String(body, "resolution_name"),
			"generate_audio":  httpx.String(body, "generate_audio"),
			"watermark":       httpx.String(body, "watermark"),
			"params":          mergedParams,
		},
	})
	// Params must be the merged map itself, not a nested copy.
	for key, value := range mergedParams {
		runCtx.Params[key] = value
	}
	delete(runCtx.Params, "params")
	if len(videoFiles) > 0 {
		runCtx.SourceVideoFrames = s.probeVideoFrames(videoFiles[0])
	}

	request := map[string]any{
		"prompt":          httpx.String(body, "prompt"),
		"seconds":         httpx.Number(body, "seconds", 0),
		"size":            httpx.String(body, "size"),
		"ratio":           httpx.String(body, "ratio"),
		"resolution_name": httpx.String(body, "resolution_name"),
		"mode":            videoMode,
		"images":          mediaNames(uploadedImages),
		"videos":          mediaNames(uploadedVideos),
		"audios":          mediaNames(uploadedAudios),
		"interpolation":   interpolation,
	}

	stored := s.tasks.Create(entry.Name, request)
	s.tasks.Enqueue(stored, func(job *task.Task, progress func(task.Update), jobCtx context.Context) (*task.Result, error) {
		result, err := s.runner.RunVideo(jobCtx, generate.RunOptions{
			Entry:          entry,
			Context:        runCtx,
			RequestID:      ctx.RequestID,
			LoraCatalog:    s.setup.LoraNames(),
			ModelAliasFor:  s.setup.ModelAliasFor,
			TimeoutMs:      s.cfg.VideoTimeoutMs,
			PollIntervalMs: s.cfg.PollIntervalMs,
			OnProgress: func(event generate.ProgressEvent) {
				progress(task.Update{Progress: event.Progress, PromptID: event.PromptID})
			},
		})
		if err != nil {
			return nil, err
		}

		file := result.Files[0]
		if interpolation != "off" {
			progress(task.Update{Progress: 0.97})
			factor := 2
			if interpolation == "3x" {
				factor = 3
			}
			interpolator := generate.NewInterpolator(s.cfg.WorkflowDir, s.comfy, s.logger)
			interpolated, err := interpolator.Interpolate(jobCtx, file.Buffer, file.Filename, factor, ctx.RequestID, s.cfg.VideoTimeoutMs)
			if err != nil {
				return nil, err
			}
			file = generate.OutputFile{
				Buffer:   interpolated.Buffer,
				MimeType: interpolated.MimeType,
				Filename: interpolated.Filename,
			}
		}
		return &task.Result{
			Buffer:   file.Buffer,
			Ext:      util.ExtFromMime(file.MimeType, file.Filename, "mp4"),
			MimeType: file.MimeType,
			PromptID: result.PromptID,
		}, nil
	})

	s.logger.Info("video %s queued %s %s", entry.Name, stored.ID, ctx.RequestID)
	cancel()
	httpx.OK(ctx.W, queuedPayload(stored.ID))
	return nil
}

// handleVideoUpscale implements POST /v1/videos/upscale.
func (s *Server) handleVideoUpscale(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	entry, err := s.requireModel(defaultStringOf(httpx.String(body, "model"), "video-seedvr2-upscale"), "video")
	if err != nil {
		return err
	}
	if entry.Category != "upscale" {
		return httpx.Errorf(http.StatusBadRequest, "not_upscale_workflow", "model %s is not a video upscale workflow", entry.Name)
	}

	videoFiles := orderedVideoRefs(ctx.Files())
	if len(videoFiles) == 0 {
		return httpx.NewError(http.StatusBadRequest, "missing_input_video", "video upscale request is missing the input video")
	}

	params, err := parseParams(body)
	if err != nil {
		return err
	}

	uploadCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()
	uploadedVideos, err := s.uploadFiles(uploadCtx, videoFiles)
	if err != nil {
		return err
	}

	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Count:          1,
		Mode:           "upscale",
		UploadedVideos: uploadedVideos,
		Params:         params,
	})

	request := map[string]any{
		"mode":       "upscale",
		"video":      firstMediaName(uploadedVideos),
		"resolution": params["resolution"],
		"fps":        params["fps"],
		"scale":      params["scale"],
	}
	stored := s.tasks.Create(entry.Name, request)
	s.tasks.Enqueue(stored, func(job *task.Task, progress func(task.Update), jobCtx context.Context) (*task.Result, error) {
		result, err := s.runner.RunVideo(jobCtx, generate.RunOptions{
			Entry:          entry,
			Context:        runCtx,
			RequestID:      ctx.RequestID,
			ModelAliasFor:  s.setup.ModelAliasFor,
			TimeoutMs:      s.cfg.VideoTimeoutMs,
			PollIntervalMs: s.cfg.PollIntervalMs,
			OnProgress: func(event generate.ProgressEvent) {
				progress(task.Update{Progress: event.Progress, PromptID: event.PromptID})
			},
		})
		if err != nil {
			return nil, err
		}
		file := result.Files[0]
		return &task.Result{
			Buffer:   file.Buffer,
			Ext:      util.ExtFromMime(file.MimeType, file.Filename, "mp4"),
			MimeType: file.MimeType,
			PromptID: result.PromptID,
		}, nil
	})

	s.logger.Info("video upscale %s queued %s %s", entry.Name, stored.ID, ctx.RequestID)
	httpx.OK(ctx.W, queuedPayload(stored.ID))
	return nil
}

// handleVideoInterpolate implements POST /v1/videos/interpolate.
func (s *Server) handleVideoInterpolate(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	videoFiles := orderedVideoRefs(ctx.Files())
	if len(videoFiles) == 0 {
		return httpx.NewError(http.StatusBadRequest, "missing_input_video", "interpolation request is missing the input video")
	}

	multiplier := strings.ToLower(strings.TrimSpace(defaultStringOf(httpx.String(body, "multiplier"), "2x")))
	if multiplier != "2x" && multiplier != "3x" {
		return httpx.NewError(http.StatusBadRequest, "invalid_interpolation", "interpolation supports only 2x or 3x")
	}

	source := videoFiles[0]
	factor := 2
	if multiplier == "3x" {
		factor = 3
	}
	originalName := defaultStringOf(source.Filename, "input.mp4")

	stored := s.tasks.Create("video-rife", map[string]any{
		"mode":       "interpolate",
		"multiplier": multiplier,
		"video":      originalName,
	})

	interpolator := generate.NewInterpolator(s.cfg.WorkflowDir, s.comfy, s.logger)
	s.tasks.Enqueue(stored, func(job *task.Task, progress func(task.Update), jobCtx context.Context) (*task.Result, error) {
		progress(task.Update{Progress: 0.1})
		timeout := s.cfg.VideoTimeoutMs
		if timeout <= 0 {
			timeout = 10 * 60 * 1000
		}
		result, err := interpolator.Interpolate(jobCtx, source.Bytes, originalName, factor, ctx.RequestID, timeout)
		if err != nil {
			return nil, err
		}
		return &task.Result{
			Buffer:   result.Buffer,
			Ext:      util.ExtFromMime(result.MimeType, result.Filename, "mp4"),
			MimeType: result.MimeType,
		}, nil
	})

	s.logger.Info("video interpolate %s queued %s %s", multiplier, stored.ID, ctx.RequestID)
	httpx.OK(ctx.W, queuedPayload(stored.ID))
	return nil
}

// requiredImageCount derives how many reference images a workflow needs.
func requiredImageCount(entry *workflow.Entry) int {
	max := 0
	for _, binding := range entry.Bindings() {
		from := binding.String("from")
		if from == "image" && binding.Bool("array") {
			if min := binding.Int("min", 0); min > max {
				max = min
			}
			continue
		}
		if !binding.Bool("required") {
			continue
		}
		if from != "image" && !binding.Has("imageIndex") {
			continue
		}
		index := binding.Int("imageIndex", 0)
		if index+1 > max {
			max = index + 1
		}
	}
	return max
}

func hasBackgroundSupport(entry *workflow.Entry) bool {
	if entry == nil {
		return false
	}
	if _, ok := entry.Binding("background"); ok {
		return true
	}
	return entry.BackgroundPrompt != ""
}

func optionalString(body map[string]any, key string) *string {
	value, ok := body[key]
	if !ok || value == nil {
		return nil
	}
	text := httpx.String(body, key)
	return &text
}

func defaultStringOf(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// toUploadResults preserves the original upload metadata alongside the reference.
func toUploadResults(files []httpx.File) []workflow.UploadResult {
	out := make([]workflow.UploadResult, 0, len(files))
	for _, file := range files {
		out = append(out, workflow.UploadResult{
			Filename: file.Filename,
			Mimetype: file.Mimetype,
			Bytes:    len(file.Bytes),
		})
	}
	return out
}

// applyLayoutPrompt replaces the prompt with a MOSS layout payload when asked.
func applyLayoutPrompt(runCtx *workflow.Context) {
	width, height := 0, 0
	if runCtx.Size != nil {
		width, height = runCtx.Size.Width, runCtx.Size.Height
	}
	prompt.ApplyMossLayoutPrompt(&runCtx.Prompt, runCtx.Params, width, height)
}

func mediaNames(items []workflow.Media) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Name)
	}
	return out
}

func firstMediaName(items []workflow.Media) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].Name
}

// queuedPayload is the response every video endpoint returns immediately.
func queuedPayload(id string) map[string]any {
	return map[string]any{
		"id":         id,
		"object":     "video",
		"status":     "queued",
		"progress":   0,
		"created_at": nowUnix(),
	}
}

// taskPublic renders a task for /v1/tasks and cancel responses.
func taskPublic(stored *task.Task) map[string]any {
	return task.PublicTask(stored)
}

func nowUnix() int64 { return time.Now().Unix() }

// contextWithTimeout is a tiny helper so cancel paths cannot block forever.
func contextWithTimeout(milliseconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(milliseconds)*time.Millisecond)
}
