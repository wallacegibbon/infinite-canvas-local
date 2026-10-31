package workflow

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Binding keys, spelled once. A typo inside a string literal is the one class of
// bug the compiler cannot catch here, so every key the engine reads lives here.
const (
	keyNode               = "node"
	keyInput              = "input"
	keyFrom               = "from"
	keyTransform          = "transform"
	keyFPS                = "fps"
	keyDefault            = "default"
	keyRandom             = "random"
	keyRequired           = "required"
	keyArray              = "array"
	keyMin                = "min"
	keyMax                = "max"
	keyInputPrefix        = "inputPrefix"
	keyIndexBase          = "indexBase"
	keyImageIndex         = "imageIndex"
	keyVideoIndex         = "videoIndex"
	keyAsLink             = "asLink"
	keyLoadNodeID         = "loadNodeId"
	keyLoadType           = "loadType"
	keyLoader             = "loader"
	keyAudioPrefix        = "audioPrefix"
	keySourceLengthInput  = "sourceLengthInput"
	keySourceLengthOutput = "sourceLengthOutput"
	keyFrameMode          = "frameMode"
	keyFirstInput         = "firstInput"
	keyLastInput          = "lastInput"
	keyMap                = "map"
	keyForceRate          = "forceRate"
)

// Binding is one entry of a workflow's `bindings` object.
//
// The raw JSON is retained and the accessors read through it. That is deliberate:
// registry.json is user-editable, and a closed struct would silently drop any
// field a future engine version adds.
type Binding struct {
	raw map[string]any
}

// NewBinding wraps a decoded JSON object.
func NewBinding(raw map[string]any) *Binding { return &Binding{raw: raw} }

// Raw returns the underlying object for re-serialization.
func (b *Binding) Raw() map[string]any {
	if b == nil || b.raw == nil {
		return map[string]any{}
	}
	return b.raw
}

// Has reports whether the key was present in the source JSON. Presence matters:
// `default: 0` is a real default, while an absent default means "skip".
func (b *Binding) Has(key string) bool {
	if b == nil {
		return false
	}
	_, ok := b.raw[key]
	return ok
}

// Value returns the raw value for a key.
func (b *Binding) Value(key string) any {
	if b == nil {
		return nil
	}
	return b.raw[key]
}

// String reads a string field.
func (b *Binding) String(key string) string {
	value, _ := b.Value(key).(string)
	return value
}

// Bool reads a boolean field.
func (b *Binding) Bool(key string) bool {
	value, _ := b.Value(key).(bool)
	return value
}

// Int reads an integer field, returning fallback when absent or non-numeric.
func (b *Binding) Int(key string, fallback int) int {
	value := b.number(key)
	if value == nil {
		return fallback
	}
	return int(*value)
}

// Float reads a float field, returning fallback when absent or non-numeric.
func (b *Binding) Float(key string, fallback float64) float64 {
	value := b.number(key)
	if value == nil {
		return fallback
	}
	return *value
}

// number coerces a binding value the way JavaScript's Number() would for the shapes
// that appear in registry.json: JSON numbers, and numeric strings from hand edits.
// Returning nil keeps "absent" distinct from a legitimate zero.
func (b *Binding) number(key string) *float64 {
	switch typed := b.Value(key).(type) {
	case float64:
		return &typed
	case int:
		value := float64(typed)
		return &value
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil
		}
		return &parsed
	}
	return nil
}

// Object reads a nested object field.
func (b *Binding) Object(key string) map[string]any {
	value, _ := b.Value(key).(map[string]any)
	return value
}

// OutputSize describes how a workflow's output canvas must be resized. Only the
// Qwen variants use it today, to align the latent size with the reference image.
type OutputSize struct {
	Node             string `json:"node"`
	LatentNode       string `json:"latentNode"`
	LatentInput      string `json:"latentInput,omitempty"`
	LatentSource     string `json:"latentSource,omitempty"`
	FromSize         bool   `json:"fromSize,omitempty"`
	SkipNativeSquare bool   `json:"skipNativeSquare,omitempty"`
}

// Lora describes how LoRA cards are injected into a workflow.
type Lora struct {
	Filter string `json:"filter,omitempty"`
	Source string `json:"source,omitempty"`
	Loader string `json:"loader,omitempty"`
}

// BaseModelConfig describes the swappable base model of a workflow.
type BaseModelConfig struct {
	Param     string            `json:"param"`
	ClassType string            `json:"classType,omitempty"`
	Input     string            `json:"input,omitempty"`
	Label     string            `json:"label,omitempty"`
	Filter    string            `json:"filter,omitempty"`
	Exclude   string            `json:"exclude,omitempty"`
	Prefix    string            `json:"prefix,omitempty"`
	Aliases   map[string]string `json:"aliases,omitempty"`
	Variants  []BaseModelEntry  `json:"variants,omitempty"`
}

// BaseModelEntry is an explicitly declared base-model variant.
type BaseModelEntry struct {
	File   string         `json:"file"`
	Label  string         `json:"label,omitempty"`
	ID     string         `json:"id,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// OutputSpec is what a workflow produces, where a graph cannot say it.
//
// The artifact kind matches the workflow type today, so it is optional. The container part
// exists because an encoder node only writes specific formats with specific quality values
// (SaveAudioAdvanced takes mp3/V0 or opus/128k, never opus/V0); declaring the accepted pairs
// here is what keeps that knowledge out of the handler.
type OutputSpec struct {
	Kind         string                `json:"kind,omitempty"`
	FormatParam  string                `json:"formatParam,omitempty"`
	QualityParam string                `json:"qualityParam,omitempty"`
	Formats      map[string]FormatSpec `json:"formats,omitempty"`
}

// FormatSpec is one accepted container. Quality is a value the encoder accepts for it; empty
// means the container takes no quality input.
type FormatSpec struct {
	Quality string `json:"quality,omitempty"`
}

// FormatNames returns the accepted containers in a stable order, for error messages.
func (o *OutputSpec) FormatNames() []string {
	if o == nil {
		return nil
	}
	names := make([]string, 0, len(o.Formats))
	for name := range o.Formats {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Source records where an imported workflow came from.
type Source struct {
	Kind string `json:"kind,omitempty"`
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	Name string `json:"name,omitempty"`
}

// Variant is one mode of a multi-mode workflow (Qwen's edit/inpaint/outpaint/t2i).
type Variant struct {
	File        string                      `json:"file"`
	Label       string                      `json:"label,omitempty"`
	Description string                      `json:"description,omitempty"`
	RawBindings OrderedMap[OrderedMap[any]] `json:"bindings,omitempty"`
	Defaults    map[string]any              `json:"defaults,omitempty"`
	OutputSize  *OutputSize                 `json:"outputSize,omitempty"`
	Source      *Source                     `json:"source,omitempty"`

	// Per-mode contract. A variant is a different graph with different wiring, so it can accept
	// a different media mix (Qwen's inpaint wants a source and a mask; t2i wants none).
	Accepts       *Accepts       `json:"accepts,omitempty"`
	Mode          string         `json:"mode,omitempty"`
	FrameMode     *FrameModeRule `json:"frameMode,omitempty"`
	PromptInject  string         `json:"promptInject,omitempty"`
	PromptReorder string         `json:"promptReorder,omitempty"`
	Outputs       *OutputSpec    `json:"outputs,omitempty"`

	// Workflow is the parsed graph, loaded next to the registry entry.
	Workflow Graph `json:"-"`
}

// Bindings returns the variant's own bindings as typed views.
func (v *Variant) Bindings() map[string]*Binding { return toBindings(v.RawBindings) }

// Entry is one registered workflow, ready to be parameterized per request.
type Entry struct {
	ID               string
	Name             string
	Type             string
	File             string
	FilePath         string
	Label            string
	Description      string
	Category         string
	Hidden           bool
	MinMedia         int
	BackgroundPrompt string

	// Declarative request contract, so the handlers stay generic. See media.go.
	Accepts           *Accepts       `json:"accepts,omitempty"`
	Mode              string         `json:"mode,omitempty"`
	FrameMode         *FrameModeRule `json:"frameMode,omitempty"`
	PromptInject      string         `json:"promptInject,omitempty"`
	PromptReorder     string         `json:"promptReorder,omitempty"`
	SourceVideoFrames bool           `json:"sourceVideoFrames,omitempty"`
	Outputs           *OutputSpec    `json:"outputs,omitempty"`

	Workflow      Graph
	RawBindings   OrderedMap[OrderedMap[any]]
	Defaults      map[string]any
	Presets       map[string]any
	Parameters    []any
	Lora          *Lora
	BaseModels    *BaseModelConfig
	OutputSize    *OutputSize
	Variants      OrderedMap[*Variant]
	VariantLabels OrderedMap[string]
	ImportedAt    string
	Source        *Source

	// Decoration applied when a base-model variant or a per-workflow main-model
	// override is in play. Request-scoped copies, never persisted.
	BaseModel       string
	BaseModelLabel  string
	BaseModelParams map[string]any
	MainModelFile   string
}

// Bindings returns the entry's bindings as typed views.
func (e *Entry) Bindings() map[string]*Binding { return toBindings(e.RawBindings) }

// Binding returns one binding by name.
func (e *Entry) Binding(name string) (*Binding, bool) {
	stored, ok := e.RawBindings.Get(name)
	if !ok {
		return nil, false
	}
	return NewBinding(stored.Values), true
}

// VariantModes returns the variant mode names in file order.
func (e *Entry) VariantModes() []string {
	out := make([]string, len(e.Variants.Keys))
	copy(out, e.Variants.Keys)
	return out
}

// VariantLabelsMap returns variantLabels as a plain map for display.
func (e *Entry) VariantLabelsMap() map[string]string {
	out := make(map[string]string, len(e.VariantLabels.Keys))
	for _, key := range e.VariantLabels.Keys {
		out[key] = e.VariantLabels.Values[key]
	}
	return out
}

// toBindings converts an ordered map of raw objects into typed views.
func toBindings(source OrderedMap[OrderedMap[any]]) map[string]*Binding {
	out := make(map[string]*Binding, len(source.Keys))
	for _, key := range source.Keys {
		out[key] = NewBinding(source.Values[key].Values)
	}
	return out
}

// rawEntry mirrors registry.json's per-workflow object.
type rawEntry struct {
	ID                string                      `json:"id"`
	Name              string                      `json:"name"`
	Type              string                      `json:"type"`
	File              string                      `json:"file"`
	Label             string                      `json:"label"`
	Description       string                      `json:"description"`
	Category          string                      `json:"category"`
	Hidden            bool                        `json:"hidden"`
	MinMedia          int                         `json:"minMedia"`
	BackgroundPrompt  string                      `json:"backgroundPrompt"`
	Accepts           *Accepts                    `json:"accepts,omitempty"`
	Mode              string                      `json:"mode,omitempty"`
	FrameMode         *FrameModeRule              `json:"frameMode,omitempty"`
	PromptInject      string                      `json:"promptInject,omitempty"`
	PromptReorder     string                      `json:"promptReorder,omitempty"`
	SourceVideoFrames bool                        `json:"sourceVideoFrames,omitempty"`
	Outputs           *OutputSpec                 `json:"outputs,omitempty"`
	Bindings          OrderedMap[OrderedMap[any]] `json:"bindings"`
	Defaults          map[string]any              `json:"defaults"`
	Presets           map[string]any              `json:"presets"`
	Parameters        []any                       `json:"parameters"`
	Lora              *Lora                       `json:"lora"`
	BaseModels        *BaseModelConfig            `json:"baseModels"`
	OutputSize        *OutputSize                 `json:"outputSize"`
	Variants          OrderedMap[*Variant]        `json:"variants"`
	VariantLabels     OrderedMap[string]          `json:"variantLabels"`
	ImportedAt        string                      `json:"importedAt"`
	Source            *Source                     `json:"source"`
}

// registryFile mirrors the on-disk registry document.
type registryFile struct {
	Version   int               `json:"version"`
	Workflows []json.RawMessage `json:"workflows"`
}
