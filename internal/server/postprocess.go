package server

// Gateway-owned post-processing of a finished artifact.
//
// `interpolation` is the one run input that reaches no binding: it raises the frame rate of the
// video the run produced. Applying it inside the run is what keeps one request one artifact — the
// canvas asks for it on the submission it already makes, instead of chaining a second run over the
// first one's output. `/v1/videos` and `/api/runs` share this implementation, so the same request
// renders the same way whichever route produced it.

import (
	"context"

	"comfyui-gateway/internal/generate"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/workflow"
)

// videoPostProcess is the artifact step a video run needs, or nil when it needs none.
func (s *Server) videoPostProcess(object, requestID, interpolation string) runResultBuilder {
	factor := workflow.InterpolationFactor(interpolation)
	if object != task.ObjectVideo || factor == 0 {
		return nil
	}
	return func(jobCtx context.Context, result *generate.RunResult, progress func(task.Update)) (*task.Result, error) {
		if result == nil || len(result.Files) == 0 {
			return filesResult(object, result)
		}
		// The graph is finished; what is left is the frame-rate pass, which is why the stored
		// progress stops short of 1.
		progress(task.Update{Progress: 0.97})
		interpolator := generate.NewInterpolator(s.cfg.WorkflowDir, s.comfy, s.logger)
		files := make([]generate.OutputFile, 0, len(result.Files))
		for _, file := range result.Files {
			raised, err := interpolator.Interpolate(jobCtx, file.Buffer, file.Filename, factor, requestID, s.cfg.VideoTimeoutMs)
			if err != nil {
				return nil, err
			}
			files = append(files, generate.OutputFile{
				Buffer:   raised.Buffer,
				MimeType: raised.MimeType,
				Filename: raised.Filename,
			})
		}
		return filesResult(object, &generate.RunResult{PromptID: result.PromptID, Files: files})
	}
}
