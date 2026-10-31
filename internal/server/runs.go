package server

// The gateway's single execution path. Everything that produces media is submitted here,
// queued on the task store, and then read back by its id. The HTTP routes above this file
// only translate their request shape into a runSpec and decide how to answer.

import (
	"context"
	"net/http"
	"time"

	"comfyui-gateway/internal/generate"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/workflow"
)

// runSpec is one validated generation, ready to be queued.
//
// Everything is already resolved here: the entry, the workflow context built from the
// request, and the timeout. Keeping the queue in one place is what makes a run outlive the
// HTTP request that started it — the reason audio no longer blocks a connection for minutes.
type runSpec struct {
	Object    string
	Entry     *workflow.Entry
	Context   *workflow.Context
	TimeoutMs int
	RequestID string
	Request   map[string]any
}

// runResultBuilder turns the runner's output into the run's stored artifact. Most runs take
// the default (one file); video interpolation uses it to post-process before storage.
type runResultBuilder func(ctx context.Context, result *generate.RunResult, progress func(task.Update)) (*task.Result, error)

// submitRun queues one run and returns its record. The record's id is the caller's handle.
func (s *Server) submitRun(spec runSpec, build runResultBuilder) *task.Task {
	stored := s.tasks.Create(spec.Object, spec.Entry.Name, spec.Request)
	s.tasks.Enqueue(stored, func(_ *task.Task, progress func(task.Update), jobCtx context.Context) (*task.Result, error) {
		opts := generate.RunOptions{
			Entry:          spec.Entry,
			Context:        spec.Context,
			RequestID:      spec.RequestID,
			LoraCatalog:    s.setup.LoraNames(),
			ModelAliasFor:  s.setup.ModelAliasFor,
			TimeoutMs:      spec.TimeoutMs,
			PollIntervalMs: s.cfg.PollIntervalMs,
			OnProgress: func(event generate.ProgressEvent) {
				progress(task.Update{Progress: event.Progress, PromptID: event.PromptID})
			},
		}

		var (
			result *generate.RunResult
			err    error
		)
		switch spec.Object {
		case task.ObjectVideo:
			result, err = s.runner.RunVideo(jobCtx, opts)
		case task.ObjectAudio:
			result, err = s.runner.RunAudio(jobCtx, opts)
		case task.ObjectImage:
			result, err = s.runner.RunImage(jobCtx, opts)
		default:
			return nil, httpx.Errorf(http.StatusBadRequest, "unsupported_run_object",
				"runs producing %q are not implemented yet", spec.Object)
		}
		if err != nil {
			return nil, err
		}
		if build != nil {
			return build(jobCtx, result, progress)
		}
		return filesResult(spec.Object, result)
	})
	return stored
}

// defaultObjectExt is only a fallback: the real extension comes from the file itself.
func defaultObjectExt(object string) string {
	switch object {
	case task.ObjectAudio:
		return "mp3"
	case task.ObjectImage:
		return "png"
	case task.ObjectText:
		return "txt"
	default:
		return "mp4"
	}
}

// waitRun blocks until the run reaches a terminal status.
//
// The run is untouched by the wait: if the caller disconnects, the queue keeps working and the
// record stays readable through GET /api/tasks/{id}. That is the whole point of the split — the
// adapter may block, the run may not depend on it.
func (s *Server) waitRun(ctx context.Context, id string) (*task.Task, error) {
	interval := time.Duration(s.cfg.PollIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 750 * time.Millisecond
	}
	for {
		stored := s.tasks.Get(id)
		if stored == nil {
			return nil, httpx.NewError(http.StatusNotFound, "run_not_found", "run not found")
		}
		switch stored.Status {
		case "completed", "failed", "cancelled":
			return stored, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// runFailure rebuilds the structured error of a failed run, so an adapter can answer with the
// same code and details ComfyUI reported instead of a flattened message.
func runFailure(stored *task.Task) error {
	if stored == nil {
		return httpx.NewError(http.StatusBadGateway, "run_failed", "the run failed")
	}
	message := "the run failed"
	if stored.Error != nil && *stored.Error != "" {
		message = *stored.Error
	}
	code := "run_failed"
	if stored.ErrorCode != nil && *stored.ErrorCode != "" {
		code = *stored.ErrorCode
	}
	status := stored.ErrorStatus
	if status == 0 {
		status = http.StatusBadGateway
	}
	return &httpx.Error{Status: status, Code: code, Message: message, Details: stored.ErrorDetails}
}
