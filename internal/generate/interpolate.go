package generate

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"comfyui-gateway/internal/comfy"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/workflow"
)

// Interpolator runs the bundled RIFE graph to raise a video's frame rate.
//
// RIFE is not a model the canvas exposes; it is a post-step applied to an already
// generated video, so it lives here rather than in the workflow registry.
type Interpolator struct {
	templatePath string
	client       *comfy.Client
	logger       *logx.Logger
}

// NewInterpolator builds an interpolator that reads workflows/_internal/video-rife.json.
func NewInterpolator(workflowDir string, client *comfy.Client, logger *logx.Logger) *Interpolator {
	return &Interpolator{
		templatePath: filepath.Join(workflowDir, "_internal", "video-rife.json"),
		client:       client,
		logger:       logger,
	}
}

// InterpolateResult is a finished interpolation.
type InterpolateResult struct {
	Buffer   []byte
	Filename string
	MimeType string
}

// Interpolate raises the frame rate of a video by 2x or 3x.
func (i *Interpolator) Interpolate(ctx context.Context, buffer []byte, filename string, multiplier int, requestID string, timeoutMs int) (*InterpolateResult, error) {
	if multiplier != 2 && multiplier != 3 {
		return nil, httpx.NewError(http.StatusBadRequest, "invalid_interpolation", "interpolation multiplier must be 2x or 3x")
	}
	if len(buffer) == 0 {
		return nil, httpx.NewError(http.StatusBadRequest, "missing_input_video", "interpolation requires an input video")
	}
	if timeoutMs <= 0 {
		timeoutMs = 10 * 60 * 1000
	}
	safeRequest := sanitizeToken(requestID)
	if safeRequest == "" {
		safeRequest = strconv.FormatInt(time.Now().UnixMilli(), 10)
	}

	uploaded, err := i.client.UploadImage(ctx, buffer, "rife-"+safeRequest+".mp4", "video/mp4")
	if err != nil {
		return nil, err
	}
	reference := uploaded.Name
	if uploaded.Subfolder != "" {
		reference = uploaded.Subfolder + "/" + uploaded.Name
	}

	template, err := workflow.ReadGraphFile(i.templatePath, 5*1024*1024)
	if err != nil {
		return nil, httpx.NewError(http.StatusInternalServerError, "rife_template_missing",
			"the RIFE workflow is missing: workflows/_internal/video-rife.json")
	}
	graph := template.Clone()

	if node, ok := graph.NodeAt("1"); ok {
		node.Inputs()["video"] = reference
	}
	if node, ok := graph.NodeAt("2"); ok {
		node.Inputs()["multiplier"] = multiplier
	}
	if node, ok := graph.NodeAt("3"); ok {
		node.Inputs()["frame_rate"] = 24 * multiplier
		node.Inputs()["filename_prefix"] = "rife/" + safeRequest
	}

	clientID := "rife-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	submitted, err := i.client.Submit(ctx, graph, clientID)
	if err != nil {
		return nil, err
	}
	promptID := submitted.PromptID

	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	started := time.Now()
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			i.cancel(promptID)
			return nil, httpx.BadRequest("cancelled", "interpolation cancelled")
		}

		history, err := i.client.History(ctx, promptID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, httpx.BadRequest("cancelled", "interpolation cancelled")
			}
			return nil, err
		}
		if history != nil {
			if failure := historyError(history); failure != "" {
				return nil, httpx.NewError(http.StatusBadGateway, "interpolation_failed", "interpolation failed: "+failure)
			}
			for _, file := range collectOutputFiles(history, false) {
				viewed, err := i.client.View(ctx, file.Filename, file.Subfolder, file.Type)
				if err != nil {
					return nil, err
				}
				mimeType := viewed.ContentType
				if mimeType == "" {
					mimeType = "video/mp4"
				}
				i.logger.Info("video interpolation %dx %s %dms", multiplier, promptID, time.Since(started).Milliseconds())
				return &InterpolateResult{Buffer: viewed.Body, Filename: file.Filename, MimeType: mimeType}, nil
			}
			if historyCompleted(history) {
				return nil, httpx.NewError(http.StatusBadGateway, "no_video_output", "the interpolation workflow produced no video")
			}
		}

		select {
		case <-time.After(1500 * time.Millisecond):
		case <-ctx.Done():
			i.cancel(promptID)
			return nil, httpx.BadRequest("cancelled", "interpolation cancelled")
		}
	}

	i.cancel(promptID)
	return nil, httpx.NewError(http.StatusGatewayTimeout, "interpolation_timeout", "interpolation timed out")
}

func (i *Interpolator) cancel(promptID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	i.client.Cancel(ctx, promptID)
}

// sanitizeToken keeps a request id safe for use in a filename and in ComfyUI's
// output-path prefix.
//
// Dots are excluded on purpose: the token is interpolated into "rife/<token>", and a
// token of ".." would climb out of that directory on ComfyUI's side.
func sanitizeToken(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-', char == '_':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}
