// Package server wires every module into the HTTP surface the canvas talks to.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/comfy"
	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/generate"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/prompt"
	"comfyui-gateway/internal/resource"
	"comfyui-gateway/internal/setup"
	"comfyui-gateway/internal/store"
	"comfyui-gateway/internal/sysinfo"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// Server holds every subsystem plus the HTTP handler.
type Server struct {
	cfg        *config.Config
	logger     *logx.Logger
	comfy      *comfy.Client
	hub        *generate.Hub
	runner     *generate.Runner
	stats      *sysinfo.Reader
	sessions   *store.Sessions
	skins      *store.Skins
	visibility *store.Visibility
	mainModels *store.MainModels
	registry   *workflow.Registry
	tasks      *task.Store
	setup      *setup.Manager
	resources  *resource.Manager
	optimizer  *prompt.Optimizer

	requests sync.Map // request id -> context.CancelFunc

	startedAt   time.Time
	lastOOM     sync.Mutex
	lastOOMAt   string
	lastOOMText string
}

// New constructs the whole gateway. Everything is loaded before the first request,
// so a half-initialized server can never answer.
func New(cfg *config.Config, logger *logx.Logger) (*Server, error) {
	comfyClient, err := comfy.New(comfy.Options{
		BaseURL:       cfg.ComfyURL,
		TimeoutMs:     cfg.RequestTimeoutMs,
		RetryAttempts: cfg.ComfyRetryAttempts,
		RetryDelayMs:  cfg.ComfyRetryDelayMs,
		Logger:        logger,
	})
	if err != nil {
		return nil, err
	}

	mainModels := store.NewMainModels(cfg.DataDir, logger)
	mainModels.Load()

	registry := workflow.NewRegistry(workflow.RegistryOptions{
		Dir:              cfg.WorkflowDir,
		MaxWorkflowBytes: cfg.MaxWorkflowBytes,
		Logger:           logger,
		MainModelFileFor: mainModels.FileForWorkflow,
	})
	if err := registry.Load(); err != nil {
		return nil, err
	}

	tasks, err := task.New(task.Options{
		DataDir:       cfg.DataDir,
		PublicBaseURL: cfg.PublicBaseURL,
		Logger:        logger,
		RetentionDays: cfg.TaskRetentionDays,
		MaxTasks:      cfg.MaxStoredTasks,
	})
	if err != nil {
		return nil, err
	}
	tasks.Load()

	skins := store.NewSkins(cfg.DataDir, logger)
	skins.Load()
	visibility := store.NewVisibility(cfg.DataDir, logger)
	visibility.Load()

	setupManager := setup.New(setup.Options{
		Config:        cfg,
		Registry:      registry,
		Comfy:         comfyClient,
		Logger:        logger,
		MainModelFile: mainModels.FileForWorkflow,
	})

	resources := resource.New(resource.Options{
		Config:     cfg,
		Setup:      setupManager,
		Registry:   registry,
		Visibility: visibility,
		MainModels: mainModels,
		Logger:     logger,
	})
	resources.Init()

	hub := generate.NewHub(cfg.ComfyURL, logger)

	server := &Server{
		cfg:        cfg,
		logger:     logger,
		comfy:      comfyClient,
		hub:        hub,
		runner:     generate.NewRunner(comfyClient, hub, logger, cfg.RequestTimeoutMs, cfg.PollIntervalMs),
		stats:      sysinfo.NewReader(),
		sessions:   store.NewSessions(time.Duration(cfg.SessionStaleSec) * time.Second),
		skins:      skins,
		visibility: visibility,
		mainModels: mainModels,
		registry:   registry,
		tasks:      tasks,
		setup:      setupManager,
		resources:  resources,
		optimizer: prompt.NewOptimizer(cfg.RootDir, logger).
			WithThinkingParam(cfg.PromptThinkingParam).
			WithMaxTokens(cfg.PromptMaxTokens),
		startedAt: time.Now(),
	}

	if cfg.ScanOnStart {
		setupManager.Start()
	}
	return server, nil
}

// Setup exposes the setup manager for lifecycle control.
func (s *Server) Setup() *setup.Manager { return s.setup }

// Tasks exposes the task store for lifecycle control.
func (s *Server) Tasks() *task.Store { return s.tasks }

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)

	// A catch-all on the mux itself keeps routing inside net/http, which is what
	// populates r.PathValue for the {id} patterns. Wrapping the mux and calling its
	// handler directly would leave those values empty.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, httpx.NotFound("not_found", "Not found: "+r.Method+" "+r.URL.Path))
	})

	return httpx.Chain(
		mux,
		httpx.Recover(s.logger),
		httpx.WithRequestID(s.logger),
		httpx.CORS(s.cfg),
		httpx.Guard(s.cfg),
	)
}

// route registers a business handler and centralizes error rendering.
func (s *Server) route(mux *http.ServeMux, pattern string, handler httpx.Handler) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		ctx := &httpx.Ctx{W: w, R: r, RequestID: httpx.RequestIDOf(r)}
		if err := handler(ctx); err != nil {
			if s.writeKnownError(w, err) {
				return
			}
			httpx.WriteError(w, err)
		}
	})
}

// writeKnownError maps subsystem errors that are not httpx.Error onto the wire.
func (s *Server) writeKnownError(w http.ResponseWriter, err error) bool {
	var comfyErr *comfy.Error
	if errors.As(err, &comfyErr) {
		status := comfyErr.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		httpx.WriteError(w, httpx.NewError(status, comfyErr.Code, comfyErr.Message).WithDetails(comfyErr.Details))
		return true
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		httpx.WriteError(w, httpx.NewError(http.StatusBadRequest, coded.Code(), err.Error()))
		return true
	}
	return false
}

// okJSON writes {ok:true, ...} which most admin endpoints return.
func okJSON(w http.ResponseWriter, values map[string]any) {
	payload := map[string]any{"ok": true}
	for key, value := range values {
		payload[key] = value
	}
	httpx.OK(w, payload)
}

// bodyOrEmpty returns the parsed request body, tolerating an absent body.
func bodyOrEmpty(ctx *httpx.Ctx) (map[string]any, error) {
	body, err := ctx.Body()
	if err != nil {
		return nil, err
	}
	if body == nil {
		return map[string]any{}, nil
	}
	return body, nil
}

// withCancel derives a cancellable context registered under the request id, so that
// POST /api/requests/:id/cancel can stop an in-flight generation.
func (s *Server) withCancel(r *http.Request, id string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(r.Context())
	if id != "" {
		s.requests.Store(id, cancel)
	}
	return ctx, func() {
		if id != "" {
			s.requests.Delete(id)
		}
		cancel()
	}
}

// cancelRequest aborts a running request by id.
func (s *Server) cancelRequest(id string) bool {
	value, ok := s.requests.Load(id)
	if !ok {
		return false
	}
	if cancel, ok := value.(context.CancelFunc); ok {
		cancel()
		return true
	}
	return false
}

// requireModel resolves a model id, rejecting unknown or wrong-typed models.
func (s *Server) requireModel(model, expectedType string) (*workflow.Entry, error) {
	name := strings.TrimSpace(model)
	if name == "" {
		return nil, httpx.NewError(http.StatusBadRequest, "missing_model", "missing model parameter")
	}
	found := s.registry.ResolveModel(name, s.setup.LoaderOptions())
	if found == nil {
		return nil, httpx.NewError(http.StatusNotFound, "model_not_found",
			"model not found: "+name+". Register a workflow in the gateway first, or refresh the model list from the canvas channel.")
	}
	if expectedType != "" && found.Entry.Type != expectedType {
		return nil, httpx.Errorf(http.StatusBadRequest, "model_type_mismatch",
			"model %s has type %s, not %s", name, found.Entry.Type, expectedType)
	}
	return found.Entry, nil
}

// parseParams accepts params as an object or a JSON string.
func parseParams(body map[string]any) (map[string]any, error) {
	value, ok := body["params"]
	if !ok || value == nil || value == "" {
		return map[string]any{}, nil
	}
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	if text, ok := value.(string); ok {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(text), &decoded); err == nil && decoded != nil {
			return decoded, nil
		}
	}
	return nil, httpx.BadRequest("invalid_params", "params must be a JSON object")
}

// matchesImageField reports whether a form field carries an image or a video frame.
func matchesImageField(field string) bool {
	lowered := strings.ToLower(field)
	return strings.Contains(lowered, "image") || strings.Contains(lowered, "frame")
}

// imageFiles returns the uploads whose field name marks them as images.
func imageFiles(files []httpx.File) []httpx.File {
	var out []httpx.File
	for _, file := range files {
		if matchesImageField(file.Field) {
			out = append(out, file)
		}
	}
	return out
}

// orderedVideoFiles collects reference frames in a stable, de-duplicated order.
func orderedVideoFiles(files []httpx.File) []httpx.File {
	groups := []string{"first_frame", "last_frame", "image", "image[]", "images"}
	var ordered []httpx.File
	seen := map[string]bool{}

	push := func(file httpx.File) {
		if len(file.Bytes) == 0 {
			return
		}
		key := strconv.Itoa(len(file.Bytes)) + ":" + file.Filename
		if seen[key] {
			return
		}
		seen[key] = true
		ordered = append(ordered, file)
	}
	for _, group := range groups {
		for _, file := range files {
			if file.Field == group {
				push(file)
			}
		}
	}
	for _, file := range files {
		if matchesImageField(file.Field) {
			push(file)
		}
	}
	if len(ordered) > 8 {
		ordered = ordered[:8]
	}
	return ordered
}

// orderedPromptImages puts prompt images into a deterministic order.
func orderedPromptImages(files []httpx.File) []httpx.File {
	byName := map[string]httpx.File{}
	for _, file := range files {
		if len(file.Bytes) == 0 {
			continue
		}
		if _, exists := byName[file.Field]; !exists {
			byName[file.Field] = file
		}
	}
	var ordered []httpx.File
	take := func(name string) {
		file, ok := byName[name]
		if !ok {
			return
		}
		delete(byName, name)
		ordered = append(ordered, file)
	}
	take("image")
	take("image1")
	take("images")
	for index := 2; index <= 15; index++ {
		take("image" + strconv.Itoa(index))
	}
	for _, file := range files {
		if len(ordered) >= 15 {
			break
		}
		if len(file.Bytes) == 0 || !matchesImageField(file.Field) {
			continue
		}
		already := false
		for _, chosen := range ordered {
			if chosen.Field == file.Field && len(chosen.Bytes) == len(file.Bytes) {
				already = true
				break
			}
		}
		if !already {
			ordered = append(ordered, file)
		}
	}
	if len(ordered) > 15 {
		ordered = ordered[:15]
	}
	return ordered
}

// orderedVideoRefs returns the single reference video, if any.
func orderedVideoRefs(files []httpx.File) []httpx.File {
	var out []httpx.File
	for _, file := range files {
		field := strings.ToLower(file.Field)
		if len(file.Bytes) == 0 {
			continue
		}
		switch field {
		case "video", "videos", "video[]", "videos[]":
			out = append(out, file)
		}
	}
	if len(out) > 1 {
		out = out[:1]
	}
	return out
}

// orderedAudioRefs returns up to two reference audio clips.
func orderedAudioRefs(files []httpx.File) []httpx.File {
	var out []httpx.File
	for _, file := range files {
		field := strings.ToLower(file.Field)
		if len(file.Bytes) == 0 {
			continue
		}
		switch field {
		case "audio", "audios", "audio[]", "audios[]":
			out = append(out, file)
		}
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

// uploadFiles pushes files into ComfyUI and returns their references.
func (s *Server) uploadFiles(ctx context.Context, files []httpx.File) ([]workflow.Media, error) {
	uploaded := make([]workflow.Media, 0, len(files))
	for index, file := range files {
		name := file.Filename
		if name == "" {
			name = "input-" + strconv.Itoa(index+1) + ".png"
		}
		unique := util.UniqueFileName(name, "")
		result, err := s.comfy.UploadImage(ctx, file.Bytes, unique, defaultMime(file.Mimetype))
		if err != nil {
			return nil, err
		}
		uploaded = append(uploaded, workflow.Media{Name: result.Name, Subfolder: result.Subfolder})
	}
	return uploaded, nil
}

func defaultMime(value string) string {
	if value == "" {
		return "image/png"
	}
	return value
}

// RGBA hint text that transparent output needs.
const (
	rgbaPrefix = "This is an RGBA format image with transparency."
	rgbaSuffix = "The image has an alpha channel and a transparent background."
)

// applyBackgroundPrompt wraps a prompt when the workflow supports transparency.
func applyBackgroundPrompt(text, background string, entry *workflow.Entry) string {
	if background != "transparent" || entry == nil || entry.BackgroundPrompt == "" {
		return text
	}
	trimmed := strings.TrimSpace(text)
	lowered := strings.ToLower(trimmed)
	if strings.Contains(lowered, "alpha channel") || strings.Contains(lowered, "rgba") {
		return trimmed
	}
	return strings.TrimSpace(rgbaPrefix + " " + trimmed + " " + rgbaSuffix)
}

// enrichParameters swaps optionsFrom placeholders for ComfyUI's live option lists.
func (s *Server) enrichParameters(parameters []any) []any {
	loaders := s.setup.LoaderOptions()
	out := make([]any, 0, len(parameters))
	for _, item := range parameters {
		object, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		source := strings.TrimSpace(util.StringField(object, "optionsFrom"))
		if source == "" {
			out = append(out, item)
			continue
		}
		classType := strings.SplitN(source, ".", 2)[0]
		copied := make(map[string]any, len(object)+1)
		for key, value := range object {
			copied[key] = value
		}
		options := make([]any, 0, len(loaders[classType]))
		for _, name := range loaders[classType] {
			options = append(options, name)
		}
		copied["options"] = options
		out = append(out, copied)
	}
	return out
}

// probeVideoFrames counts the frames of an uploaded video, 0 when unknown.
func (s *Server) probeVideoFrames(file httpx.File) int {
	if len(file.Bytes) == 0 {
		return 0
	}
	extension := util.ExtFromMime(file.Mimetype, file.Filename, "mp4")
	temporary := filepath.Join(os.TempDir(), "comfy-probe-"+util.UUIDShort()+"."+extension)
	if err := os.WriteFile(temporary, file.Bytes, 0o644); err != nil {
		return 0
	}
	defer os.Remove(temporary)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	args := []string{
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=avg_frame_rate",
		"-show_entries", "format=duration",
		"-of", "json", temporary,
	}
	s.logger.Info("running external command: %s", util.CommandLine("ffprobe", args...))
	out, err := exec.CommandContext(ctx, "ffprobe", args...).Output()
	if err != nil {
		// Degrading to "unknown length" is legitimate, but it must be visible: the
		// character-swap workflows silently produce the wrong duration without it.
		if errors.Is(err, exec.ErrNotFound) {
			s.logger.Warn("ffprobe is not installed; the source video length is unknown and character-swap workflows fall back to the default duration")
		} else {
			s.logger.Warn("ffprobe failed to read the video metadata: %s", err)
		}
		return 0
	}

	var parsed struct {
		Streams []struct {
			AvgFrameRate string `json:"avg_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0
	}

	fps := 0.0
	if len(parsed.Streams) > 0 {
		parts := strings.Split(parsed.Streams[0].AvgFrameRate, "/")
		switch len(parts) {
		case 2:
			numerator, _ := strconv.ParseFloat(parts[0], 64)
			denominator, _ := strconv.ParseFloat(parts[1], 64)
			if denominator != 0 {
				fps = numerator / denominator
			}
		case 1:
			fps, _ = strconv.ParseFloat(parts[0], 64)
		}
	}
	duration, _ := strconv.ParseFloat(parsed.Format.Duration, 64)
	if fps <= 0 || duration <= 0 {
		return 0
	}
	frames := int(fps*duration + 0.5)
	if frames < 1 {
		return 1
	}
	return frames
}

// recordOOM remembers the last out-of-memory failure for /api/system/stats.
func (s *Server) recordOOM(message string) {
	s.lastOOM.Lock()
	defer s.lastOOM.Unlock()
	s.lastOOMAt = util.NowIso()
	s.lastOOMText = message
}

// serveFile writes a local file from the output directory.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, name string) error {
	safe := util.SafeFileName(name)
	if safe == "" || safe == "file" {
		return httpx.NotFound("file_not_found", "file not found")
	}
	target := filepath.Join(s.cfg.OutputDir, safe)
	if !util.Exists(target) {
		return httpx.NotFound("file_not_found", "file not found")
	}
	http.ServeFile(w, r, target)
	return nil
}
