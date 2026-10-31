package server

import (
	"context"
	"time"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/prompt"
	"comfyui-gateway/internal/workflow"
)

// optionalString reads a body field as a pointer, so "absent" and "empty" stay different.
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

// applyLayoutPrompt replaces the prompt with a MOSS layout payload when asked.
func applyLayoutPrompt(runCtx *workflow.Context) {
	width, height := 0, 0
	if runCtx.Size != nil {
		width, height = runCtx.Size.Width, runCtx.Size.Height
	}
	prompt.ApplyMossLayoutPrompt(&runCtx.Prompt, runCtx.Params, width, height)
}

// contextWithTimeout is a tiny helper so cancel paths cannot block forever.
func contextWithTimeout(milliseconds int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Duration(milliseconds)*time.Millisecond)
}
