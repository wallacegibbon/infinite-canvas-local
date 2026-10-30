package workflow

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// registryFileName is the single index file inside the workflow directory.
const registryFileName = "registry.json"

// Resolved pairs an entry with the base-model variant it was resolved to.
type Resolved struct {
	Entry   *Entry
	Variant *BaseModelVariant
}

// Registry is the in-memory view of registry.json plus the graphs on disk.
type Registry struct {
	dir              string
	registryPath     string
	maxWorkflowBytes int64
	logger           *logx.Logger
	mainModelFile    func(workflowID string) string

	loadMu sync.Mutex
	mu     sync.RWMutex
	cache  map[string]*Entry
	byName map[string]*Entry
	order  []string
	sig    string
	loaded bool
}

// RegistryOptions configures a Registry.
type RegistryOptions struct {
	Dir              string
	MaxWorkflowBytes int64
	Logger           *logx.Logger
	MainModelFileFor func(workflowID string) string
}

// NewRegistry builds a registry. Call Load before use.
func NewRegistry(opts RegistryOptions) *Registry {
	maxBytes := opts.MaxWorkflowBytes
	if maxBytes <= 0 {
		maxBytes = 5 * 1024 * 1024
	}
	mainModelFile := opts.MainModelFileFor
	if mainModelFile == nil {
		mainModelFile = func(string) string { return "" }
	}
	return &Registry{
		dir:              opts.Dir,
		registryPath:     filepath.Join(opts.Dir, registryFileName),
		maxWorkflowBytes: maxBytes,
		logger:           opts.Logger,
		mainModelFile:    mainModelFile,
		cache:            map[string]*Entry{},
		byName:           map[string]*Entry{},
	}
}

// Dir returns the workflow directory.
func (r *Registry) Dir() string { return r.dir }

// Load reads registry.json and every referenced graph.
func (r *Registry) Load() error {
	r.loadMu.Lock()
	defer r.loadMu.Unlock()
	return r.loadLocked()
}

func (r *Registry) loadLocked() error {
	rawEntries, err := r.readRegistryRaw()
	if err != nil {
		return err
	}

	cache := map[string]*Entry{}
	byName := map[string]*Entry{}
	var order []string

	for _, raw := range rawEntries {
		var header struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			File string `json:"file"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			r.logger.Warn("failed to load a workflow (unknown): %s", err)
			continue
		}
		if header.File == "" {
			r.logger.Warn("failed to load workflow %s: missing file field", header.ID)
			continue
		}

		entry, err := r.buildEntry(raw, header.File)
		if err != nil {
			r.logger.Warn("failed to load workflow %s: %s", header.File, err)
			continue
		}
		cache[entry.ID] = entry
		byName[entry.Name] = entry
		if entry.ID != entry.Name {
			byName[entry.ID] = entry
		}
		order = append(order, entry.ID)
	}

	signature := r.computeSignature(rawEntries)

	r.mu.Lock()
	r.cache = cache
	r.byName = byName
	r.order = order
	r.sig = signature
	r.loaded = true
	r.mu.Unlock()
	return nil
}

// buildEntry turns one raw registry object into an Entry with its graph loaded.
func (r *Registry) buildEntry(raw json.RawMessage, file string) (*Entry, error) {
	var decoded rawEntry
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}

	filePath := file
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(r.dir, file)
	}
	graph, err := r.readGraph(filePath)
	if err != nil {
		return nil, err
	}
	if graph == nil {
		r.logger.Warn("skipping UI-format workflow %s; export it from ComfyUI in API format instead.", file)
		return nil, fmt.Errorf("not an API workflow")
	}

	workflowType := InferType(graph, decoded.Type)
	id := decoded.ID
	if id == "" {
		id = util.Slugify(decoded.Name, "workflow-"+fmt.Sprint(time.Now().UnixMilli()))
	}
	name := EnsureTypePrefix(defaultString(decoded.Name, id), workflowType)

	entry := &Entry{
		ID:               id,
		Name:             name,
		Type:             workflowType,
		File:             file,
		FilePath:         filePath,
		Label:            decoded.Label,
		Description:      decoded.Description,
		Category:         decoded.Category,
		Hidden:           decoded.Hidden,
		MinMedia:         decoded.MinMedia,
		BackgroundPrompt: decoded.BackgroundPrompt,
		Workflow:         graph,
		Defaults:         decoded.Defaults,
		Presets:          decoded.Presets,
		Parameters:       decoded.Parameters,
		Lora:             decoded.Lora,
		BaseModels:       decoded.BaseModels,
		OutputSize:       decoded.OutputSize,
		VariantLabels:    decoded.VariantLabels,
		ImportedAt:       decoded.ImportedAt,
		Source:           decoded.Source,
	}
	if entry.Defaults == nil {
		entry.Defaults = map[string]any{}
	}
	if entry.Presets == nil {
		entry.Presets = map[string]any{}
	}
	if entry.Parameters == nil {
		entry.Parameters = []any{}
	}

	// An authored binding set wins; otherwise the engine infers one.
	if decoded.Bindings.IsZero() {
		entry.RawBindings = InferBindings(graph)
	} else {
		entry.RawBindings = decoded.Bindings
	}

	if decoded.Variants.Len() > 0 {
		entry.Variants = OrderedMap[*Variant]{Values: map[string]*Variant{}}
		for _, mode := range decoded.Variants.Keys {
			variant := decoded.Variants.Values[mode]
			if variant == nil || variant.File == "" {
				continue
			}
			variantPath := variant.File
			if !filepath.IsAbs(variantPath) {
				variantPath = filepath.Join(r.dir, variantPath)
			}
			variantGraph, err := r.readGraph(variantPath)
			if err != nil || variantGraph == nil {
				r.logger.Warn("skipping invalid variant %s: %s", mode, variant.File)
				continue
			}
			if variant.RawBindings.IsZero() {
				variant.RawBindings = entry.RawBindings
			}
			if variant.Defaults == nil {
				variant.Defaults = entry.Defaults
			}
			variant.Workflow = variantGraph
			entry.Variants.Keys = append(entry.Variants.Keys, mode)
			entry.Variants.Values[mode] = variant
		}
	}

	return entry, nil
}

// readGraph loads a graph, returning nil for UI-format or invalid documents.
func (r *Registry) readGraph(path string) (Graph, error) {
	data, err := readFileLimited(path, r.maxWorkflowBytes)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	if IsUIWorkflow(decoded) {
		return nil, nil
	}
	if !IsAPIWorkflow(decoded) {
		return nil, fmt.Errorf("not a valid ComfyUI API-format workflow")
	}
	graph, _ := decoded.(map[string]any)
	return Graph(graph), nil
}

// readRegistryRaw returns the raw workflow objects, preserving untouched fields.
func (r *Registry) readRegistryRaw() ([]json.RawMessage, error) {
	if _, err := os.Stat(r.registryPath); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		payload, _ := json.MarshalIndent(map[string]any{"version": 1, "workflows": []any{}}, "", "  ")
		if err := util.WriteFileAtomic(r.registryPath, append(payload, '\n')); err != nil {
			return nil, err
		}
	}
	data, err := readFileLimited(r.registryPath, r.maxWorkflowBytes)
	if err != nil {
		return nil, err
	}
	var document struct {
		Workflows []json.RawMessage `json:"workflows"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return document.Workflows, nil
}

// computeSignature fingerprints the registry and every referenced graph so that
// reloadIfChanged can detect edits without parsing everything.
func (r *Registry) computeSignature(rawEntries []json.RawMessage) string {
	var parts []string
	if info, err := os.Stat(r.registryPath); err == nil {
		parts = append(parts, fmt.Sprintf("registry:%d:%d", info.ModTime().UnixMilli(), info.Size()))
	} else {
		parts = append(parts, "registry:missing")
	}
	for _, raw := range rawEntries {
		var decoded rawEntry
		if err := json.Unmarshal(raw, &decoded); err != nil {
			continue
		}
		files := []string{decoded.File}
		for _, mode := range decoded.Variants.Keys {
			if variant := decoded.Variants.Values[mode]; variant != nil && variant.File != "" {
				files = append(files, variant.File)
			}
		}
		for _, file := range files {
			if file == "" {
				continue
			}
			path := file
			if !filepath.IsAbs(path) {
				path = filepath.Join(r.dir, path)
			}
			if info, err := os.Stat(path); err == nil {
				parts = append(parts, fmt.Sprintf("%s:%d:%d", path, info.ModTime().UnixMilli(), info.Size()))
			} else {
				parts = append(parts, path+":missing")
			}
		}
	}
	return strings.Join(parts, "|")
}

// ReloadIfChanged re-reads the registry when files changed underneath us, which is
// how a user's manual edit to registry.json takes effect without a restart.
func (r *Registry) ReloadIfChanged() {
	r.mu.RLock()
	loaded := r.loaded
	r.mu.RUnlock()
	if !loaded {
		_ = r.Load()
		return
	}

	rawEntries, err := r.readRegistryRaw()
	if err != nil {
		r.logger.Warn("registry reload check failed: %s", err)
		return
	}
	next := r.computeSignature(rawEntries)
	r.mu.RLock()
	current := r.sig
	r.mu.RUnlock()
	if next != current {
		if err := r.Load(); err != nil {
			r.logger.Warn("registry reload failed: %s", err)
		}
	}
}

// Entries returns every loaded entry in registry file order.
func (r *Registry) Entries() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.order))
	for _, id := range r.order {
		if entry, ok := r.cache[id]; ok {
			out = append(out, entry)
		}
	}
	return out
}

// Len returns the number of loaded entries.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}

// List returns the public projections, sorted by name.
func (r *Registry) List() []PublicEntry {
	entries := r.Entries()
	out := make([]PublicEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, PublicProjection(entry))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Resolve finds an entry by id or by name, case-insensitively as a last resort.
func (r *Registry) Resolve(idOrName string) *Entry {
	if idOrName == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if entry, ok := r.cache[idOrName]; ok {
		return entry
	}
	if entry, ok := r.byName[idOrName]; ok {
		return entry
	}
	lowered := strings.ToLower(idOrName)
	for _, id := range r.order {
		if entry, ok := r.cache[id]; ok && strings.ToLower(entry.Name) == lowered {
			return entry
		}
	}
	return nil
}

// ResolveForMode swaps in a variant's graph and bindings.
func (r *Registry) ResolveForMode(entry *Entry, mode string) *Entry {
	if entry == nil || mode == "" {
		return entry
	}
	variant, ok := entry.Variants.Get(mode)
	if !ok || variant == nil {
		return entry
	}
	merged := *entry
	if variant.Workflow != nil {
		merged.Workflow = variant.Workflow
	}
	if !variant.RawBindings.IsZero() {
		merged.RawBindings = variant.RawBindings
	}
	if variant.Defaults != nil {
		merged.Defaults = variant.Defaults
	}
	if variant.OutputSize != nil {
		merged.OutputSize = variant.OutputSize
	}
	return &merged
}

// decorateWithMainModel applies a per-workflow main-model override, if any.
func (r *Registry) decorateWithMainModel(entry *Entry, variant *BaseModelVariant, loaderOptions LoaderOptions) *Entry {
	decorated := DecorateEntry(entry, variant, loaderOptions)
	if variant != nil {
		return decorated
	}
	file := r.mainModelFile(entry.ID)
	if file == "" {
		return decorated
	}
	target := FindUnetLoader(entry.Workflow)
	if target == nil {
		return decorated
	}
	installed := false
	for _, candidate := range loaderOptions["UNETLoader"] {
		if candidate == file {
			installed = true
			break
		}
	}
	if !installed {
		return decorated
	}

	// If a binding already targets the loader, drive it through the parameter so
	// the request-scoped machinery stays in one place.
	for _, key := range entry.RawBindings.Keys {
		binding := NewBinding(entry.RawBindings.Values[key].Values)
		if binding.String(keyNode) != target.NodeID || binding.String(keyInput) != target.Input {
			continue
		}
		params := map[string]any{}
		for name, value := range decorated.BaseModelParams {
			params[name] = value
		}
		params[key] = file
		decorated.BaseModelParams = params
		return decorated
	}
	decorated.MainModelFile = file
	return decorated
}

// ListModels expands the registry into the model list served by /v1/models.
func (r *Registry) ListModels(loaderOptions LoaderOptions) []*ModelDescriptor {
	entries := r.Entries()
	var descriptors []*ModelDescriptor
	for _, descriptor := range ListModelDescriptors(entries, loaderOptions) {
		file := r.mainModelFile(descriptor.Entry.ID)
		if descriptor.Variant != nil && file != "" && descriptor.Variant.File == file {
			continue
		}
		copied := *descriptor
		copied.Entry = r.decorateWithMainModel(descriptor.Entry, descriptor.Variant, loaderOptions)
		descriptors = append(descriptors, &copied)
	}

	for _, entry := range entries {
		file := r.mainModelFile(entry.ID)
		if file == "" || FindUnetLoader(entry.Workflow) == nil {
			continue
		}
		present := false
		for _, candidate := range loaderOptions["UNETLoader"] {
			if candidate == file {
				present = true
				break
			}
		}
		if !present {
			continue
		}
		alreadyListed := false
		for _, descriptor := range descriptors {
			if descriptor.BaseID == entry.ID && descriptor.Variant == nil {
				alreadyListed = true
				break
			}
		}
		if alreadyListed {
			continue
		}
		descriptors = append(descriptors, &ModelDescriptor{
			ModelID:     entry.Name,
			BaseID:      entry.ID,
			Entry:       r.decorateWithMainModel(entry, nil, loaderOptions),
			Variant:     nil,
			DefaultFile: BaseModelDefault(entry),
		})
	}
	return descriptors
}

// ResolveModel maps an incoming model id onto an entry plus optional variant.
//
// A client may address a workflow by its base name, by an id, or by a
// "<workflow>__<variant>" id produced by /v1/models; all three must work.
func (r *Registry) ResolveModel(idOrName string, loaderOptions LoaderOptions) *Resolved {
	if direct := r.Resolve(idOrName); direct != nil {
		if override := r.mainModelFile(direct.ID); override != "" {
			for _, candidate := range loaderOptions["UNETLoader"] {
				if candidate == override {
					return &Resolved{Entry: r.decorateWithMainModel(direct, nil, loaderOptions)}
				}
			}
		}

		config := direct.BaseModels
		defaultFile := BaseModelDefault(direct)
		classType := "UNETLoader"
		if config != nil && config.ClassType != "" {
			classType = config.ClassType
		}
		files := loaderOptions[classType]
		// The authored default may be missing while a sibling variant is installed.
		if config != nil && defaultFile != "" && !containsString(files, defaultFile) {
			if variants := ListBaseModels(direct, loaderOptions); len(variants) > 0 {
				return &Resolved{
					Entry:   DecorateEntry(direct, variants[0], loaderOptions),
					Variant: variants[0],
				}
			}
		}
		return &Resolved{Entry: r.decorateWithMainModel(direct, nil, loaderOptions)}
	}

	for _, entry := range r.Entries() {
		if variant := ResolveBaseModelVariant(entry, idOrName, loaderOptions); variant != nil {
			return &Resolved{Entry: DecorateEntry(entry, variant, loaderOptions), Variant: variant}
		}
	}
	return nil
}

// Schema renders the machine-readable parameter description of an entry.
func (r *Registry) Schema(entry *Entry) Schema {
	return BuildSchema(entry)
}

// RegisterGraphFile registers a graph file that is already in the workflow directory.
//
// The file name is the workflow's identity. It must be "<type>-<slug>.json", where <type> is
// one of image, video, audio or text: id, name and file are then the same string, nothing is
// copied, renamed or re-slugged, and the file a user edits is the file that runs.
//
// A file that does not follow the rule is skipped rather than repaired — silently renaming
// someone's file makes its name meaningless the next time they look at it — and the reason
// travels back to the scan, which reports it. The workflow set is fixed and hand-managed, so
// a rejected file is a naming mistake worth fixing, not a case to paper over.
func (r *Registry) RegisterGraphFile(absolute string) (string, []string, error) {
	base := filepath.Base(absolute)
	stem := strings.TrimSuffix(base, ".json")
	if err := validateGraphFileName(stem); err != nil {
		return "", nil, err
	}

	data, err := readFileLimited(absolute, r.maxWorkflowBytes)
	if err != nil {
		return "", nil, httpx.BadRequest("read_failed", "failed to read the workflow file: "+err.Error())
	}
	graph, err := decodeImportJSON(data)
	if err != nil {
		return "", nil, err
	}

	workflowType := InferType(graph, "")
	if declared, _ := typePrefixOf(stem); TypePrefixes[workflowType] != declared {
		return "", nil, httpx.NewError(http.StatusUnprocessableEntity, "workflow_type_mismatch",
			fmt.Sprintf("the file name starts with %s- but the graph looks like %s; rename the file to %s-…",
				declared, workflowType, workflowType))
	}
	if r.hasID(stem) {
		return "", nil, httpx.NewError(http.StatusUnprocessableEntity, "workflow_id_taken",
			"another registry entry already uses this id and points at a different file")
	}

	merged := InferBindings(graph)
	warnings := OutputWarnings(graph)
	warnings = append(warnings, BindingWarnings(graph, merged)...)
	if _, ok := merged.Get("prompt"); !ok {
		warnings = append(warnings, "no prompt binding could be inferred; set one manually in registry.json.")
	}

	entryPayload := map[string]any{
		"id":         stem,
		"name":       stem,
		"type":       workflowType,
		"file":       base,
		"bindings":   merged,
		"defaults":   nonNilMap(nil),
		"presets":    nonNilMap(nil),
		"parameters": nonNilSlice(nil),
		"importedAt": util.NowIso(),
		"source":     &Source{Kind: "path", Path: absolute},
	}
	encodedEntry, err := json.Marshal(entryPayload)
	if err != nil {
		return "", nil, httpx.NewError(http.StatusInternalServerError, "internal_error", "failed to write registry.json")
	}

	rawEntries, err := r.readRegistryRaw()
	if err != nil {
		return "", nil, httpx.NewError(http.StatusInternalServerError, "internal_error", "failed to read registry.json")
	}
	next := make([]json.RawMessage, 0, len(rawEntries)+1)
	next = append(next, rawEntries...)
	next = append(next, encodedEntry)
	if err := r.writeRegistry(next); err != nil {
		return "", nil, err
	}
	if err := r.Load(); err != nil {
		return "", nil, err
	}
	r.logger.Info("registered workflow %s from %s", stem, base)
	return stem, warnings, nil
}

// typePrefixOf returns the capability a file name declares, by its leading "<type>-".
func typePrefixOf(stem string) (string, bool) {
	for _, candidate := range []string{"image", "video", "audio", "text"} {
		if strings.HasPrefix(stem, candidate+"-") {
			return candidate, true
		}
	}
	return "", false
}

// validateGraphFileName enforces "<type>-<slug>.json".
//
// The slug rule is util.Slugify's own: the id has to be usable as a URL path segment, and if
// the file name were allowed to differ from the slug the two would drift apart again.
func validateGraphFileName(stem string) error {
	if stem == "" || util.Slugify(stem, "") != stem {
		return httpx.NewError(http.StatusUnprocessableEntity, "workflow_name_invalid",
			"the file name must be lowercase letters, digits, '-' or '_' (got "+stem+")")
	}
	if _, ok := typePrefixOf(stem); !ok {
		return httpx.NewError(http.StatusUnprocessableEntity, "workflow_prefix_required",
			"the file name must start with its type: image-, video-, audio- or text- (for example image-my-flow.json)")
	}
	return nil
}

// decodeImportJSON validates that the payload is a submittable API graph.
func decodeImportJSON(data []byte) (Graph, error) {
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, httpx.BadRequest("invalid_json", "failed to parse JSON: "+err.Error())
	}
	if IsUIWorkflow(decoded) {
		return nil, httpx.NewError(http.StatusUnprocessableEntity, "ui_workflow_not_supported",
			"this is a ComfyUI UI-format workflow and cannot be submitted; export it from ComfyUI in API format and put that file in the workflows directory.")
	}
	if !IsAPIWorkflow(decoded) {
		return nil, httpx.NewError(http.StatusUnprocessableEntity, "invalid_api_workflow",
			"not a valid ComfyUI API-format workflow (top-level nodes need class_type + inputs).")
	}
	graph, _ := decoded.(map[string]any)
	return Graph(graph), nil
}

// ScanSummary reports what a scan added, skipped, or failed on.
type ScanSummary struct {
	Added   []string      `json:"added"`
	Skipped []ScanFailure `json:"skipped"`
	Failed  []ScanFailure `json:"failed"`
	// Warnings are per-file remarks that did not stop registration (no output node, no
	// prompt binding). Registering is not the same as being usable, so they are reported.
	Warnings []string `json:"warnings"`
}

// skipCodes are the reasons a scan leaves a graph file alone on purpose. They are reported
// as "skipped" because the file needs a rename or an export, not an investigation.
var skipCodes = map[string]bool{
	"ui_workflow_not_supported": true,
	"workflow_name_invalid":     true,
	"workflow_prefix_required":  true,
	"workflow_type_mismatch":    true,
	"workflow_id_taken":         true,
}

// ScanFailure describes one file a scan could not import.
type ScanFailure struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Scan imports any unregistered *.json graph sitting in the workflow directory.
func (r *Registry) Scan() (*ScanSummary, error) {
	rawEntries, err := r.readRegistryRaw()
	if err != nil {
		return nil, err
	}

	usedFiles := map[string]bool{}
	usedSources := map[string]bool{}
	for _, raw := range rawEntries {
		var decoded rawEntry
		if err := json.Unmarshal(raw, &decoded); err != nil {
			continue
		}
		if decoded.File != "" {
			usedFiles[decoded.File] = true
		}
		if decoded.Source != nil && decoded.Source.Path != "" {
			usedSources[resolveOrJoin(r.dir, decoded.Source.Path)] = true
		}
		for _, mode := range decoded.Variants.Keys {
			variant := decoded.Variants.Values[mode]
			if variant == nil {
				continue
			}
			if variant.File != "" {
				usedFiles[variant.File] = true
			}
			if variant.Source != nil && variant.Source.Path != "" {
				usedSources[resolveOrJoin(r.dir, variant.Source.Path)] = true
			}
		}
	}

	dirEntries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, err
	}
	summary := &ScanSummary{Added: []string{}, Skipped: []ScanFailure{}, Failed: []ScanFailure{}, Warnings: []string{}}
	for _, dirEntry := range dirEntries {
		name := dirEntry.Name()
		if dirEntry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".json") || name == registryFileName {
			continue
		}
		if usedFiles[name] {
			continue
		}
		absolute := filepath.Join(r.dir, name)
		if usedSources[absolute] {
			continue
		}
		registered, warnings, err := r.RegisterGraphFile(absolute)
		if err != nil {
			var appErr *httpx.Error
			if ok := asHTTPError(err, &appErr); ok && skipCodes[appErr.Code] {
				summary.Skipped = append(summary.Skipped, ScanFailure{File: name, Reason: appErr.Message})
			} else {
				summary.Failed = append(summary.Failed, ScanFailure{File: name, Reason: err.Error()})
			}
			continue
		}
		summary.Added = append(summary.Added, registered)
		for _, warning := range warnings {
			summary.Warnings = append(summary.Warnings, name+": "+warning)
		}
	}
	return summary, nil
}

// Remove deletes a workflow from the registry. The graph file is left on disk so
// that a scan can re-import it, matching the old behaviour.
func (r *Registry) Remove(id string) (bool, error) {
	rawEntries, err := r.readRegistryRaw()
	if err != nil {
		return false, err
	}
	next := make([]json.RawMessage, 0, len(rawEntries))
	removed := false
	for _, raw := range rawEntries {
		var header struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &header); err == nil && header.ID == id {
			removed = true
			continue
		}
		next = append(next, raw)
	}
	if !removed {
		return false, nil
	}
	if err := r.writeRegistry(next); err != nil {
		return false, err
	}
	return true, r.Load()
}

// writeRegistry persists the workflow list atomically.
func (r *Registry) writeRegistry(entries []json.RawMessage) error {
	var buffer strings.Builder
	buffer.WriteString("{\n  \"version\": 1,\n  \"workflows\": [\n")
	for i, raw := range entries {
		var indented any
		if err := json.Unmarshal(raw, &indented); err != nil {
			continue
		}
		encoded, err := json.MarshalIndent(indented, "    ", "  ")
		if err != nil {
			continue
		}
		buffer.WriteString("    ")
		buffer.Write(encoded)
		if i < len(entries)-1 {
			buffer.WriteString(",")
		}
		buffer.WriteString("\n")
	}
	buffer.WriteString("  ]\n}\n")
	return util.WriteFileAtomic(r.registryPath, []byte(buffer.String()))
}

func (r *Registry) hasID(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.cache[id]
	return exists
}

// mergeBindings overlays explicit bindings on top of inferred ones, keeping the
// inferred ordering for keys the author did not touch.
func mergeBindings(inferred, override OrderedMap[OrderedMap[any]]) OrderedMap[OrderedMap[any]] {
	out := OrderedMap[OrderedMap[any]]{Values: map[string]OrderedMap[any]{}}
	seen := map[string]bool{}
	for _, key := range inferred.Keys {
		out.Keys = append(out.Keys, key)
		out.Values[key] = inferred.Values[key]
		seen[key] = true
	}
	for _, key := range override.Keys {
		if !seen[key] {
			out.Keys = append(out.Keys, key)
		}
		out.Values[key] = override.Values[key]
	}
	return out
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nonNilSlice(value []any) []any {
	if value == nil {
		return []any{}
	}
	return value
}

// readFileLimited reads a file, refusing anything larger than maxBytes.
func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a file: %s", path)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file too large: %d bytes", info.Size())
	}
	return os.ReadFile(path)
}

// ReadGraphFile loads a graph from disk with the registry's size ceiling.
func ReadGraphFile(path string, maxBytes int64) (Graph, error) {
	data, err := readFileLimited(path, maxBytes)
	if err != nil {
		return nil, err
	}
	return DecodeGraph(data)
}
