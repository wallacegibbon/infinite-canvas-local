package prompt

import (
	"strings"
	"testing"
)

func TestReorderQwenEditPrompt(t *testing.T) {
	// The canvas sends the target image last, so "图片1" (the user's target) is now
	// the Nth upload and must be renumbered accordingly.
	result := ReorderQwenEditPrompt("把图片1里的人换成图片2里的风格", 3)

	if !strings.HasPrefix(result, "图片顺序：") {
		t.Errorf("missing the reordering header:\n%s", result)
	}
	if !strings.Contains(result, "图片3是目标图") {
		t.Errorf("target image should be 图片3:\n%s", result)
	}
	if !strings.Contains(result, "图片1里的风格") {
		t.Errorf("reference image 2 should become 图片1:\n%s", result)
	}
}

func TestReorderQwenEditPromptSingleImageIsNoop(t *testing.T) {
	if got := ReorderQwenEditPrompt("  一张图  ", 1); got != "一张图" {
		t.Errorf("got %q, want the trimmed input unchanged", got)
	}
}

func TestBuildMossLayoutPromptEmpty(t *testing.T) {
	if got := BuildMossLayoutPrompt(map[string]any{}, "", 0, 0); got != "" {
		t.Errorf("expected an empty result for an empty layout, got %q", got)
	}
}

func TestApplyMossLayoutPromptRespectsEnableFlag(t *testing.T) {
	layout := map[string]any{"summary": "s", "regions": []any{}}
	promptText := "original"

	if ApplyMossLayoutPrompt(&promptText, map[string]any{"layout": layout}, 1024, 1024) {
		t.Error("layout must not apply without layoutEnabled")
	}
	if promptText != "original" {
		t.Errorf("prompt was modified: %q", promptText)
	}

	layout["enabled"] = true
	if !ApplyMossLayoutPrompt(&promptText, map[string]any{"layout": layout}, 1024, 1024) {
		t.Error("layout.enabled=true should apply")
	}
}
