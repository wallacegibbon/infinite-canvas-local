package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/prompt"
	"comfyui-gateway/internal/task"
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

	// Every per-workflow media rule (text-to-video takes none, frames take one or two images,
	// multi-reference takes up to six images and a video, character swap needs a video) lives in
	// registry.json now; this call is the only place that judges a request's media mix.
	plan, err := workflow.PlanMedia(entry, workflow.MediaCounts{
		Images: len(files),
		Videos: len(videoFiles),
		Audios: len(audioFiles),
	}, strings.TrimSpace(httpx.String(body, "mode")), mergedParams)
	if err != nil {
		return err
	}
	videoMode := plan.Mode
	if plan.FrameMode != "" {
		mergedParams["frameMode"] = plan.FrameMode
	}

	uploadCtx, cleanup := s.withCancel(ctx.R, ctx.RequestID)
	defer cleanup()

	uploadedImages, err := s.uploadFiles(uploadCtx, files)
	if err != nil {
		return err
	}
	uploadedVideos, err := s.uploadFiles(uploadCtx, videoFiles)
	if err != nil {
		return err
	}
	uploadedAudios, err := s.uploadFiles(uploadCtx, audioFiles)
	if err != nil {
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
	if entry.SourceVideoFrames && len(videoFiles) > 0 {
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
	}

	stored := s.submitRun(runSpec{
		Object:    task.ObjectVideo,
		Entry:     entry,
		Context:   runCtx,
		TimeoutMs: s.cfg.VideoTimeoutMs,
		RequestID: ctx.RequestID,
		Request:   request,
	}, nil)

	s.logger.Info("video %s queued %s %s", entry.Name, stored.ID, ctx.RequestID)
	httpx.OK(ctx.W, queuedPayload(s.tasks.Get(stored.ID)))
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
	stored := s.submitRun(runSpec{
		Object:    task.ObjectVideo,
		Entry:     entry,
		Context:   runCtx,
		TimeoutMs: s.cfg.VideoTimeoutMs,
		RequestID: ctx.RequestID,
		Request:   request,
	}, nil)

	s.logger.Info("video upscale %s queued %s %s", entry.Name, stored.ID, ctx.RequestID)
	httpx.OK(ctx.W, queuedPayload(s.tasks.Get(stored.ID)))
	return nil
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
func queuedPayload(stored *task.Task) map[string]any {
	object := task.ObjectVideo
	id := ""
	if stored != nil {
		object = stored.Object
		id = stored.ID
	}
	return map[string]any{
		"id":         id,
		"object":     object,
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
