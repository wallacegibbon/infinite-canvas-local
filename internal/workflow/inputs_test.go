package workflow

import (
	"io"
	"sort"
	"strings"
	"testing"

	"comfyui-gateway/internal/logx"
)

// bindings builds an OrderedMap the way JSON decoding does: keys in order, values by key.
func bindings(pairs ...any) OrderedMap[OrderedMap[any]] {
	out := OrderedMap[OrderedMap[any]]{Values: map[string]OrderedMap[any]{}}
	for index := 0; index < len(pairs); index += 2 {
		key := pairs[index].(string)
		value := pairs[index+1].(map[string]any)
		out.Keys = append(out.Keys, key)
		out.Values[key] = OrderedMap[any]{Keys: mapKeysInOrder(value), Values: value}
	}
	return out
}

func mapKeysInOrder(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func inputEntry() *Entry {
	stepsMax := 30.0
	stepsMin := 1.0
	return &Entry{
		Name: "inputs-test",
		RawBindings: bindings(
			"prompt", map[string]any{"node": "4", "input": "text", "required": true},
			"seed", map[string]any{"node": "8", "input": "seed", "from": "params.seed", "random": true},
			"steps", map[string]any{"node": "8", "input": "steps", "from": "params.steps"},
			"refs", map[string]any{
				"node": "9", "input": "images", "from": "image", "array": true, "inputPrefix": "ref_",
			},
		),
		Parameters: []any{
			map[string]any{"key": "steps", "type": "number", "min": stepsMin, "max": stepsMax},
			map[string]any{"key": "sampler", "type": "select", "options": []any{"euler", "dpmpp_2m"}},
		},
		Presets: map[string]any{"quality": map[string]any{"low": map[string]any{}, "high": map[string]any{}}},
	}
}

func TestValidateInputsAcceptsTheDeclaredSurface(t *testing.T) {
	entry := inputEntry()
	inputs := map[string]any{
		"prompt":  "a cat",
		"size":    "1024x1024",
		"count":   3,
		"quality": "high",
		"seconds": 5,
		"seed":    0,
		"steps":   8,
		// Advisory: ComfyUI has more samplers than the UI lists, so the list cannot reject.
		"sampler": "dpmpp_2m_sde_gpu",
	}
	if err := ValidateInputs(entry, inputs, InputLimits{MaxCount: 15}); err != nil {
		t.Fatalf("valid inputs rejected: %v", err)
	}
}

func TestValidateInputsRejections(t *testing.T) {
	cases := []struct {
		name   string
		inputs map[string]any
		code   string
		text   string
	}{
		{"unknown key", map[string]any{"prompt": "x", "setps": 4}, "unknown_input", "unknown input"},
		{"missing prompt", map[string]any{}, "missing_input", "requires a non-empty prompt"},
		{"blank prompt", map[string]any{"prompt": "   "}, "missing_input", "non-empty prompt"},
		{"bad size", map[string]any{"prompt": "x", "size": "huge"}, "invalid_input", "size string"},
		{"count is fractional", map[string]any{"prompt": "x", "count": 1.5}, "invalid_input", "whole number"},
		{"count below range", map[string]any{"prompt": "x", "count": 0}, "invalid_input", "at least 1"},
		{"count above range", map[string]any{"prompt": "x", "count": 20}, "invalid_input", "at most 15"},
		{"steps out of range", map[string]any{"prompt": "x", "steps": 99}, "invalid_input", "at most 30"},
		{"steps not numeric", map[string]any{"prompt": "x", "steps": "many"}, "invalid_input", "must be a number"},
		{"unknown quality", map[string]any{"prompt": "x", "quality": "ultra"}, "invalid_input", "must be one of"},
	}
	for _, testCase := range cases {
		err := ValidateInputs(inputEntry(), testCase.inputs, InputLimits{MaxCount: 15})
		if err == nil {
			t.Errorf("%s: accepted", testCase.name)
			continue
		}
		if got := errorCode(t, err); got != testCase.code {
			t.Errorf("%s: code = %q, want %q", testCase.name, got, testCase.code)
		}
		if !strings.Contains(err.Error(), testCase.text) {
			t.Errorf("%s: message %q does not contain %q", testCase.name, err.Error(), testCase.text)
		}
	}
}

// Media binding keys are not inputs: pointing the caller at media.<role> is more useful than a bare
// "unknown input".
func TestValidateInputsPointsMediaAtTheMediaMap(t *testing.T) {
	err := ValidateInputs(inputEntry(), map[string]any{"prompt": "x", "refs": []any{"upl_1"}}, InputLimits{})
	if err == nil {
		t.Fatal("a media binding key was accepted as an input")
	}
	if !strings.Contains(err.Error(), "use media.<role>") {
		t.Errorf("message = %q", err.Error())
	}
}

// The schema served by /api/workflows/{id}/schema describes exactly what a run accepts.
func TestSchemaListsTypedInputs(t *testing.T) {
	entry := inputEntry()
	schema := BuildSchema(entry)
	byKey := map[string]InputSpec{}
	for _, spec := range schema.Inputs {
		byKey[spec.Key] = spec
	}
	for _, key := range []string{"prompt", "size", "count", "quality", "seconds", "mode", "steps", "sampler", "seed"} {
		if _, ok := byKey[key]; !ok {
			t.Errorf("schema is missing %q", key)
		}
	}
	if !byKey["prompt"].Required {
		t.Error("prompt must be required")
	}
	if byKey["count"].Type != "integer" || byKey["count"].Max == nil || *byKey["count"].Max != 15 {
		t.Errorf("count spec = %+v", byKey["count"])
	}
	if !byKey["quality"].StrictOptions {
		t.Error("quality must enforce its declared options")
	}
	if byKey["sampler"].StrictOptions {
		t.Error("sampler options are advisory, not a closed set")
	}
}

// Every shipped workflow must accept the one input it always needs (a prompt) and reject a typo.
func TestShippedWorkflowsValidateTheirInputs(t *testing.T) {
	registry := NewRegistry(RegistryOptions{Dir: shippedWorkflowsDir, Logger: logx.NewWithWriter("test", io.Discard)})
	if err := registry.Load(); err != nil {
		t.Fatalf("load shipped registry: %v", err)
	}
	for _, entry := range registry.Entries() {
		specs := entry.InputSpecs(InputLimits{MaxCount: 15})
		hasPrompt := false
		for _, spec := range specs {
			if spec.Key == "prompt" {
				hasPrompt = true
			}
		}
		if !hasPrompt {
			t.Errorf("%s has no prompt input", entry.ID)
		}
		if err := ValidateInputs(entry, map[string]any{"prompt": "a cat"}, InputLimits{MaxCount: 15}); err != nil {
			t.Errorf("%s rejected a minimal request: %v", entry.ID, err)
		}
		if err := ValidateInputs(entry, map[string]any{"prompt": "a cat", "nonsense": 1}, InputLimits{MaxCount: 15}); err == nil {
			t.Errorf("%s accepted an unknown input", entry.ID)
		}
	}
}
