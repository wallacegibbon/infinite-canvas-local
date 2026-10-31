package server

// The run protocol from docs/protocol.md: one resource for every generation.
//
//   POST   /api/runs                      submit, always 202 with the run
//   GET    /api/runs                      list (status/workflow filters)
//   GET    /api/runs/{id}                 status, progress, outputs, error
//   GET    /api/runs/{id}/outputs/{index} artifact bytes
//   GET    /api/runs/{id}/preview         in-flight preview frame, when one exists
//   DELETE /api/runs/{id}                 cancel (also interrupts ComfyUI)
//
// Media goes in as upload ids (POST /api/uploads), so a role is explicit and the same file can be
// reused by several runs. The `/v1/*` routes stay as adapters until the canvas has migrated.

import (
	"context"
	"encoding/base64"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"comfyui-gateway/internal/generate"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/upload"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

func (s *Server) routesRuns(mux *http.ServeMux) {
	s.route(mux, "POST /api/uploads", s.handleUploadCreate)
	s.route(mux, "GET /api/uploads", s.handleUploadList)
	s.route(mux, "POST /api/runs", s.handleRunCreate)
	s.route(mux, "GET /api/runs", s.handleRunList)
	s.route(mux, "GET /api/runs/{id}", s.handleRunGet)
	s.route(mux, "GET /api/runs/{id}/outputs/{index}", s.handleRunOutput)
	s.route(mux, "GET /api/runs/{id}/preview", s.handleRunPreview)
	s.route(mux, "DELETE /api/runs/{id}", s.handleRunCancel)
}

// mediaRole maps a role name a client may use to the kind of file it must carry. The order is the
// order images are handed to the bindings, which is why `source`/`mask` come first: Qwen's inpaint
// reads image index 0 and 1.
var mediaRoles = []struct {
	role string
	kind string
}{
	{"source", upload.KindImage},
	{"mask", upload.KindImage},
	{"first_frame", upload.KindImage},
	{"last_frame", upload.KindImage},
	{"reference", upload.KindImage},
	{"image", upload.KindImage},
	{"images", upload.KindImage},
	{"video", upload.KindVideo},
	{"videos", upload.KindVideo},
	{"reference_video", upload.KindVideo},
	{"audio", upload.KindAudio},
	{"audios", upload.KindAudio},
	{"reference_audio", upload.KindAudio},
}

// handleUploadCreate implements POST /api/uploads: one media file in, an id out.
func (s *Server) handleUploadCreate(ctx *httpx.Ctx) error {
	files := ctx.Files()
	if len(files) != 1 {
		return httpx.NewError(http.StatusBadRequest, "single_file_required", "an upload carries exactly one file")
	}
	file := files[0]
	kind := upload.KindOf(file.Mimetype, file.Filename)
	if kind == "" {
		return httpx.Errorf(http.StatusBadRequest, "unsupported_media_kind",
			"cannot use %q (%s); uploads are images, videos or audio clips",
			defaultStringOf(file.Filename, "the uploaded file"), defaultStringOf(file.Mimetype, "unknown type"))
	}
	stored, err := s.uploads.Save(kind, file.Filename, file.Mimetype, file.Bytes)
	if err != nil {
		if err == upload.ErrTooLarge {
			return httpx.Errorf(http.StatusRequestEntityTooLarge, "upload_too_large",
				"the upload exceeds the configured limit (%d bytes)", s.cfg.MaxUploadBytes)
		}
		return httpx.NewError(http.StatusInternalServerError, "upload_failed", "failed to store the upload: "+err.Error())
	}

	// Copy it into ComfyUI now so the run does not pay for it; if that fails the bytes are on disk
	// and the run retries, which is why the upload still succeeds.
	if _, err := s.comfyMedia(ctx.R.Context(), stored); err != nil {
		s.logger.Warn("upload %s stored but not copied to ComfyUI yet: %s", stored.ID, err)
	}
	okJSON(ctx.W, map[string]any{"upload": uploadPayload(s.uploads.Get(stored.ID))})
	return nil
}

// handleUploadList implements GET /api/uploads.
func (s *Server) handleUploadList(ctx *httpx.Ctx) error {
	files := s.uploads.List()
	rows := make([]map[string]any, 0, len(files))
	for _, file := range files {
		rows = append(rows, uploadPayload(file))
	}
	okJSON(ctx.W, map[string]any{"uploads": rows})
	return nil
}

func uploadPayload(file *upload.File) map[string]any {
	if file == nil {
		return nil
	}
	return map[string]any{
		"id":         file.ID,
		"kind":       file.Kind,
		"filename":   file.Filename,
		"mime":       file.MimeType,
		"bytes":      file.Bytes,
		"created_at": file.CreatedAt,
	}
}

// handleRunCreate implements POST /api/runs.
func (s *Server) handleRunCreate(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}

	entry, err := s.resolveWorkflow(httpx.String(body, "workflow"))
	if err != nil {
		return err
	}

	rawInputs, present := body["inputs"]
	if present && rawInputs != nil {
		if _, ok := rawInputs.(map[string]any); !ok {
			return httpx.NewError(http.StatusBadRequest, "invalid_inputs", "inputs must be an object")
		}
	}
	inputs := httpx.Object(body, "inputs")
	if inputs == nil {
		inputs = map[string]any{}
	}

	media, err := s.resolveRunMedia(ctx, entry, httpx.Object(body, "media"))
	if err != nil {
		return err
	}

	// A workflow with variants needs a mode, and the first variant is the default — the same rule
	// the image-edit adapter applies. The variant carries its own graph, bindings and contract, so
	// resolving it before planning is what makes an inpaint request mean "source + mask".
	mode := httpx.String(inputs, "mode")
	if entry.Variants.Len() > 0 {
		if mode == "" {
			mode = entry.Variants.Keys[0]
		}
		if _, ok := entry.Variants.Get(mode); !ok {
			return httpx.Errorf(http.StatusBadRequest, "unsupported_mode",
				"workflow %s has no mode %q; modes are %s", entry.Name, mode, strings.Join(entry.Variants.Keys, ", "))
		}
		entry = s.registry.ResolveForMode(entry, mode)
	}

	// Typed before planning: an unknown key or a wrong value must be a 400 now, not a failed run
	// minutes from now. The contract comes from the resolved variant, so a mode's own bindings
	// and parameters apply.
	if err := workflow.ValidateInputs(entry, inputs, workflow.InputLimits{MaxCount: s.cfg.MaxImageCount}); err != nil {
		return err
	}

	params := make(map[string]any, len(inputs))
	for key, value := range inputs {
		params[key] = value
	}
	plan, err := workflow.PlanMedia(entry, media.counts, mode, params)
	if err != nil {
		return err
	}
	if plan.FrameMode != "" {
		params["frameMode"] = plan.FrameMode
	}

	object := entry.Type
	if entry.Outputs != nil && entry.Outputs.Kind != "" {
		object = entry.Outputs.Kind
	}
	switch object {
	case task.ObjectImage, task.ObjectVideo, task.ObjectAudio:
	default:
		return httpx.Errorf(http.StatusBadRequest, "unsupported_run_object",
			"workflow %s produces %q, which the gateway cannot run yet", entry.Name, object)
	}

	runCtx := workflow.BuildContext(workflow.BuildContextParams{
		Prompt:         httpx.String(inputs, "prompt"),
		Negative:       optionalString(inputs, "negative"),
		Size:           httpx.String(inputs, "size"),
		Count:          httpx.Number(inputs, "count", httpx.Number(inputs, "n", 0)),
		Quality:        httpx.String(inputs, "quality"),
		Seconds:        httpx.Number(inputs, "seconds", httpx.Number(inputs, "duration", 0)),
		Mode:           plan.Mode,
		Images:         media.uploadResults,
		UploadedImages: media.images,
		UploadedVideos: media.videos,
		UploadedAudios: media.audios,
		Params:         params,
		Presets:        entry.Presets,
	})
	runCtx.SourceVideoFrames = media.sourceVideoFrames

	stored := s.submitRun(runSpec{
		Object:    object,
		Entry:     entry,
		Context:   runCtx,
		TimeoutMs: s.runTimeout(object),
		RequestID: ctx.RequestID,
		Request:   map[string]any{"inputs": inputs, "media": media.summary},
	}, nil)
	s.logger.Info("run %s queued %s (%s) %s", object, stored.ID, entry.Name, ctx.RequestID)

	// Snapshot: the queue may already be running this record, and serialising the live one races
	// the worker's progress updates.
	httpx.JSON(ctx.W, http.StatusAccepted, s.runPayload(s.tasks.Get(stored.ID)))
	return nil
}

// resolveWorkflow finds a workflow by the id/name/variant a request named.
func (s *Server) resolveWorkflow(name string) (*workflow.Entry, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, httpx.NewError(http.StatusBadRequest, "missing_workflow", "missing workflow parameter")
	}
	resolved := s.registry.ResolveModel(name, s.setup.LoaderOptions())
	if resolved == nil {
		return nil, httpx.Errorf(http.StatusNotFound, "workflow_not_found",
			"workflow not found: %s. Registered workflows are listed by GET /api/workflows.", name)
	}
	return resolved.Entry, nil
}

// runTimeout picks the timeout for an artifact kind. Video and audio default to no limit; images
// keep the historical request timeout.
func (s *Server) runTimeout(object string) int {
	switch object {
	case task.ObjectVideo:
		return s.cfg.VideoTimeoutMs
	case task.ObjectAudio:
		return s.cfg.AudioTimeoutMs
	default:
		return s.cfg.RequestTimeoutMs
	}
}

// resolvedMedia is the request's media after upload lookup, in binding order.
type resolvedMedia struct {
	images            []workflow.Media
	videos            []workflow.Media
	audios            []workflow.Media
	summary           map[string]any
	counts            workflow.MediaCounts
	uploadResults     []workflow.UploadResult
	sourceVideoFrames int
}

// resolveRunMedia turns upload ids into ComfyUI references, checking each role's kind.
func (s *Server) resolveRunMedia(ctx *httpx.Ctx, entry *workflow.Entry, media map[string]any) (*resolvedMedia, error) {
	resolved := &resolvedMedia{summary: map[string]any{}}
	if len(media) == 0 {
		return resolved, nil
	}

	firstVideoPath := ""
	known := make(map[string]string, len(mediaRoles))
	for _, role := range mediaRoles {
		known[role.role] = role.kind
	}
	for role := range media {
		if _, ok := known[role]; !ok {
			return nil, httpx.Errorf(http.StatusBadRequest, "unsupported_media_role",
				"unknown media role %q; roles are source, mask, first_frame, last_frame, reference, image, video, audio", role)
		}
	}

	for _, role := range mediaRoles {
		ids := mediaIDs(media[role.role])
		if len(ids) == 0 {
			continue
		}
		for _, id := range ids {
			file := s.uploads.Get(id)
			if file == nil {
				return nil, httpx.Errorf(http.StatusNotFound, "upload_not_found", "upload not found: %s", id)
			}
			if file.Kind != role.kind {
				return nil, httpx.Errorf(http.StatusBadRequest, "media_kind_mismatch",
					"role %q needs %s, but upload %s is %s", role.role, role.kind, id, file.Kind)
			}
			reference, err := s.comfyMedia(ctx.R.Context(), file)
			if err != nil {
				return nil, err
			}
			if role.kind == upload.KindVideo && len(resolved.videos) == 0 {
				firstVideoPath = file.Path
			}
			switch role.kind {
			case upload.KindImage:
				resolved.images = append(resolved.images, reference)
				resolved.uploadResults = append(resolved.uploadResults, workflow.UploadResult{
					Filename: file.Filename, OriginalName: file.Filename, Mimetype: file.MimeType, Bytes: int(file.Bytes),
				})
			case upload.KindVideo:
				resolved.videos = append(resolved.videos, reference)
			case upload.KindAudio:
				resolved.audios = append(resolved.audios, reference)
			}
			resolved.summary[role.role] = appendStrings(resolved.summary[role.role], id)
		}
	}

	resolved.counts = workflow.MediaCounts{
		Images: len(resolved.images),
		Videos: len(resolved.videos),
		Audios: len(resolved.audios),
	}
	if entry.SourceVideoFrames && firstVideoPath != "" {
		// Character swap reads the source video's length; the upload is already on disk, so
		// ffprobe reads it in place instead of through a temporary copy.
		resolved.sourceVideoFrames = s.probeVideoPath(firstVideoPath)
	}
	return resolved, nil
}

// comfyMedia returns the ComfyUI reference for an upload, pushing the bytes once if needed.
func (s *Server) comfyMedia(ctx context.Context, file *upload.File) (workflow.Media, error) {
	if file.ComfyName != "" {
		return workflow.Media{Name: file.ComfyName, Subfolder: file.ComfySubfolder}, nil
	}
	data, err := file.Read()
	if err != nil {
		return workflow.Media{}, httpx.NewError(http.StatusInternalServerError, "upload_read_failed",
			"cannot read upload "+file.ID+": "+err.Error())
	}
	pushed, err := s.comfy.UploadImage(ctx, data, util.UniqueFileName(file.Filename, ""), defaultMime(file.MimeType))
	if err != nil {
		return workflow.Media{}, err
	}
	s.uploads.SetComfyReference(file.ID, pushed.Name, pushed.Subfolder)
	return workflow.Media{Name: pushed.Name, Subfolder: pushed.Subfolder}, nil
}

// handleRunList implements GET /api/runs.
func (s *Server) handleRunList(ctx *httpx.Ctx) error {
	status := strings.ToLower(strings.TrimSpace(ctx.R.URL.Query().Get("status")))
	workflowName := strings.TrimSpace(ctx.R.URL.Query().Get("workflow"))

	runs := make([]map[string]any, 0)
	for _, stored := range s.tasks.List() {
		if workflowName != "" && stored.Model != workflowName {
			continue
		}
		if status != "" && runStatus(stored.Status) != status {
			continue
		}
		runs = append(runs, s.runPayload(stored))
	}
	okJSON(ctx.W, map[string]any{"runs": runs})
	return nil
}

// handleRunGet implements GET /api/runs/{id}.
func (s *Server) handleRunGet(ctx *httpx.Ctx) error {
	stored, err := s.requireRun(ctx.R.PathValue("id"))
	if err != nil {
		return err
	}
	httpx.OK(ctx.W, s.runPayload(stored))
	return nil
}

// handleRunOutput implements GET /api/runs/{id}/outputs/{index}.
func (s *Server) handleRunOutput(ctx *httpx.Ctx) error {
	stored, err := s.requireRun(ctx.R.PathValue("id"))
	if err != nil {
		return err
	}
	index, convErr := strconv.Atoi(ctx.R.PathValue("index"))
	if convErr != nil || index < 0 {
		return httpx.NewError(http.StatusBadRequest, "invalid_output_index", "the output index must be a number")
	}
	outputs := stored.Outputs
	if len(outputs) == 0 && stored.File != nil && *stored.File != "" {
		outputs = []task.TaskOutput{{Index: 0, File: *stored.File, MimeType: deref(stored.MimeType), Bytes: stored.Bytes}}
	}
	if index >= len(outputs) {
		return httpx.Errorf(http.StatusNotFound, "output_not_found",
			"run %s has %d output(s); index %d does not exist", stored.ID, len(outputs), index)
	}
	path := s.tasks.OutputPath(stored.ID, index)
	if path == "" {
		return httpx.NewError(http.StatusNotFound, "output_not_found", "the output file is gone")
	}
	if outputs[index].MimeType != "" {
		ctx.W.Header().Set("Content-Type", outputs[index].MimeType)
	}
	ctx.W.Header().Set("Content-Disposition", "inline; filename=\""+filepath.Base(path)+"\"")
	http.ServeFile(ctx.W, ctx.R, path)
	return nil
}

// handleRunPreview implements GET /api/runs/{id}/preview: a best-effort in-flight frame.
func (s *Server) handleRunPreview(ctx *httpx.Ctx) error {
	stored, err := s.requireRun(ctx.R.PathValue("id"))
	if err != nil {
		return err
	}
	if stored.PromptID == nil {
		return httpx.NewError(http.StatusNotFound, "preview_not_ready", "no preview available yet")
	}
	preview := s.hub.Get(*stored.PromptID)
	if preview == nil {
		return httpx.NewError(http.StatusNotFound, "preview_not_ready", "no preview available yet")
	}
	data, err := base64.StdEncoding.DecodeString(preview.Data)
	if err != nil {
		return httpx.NewError(http.StatusInternalServerError, "invalid_preview", "the preview data is corrupt")
	}
	ctx.W.Header().Set("Cache-Control", "no-store")
	ctx.W.Header().Set("Content-Type", defaultStringOf(preview.Mime, "image/jpeg"))
	ctx.W.WriteHeader(http.StatusOK)
	_, _ = ctx.W.Write(data)
	return nil
}

// handleRunCancel implements DELETE /api/runs/{id}: idempotent, and it interrupts ComfyUI too.
func (s *Server) handleRunCancel(ctx *httpx.Ctx) error {
	stored, err := s.requireRun(ctx.R.PathValue("id"))
	if err != nil {
		return err
	}
	if stored.PromptID != nil && *stored.PromptID != "" {
		promptID := *stored.PromptID
		go func() {
			requestCtx, cancel := contextWithTimeout(10000)
			defer cancel()
			s.comfy.Cancel(requestCtx, promptID)
		}()
	}
	updated := s.tasks.Cancel(stored.ID)
	httpx.OK(ctx.W, s.runPayload(updated))
	return nil
}

func (s *Server) requireRun(id string) (*task.Task, error) {
	stored := s.tasks.Get(strings.TrimSpace(id))
	if stored == nil {
		return nil, httpx.Errorf(http.StatusNotFound, "run_not_found", "run not found: %s", id)
	}
	return stored, nil
}

// runStatus maps the queue's vocabulary onto the run protocol's.
func runStatus(status string) string {
	switch status {
	case "completed":
		return "succeeded"
	case "queued", "running", "failed", "cancelled":
		return status
	default:
		return status
	}
}

// runPayload renders one run for the wire.
func (s *Server) runPayload(stored *task.Task) map[string]any {
	if stored == nil {
		return nil
	}
	payload := map[string]any{
		"id":          stored.ID,
		"workflow":    stored.Model,
		"status":      runStatus(stored.Status),
		"progress":    s.runProgress(stored),
		"outputs":     s.runOutputs(stored),
		"error":       runErrorPayload(stored),
		"request":     stored.Request,
		"created_at":  stored.CreatedAt,
		"started_at":  stored.StartedAt,
		"finished_at": stored.CompletedAt,
	}
	if stored.PromptID != nil {
		payload["prompt_id"] = *stored.PromptID
	}
	return payload
}

// runProgress prefers the live step count from the preview hub and falls back to the fraction the
// queue recorded, so a queued run still reports something before ComfyUI starts.
func (s *Server) runProgress(stored *task.Task) map[string]any {
	progress := map[string]any{"phase": stored.Status, "step": 0, "total": 0, "percent": stored.Progress}
	if stored.PromptID != nil {
		if live := s.hub.GetProgress(*stored.PromptID); live != nil {
			progress["step"] = live.Step
			progress["total"] = live.Total
			if live.Total > 0 {
				progress["percent"] = float64(live.Step) / float64(live.Total)
			}
			if live.State != "" {
				progress["phase"] = live.State
			}
		}
	}
	return progress
}

// runOutputs renders the artifact list. Records written before multi-output tasks existed carry
// only the first file, so it is synthesized from the legacy fields.
func (s *Server) runOutputs(stored *task.Task) []map[string]any {
	outputs := stored.Outputs
	if len(outputs) == 0 && stored.File != nil && *stored.File != "" {
		outputs = []task.TaskOutput{{
			Index:    0,
			File:     *stored.File,
			MimeType: deref(stored.MimeType),
			Bytes:    stored.Bytes,
		}}
	}
	rows := make([]map[string]any, 0, len(outputs))
	for index, output := range outputs {
		rows = append(rows, map[string]any{
			"index":    index,
			"kind":     stored.Object,
			"mime":     output.MimeType,
			"bytes":    output.Bytes,
			"filename": filepath.Base(output.File),
			"url":      "/api/runs/" + stored.ID + "/outputs/" + strconv.Itoa(index),
		})
	}
	return rows
}

func runErrorPayload(stored *task.Task) any {
	if stored.Error == nil || *stored.Error == "" {
		return nil
	}
	payload := map[string]any{"message": *stored.Error}
	if stored.ErrorCode != nil && *stored.ErrorCode != "" {
		payload["code"] = *stored.ErrorCode
	}
	if stored.ErrorDetails != nil {
		payload["details"] = stored.ErrorDetails
	}
	return payload
}

func mediaIDs(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	}
	return nil
}

func appendStrings(existing any, value string) []string {
	current, _ := existing.([]string)
	return append(current, value)
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// filesResult maps every artifact of a run; an image request with count > 1 returns several.
func filesResult(object string, result *generate.RunResult) (*task.Result, error) {
	if result == nil || len(result.Files) == 0 {
		return nil, httpx.NewError(http.StatusBadGateway, "no_output_files", "the workflow produced no output file")
	}
	outputs := make([]task.ResultFile, 0, len(result.Files))
	for _, file := range result.Files {
		outputs = append(outputs, task.ResultFile{
			Buffer:   file.Buffer,
			Ext:      util.ExtFromMime(file.MimeType, file.Filename, defaultObjectExt(object)),
			MimeType: file.MimeType,
		})
	}
	return &task.Result{Outputs: outputs, PromptID: result.PromptID}, nil
}
