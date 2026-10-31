package server

// The gateway's single execution path. Everything that produces media is submitted here,
// queued on the task store, and then read back by its id. The HTTP routes above this file
// only translate their request shape into a runSpec and decide how to answer.

import (
	"context"
	"net/http"

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

// submitRun queues one run and returns its record. The record's id is the caller's handle.
func (s *Server) submitRun(spec runSpec) *task.Task {
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
