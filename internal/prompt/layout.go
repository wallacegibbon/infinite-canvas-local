package prompt

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// MOSS layout prompting.
//
// MOSS-Image accepts a structured layout description with absolute, centrally
// aligned yxyx boxes. Converting normalized region boxes into that space here keeps
// the canvas free of model-specific geometry.

var (
	reCaptionPrefix = regexp.MustCompile(`^(物体|人物|文字)\s*[：:]\s*`)
	reTrailingPunct = regexp.MustCompile(`[。；;，,\s]+$`)
)

// kindLabels renders region kinds in Chinese for the summary list.
var kindLabels = map[string]string{"object": "物体", "character": "人物"}

var validLabels = map[string]bool{"object": true, "character": true, "text": true}

// LayoutItem is one region in the MOSS layout payload.
type LayoutItem struct {
	Label   string   `json:"label"`
	Caption string   `json:"caption"`
	Text    string   `json:"text,omitempty"`
	Palette []string `json:"palette,omitempty"`
	BBox    [4]int   `json:"bbox"`
}

type layoutSummary struct {
	Caption string `json:"caption"`
}

type layoutPayload struct {
	Summary        layoutSummary `json:"summary"`
	Items          []LayoutItem  `json:"items"`
	BBoxCoordSpace string        `json:"bbox_coord_space"`
	BBoxFormat     string        `json:"bbox_format"`
}

// BuildMossLayoutPrompt renders a layout request into the JSON string MOSS expects.
func BuildMossLayoutPrompt(layout map[string]any, fallbackPrompt string, fallbackWidth, fallbackHeight int) string {
	regions := regionsOf(layout)
	summaryRaw := stringField(layout, "summary")
	if len(regions) == 0 && summaryRaw == "" {
		return ""
	}

	width := maxInt(16, int(jsRound(numberField(layout, "width", float64(fallbackWidth)))))
	height := maxInt(16, int(jsRound(numberField(layout, "height", float64(fallbackHeight)))))
	if width == 16 && fallbackWidth == 0 {
		width = 1024
	}
	if height == 16 && fallbackHeight == 0 {
		height = 1024
	}

	summary := strings.TrimSpace(summaryRaw)
	if summary == "" {
		summary = strings.TrimSpace(fallbackPrompt)
	}
	merged := mergeDisplayList(summary, buildDisplayList(regions))

	if len(regions) > 32 {
		regions = regions[:32]
	}
	items := make([]LayoutItem, 0, len(regions))
	for _, region := range regions {
		box := normalizeBox(region)
		label := stringField(region, "label")
		if !validLabels[label] {
			label = "object"
		}
		item := LayoutItem{Label: label, Caption: strings.TrimSpace(stringField(region, "caption"))}
		if label == "text" {
			item.Text = stringField(region, "text")
		}
		if palette := stringListField(region, "palette"); len(palette) > 0 {
			if len(palette) > 5 {
				palette = palette[:5]
			}
			item.Palette = palette
		}
		item.BBox = [4]int{
			centeredEdge(box.y, float64(height), height),
			centeredEdge(box.x, float64(width), width),
			int(math.Ceil(box.y*float64(height)+box.h*float64(height))) - height/2,
			int(math.Ceil(box.x*float64(width)+box.w*float64(width))) - width/2,
		}
		items = append(items, item)
	}

	payload := layoutPayload{
		Summary:        layoutSummary{Caption: merged},
		Items:          items,
		BBoxCoordSpace: "abs_centralized",
		BBoxFormat:     "yxyx",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// ApplyMossLayoutPrompt replaces the prompt with a layout payload when one was asked
// for. It reports whether it changed anything.
func ApplyMossLayoutPrompt(ctxPrompt *string, params map[string]any, fallbackWidth, fallbackHeight int) bool {
	if params == nil {
		return false
	}
	layout, ok := params["layout"].(map[string]any)
	if !ok || len(layout) == 0 {
		return false
	}
	enabled, _ := params["layoutEnabled"].(bool)
	if !enabled {
		if flag, ok := layout["enabled"].(bool); !ok || !flag {
			return false
		}
	}
	rendered := BuildMossLayoutPrompt(layout, *ctxPrompt, fallbackWidth, fallbackHeight)
	if rendered == "" {
		return false
	}
	*ctxPrompt = rendered
	return true
}

// regionsOf reads the region list defensively.
func regionsOf(layout map[string]any) []map[string]any {
	raw, ok := layout["regions"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if region, ok := item.(map[string]any); ok {
			out = append(out, region)
		}
	}
	return out
}

// displayCaption strips the redundant kind prefix from a caption.
func displayCaption(caption string) string {
	return strings.TrimSpace(reCaptionPrefix.ReplaceAllString(caption, ""))
}

// buildDisplayList renders the non-text regions as a semicolon-separated list.
func buildDisplayList(regions []map[string]any) string {
	parts := make([]string, 0, len(regions))
	for _, region := range regions {
		label := stringField(region, "label")
		if label == "text" {
			continue
		}
		caption := displayCaption(strings.TrimSpace(stringField(region, "caption")))
		if caption == "" {
			continue
		}
		kind, ok := kindLabels[label]
		if !ok {
			kind = "物体"
		}
		parts = append(parts, kind+"："+caption)
	}
	return strings.Join(parts, "；")
}

// mergeDisplayList appends the item list to the summary when it is missing.
//
// MOSS literalizes CJK item captions unless the whole-image summary also lists them,
// so the two must agree.
func mergeDisplayList(summary, display string) string {
	if display == "" || strings.Contains(summary, display) {
		return summary
	}
	base := reTrailingPunct.ReplaceAllString(summary, "")
	if base != "" {
		return base + "。" + display + "。"
	}
	return display + "。"
}

// normalizedBox is a region box in 0..1 space.
type normalizedBox struct {
	x, y, w, h float64
}

// normalizeBox clamps a region's box into the unit square.
func normalizeBox(region map[string]any) normalizedBox {
	source := region
	if inner, ok := region["bbox"].(map[string]any); ok {
		source = inner
	}
	x := clamp01(numberField(source, "x", 0))
	y := clamp01(numberField(source, "y", 0))
	width := math.Min(1-x, clamp01(numberField(source, "width", 0)))
	height := math.Min(1-y, clamp01(numberField(source, "height", 0)))
	return normalizedBox{x: x, y: y, w: width, h: height}
}

// centeredEdge converts a normalized edge into centralized absolute space.
func centeredEdge(start, size float64, total int) int {
	return int(jsRound(start*size)) - total/2
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) {
		return 0
	}
	return math.Min(1, math.Max(0, value))
}

// jsRound matches JavaScript's Math.round for positive halves.
func jsRound(value float64) float64 {
	return math.Floor(value + 0.5)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func numberField(object map[string]any, key string, fallback float64) float64 {
	switch typed := object[key].(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	}
	return fallback
}

func stringListField(object map[string]any, key string) []string {
	raw, ok := object[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
