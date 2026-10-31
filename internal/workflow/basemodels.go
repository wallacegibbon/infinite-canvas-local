package workflow

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"comfyui-gateway/internal/util"
)

// LoaderOptions maps a node class (e.g. UNETLoader) to the model files ComfyUI
// reports for it. It is the authoritative list of what is installed.
type LoaderOptions map[string][]string

// BaseModelVariant is one selectable base model of a workflow.
type BaseModelVariant struct {
	ID      string
	File    string
	Label   string
	Params  map[string]any
	ModelID string
}

// UnetLoader locates the UNETLoader node that a main-model override should target.
type UnetLoader struct {
	NodeID string
	Input  string
	File   string
}

// ModelDescriptor is one model id a workflow answers to.
type ModelDescriptor struct {
	ModelID     string
	BaseID      string
	Entry       *Entry
	Variant     *BaseModelVariant
	DefaultFile string
}

var reShortType = regexp.MustCompile(`_`)

// ImageInputCapacity reports how many reference images a workflow can accept.
func ImageInputCapacity(entry *Entry) int {
	count := 0
	for _, binding := range entry.Bindings() {
		from := binding.String(keyFrom)
		if from != "image" && !binding.Has(keyImageIndex) && !binding.Bool(keyFrameMode) {
			continue
		}
		switch {
		case binding.Bool(keyFrameMode):
			count += maxInt(2, binding.Int(keyMax, 0))
		case binding.Bool(keyArray):
			count += maxInt(1, binding.Int(keyMax, 1))
		default:
			count++
		}
	}
	return count
}

// FindUnetLoader returns the first UNETLoader node with a usable unet_name.
func FindUnetLoader(graph Graph) *UnetLoader {
	for _, id := range graph.IDs() {
		node, ok := graph.NodeAt(id)
		if !ok || node.ClassType() != "UNETLoader" {
			continue
		}
		file, _ := node.Inputs()["unet_name"].(string)
		if file != "" {
			return &UnetLoader{NodeID: id, Input: "unet_name", File: file}
		}
	}
	return nil
}

// BaseModelDefault returns the graph's authored base model file.
func BaseModelDefault(entry *Entry) string {
	config := entry.BaseModels
	if config == nil {
		return ""
	}
	target := loaderNodeFor(entry, config)
	if target == nil {
		return ""
	}
	node, ok := entry.Workflow.NodeAt(target.NodeID)
	if !ok {
		return ""
	}
	value, _ := node.Inputs()[target.Input].(string)
	return value
}

// loaderNodeFor finds the node a base-model config points at, preferring an
// explicit binding over a class-type search.
func loaderNodeFor(entry *Entry, config *BaseModelConfig) *UnetLoader {
	if binding, ok := entry.Binding(config.Param); ok {
		nodeID := binding.String(keyNode)
		input := binding.String(keyInput)
		if nodeID != "" && input != "" {
			return &UnetLoader{NodeID: nodeID, Input: input}
		}
	}
	classType := defaultString(config.ClassType, "UNETLoader")
	input := defaultString(config.Input, "unet_name")
	for _, id := range entry.Workflow.IDs() {
		node, ok := entry.Workflow.NodeAt(id)
		if !ok || node.ClassType() != classType {
			continue
		}
		if _, exists := node.Inputs()[input]; exists {
			return &UnetLoader{NodeID: id, Input: input}
		}
	}
	return nil
}

// matchesPattern applies a case-insensitive regex, treating a bad pattern as no match.
func matchesPattern(file, pattern string) bool {
	if pattern == "" {
		return false
	}
	compiled, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return false
	}
	return compiled.MatchString(file)
}

// accepted reports whether a file passes the config's filter/exclude pair.
func accepted(file string, config *BaseModelConfig) bool {
	if config.Filter != "" && !matchesPattern(file, config.Filter) {
		return false
	}
	return !matchesPattern(file, config.Exclude)
}

func variantSlug(value string) string {
	return reShortType.ReplaceAllString(util.Slugify(value, "model"), "-")
}

var shortTypes = map[string]string{
	"image": "img",
	"video": "vid",
	"audio": "aud",
	"text":  "txt",
}

func shortType(workflowType string) string {
	if short, ok := shortTypes[workflowType]; ok {
		return short
	}
	if workflowType == "" {
		return "model"
	}
	return workflowType
}

func stripExtension(file string) string {
	base := filepath.Base(util.NormalizeSlashes(file))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// aliasFor returns the short alias configured for a base model file.
func aliasFor(config *BaseModelConfig, file string) string {
	if config == nil || config.Aliases == nil {
		return ""
	}
	if alias, ok := config.Aliases[file]; ok {
		return alias
	}
	if alias, ok := config.Aliases[util.BaseName(file)]; ok {
		return alias
	}
	return ""
}

func loaderFiles(config *BaseModelConfig, loaderOptions LoaderOptions) []string {
	classType := "UNETLoader"
	if config != nil && config.ClassType != "" {
		classType = config.ClassType
	}
	return loaderOptions[classType]
}

func filePresent(config *BaseModelConfig, loaderOptions LoaderOptions, file string) bool {
	for _, candidate := range loaderFiles(config, loaderOptions) {
		if candidate == file {
			return true
		}
	}
	return false
}

// ListBaseModels enumerates the selectable base models for a workflow.
func ListBaseModels(entry *Entry, loaderOptions LoaderOptions) []*BaseModelVariant {
	config := entry.BaseModels
	if config == nil || config.Param == "" {
		return nil
	}

	explicit := config.Variants
	var files []string
	if len(explicit) > 0 {
		for _, item := range explicit {
			if item.File != "" {
				files = append(files, item.File)
			}
		}
	} else {
		for _, file := range loaderFiles(config, loaderOptions) {
			if accepted(file, config) {
				files = append(files, file)
			}
		}
	}

	seen := map[string]bool{}
	out := make([]*BaseModelVariant, 0, len(files))
	for _, file := range files {
		var declared *BaseModelEntry
		for i := range explicit {
			if explicit[i].File == file {
				declared = &explicit[i]
				break
			}
		}

		base := ""
		if declared != nil && declared.ID != "" {
			base = variantSlug(declared.ID)
		}
		if base == "" {
			alias := aliasFor(config, file)
			if alias == "" {
				alias = stripExtension(file)
			}
			base = variantSlug(alias)
		}

		id := base
		for index := 1; seen[id]; index++ {
			id = base + "-" + strconv.Itoa(index+1)
		}
		seen[id] = true

		label := ""
		if declared != nil && declared.Label != "" {
			label = declared.Label
		}
		if label == "" {
			label = aliasFor(config, file)
		}
		if label == "" {
			label = stripExtension(file)
		}

		params := map[string]any{}
		if declared != nil && declared.Params != nil {
			params = declared.Params
		}
		out = append(out, &BaseModelVariant{ID: id, File: file, Label: label, Params: params})
	}
	return out
}

// ModelIDFor builds the externally visible model id for a base-model variant.
func ModelIDFor(entry *Entry, variant *BaseModelVariant) string {
	if variant == nil {
		return entry.Name
	}
	if entry.BaseModels != nil && entry.BaseModels.Prefix != "" {
		return shortType(entry.Type) + "-" + variantSlug(entry.BaseModels.Prefix) + "-" + variant.ID
	}
	return entry.Name + "__" + variant.ID
}

// ListModelDescriptors expands entries into the model ids a workflow answers to.
func ListModelDescriptors(entries []*Entry, loaderOptions LoaderOptions) []*ModelDescriptor {
	var descriptors []*ModelDescriptor
	for _, entry := range entries {
		defaultFile := BaseModelDefault(entry)
		defaultPresent := entry.BaseModels == nil || defaultFile == "" ||
			filePresent(entry.BaseModels, loaderOptions, defaultFile)
		if defaultPresent {
			descriptors = append(descriptors, &ModelDescriptor{
				ModelID: entry.Name, BaseID: entry.ID, Entry: entry, DefaultFile: defaultFile,
			})
		}
		for _, variant := range ListBaseModels(entry, loaderOptions) {
			if variant.File == defaultFile {
				continue
			}
			if entry.BaseModels != nil && !filePresent(entry.BaseModels, loaderOptions, variant.File) {
				continue
			}
			descriptors = append(descriptors, &ModelDescriptor{
				ModelID:     ModelIDFor(entry, variant),
				BaseID:      entry.ID,
				Entry:       entry,
				Variant:     variant,
				DefaultFile: defaultFile,
			})
		}
	}
	return descriptors
}

// optionsFor returns the base-model files a workflow may switch between.
func optionsFor(entry *Entry, config *BaseModelConfig, loaderOptions LoaderOptions) []string {
	if len(config.Variants) > 0 {
		files := make([]string, 0, len(config.Variants))
		for _, item := range config.Variants {
			if item.File != "" {
				files = append(files, item.File)
			}
		}
		return files
	}
	var files []string
	for _, file := range loaderFiles(config, loaderOptions) {
		if accepted(file, config) {
			files = append(files, file)
		}
	}
	return files
}

// DecorateParameters injects the base-model selector into the parameter list.
func DecorateParameters(entry *Entry, variant *BaseModelVariant, loaderOptions LoaderOptions) []any {
	parameters := make([]any, 0, len(entry.Parameters)+1)
	for _, item := range entry.Parameters {
		if object, ok := item.(map[string]any); ok {
			copied := make(map[string]any, len(object))
			for key, value := range object {
				copied[key] = value
			}
			parameters = append(parameters, copied)
			continue
		}
		parameters = append(parameters, item)
	}

	config := entry.BaseModels
	if config == nil {
		return parameters
	}
	options := optionsFor(entry, config, loaderOptions)
	defaultFile := BaseModelDefault(entry)
	if variant != nil {
		defaultFile = variant.File
	}

	for _, item := range parameters {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if object["key"] != config.Param {
			continue
		}
		object["type"] = "select"
		object["options"] = options
		if _, exists := object["default"]; !exists {
			object["default"] = defaultFile
		}
		return parameters
	}

	label := config.Label
	if label == "" {
		label = "Base model"
	}
	selector := map[string]any{
		"key":         config.Param,
		"label":       label,
		"type":        "select",
		"options":     options,
		"default":     defaultFile,
		"description": "Switch the base model for this workflow; the CLIP and VAE must match it.",
	}
	return append([]any{selector}, parameters...)
}

// ResolveBaseModelVariant matches an incoming model id against a workflow's variants.
func ResolveBaseModelVariant(entry *Entry, modelID string, loaderOptions LoaderOptions) *BaseModelVariant {
	if entry.BaseModels == nil {
		return nil
	}
	requested := strings.ToLower(modelID)
	for _, variant := range ListBaseModels(entry, loaderOptions) {
		id := ModelIDFor(entry, variant)
		if strings.ToLower(id) == requested ||
			strings.ToLower(entry.Name+"__"+variant.ID) == requested {
			variant.ModelID = id
			return variant
		}
	}
	return nil
}

// DecorateEntry returns a request-scoped copy carrying variant-specific fields.
func DecorateEntry(entry *Entry, variant *BaseModelVariant, loaderOptions LoaderOptions) *Entry {
	parameters := DecorateParameters(entry, variant, loaderOptions)

	decorated := *entry
	decorated.Parameters = parameters
	if variant == nil {
		return &decorated
	}

	decorated.Name = variant.ModelID
	decorated.BaseModel = variant.File
	decorated.BaseModelLabel = variant.Label
	params := map[string]any{}
	if entry.BaseModels != nil {
		params[entry.BaseModels.Param] = variant.File
	}
	for key, value := range variant.Params {
		params[key] = value
	}
	decorated.BaseModelParams = params
	return &decorated
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
