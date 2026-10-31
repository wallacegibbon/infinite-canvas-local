package workflow

import (
	"io"
	"testing"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
)

// errorCode returns the stable code of an error produced by PlanMedia.
func errorCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var appErr *httpx.Error
	if !asHTTPError(err, &appErr) {
		t.Fatalf("error %v is not an httpx.Error", err)
	}
	return appErr.Code
}

func TestPlanMediaRejectsMediaForTextOnlyWorkflow(t *testing.T) {
	entry := &Entry{
		Name: "t2v",
		Mode: "text",
		Accepts: &Accepts{
			Images: &MediaLimit{Max: 0},
			Videos: &MediaLimit{Max: 0},
			Audios: &MediaLimit{Max: 0},
		},
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 1}, "", nil); errorCode(t, err) != "unsupported_media" {
		t.Errorf("code = %q", errorCode(t, err))
	}
	plan, err := PlanMedia(entry, MediaCounts{}, "", nil)
	if err != nil {
		t.Fatalf("no media should be accepted: %v", err)
	}
	if plan.Mode != "text" {
		t.Errorf("mode = %q, want the workflow's own text mode", plan.Mode)
	}
}

func TestPlanMediaFrameModeNeeds(t *testing.T) {
	entry := &Entry{
		Name:      "frames",
		Mode:      "frames",
		FrameMode: &FrameModeRule{Default: "byCount", Images: map[string]int{"first": 1, "last": 1, "both": 2}},
		Accepts: &Accepts{
			Images: &MediaLimit{Min: 1, Max: 2},
			Videos: &MediaLimit{Max: 0},
			Audios: &MediaLimit{Max: 0},
		},
	}

	if _, err := PlanMedia(entry, MediaCounts{}, "", nil); errorCode(t, err) != "missing_reference_image" {
		t.Errorf("no image code = %q", errorCode(t, err))
	}

	plan, err := PlanMedia(entry, MediaCounts{Images: 1}, "", nil)
	if err != nil {
		t.Fatalf("one image: %v", err)
	}
	if plan.FrameMode != "first" || plan.Images != 1 {
		t.Errorf("one image -> frameMode %q / %d images", plan.FrameMode, plan.Images)
	}

	plan, err = PlanMedia(entry, MediaCounts{Images: 2}, "", nil)
	if err != nil {
		t.Fatalf("two images: %v", err)
	}
	if plan.FrameMode != "both" || plan.Images != 2 {
		t.Errorf("two images -> frameMode %q / %d images", plan.FrameMode, plan.Images)
	}

	// An explicit mode with too few images fails on the image minimum, not silently.
	if _, err := PlanMedia(entry, MediaCounts{Images: 1}, "", map[string]any{"frameMode": "both"}); errorCode(t, err) != "missing_reference_image" {
		t.Errorf("explicit both with one image code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 1}, "", map[string]any{"frameMode": "middle"}); errorCode(t, err) != "invalid_frame_mode" {
		t.Errorf("bogus frame mode code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 1, Audios: 1}, "", nil); errorCode(t, err) != "unsupported_media" {
		t.Errorf("audio on a frame workflow code = %q", errorCode(t, err))
	}
}

func TestPlanMediaReferenceWorkflow(t *testing.T) {
	entry := &Entry{
		Name: "ref2va",
		Mode: "reference",
		Accepts: &Accepts{
			Images:   &MediaLimit{Max: 6},
			Videos:   &MediaLimit{Max: 1},
			Audios:   &MediaLimit{Max: 2},
			MinTotal: 1,
		},
	}
	if _, err := PlanMedia(entry, MediaCounts{}, "", nil); errorCode(t, err) != "missing_reference_media" {
		t.Errorf("no media code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 7}, "", nil); errorCode(t, err) != "unsupported_media" {
		t.Errorf("seven images code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Audios: 3}, "", nil); errorCode(t, err) != "unsupported_media" {
		t.Errorf("three audio clips code = %q", errorCode(t, err))
	}
	plan, err := PlanMedia(entry, MediaCounts{Images: 6, Videos: 1, Audios: 2}, "", nil)
	if err != nil {
		t.Fatalf("full media set: %v", err)
	}
	if plan.Mode != "reference" {
		t.Errorf("mode = %q", plan.Mode)
	}
}

func TestPlanMediaCharacterSwapNeedsVideoAndImage(t *testing.T) {
	entry := &Entry{
		Name: "swap",
		Accepts: &Accepts{
			Images:   &MediaLimit{Min: 1, Max: 6},
			Videos:   &MediaLimit{Min: 1, Max: 1},
			Audios:   &MediaLimit{Max: 2},
			MinTotal: 2,
		},
	}
	// The total floor is checked first: one video alone is below the two items this workflow
	// needs, so the answer names the media floor rather than a specific kind.
	if _, err := PlanMedia(entry, MediaCounts{Videos: 1}, "", nil); errorCode(t, err) != "missing_reference_media" {
		t.Errorf("video only code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Videos: 1, Audios: 1}, "", nil); errorCode(t, err) != "missing_reference_image" {
		t.Errorf("video + audio code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 1}, "", nil); errorCode(t, err) != "missing_reference_media" {
		t.Errorf("image only code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 1, Videos: 1}, "", nil); err != nil {
		t.Errorf("video + image: %v", err)
	}
}

// A workflow without a declared contract keeps the historical total-media floor and is otherwise
// unconstrained, so adding a workflow cannot break because it forgot a new field.
func TestPlanMediaUndeclaredContract(t *testing.T) {
	entry := &Entry{Name: "legacy", MinMedia: 1}
	if _, err := PlanMedia(entry, MediaCounts{}, "", nil); errorCode(t, err) != "missing_reference_media" {
		t.Errorf("code = %q", errorCode(t, err))
	}
	if _, err := PlanMedia(entry, MediaCounts{Images: 3}, "requested", nil); err != nil {
		t.Errorf("undeclared contract should not constrain: %v", err)
	}
}

// The shipped registry is the real contract. These assertions are the old hardcoded rules,
// expressed against the data, so a registry edit that weakens them fails here.
func TestShippedWorkflowsDeclareTheirMediaContract(t *testing.T) {
	registry := NewRegistry(RegistryOptions{
		Dir:    shippedWorkflowsDir,
		Logger: logx.NewWithWriter("test", io.Discard),
	})
	if err := registry.Load(); err != nil {
		t.Fatalf("load shipped registry: %v", err)
	}
	plan := func(id string, counts MediaCounts, mode string, params map[string]any) *MediaPlan {
		t.Helper()
		entry := shippedEntry(t, registry, id)
		result, err := PlanMedia(entry, counts, mode, params)
		if err != nil {
			t.Fatalf("%s with %+v: %v", id, counts, err)
		}
		return result
	}
	refused := func(id string, counts MediaCounts, want string) {
		t.Helper()
		entry := shippedEntry(t, registry, id)
		if _, err := PlanMedia(entry, counts, "", nil); errorCode(t, err) != want {
			t.Errorf("%s with %+v: code = %q, want %q", id, counts, errorCode(t, err), want)
		}
	}

	for _, id := range []string{"video-h3-t2v", "video-h3-t2v-novdn"} {
		if got := plan(id, MediaCounts{}, "", nil).Mode; got != "text" {
			t.Errorf("%s mode = %q", id, got)
		}
		refused(id, MediaCounts{Images: 1}, "unsupported_media")
	}

	for _, id := range []string{"video-h3-frames", "video-h3-frames-novdn"} {
		if got := plan(id, MediaCounts{Images: 2}, "", nil); got.FrameMode != "both" {
			t.Errorf("%s frameMode = %q", id, got.FrameMode)
		}
		if got := plan(id, MediaCounts{Images: 1}, "", nil); got.FrameMode != "first" {
			t.Errorf("%s frameMode = %q", id, got.FrameMode)
		}
		refused(id, MediaCounts{}, "missing_reference_image")
		refused(id, MediaCounts{Images: 1, Audios: 1}, "unsupported_media")
	}

	for _, id := range []string{"video-h3-ref2va", "video-h3-ref2va-novdn"} {
		if got := plan(id, MediaCounts{Images: 6, Videos: 1, Audios: 2}, "", nil).Mode; got != "reference" {
			t.Errorf("%s mode = %q", id, got)
		}
		refused(id, MediaCounts{}, "missing_reference_media")
		refused(id, MediaCounts{Images: 7}, "unsupported_media")
	}

	plan("video-h3-character-swap", MediaCounts{Images: 1, Videos: 1}, "", nil)
	refused("video-h3-character-swap", MediaCounts{Videos: 1}, "missing_reference_media")

	// Qwen's four modes are variants now, each with its own contract.
	qwen := shippedEntry(t, registry, "image-qwen21")
	// Reference reordering is what makes Qwen's multi-image edit keep the source as the last
	// reference; it is declared on the variant, so a registry edit cannot silently drop it.
	if got := registry.ResolveForMode(qwen, "edit").PromptReorder; got != "qwen-edit" {
		t.Errorf("edit promptReorder = %q, want qwen-edit", got)
	}
	if got := registry.ResolveForMode(qwen, "inpaint").PromptReorder; got != "" {
		t.Errorf("inpaint must not reorder references, got %q", got)
	}

	modes := map[string]int{"t2i": 0, "edit": 4, "inpaint": 2, "outpaint": 1}
	for mode, allowed := range modes {
		resolved := registry.ResolveForMode(qwen, mode)
		if _, err := PlanMedia(resolved, MediaCounts{Images: allowed}, mode, nil); err != nil {
			t.Errorf("%s with %d image(s): %v", mode, allowed, err)
		}
		if _, err := PlanMedia(resolved, MediaCounts{Images: allowed + 1}, mode, nil); errorCode(t, err) != "unsupported_media" {
			t.Errorf("%s with %d image(s): code = %q, want unsupported_media", mode, allowed+1, errorCode(t, err))
		}
	}
}

// Moss prompts get the layout JSON appended, and that is declared rather than matched by id.
func TestShippedMossWorkflowsDeclarePromptInjection(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Dir: shippedWorkflowsDir, Logger: logx.NewWithWriter("test", io.Discard)})
	if err := registry.Load(); err != nil {
		t.Fatalf("load shipped registry: %v", err)
	}
	found := 0
	for _, entry := range registry.Entries() {
		if entry.Type != "image" {
			continue
		}
		if entry.PromptInject == "moss-layout" {
			found++
		}
	}
	if found != 3 {
		t.Errorf("moss-layout injections = %d, want the three MOSS workflows", found)
	}
}

// shippedEntry finds a workflow by id in a loaded registry.
func shippedEntry(t *testing.T, registry *Registry, id string) *Entry {
	t.Helper()
	for _, entry := range registry.Entries() {
		if entry.ID == id {
			return entry
		}
	}
	t.Fatalf("%s is not registered", id)
	return nil
}
