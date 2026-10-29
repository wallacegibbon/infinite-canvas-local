package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// loaderInfo is the resolved option set for one loader node class.
type loaderInfo struct {
	input   string
	options map[string]bool
}

// Scan refreshes the whole availability view from ComfyUI.
//
// Only one scan runs at a time: the canvas can trigger a manual scan while the
// startup scan is still going, and two concurrent scans would fight over the
// registry while it is being rewritten.
func (m *Manager) Scan() (Status, error) {
	m.scanMu.Lock()
	defer m.scanMu.Unlock()

	started := time.Now()
	m.mu.Lock()
	m.scanning = true
	m.status.Phase = "scanning"
	scanStarted := util.NowIso()
	m.status.ScanStartedAt = &scanStarted
	m.status.ScannedAt = nil
	m.status.LastError = nil
	m.mu.Unlock()
	m.persist()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// No ComfyUI client means nothing to scan (a partially wired manager, or a test).
	if m.comfy == nil {
		return m.degrade(errors.New("no ComfyUI client configured"), started)
	}
	if _, err := m.comfy.SystemStats(ctx); err != nil {
		return m.degrade(err, started)
	}
	objectInfo, err := m.comfy.ObjectInfo(ctx)
	if err != nil {
		return m.degrade(err, started)
	}
	folderPaths := m.comfy.FolderPaths(ctx)

	classes := map[string]bool{}
	for name := range objectInfo {
		classes[name] = true
	}
	loaders, catalog := collectLoaderOptions(objectInfo)

	m.mu.Lock()
	m.nodeClasses = classes
	m.loaderCatalog = catalog
	m.folderPathCatalog = folderPaths
	m.rebuildAutoAliasesLocked()
	loras := sortedKeysOfSet(catalog["LoraLoaderModelOnly"])
	if len(loras) == 0 {
		loras = sortedKeysOfSet(catalog["LoraLoader"])
	}
	m.status.Loras = loras
	m.status.ComfyReachable = true
	m.mu.Unlock()

	gatewayScan, err := m.registry.Scan()
	if err != nil {
		m.logger.Warn("failed to scan gateway workflows: %s", err)
	}
	comfyScan := m.importComfyWorkflows()
	m.registry.ReloadIfChanged()

	entries := make([]*workflow.Entry, 0, m.registry.Len())
	for _, projected := range m.registry.List() {
		if entry := m.registry.Resolve(projected.ID); entry != nil {
			entries = append(entries, entry)
		}
	}

	availability := map[string]*Availability{}
	workflows := make([]WorkflowState, 0, len(entries))
	missingModels := map[string]bool{}
	missingNodes := map[string]bool{}

	for _, entry := range entries {
		graphs := []workflow.Graph{entry.Workflow}
		for _, mode := range entry.Variants.Keys {
			if variant := entry.Variants.Values[mode]; variant != nil && variant.Workflow != nil {
				graphs = append(graphs, variant.Workflow)
			}
		}

		var problems []Problem
		seen := map[string]bool{}
		for _, graph := range graphs {
			for _, problem := range m.validateWorkflow(graph, classes, loaders, entry) {
				key := problem.Type + ":" + problem.Message
				if seen[key] {
					continue
				}
				seen[key] = true
				problems = append(problems, problem)
			}
		}
		if problems == nil {
			problems = []Problem{}
		}

		available := len(problems) == 0
		availability[entry.ID] = &Availability{Available: available, Problems: problems}

		// Hidden helper workflows still get validated, so tooling endpoints cannot
		// advertise an upscaler whose node is not installed. They just do not count
		// toward the totals the user sees.
		if entry.Hidden {
			continue
		}
		for _, problem := range problems {
			switch problem.Type {
			case "model":
				missingModels[problem.Message] = true
			case "node":
				missingNodes[problem.Message] = true
			}
		}
		workflows = append(workflows, WorkflowState{
			ID:        entry.ID,
			Name:      entry.Name,
			Type:      entry.Type,
			Available: available,
			Problems:  problems,
		})
	}

	counts := Counts{Total: len(workflows)}
	for _, item := range workflows {
		if item.Available {
			counts.Available++
		} else {
			counts.Unavailable++
		}
	}

	scannedAt := util.NowIso()
	m.mu.Lock()
	m.available = availability
	m.status.Phase = "ready"
	m.status.ScannedAt = &scannedAt
	m.status.ScanDurationMs = time.Since(started).Milliseconds()
	m.status.Counts = counts
	m.status.Workflows = workflows
	m.status.Missing = Missing{
		Models:      sortedKeysOfSet(missingModels),
		Nodes:       sortedKeysOfSet(missingNodes),
		UIWorkflows: comfyScan.ui,
		UISamples:   comfyScan.samples,
	}
	m.hasScanned = true
	m.scanning = false
	m.mu.Unlock()

	m.persist()
	m.logger.Info("setup scan ready: %d/%d workflows, %d UI workflow(s) skipped, %dms",
		counts.Available, counts.Total, comfyScan.ui, time.Since(started).Milliseconds())
	if len(gatewayScan.Added) > 0 || comfyScan.imported > 0 {
		m.logger.Info("auto-imported workflows: gateway=%d, comfy=%d", len(gatewayScan.Added), comfyScan.imported)
	}
	return m.Status(), nil
}

// degrade records a failed scan without discarding the last good availability data.
func (m *Manager) degrade(cause error, started time.Time) (Status, error) {
	scannedAt := util.NowIso()
	message := util.Truncate(cause.Error(), 500)

	m.mu.Lock()
	m.status.Phase = "degraded"
	m.status.ComfyReachable = false
	m.status.LastError = &message
	m.status.ScanDurationMs = time.Since(started).Milliseconds()
	m.status.ScannedAt = &scannedAt
	m.scanning = false
	m.mu.Unlock()

	m.persist()
	m.logger.Warn("setup scan degraded: %s", message)
	return m.Status(), nil
}

// persist writes the scan result so the canvas can show it before the first scan
// of the next boot completes.
func (m *Manager) persist() {
	m.mu.Lock()
	snapshot := m.status
	m.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		m.logger.Warn("failed to serialise the setup state: %s", err)
		return
	}
	if err := util.WriteFileAtomic(m.statusFile, append(data, '\n')); err != nil {
		m.logger.Warn("failed to save the setup state: %s", err)
	}
}

// collectLoaderOptions extracts the installed file list for every loader class.
//
// ComfyUI has two object_info shapes for a combo input: a bare array of options
// (older) and ["COMBO", {options: [...]}] (newer). Both must be understood, and
// optional inputs must be read as well as required ones.
func collectLoaderOptions(objectInfo map[string]any) (map[string]loaderInfo, map[string][]string) {
	loaders := map[string]loaderInfo{}
	catalog := map[string][]string{}

	for _, spec := range workflow.ModelLoaderSpecs {
		nodeInfo, _ := objectInfo[spec.ClassType].(map[string]any)
		inputInfo, _ := nodeInfo["input"].(map[string]any)
		required, _ := inputInfo["required"].(map[string]any)
		optional, _ := inputInfo["optional"].(map[string]any)

		definition, ok := required[spec.Input]
		if !ok {
			definition = optional[spec.Input]
		}
		list, _ := definition.([]any)

		var options []string
		if len(list) > 0 {
			if direct, ok := list[0].([]any); ok {
				options = toStringSlice(direct)
			}
		}
		if len(options) == 0 && len(list) > 1 {
			if second, ok := list[1].(map[string]any); ok {
				if nested, ok := second["options"].([]any); ok {
					options = toStringSlice(nested)
				}
			}
		}
		if options == nil {
			continue
		}
		set := make(map[string]bool, len(options))
		for _, option := range options {
			set[option] = true
		}
		loaders[spec.ClassType] = loaderInfo{input: spec.Input, options: set}
		sorted := append([]string{}, options...)
		sort.Strings(sorted)
		catalog[spec.ClassType] = sorted
	}
	return loaders, catalog
}

// rebuildAutoAliasesLocked indexes installed files by basename.
//
// Files with the same basename in different subdirectories are the same model and can
// be resolved automatically. Ambiguous names are skipped, leaving the decision to the
// user instead of guessing.
func (m *Manager) rebuildAutoAliasesLocked() {
	auto := map[string]map[string]string{}
	for _, spec := range workflow.ModelLoaderSpecs {
		options := m.loaderCatalog[spec.ClassType]
		if len(options) == 0 {
			continue
		}
		byBase := map[string][]string{}
		for _, option := range options {
			if !isModelOption(option) {
				continue
			}
			base := normalizeBaseName(option)
			byBase[base] = append(byBase[base], option)
		}
		resolved := map[string]string{}
		for base, list := range byBase {
			if len(list) == 1 {
				resolved[base] = list[0]
			}
		}
		auto[spec.ClassType] = resolved
	}
	m.auto = auto
}

// validateWorkflow checks every node class and model file a graph references.
func (m *Manager) validateWorkflow(graph workflow.Graph, classes map[string]bool, loaders map[string]loaderInfo, entry *workflow.Entry) []Problem {
	var problems []Problem

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, id := range graph.IDs() {
		node, ok := graph.NodeAt(id)
		if !ok {
			continue
		}
		classType := node.ClassType()
		if classType == "" || !classes[classType] {
			problems = append(problems, Problem{
				Type:    "node",
				Message: "missing node " + defaultIfEmpty(classType, "?") + " (node " + id + "）",
			})
			continue
		}
		loader, ok := loaders[classType]
		if !ok {
			continue
		}
		value, ok := node.Inputs()[loader.input].(string)
		if !ok || value == "" || value == "none" {
			// "none" is ComfyUI's sentinel for an explicitly disabled input
			// (tiny_vae, upscaler_model); it is not a missing file.
			continue
		}
		if loader.options[value] {
			continue
		}
		override := m.mainModelFile(entry.ID)
		if override != "" && classType == "UNETLoader" && loader.input == "unet_name" && loader.options[override] {
			continue
		}
		if m.hasBaseModelAlternativeLocked(entry, classType, loader.input, loader.options) {
			continue
		}
		if m.modelAliasForLocked(classType, loader.input, value) != "" {
			continue
		}
		problems = append(problems, Problem{
			Type:       "model",
			Message:    "missing model " + classType + "." + loader.input + "=" + value,
			ClassType:  classType,
			Input:      loader.input,
			Value:      value,
			Candidates: m.modelCandidatesLocked(classType, value),
		})
	}
	return problems
}

// hasBaseModelAlternativeLocked reports whether a workflow can fall back to another
// installed base model, in which case a missing authored default is not fatal.
func (m *Manager) hasBaseModelAlternativeLocked(entry *workflow.Entry, classType, input string, options map[string]bool) bool {
	config := entry.BaseModels
	if config == nil {
		return false
	}
	if defaultIfEmpty(config.ClassType, "UNETLoader") != classType {
		return false
	}
	if defaultIfEmpty(config.Input, "unet_name") != input {
		return false
	}
	for file := range options {
		if !acceptedByConfig(file, config) {
			continue
		}
		return true
	}
	return false
}

// acceptedByConfig applies a base-model config's filter/exclude pair.
func acceptedByConfig(file string, config *workflow.BaseModelConfig) bool {
	if config.Filter != "" {
		compiled, err := regexp.Compile("(?i)" + config.Filter)
		if err != nil || !compiled.MatchString(file) {
			return false
		}
	}
	if config.Exclude != "" {
		compiled, err := regexp.Compile("(?i)" + config.Exclude)
		if err == nil && compiled.MatchString(file) {
			return false
		}
	}
	return true
}

// modelAliasForLocked is the alias resolver without taking the lock again.
func (m *Manager) modelAliasForLocked(classType, input, value string) string {
	if value == "" {
		return ""
	}
	if user, ok := m.aliases[aliasKey(classType, input, value)]; ok && user != "" {
		if actual := m.findInstalledOptionLocked(classType, user); actual != "" {
			return actual
		}
		if m.aliasTargetExistsLocked(classType, user) {
			return util.NormalizeSlashes(user)
		}
	}
	if byBase, ok := m.auto[classType]; ok {
		if auto, ok := byBase[normalizeBaseName(value)]; ok {
			if util.NormalizeSlashes(auto) != util.NormalizeSlashes(value) {
				return auto
			}
		}
	}
	return ""
}

// comfyImportResult reports what importComfyWorkflows did.
type comfyImportResult struct {
	imported int
	ui       int
	samples  []string
}

// importComfyWorkflows pulls API-format graphs out of ComfyUI's own workflow folder.
func (m *Manager) importComfyWorkflows() comfyImportResult {
	result := comfyImportResult{samples: []string{}}
	if m.cfg.ComfyWorkflowsDir == "" || !util.DirExists(m.cfg.ComfyWorkflowsDir) {
		return result
	}

	importedNames := map[string]bool{}
	for _, projected := range m.registry.List() {
		entry := m.registry.Resolve(projected.ID)
		if entry == nil {
			continue
		}
		if entry.Source != nil && entry.Source.Path != "" {
			path := entry.Source.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(m.cfg.ComfyWorkflowsDir, path)
			}
			m.importedComfy[path] = true
		}
		if entry.Source != nil && entry.Source.Kind == "comfyui" && entry.Source.Name != "" {
			importedNames[filepath.Base(entry.Source.Name)] = true
		}
	}

	entries, err := os.ReadDir(m.cfg.ComfyWorkflowsDir)
	if err != nil {
		return result
	}

	for _, dirEntry := range entries {
		name := dirEntry.Name()
		if dirEntry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if importedNames[filepath.Base(name)] {
			continue
		}
		file := filepath.Join(m.cfg.ComfyWorkflowsDir, name)
		info, statErr := dirEntry.Info()
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		signature := info.ModTime().String() + ":" + strconv.FormatInt(info.Size(), 10)

		m.mu.Lock()
		cached, hasCache := m.comfyFileCache[file]
		m.mu.Unlock()
		if hasCache && cached.signature == signature {
			if cached.kind == "ui" {
				result.ui++
			}
			continue
		}

		if info.Size() > m.cfg.MaxWorkflowBytes {
			m.cacheComfyFile(file, signature, "too_large")
			continue
		}
		data, readErr := os.ReadFile(file)
		if readErr != nil {
			m.cacheComfyFile(file, signature, "error")
			m.logger.Warn("failed to read ComfyUI workflow %s: %s", name, readErr)
			continue
		}

		var decoded any
		if err := json.Unmarshal(stripBOMBytes(data), &decoded); err != nil {
			m.cacheComfyFile(file, signature, "invalid")
			continue
		}
		if workflow.IsUIWorkflow(decoded) {
			m.cacheComfyFile(file, signature, "ui")
			result.ui++
			if len(result.samples) < 5 {
				result.samples = append(result.samples, name)
			}
			continue
		}
		if !workflow.IsAPIWorkflow(decoded) {
			m.cacheComfyFile(file, signature, "invalid")
			continue
		}
		m.cacheComfyFile(file, signature, "api")

		m.mu.Lock()
		alreadyImported := m.importedComfy[file]
		m.mu.Unlock()
		if alreadyImported {
			continue
		}
		if _, err := m.registry.ImportWorkflow(workflow.ImportWorkflowRequest{
			Path: file,
			Name: strings.TrimSuffix(name, ".json"),
		}); err != nil {
			m.logger.Warn("failed to auto-import ComfyUI workflow %s: %s", name, err)
			continue
		}
		m.mu.Lock()
		m.importedComfy[file] = true
		m.mu.Unlock()
		result.imported++
	}
	return result
}

func (m *Manager) cacheComfyFile(file, signature, kind string) {
	m.mu.Lock()
	m.comfyFileCache[file] = comfyFileEntry{signature: signature, kind: kind}
	m.mu.Unlock()
}

func toStringSlice(items []any) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func sortedKeysOfSet(source any) []string {
	var out []string
	switch typed := source.(type) {
	case map[string]bool:
		out = make([]string, 0, len(typed))
		for key := range typed {
			out = append(out, key)
		}
	case []string:
		out = append([]string{}, typed...)
	}
	sort.Strings(out)
	return out
}

func defaultIfEmpty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func stripBOMBytes(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}
