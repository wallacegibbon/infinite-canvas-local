package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"comfyui-gateway/internal/comfy"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// OutputFile is one retrieved artifact.
type OutputFile struct {
	Buffer    []byte
	MimeType  string
	Filename  string
	Subfolder string
	Type      string
	NodeID    string
}

// RunResult is the outcome of running one workflow.
type RunResult struct {
	PromptID string
	Files    []OutputFile
	Workflow workflow.Graph
}

// ProgressEvent reports coarse generation milestones.
type ProgressEvent struct {
	Phase    string
	PromptID string
	Progress float64
	Attempt  int
	Attempts int
}

// RunOptions carries everything one generation needs.
type RunOptions struct {
	Entry                 *workflow.Entry
	Context               *workflow.Context
	RequestID             string
	LoraCatalog           []string
	ModelAliasFor         func(classType, input, value string) string
	TimeoutMs             int
	PollIntervalMs        int
	IncludePreviewOutputs bool
	OnProgress            func(ProgressEvent)
}

// Runner executes workflows against ComfyUI.
type Runner struct {
	comfy          *comfy.Client
	hub            *Hub
	logger         *logx.Logger
	timeoutMs      int
	pollIntervalMs int
}

// NewRunner builds a runner. Defaults mirror the configuration defaults.
func NewRunner(client *comfy.Client, hub *Hub, logger *logx.Logger, timeoutMs, pollIntervalMs int) *Runner {
	if timeoutMs <= 0 {
		timeoutMs = 10 * 60 * 1000
	}
	if pollIntervalMs <= 0 {
		pollIntervalMs = 750
	}
	return &Runner{comfy: client, hub: hub, logger: logger, timeoutMs: timeoutMs, pollIntervalMs: pollIntervalMs}
}

// RunWorkflow parameterizes the graph, submits it, and waits for the outputs.
func (r *Runner) RunWorkflow(ctx context.Context, opts RunOptions) (*RunResult, error) {
	if ctx.Err() != nil {
		return nil, cancelled()
	}

	resolved := workflow.ResolveMode(opts.Entry, opts.Context.Mode)

	// Base-model parameters are baked into the entry when a variant was resolved;
	// request-level params win over them.
	params := opts.Context.Params
	if len(resolved.BaseModelParams) > 0 {
		merged := make(map[string]any, len(resolved.BaseModelParams)+len(opts.Context.Params))
		for key, value := range resolved.BaseModelParams {
			merged[key] = value
		}
		for key, value := range opts.Context.Params {
			merged[key] = value
		}
		params = merged
	}

	// bindings read params (frameMode, resolution, editCanvasSize), so the merged
	// view must be visible to them too.
	contextForBindings := *opts.Context
	contextForBindings.Params = params

	aliased := workflow.ApplyModelAliases(resolved.Workflow, opts.ModelAliasFor)
	bound, err := workflow.ApplyBindings(aliased, resolved, &contextForBindings)
	if err != nil {
		return nil, err
	}
	bound = workflow.ApplyMainModelFile(bound, resolved.MainModelFile)

	loraOptions := LoraOptions{Available: opts.LoraCatalog}
	if resolved.Lora != nil {
		loraOptions.Filter = resolved.Lora.Filter
		loraOptions.SourceNode = resolved.Lora.Source
		if resolved.Lora.Loader != "" {
			loraOptions.LoaderClass = resolved.Lora.Loader
		}
	}
	withLoras, applied, err := ApplyLoraChain(bound, params, loraOptions)
	if err != nil {
		return nil, err
	}
	if len(applied) > 0 {
		names := make([]string, 0, len(applied))
		for _, lora := range applied {
			names = append(names, fmt.Sprintf("%s@%g", path.Base(util.NormalizeSlashes(lora.Name)), lora.Strength))
		}
		r.logger.Info("LoRA %s: %s", resolved.Name, strings.Join(names, " -> "))
	}
	finalGraph := workflow.ApplyModelAliases(withLoras, opts.ModelAliasFor)

	clientID := util.RandomID("canvas-")
	// Previews are optional; a failed socket must not fail the request.
	r.hub.Ensure(ctx, clientID)

	submitted, err := r.comfy.Submit(ctx, finalGraph, clientID)
	if err != nil {
		return nil, err
	}
	promptID := submitted.PromptID
	r.hub.BindPrompt(clientID, promptID, opts.RequestID, EstimateStepPlan(finalGraph))

	r.emit(opts.OnProgress, ProgressEvent{Phase: "submitted", PromptID: promptID, Progress: 0.05})

	timeoutMs := opts.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = r.timeoutMs
	}
	pollInterval := opts.PollIntervalMs
	if pollInterval <= 0 {
		pollInterval = r.pollIntervalMs
	}

	history, err := r.waitForHistory(ctx, promptID, timeoutMs, pollInterval)
	if err != nil {
		return nil, err
	}
	r.emit(opts.OnProgress, ProgressEvent{Phase: "completed", PromptID: promptID, Progress: 0.65})

	collected := collectOutputFiles(history, opts.IncludePreviewOutputs)
	if len(collected) == 0 {
		return nil, &httpx.Error{
			Status:  http.StatusBadGateway,
			Code:    "no_output_files",
			Message: "ComfyUI produced no output files",
		}
	}

	files := make([]OutputFile, 0, len(collected))
	for _, item := range collected {
		viewed, err := r.comfy.View(ctx, item.Filename, item.Subfolder, item.Type)
		if err != nil {
			return nil, err
		}
		mimeType := viewed.ContentType
		if strings.EqualFold(mimeType, "application/octet-stream") {
			mimeType = util.MimeFromFilename(item.Filename)
		}
		files = append(files, OutputFile{
			Buffer:    viewed.Body,
			MimeType:  mimeType,
			Filename:  path.Base(item.Filename),
			Subfolder: item.Subfolder,
			Type:      item.Type,
			NodeID:    item.NodeID,
		})
	}
	r.emit(opts.OnProgress, ProgressEvent{Phase: "downloaded", PromptID: promptID, Progress: 0.95})
	return &RunResult{PromptID: promptID, Files: files, Workflow: finalGraph}, nil
}

// waitForHistory polls /history until the job finishes, fails, or times out.
func (r *Runner) waitForHistory(ctx context.Context, promptID string, timeoutMs, pollIntervalMs int) (map[string]any, error) {
	var deadline time.Time
	if timeoutMs > 0 {
		deadline = time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	}
	poll := time.Duration(pollIntervalMs) * time.Millisecond

	for {
		if ctx.Err() != nil {
			return nil, cancelled()
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			r.cancelPrompt(promptID)
			return nil, &httpx.Error{
				Status:  http.StatusGatewayTimeout,
				Code:    "generation_timeout",
				Message: "generation timed out",
			}
		}

		history, err := r.comfy.History(ctx, promptID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, cancelled()
			}
			return nil, err
		}
		if failure := historyError(history); failure != "" {
			return nil, executionFailure(failure)
		}
		if historyCompleted(history) {
			return history, nil
		}

		select {
		case <-time.After(poll):
		case <-ctx.Done():
			return nil, cancelled()
		}
	}
}

// cancelPrompt asks ComfyUI to stop a job, using a detached context so that
// cancellation still reaches ComfyUI after the caller's context is already dead.
func (r *Runner) cancelPrompt(promptID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r.comfy.Cancel(ctx, promptID)
}

// RunImage runs a workflow until `count` images have been produced.
//
// Some graphs honor batch_size and return everything at once; others return one
// image per run. Looping until the count is satisfied covers both without the
// canvas silently losing images.
func (r *Runner) RunImage(ctx context.Context, opts RunOptions) (*RunResult, error) {
	count := opts.Context.Count
	if count < 1 {
		count = 1
	}
	var all []OutputFile
	var lastPromptID string
	for attempt := 0; attempt < count && len(all) < count; attempt++ {
		attemptIndex := attempt
		attemptOptions := opts
		previous := opts.OnProgress
		attemptOptions.OnProgress = func(event ProgressEvent) {
			event.Attempt = attemptIndex
			event.Attempts = count
			if previous != nil {
				previous(event)
			}
		}
		result, err := r.RunWorkflow(ctx, attemptOptions)
		if err != nil {
			return nil, err
		}
		lastPromptID = result.PromptID
		all = append(all, result.Files...)
	}
	if len(all) > count {
		all = all[:count]
	}
	return &RunResult{PromptID: lastPromptID, Files: all}, nil
}

// RunVideo runs a workflow and returns only the video artifact.
func (r *Runner) RunVideo(ctx context.Context, opts RunOptions) (*RunResult, error) {
	result, err := r.RunWorkflow(ctx, opts)
	if err != nil {
		return nil, err
	}
	for _, file := range result.Files {
		if strings.HasPrefix(strings.ToLower(file.MimeType), "video/") ||
			isVideoExtension(file.Filename) {
			return &RunResult{PromptID: result.PromptID, Files: []OutputFile{file}}, nil
		}
	}
	names := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		names = append(names, file.Filename)
	}
	encoded, _ := json.Marshal(names)
	return nil, &httpx.Error{
		Status:  http.StatusBadGateway,
		Code:    "no_video_output",
		Message: "the video workflow produced no video file (only images or animations)",
		Details: string(encoded),
	}
}

// RunAudio runs a workflow and returns only the audio artifact.
func (r *Runner) RunAudio(ctx context.Context, opts RunOptions) (*RunResult, error) {
	result, err := r.RunWorkflow(ctx, opts)
	if err != nil {
		return nil, err
	}
	for _, file := range result.Files {
		if strings.HasPrefix(strings.ToLower(file.MimeType), "audio/") ||
			isAudioExtension(file.Filename) {
			return &RunResult{PromptID: result.PromptID, Files: []OutputFile{file}}, nil
		}
	}
	names := make([]string, 0, len(result.Files))
	for _, file := range result.Files {
		names = append(names, file.Filename)
	}
	encoded, _ := json.Marshal(names)
	return nil, &httpx.Error{
		Status:  http.StatusBadGateway,
		Code:    "no_audio_output",
		Message: "the audio workflow produced no audio file (only images or videos)",
		Details: string(encoded),
	}
}

func isVideoExtension(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".mp4", ".webm", ".mov":
		return true
	}
	return false
}

func isAudioExtension(filename string) bool {
	switch strings.ToLower(path.Ext(filename)) {
	case ".mp3", ".flac", ".wav", ".ogg", ".opus", ".m4a", ".aac":
		return true
	}
	return false
}

// collectedFile is one output entry from a ComfyUI history record.
type collectedFile struct {
	Filename  string
	Subfolder string
	Type      string
	NodeID    string
}

// historyCompleted reports whether an execution finished successfully.
func historyCompleted(history map[string]any) bool {
	if history == nil {
		return false
	}
	status, _ := history["status"].(map[string]any)
	if completed, ok := status["completed"].(bool); ok && completed {
		return true
	}
	if state, _ := status["status_str"].(string); state == "success" {
		return true
	}
	outputs, _ := history["outputs"].(map[string]any)
	return len(outputs) > 0
}

// historyError extracts a failure message, if any.
func historyError(history map[string]any) string {
	if history == nil {
		return ""
	}
	status, _ := history["status"].(map[string]any)
	if state, _ := status["status_str"].(string); state != "error" {
		return ""
	}
	var parts []string
	if messages, ok := status["messages"].([]any); ok {
		for _, item := range messages {
			switch typed := item.(type) {
			case string:
				if typed != "" {
					parts = append(parts, typed)
				}
			case []any:
				for _, nested := range typed {
					if text := messageText(nested); text != "" {
						parts = append(parts, text)
					}
				}
			default:
				if text := messageText(typed); text != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	joined := strings.Join(parts, "；")
	if joined == "" {
		return "ComfyUI execution failed"
	}
	return joined
}

func messageText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["message"].(string); ok {
			return text
		}
		encoded, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(encoded)
	}
	return ""
}

// executionFailure wraps a ComfyUI execution error, adding the SageAttention hint
// when that is the actual cause. The raw ComfyUI text is unhelpful on its own.
func executionFailure(failure string) error {
	hint := ""
	if strings.Contains(failure, "sageattention is not new enough") ||
		strings.Contains(failure, "cannot apply MiniMax H3 Memory Efficient Sage Attention Patch") {
		hint = ". H3 needs SageAttention: run `pip install sageattention` in the ComfyUI Python environment (see docs/runtime-deps.md), restart ComfyUI, then retry."
	}
	return &httpx.Error{
		Status:  http.StatusBadGateway,
		Code:    "comfy_execution_failed",
		Message: "ComfyUI execution failed：" + util.Truncate(failure, 1200) + hint,
		Details: failure,
	}
}

// collectOutputFiles flattens a history record's outputs, de-duplicated.
func collectOutputFiles(history map[string]any, includePreview bool) []collectedFile {
	outputs, _ := history["outputs"].(map[string]any)
	var files []collectedFile
	seen := map[string]bool{}
	for nodeID, raw := range outputs {
		output, _ := raw.(map[string]any)
		for _, key := range []string{"images", "gifs", "videos", "audio"} {
			items, _ := output[key].([]any)
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					continue
				}
				filename, _ := entry["filename"].(string)
				if filename == "" {
					continue
				}
				kind, _ := entry["type"].(string)
				if kind != "" && kind != "output" && !(includePreview && kind == "temp") {
					continue
				}
				subfolder, _ := entry["subfolder"].(string)
				dedupe := fmt.Sprintf("%s:%s:%s", defaultKind(kind), subfolder, filename)
				if seen[dedupe] {
					continue
				}
				seen[dedupe] = true
				files = append(files, collectedFile{
					Filename:  filename,
					Subfolder: subfolder,
					Type:      defaultKind(kind),
					NodeID:    nodeID,
				})
			}
		}
	}
	return files
}

func defaultKind(kind string) string {
	if kind == "" {
		return "output"
	}
	return kind
}

func (r *Runner) emit(callback func(ProgressEvent), event ProgressEvent) {
	if callback != nil {
		callback(event)
	}
}

func cancelled() error {
	return &httpx.Error{Status: http.StatusBadRequest, Code: "cancelled", Message: "generation cancelled"}
}
