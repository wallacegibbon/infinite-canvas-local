package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureSpeakerTagsAddsIDs(t *testing.T) {
	prompt := `integrated_multimodal_description:
一个男人推开门，走进房间。<d>[Chinese] 你回来了。</d>
overall_soundscape: 风声`

	result := EnsureSpeakerTags(prompt)

	if !strings.Contains(result.Prompt, "男人 (S1)") {
		t.Errorf("expected a speaker id after the subject, got:\n%s", result.Prompt)
	}
	if result.Fixed == 0 {
		t.Error("expected Fixed > 0")
	}
	// Untouched sections must survive verbatim.
	if !strings.Contains(result.Prompt, "overall_soundscape: 风声") {
		t.Errorf("soundscape was modified:\n%s", result.Prompt)
	}
}

func TestEnsureSpeakerTagsLeavesExistingIDsAlone(t *testing.T) {
	prompt := `integrated_multimodal_description:
一个女人 (S1) 站在窗边。<d>[Chinese] 天亮了。</d>`
	result := EnsureSpeakerTags(prompt)
	if result.Fixed != 0 {
		t.Errorf("Fixed = %d, want 0 for an already-labelled line", result.Fixed)
	}
	if strings.Count(result.Prompt, "(S1)") != 1 {
		t.Errorf("speaker id duplicated:\n%s", result.Prompt)
	}
}

func TestEnsureSpeakerTagsReportsUnattributableDialog(t *testing.T) {
	prompt := `integrated_multimodal_description:
<d>[Chinese] 有人在吗？</d>`
	result := EnsureSpeakerTags(prompt)
	if len(result.Issues) == 0 {
		t.Fatal("expected a missing-speaker issue")
	}
	if result.Issues[0].Reason != "missing-speaker" {
		t.Errorf("reason = %q", result.Issues[0].Reason)
	}
}

func TestEnsureSpeakerTagsMarksOffscreen(t *testing.T) {
	prompt := `integrated_multimodal_description:
画面外传来旁白 (S1) 的声音。<d>[Chinese] 后来呢？</d>`
	result := EnsureSpeakerTags(prompt)
	if !strings.Contains(result.Prompt, "off-screen") {
		t.Errorf("expected an off-screen marker, got:\n%s", result.Prompt)
	}
}

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

func TestBuildMossLayoutPrompt(t *testing.T) {
	layout := map[string]any{
		"width":   1024.0,
		"height":  1024.0,
		"summary": "一个安静的室内场景。",
		"regions": []any{
			map[string]any{
				"label":   "character",
				"caption": "人物：穿大衣的女人",
				"bbox":    map[string]any{"x": 0.1, "y": 0.2, "width": 0.3, "height": 0.4},
			},
			map[string]any{
				"label":   "text",
				"caption": "文字",
				"text":    "HELLO",
				"bbox":    map[string]any{"x": 0.5, "y": 0.5, "width": 0.2, "height": 0.1},
			},
		},
	}

	rendered := BuildMossLayoutPrompt(layout, "fallback", 512, 512)
	if rendered == "" {
		t.Fatal("expected a rendered payload")
	}

	var payload struct {
		Summary struct {
			Caption string `json:"caption"`
		} `json:"summary"`
		Items []struct {
			Label   string `json:"label"`
			Caption string `json:"caption"`
			Text    string `json:"text"`
			BBox    [4]int `json:"bbox"`
		} `json:"items"`
		BBoxCoordSpace string `json:"bbox_coord_space"`
		BBoxFormat     string `json:"bbox_format"`
	}
	if err := json.Unmarshal([]byte(rendered), &payload); err != nil {
		t.Fatalf("rendered payload is not valid JSON: %v\n%s", err, rendered)
	}

	if payload.BBoxCoordSpace != "abs_centralized" || payload.BBoxFormat != "yxyx" {
		t.Errorf("bbox metadata = %q/%q", payload.BBoxCoordSpace, payload.BBoxFormat)
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(payload.Items))
	}
	// Items keep the authored caption verbatim; only the summary's display list
	// strips the redundant kind prefix.
	if payload.Items[0].Caption != "人物：穿大衣的女人" {
		t.Errorf("caption = %q, want it preserved verbatim", payload.Items[0].Caption)
	}
	if payload.Items[1].Text != "HELLO" {
		t.Errorf("text item lost its content: %+v", payload.Items[1])
	}
	// The summary must list the captions, or MOSS renders them literally.
	if !strings.Contains(payload.Summary.Caption, "穿大衣的女人") {
		t.Errorf("summary = %q, expected the item list to be merged in", payload.Summary.Caption)
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

func TestSanitizeH3StripsReasoning(t *testing.T) {
	raw := "```\n思考过程：用户想要一个视频。\nsubject_definitions:\n- 主体\n" +
		"integrated_multimodal_description:\n画面描述\noverall_soundscape:\n风声\n```"

	got := sanitizeH3(raw)
	if strings.Contains(got, "思考过程") {
		t.Errorf("reasoning leaked:\n%s", got)
	}
	if !strings.HasPrefix(got, "subject_definitions:") {
		t.Errorf("expected to start at the first real field, got:\n%s", got)
	}
	if strings.Contains(got, "```") {
		t.Errorf("code fence leaked:\n%s", got)
	}
}

func TestEnsureFrameAlignmentPrompt(t *testing.T) {
	got := ensureFrameAlignmentPrompt("画面正文", "首帧图生视频(I2VA)", 5)
	if !strings.Contains(got, "at 0.00 seconds into the target video") {
		t.Errorf("missing I2VA alignment sentence:\n%s", got)
	}

	both := ensureFrameAlignmentPrompt("画面正文", "首尾帧视频(FL2VA)", 5)
	if !strings.Contains(both, "Picture 2") {
		t.Errorf("missing FL2VA framing sentence:\n%s", both)
	}

	// A text-to-video prompt must not gain an alignment sentence.
	if got := ensureFrameAlignmentPrompt("画面正文", "文生视频(T2VA)", 5); got != "画面正文" {
		t.Errorf("T2VA prompt was modified: %q", got)
	}
}

func TestChatEndpointNormalization(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:8099":                       "http://127.0.0.1:8099/v1/chat/completions",
		"http://127.0.0.1:8099/":                      "http://127.0.0.1:8099/v1/chat/completions",
		"http://127.0.0.1:8099/v1":                    "http://127.0.0.1:8099/v1/chat/completions",
		"https://api.example.com/v1/chat/completions": "https://api.example.com/v1/chat/completions",
	}
	for input, want := range cases {
		got, err := chatEndpoint(input)
		if err != nil {
			t.Fatalf("chatEndpoint(%q): %v", input, err)
		}
		if got != want {
			t.Errorf("chatEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := chatEndpoint("ftp://x"); err == nil {
		t.Error("expected an error for a non-http scheme")
	}
}
