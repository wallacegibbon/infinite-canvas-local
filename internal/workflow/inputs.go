package workflow

// Typed request inputs, validated at submission.
//
// `inputs` is open by design: a workflow's knobs are its bindings (`seed`, `steps`, `sampler`,
// `format`, a LoRA list), so a new workflow needs no new route or struct. Open is not the same as
// unchecked, though: an unknown key is a typo that would otherwise be ignored silently, and a value
// of the wrong type only fails minutes later inside ComfyUI. This file is the one place that says
// what a run may carry.

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/util"
)

// InputLimits are engine-wide bounds that are not a property of one workflow.
type InputLimits struct {
	// MaxCount caps `count`; the canvas generates batches of images up to this size.
	MaxCount int
}

// InputSpec is one accepted input field.
type InputSpec struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"` // string, number, integer, size, boolean
	Required bool     `json:"required,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Options  []string `json:"options,omitempty"`
	// StrictOptions is set only where the gateway owns the value set (a quality preset name).
	// A sampler name, by contrast, is ComfyUI's vocabulary: the list in registry.json is a UI
	// suggestion and must not reject a valid value the UI never offered.
	StrictOptions bool   `json:"-"`
	Default       any    `json:"default,omitempty"`
	Label         string `json:"label,omitempty"`
}

// semanticInputs are the keys that drive the request itself rather than a binding.
var semanticInputs = []string{"prompt", "negative", "size", "count", "n", "quality", "seconds", "duration", "mode"}

// InputSpecs is the typed contract of a workflow, assembled from three sources: the request-level
// keys above, the workflow's declared `parameters`, and its non-media binding keys.
func (e *Entry) InputSpecs(limits InputLimits) []InputSpec {
	if e == nil {
		return nil
	}
	specs := make([]InputSpec, 0, len(semanticInputs)+len(e.Parameters))
	seen := map[string]bool{}
	add := func(spec InputSpec) {
		if spec.Key == "" || seen[spec.Key] {
			return
		}
		seen[spec.Key] = true
		specs = append(specs, spec)
	}

	promptRequired := false
	bindings := e.Bindings()
	if binding, ok := bindings["prompt"]; ok {
		promptRequired = !binding.Has("default") || binding.Bool("required")
		if !binding.Bool("required") && !binding.Has("default") {
			// A prompt binding without a default is required in practice: the node input would
			// otherwise keep whatever the export happened to contain.
			promptRequired = true
		}
	}

	add(InputSpec{Key: "prompt", Type: "string", Required: promptRequired, Label: "Prompt"})
	add(InputSpec{Key: "negative", Type: "string", Label: "Negative prompt"})
	add(InputSpec{Key: "size", Type: "size", Label: "Size"})
	maxCount := limits.MaxCount
	if maxCount <= 0 {
		maxCount = 15
	}
	maxCountValue := float64(maxCount)
	minCount := 1.0
	add(InputSpec{Key: "count", Type: "integer", Min: &minCount, Max: &maxCountValue, Label: "Count"})
	add(InputSpec{Key: "n", Type: "integer", Min: &minCount, Max: &maxCountValue, Label: "Count"})
	add(InputSpec{Key: "quality", Type: "string", Options: e.qualityOptions(), StrictOptions: len(e.qualityOptions()) > 0, Label: "Quality"})
	minimumSeconds := 0.0
	add(InputSpec{Key: "seconds", Type: "number", Min: &minimumSeconds, Label: "Seconds"})
	add(InputSpec{Key: "duration", Type: "number", Min: &minimumSeconds, Label: "Duration"})
	add(InputSpec{Key: "mode", Type: "string", Options: e.VariantModes(), Label: "Mode"})

	// Declared parameters carry the workflow's own ranges. They are authoritative for numbers.
	for _, raw := range e.Parameters {
		object, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		key, _ := object["key"].(string)
		if key == "" {
			continue
		}
		spec := InputSpec{Key: key, Type: parameterType(object), Label: stringField(object, "label")}
		spec.Min = numberPointer(object["min"])
		spec.Max = numberPointer(object["max"])
		spec.Default = object["default"]
		spec.Options = stringList(object["options"])
		add(spec)
	}

	// Any remaining non-media binding is a valid input too (a model file override, for example).
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		binding := bindings[key]
		if binding.IsMedia() {
			continue
		}
		add(InputSpec{Key: key, Type: bindingInputType(binding), Label: key})
	}
	return specs
}

// MediaBindingKeys lists the binding names that carry media. They are not valid `inputs`: media goes
// in the run's `media` map, by role.
func (e *Entry) MediaBindingKeys() []string {
	if e == nil {
		return nil
	}
	keys := make([]string, 0)
	for key, binding := range e.Bindings() {
		if binding.IsMedia() {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// ValidateInputs rejects unknown keys, wrong types and out-of-range values before a run is queued.
//
// The check runs at submission because a binding error happens inside the runner, which for a
// queued run means the caller gets a failed run minutes later instead of a `400` now.
func ValidateInputs(entry *Entry, inputs map[string]any, limits InputLimits) error {
	specs := entry.InputSpecs(limits)
	byKey := make(map[string]InputSpec, len(specs))
	accepted := make([]string, 0, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
		accepted = append(accepted, spec.Key)
	}
	sort.Strings(accepted)

	mediaKeys := map[string]bool{}
	for _, key := range entry.MediaBindingKeys() {
		mediaKeys[key] = true
	}

	for key, value := range inputs {
		if value == nil {
			continue
		}
		spec, ok := byKey[key]
		if !ok {
			if mediaKeys[key] {
				return httpx.Errorf(http.StatusBadRequest, "unknown_input",
					"%s carries media; use media.<role> instead of inputs (accepted inputs: %s)",
					key, strings.Join(accepted, ", "))
			}
			return httpx.Errorf(http.StatusBadRequest, "unknown_input",
				"unknown input %q; workflow %s accepts: %s", key, entry.Name, strings.Join(accepted, ", "))
		}
		if err := validateInputValue(entry, spec, value); err != nil {
			return err
		}
	}

	if spec, ok := byKey["prompt"]; ok && spec.Required {
		prompt, _ := inputs["prompt"].(string)
		if strings.TrimSpace(prompt) == "" {
			return httpx.Errorf(http.StatusBadRequest, "missing_input",
				"workflow %s requires a non-empty prompt", entry.Name)
		}
	}
	return nil
}

func validateInputValue(entry *Entry, spec InputSpec, value any) error {
	fail := func(reason string) error {
		return httpx.Errorf(http.StatusBadRequest, "invalid_input",
			"input %q %s (workflow %s)", spec.Key, reason, entry.Name)
	}

	switch spec.Type {
	case "size":
		text, ok := value.(string)
		if !ok {
			return fail("must be a size string like 1024x1024")
		}
		if util.ParseSize(strings.TrimSpace(text)) == nil {
			return fail("must be a size string like 1024x1024")
		}
	case "string":
		text, ok := coerceString(value)
		if !ok {
			return fail("must be a string")
		}
		if len(spec.Options) > 0 && spec.StrictOptions && !containsString(spec.Options, text) {
			return fail("must be one of: " + strings.Join(spec.Options, ", "))
		}
	case "number", "integer":
		number, ok := coerceNumber(value)
		if !ok {
			return fail("must be a number")
		}
		if spec.Type == "integer" && math.Trunc(number) != number {
			return fail("must be a whole number")
		}
		if spec.Min != nil && number < *spec.Min {
			return fail("must be at least " + formatNumber(*spec.Min))
		}
		if spec.Max != nil && number > *spec.Max {
			return fail("must be at most " + formatNumber(*spec.Max))
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("must be true or false")
		}
	default:
		// An inferred binding type accepts anything the binding engine can coerce.
	}
	return nil
}

// IsMedia reports whether a binding consumes uploaded media rather than a scalar value.
func (b *Binding) IsMedia() bool {
	if b == nil {
		return false
	}
	switch b.String(keyFrom) {
	case "image", "video", "audio":
		return true
	}
	return b.Bool(keyArray) || b.Bool(keyFrameMode) || b.Has(keyImageIndex) ||
		b.Has(keyVideoIndex) || b.String(keyLoadType) != ""
}

func (e *Entry) qualityOptions() []string {
	quality, ok := e.Presets["quality"].(map[string]any)
	if !ok {
		return nil
	}
	options := make([]string, 0, len(quality)+1)
	for name := range quality {
		options = append(options, name)
	}
	sort.Strings(options)
	// "auto" means "no preset", which is the default every adapter sends.
	return append([]string{"auto"}, options...)
}

func parameterType(object map[string]any) string {
	switch stringField(object, "type") {
	case "number":
		return "number"
	case "boolean":
		return "boolean"
	default:
		return "string"
	}
}

// bindingInputType infers a type from the binding it feeds, so an inferred key is still checked
// when the binding makes the expectation obvious.
func bindingInputType(binding *Binding) string {
	if binding == nil {
		return ""
	}
	switch binding.String(keyTransform) {
	case "int", "round", "snap16", "snap32", "secondsToFrames", "secondsToFrames17":
		return "integer"
	}
	if binding.Bool(keyRandom) || binding.Has(keyDefault) {
		switch binding.Value(keyDefault).(type) {
		case float64, int:
			return "number"
		case string:
			return "string"
		}
	}
	return ""
}

func coerceString(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), true
	case int:
		return strconv.Itoa(typed), true
	}
	return "", false
}

func coerceNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func numberPointer(value any) *float64 {
	number, ok := coerceNumber(value)
	if !ok {
		return nil
	}
	return &number
}

func stringField(object map[string]any, key string) string {
	text, _ := object[key].(string)
	return text
}

func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func formatNumber(value float64) string {
	if value == math.Trunc(value) {
		return strconv.FormatInt(int64(value), 10)
	}
	return fmt.Sprintf("%g", value)
}
