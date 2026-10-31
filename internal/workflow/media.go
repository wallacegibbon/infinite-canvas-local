package workflow

// Media acceptance, declared in registry.json instead of written into the handlers.
//
// A workflow's graph cannot describe what a request may carry: how many reference images the
// first/last-frame wiring needs, that a text-to-video graph accepts no media at all, that an
// inpaint variant wants exactly a source and a mask. Those rules used to live in
// `routes_video.go` / `routes_generation.go` as `entry.ID == "video-h3-…"` branches, which meant
// adding a workflow required editing Go. They are data now, validated in one place.

import (
	"net/http"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/util"
)

// MediaLimit is how much of one media kind a workflow accepts.
//
// A declared kind has a maximum: `"videos": {"max": 0}` is how "this workflow takes no video"
// is spelled. An undeclared kind is not constrained, which is what keeps a workflow that only
// cares about images from having to describe video and audio too.
type MediaLimit struct {
	Min   int      `json:"min,omitempty"`
	Max   int      `json:"max,omitempty"`
	Roles []string `json:"roles,omitempty"` // reserved for the upload protocol
}

// Accepts is a workflow's media contract.
type Accepts struct {
	Images   *MediaLimit `json:"images,omitempty"`
	Videos   *MediaLimit `json:"videos,omitempty"`
	Audios   *MediaLimit `json:"audios,omitempty"`
	MinTotal int         `json:"minTotal,omitempty"`
	Note     string      `json:"note,omitempty"` // appended to errors; workflow-specific hint
}

// FrameModeRule says the workflow consumes first/last frames and how many images each mode needs.
type FrameModeRule struct {
	Param   string         `json:"param,omitempty"`   // request param, default "frameMode"
	Default string         `json:"default,omitempty"` // "first" (default) or "byCount"
	Images  map[string]int `json:"images,omitempty"`  // frameMode -> minimum images
}

// MediaCounts is what one request carries, one count per kind.
type MediaCounts struct {
	Images int
	Videos int
	Audios int
}

// Total is the number of reference media items, which is what MinTotal compares.
func (c MediaCounts) Total() int { return c.Images + c.Videos + c.Audios }

// MediaPlan is the resolved contract of one request: the execution mode to run, the frame mode
// to write into params, and the minimums that were enforced.
type MediaPlan struct {
	Mode      string
	FrameMode string
	Images    int
	Videos    int
	Audios    int
}

// PlanMedia resolves a request against the workflow's declared media contract.
//
// It is the only place that decides whether a request's media mix is acceptable, so the answer is
// the same for every route. Errors carry stable codes and the workflow's own note, so the reader
// learns what to change without reading the graph.
func PlanMedia(entry *Entry, counts MediaCounts, requestedMode string, params map[string]any) (*MediaPlan, error) {
	if entry == nil {
		return nil, httpx.NewError(http.StatusNotFound, "model_not_found", "unknown workflow")
	}

	plan := &MediaPlan{Mode: defaultString(entry.Mode, requestedMode)}
	if entry.Accepts != nil {
		if err := checkMaximums(entry, counts); err != nil {
			return nil, err
		}
	}

	// Frame modes are resolved before minima: the mode decides how many images are required.
	if entry.FrameMode != nil {
		param := defaultString(entry.FrameMode.Param, "frameMode")
		frameMode := util.StringField(params, param)
		if frameMode == "" {
			if entry.FrameMode.Default == "byCount" && counts.Images >= 2 {
				frameMode = "both"
			} else {
				frameMode = "first"
			}
		}
		switch frameMode {
		case "first", "last", "both":
		default:
			return nil, httpx.NewError(http.StatusBadRequest, "invalid_frame_mode",
				"frameMode must be first, last or both")
		}
		plan.FrameMode = frameMode
		plan.Images = 1
		if declared, ok := entry.FrameMode.Images[frameMode]; ok {
			plan.Images = declared
		}
	}

	accepts := entry.Accepts
	if accepts == nil {
		// A workflow that has not declared its contract keeps the old total-media floor.
		if counts.Total() < entry.MinMedia {
			return nil, missingMedia(entry, entry.MinMedia, counts.Total(), "")
		}
		return plan, nil
	}

	if accepts.Images != nil && accepts.Images.Min > plan.Images {
		plan.Images = accepts.Images.Min
	}
	if accepts.Videos != nil {
		plan.Videos = accepts.Videos.Min
	}
	if accepts.Audios != nil {
		plan.Audios = accepts.Audios.Min
	}

	minTotal := accepts.MinTotal
	if minTotal == 0 && accepts.Images == nil && accepts.Videos == nil && accepts.Audios == nil {
		// `minTotal` alone is a valid contract ("any one media item").
		minTotal = entry.MinMedia
	}
	if minTotal > 0 && counts.Total() < minTotal {
		return nil, missingMedia(entry, minTotal, counts.Total(), accepts.Note)
	}

	if counts.Images < plan.Images {
		return nil, httpx.Errorf(http.StatusBadRequest, "missing_reference_image",
			"workflow %s needs at least %d reference image(s), got %d%s",
			entry.Name, plan.Images, counts.Images, hint(accepts.Note))
	}
	if counts.Videos < plan.Videos {
		return nil, httpx.Errorf(http.StatusBadRequest, "missing_reference_media",
			"workflow %s needs at least %d reference video(s), got %d%s",
			entry.Name, plan.Videos, counts.Videos, hint(accepts.Note))
	}
	if counts.Audios < plan.Audios {
		return nil, httpx.Errorf(http.StatusBadRequest, "missing_reference_media",
			"workflow %s needs at least %d reference audio clip(s), got %d%s",
			entry.Name, plan.Audios, counts.Audios, hint(accepts.Note))
	}
	return plan, nil
}

func checkMaximums(entry *Entry, counts MediaCounts) error {
	limits := []struct {
		limit  *MediaLimit
		kind   string
		count  int
		plural string
	}{
		{entry.Accepts.Images, "image", counts.Images, "images"},
		{entry.Accepts.Videos, "video", counts.Videos, "videos"},
		{entry.Accepts.Audios, "audio", counts.Audios, "audio clip(s)"},
	}
	for _, item := range limits {
		// A declared kind is authoritative: max 0 means "takes none".
		if item.limit == nil || item.count <= item.limit.Max {
			continue
		}
		if item.limit.Max == 0 {
			return httpx.Errorf(http.StatusBadRequest, "unsupported_media",
				"workflow %s accepts no reference %s, got %d%s",
				entry.Name, item.plural, item.count, hint(entry.Accepts.Note))
		}
		return httpx.Errorf(http.StatusBadRequest, "unsupported_media",
			"workflow %s accepts at most %d reference %s, got %d%s",
			entry.Name, item.limit.Max, item.plural, item.count, hint(entry.Accepts.Note))
	}
	return nil
}

func missingMedia(entry *Entry, minimum, got int, note string) error {
	return httpx.Errorf(http.StatusBadRequest, "missing_reference_media",
		"workflow %s needs at least %d reference media item(s) (image/video/audio), got %d%s",
		entry.Name, minimum, got, hint(note))
}

func hint(note string) string {
	if note == "" {
		return ""
	}
	return " (" + note + ")"
}
