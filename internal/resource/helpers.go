package resource

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"comfyui-gateway/internal/setup"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// referencedFilesCache remembers which filenames a workflow graph mentions.
type referencedFilesCacheEntry struct {
	stamp string
	names map[string]bool
}

var (
	referencedMu    sync.Mutex
	referencedFiles = map[string]referencedFilesCacheEntry{}
)

// workflowReferencedFiles lists the model filenames a graph actually references.
//
// resources/index.json can still declare a bake artifact that the workflow no longer
// uses (for example after switching to a plain VAE loader). Filtering bake entries
// through this avoids reporting a phantom "missing" file forever.
func (m *Manager) workflowReferencedFiles(entry *workflow.Entry) map[string]bool {
	if entry == nil || entry.FilePath == "" {
		return nil
	}
	info, err := os.Stat(entry.FilePath)
	if err != nil {
		return nil
	}
	stamp := info.ModTime().String() + ":" + util.Itoa(info.Size())

	referencedMu.Lock()
	if cached, ok := referencedFiles[entry.FilePath]; ok && cached.stamp == stamp {
		referencedMu.Unlock()
		return cached.names
	}
	referencedMu.Unlock()

	names := map[string]bool{}
	for _, id := range entry.Workflow.IDs() {
		node, ok := entry.Workflow.NodeAt(id)
		if !ok {
			continue
		}
		for _, value := range node.Inputs() {
			if text, ok := value.(string); ok {
				names[filepath.Base(util.NormalizeSlashes(text))] = true
			}
		}
	}

	referencedMu.Lock()
	referencedFiles[entry.FilePath] = referencedFilesCacheEntry{stamp: stamp, names: names}
	referencedMu.Unlock()
	return names
}

// packReferencedFiles unions the referenced filenames of a pack's workflows.
func (m *Manager) packReferencedFiles(pack *Pack) map[string]bool {
	if pack.WorkflowDeps.Len() == 0 {
		return nil
	}
	union := map[string]bool{}
	for _, workflowID := range pack.WorkflowDeps.Keys {
		for name := range m.workflowReferencedFiles(m.registry.Resolve(workflowID)) {
			union[name] = true
		}
	}
	if len(union) == 0 {
		return nil
	}
	return union
}

// bakeStillReferenced reports whether a bake artifact is still used by the workflows.
func bakeStillReferenced(item *Item, resource *Resource, referenced map[string]bool) bool {
	if referenced == nil || resource == nil || item.Type != "bake" {
		return true
	}
	_, file := targetParts(resource.Target)
	if file == "" {
		return true
	}
	return referenced[file]
}

// mainModelInfo describes a pack's main-model selector.
func (m *Manager) mainModelInfo(pack *Pack, installed map[string]bool) *MainModel {
	seen := map[string]bool{}
	var workflowIDs []string
	for _, id := range pack.Workflows {
		if !seen[id] {
			seen[id] = true
			workflowIDs = append(workflowIDs, id)
		}
	}
	for _, id := range pack.WorkflowDeps.Keys {
		if !seen[id] {
			seen[id] = true
			workflowIDs = append(workflowIDs, id)
		}
	}
	if len(workflowIDs) == 0 {
		return nil
	}

	type resolved struct {
		id    string
		entry *workflow.Entry
	}
	var entries []resolved
	var files []string
	fileSeen := map[string]bool{}
	for _, id := range workflowIDs {
		entry := m.registry.Resolve(id)
		if entry == nil {
			continue
		}
		entries = append(entries, resolved{id: id, entry: entry})
		if loader := workflow.FindUnetLoader(entry.Workflow); loader != nil && loader.File != "" && !fileSeen[loader.File] {
			fileSeen[loader.File] = true
			files = append(files, loader.File)
		}
	}
	if len(files) == 0 {
		return nil
	}

	m.mu.Lock()
	index := m.index
	m.mu.Unlock()

	// A bake artifact is a merged checkpoint, not a swappable base model.
	bakeFiles := map[string]bool{}
	resourceByID := map[string]*Resource{}
	resourceByFile := map[string]*Resource{}
	if index != nil {
		for _, item := range index.Resources {
			resourceByID[item.ID] = item
			_, file := targetParts(item.Target)
			if item.Type == "bake" {
				bakeFiles[file] = true
			}
			if _, taken := resourceByFile[file]; !taken && file != "" {
				resourceByFile[file] = item
			}
		}
	}

	var overrideIDs []string
	for _, item := range entries {
		loader := workflow.FindUnetLoader(item.entry.Workflow)
		if loader == nil || bakeFiles[loader.File] {
			continue
		}
		overrideIDs = append(overrideIDs, item.id)
	}

	selected := ""
	if m.mainModels != nil {
		selected = m.mainModels.FileForPack(pack.ID)
	}
	selectedInstalled := selected != "" && installed[selected]

	familyFilter := ""
	for _, item := range entries {
		if item.entry.BaseModels != nil && item.entry.BaseModels.Filter != "" {
			familyFilter = item.entry.BaseModels.Filter
			break
		}
	}
	matchesFamily := func(file string) bool {
		if familyFilter == "" {
			return true
		}
		compiled, err := regexp.Compile("(?i)" + familyFilter)
		if err != nil {
			return true
		}
		return compiled.MatchString(file)
	}

	var options []MainModelOption
	optionIndex := map[string]int{}
	addOption := func(option MainModelOption) {
		if _, exists := optionIndex[option.File]; exists {
			return
		}
		optionIndex[option.File] = len(options)
		options = append(options, option)
	}

	for _, id := range pack.Resources {
		resource, ok := resourceByID[id]
		if !ok || resource.Type == "bake" {
			continue
		}
		dir, file := targetParts(resource.Target)
		if dir != "diffusion_models" || file == "" {
			continue
		}
		addOption(MainModelOption{
			File:       file,
			Label:      defaultString(resource.Label, file),
			Installed:  installed[file],
			Source:     "pack",
			Matches:    true,
			ResourceID: resource.ID,
		})
	}

	var installedNames []string
	for name := range installed {
		installedNames = append(installedNames, name)
	}
	sort.Strings(installedNames)
	for _, file := range installedNames {
		if _, exists := optionIndex[file]; exists || bakeFiles[file] {
			continue
		}
		addOption(MainModelOption{
			File:      file,
			Label:     stripExtension(file),
			Installed: true,
			Source:    "local",
			Matches:   matchesFamily(file),
		})
	}
	if selected != "" {
		if _, exists := optionIndex[selected]; !exists {
			addOption(MainModelOption{File: selected, Label: stripExtension(selected), Source: "local"})
		}
	}

	sort.SliceStable(options, func(i, j int) bool {
		if options[i].Source != options[j].Source {
			return options[i].Source == "pack"
		}
		return options[i].Label < options[j].Label
	})

	defaultFile := ""
	for _, file := range files {
		if !bakeFiles[file] {
			defaultFile = file
			break
		}
	}
	if defaultFile == "" {
		defaultFile = files[0]
	}

	defaultLabel := ""
	if len(files) > 1 {
		defaultLabel = "each workflow's own base model"
	} else if resource, ok := resourceByFile[files[0]]; ok && resource.Label != "" {
		defaultLabel = resource.Label
	} else {
		defaultLabel = stripExtension(files[0])
	}

	selectedLabel := ""
	if index, ok := optionIndex[selected]; ok {
		selectedLabel = options[index].Label
	}

	return &MainModel{
		WorkflowIDs:       nonNilStrings(overrideIDs),
		Files:             files,
		DefaultFile:       defaultFile,
		DefaultLabel:      defaultLabel,
		Selected:          selected,
		SelectedLabel:     selectedLabel,
		SelectedInstalled: selectedInstalled,
		Options:           options,
	}
}

// stripExtension returns a filename without its extension.
func stripExtension(file string) string {
	base := filepath.Base(util.NormalizeSlashes(file))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// SetResourceReplacement points a resource at an already installed file.
func (m *Manager) SetResourceReplacement(resourceID, file string) (*Status, error) {
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()

	resource := findResource(index, resourceID)
	if resource == nil {
		return nil, httpxBadRequest("resource_not_found", "resource not found")
	}
	dir, name := targetParts(resource.Target)
	classType, ok := loaderClassFor(dir)
	if !ok {
		return nil, httpxBadRequest("unsupported_resource", "this resource does not support replacement")
	}
	input := "unet_name"
	for _, spec := range workflow.ModelLoaderSpecs {
		if spec.ClassType == classType {
			input = spec.Input
			break
		}
	}

	trimmed := strings.TrimSpace(file)
	entry := map[string]any{"classType": classType, "input": input, "from": name}
	if trimmed != "" && util.NormalizeSlashes(trimmed) != util.NormalizeSlashes(name) {
		entry["to"] = trimmed
	}
	if _, err := m.setup.SetModelAlias(entry); err != nil {
		return nil, err
	}
	return m.Status(), nil
}

// PickResourceReplacement opens a file dialog for a resource's model directory.
func (m *Manager) PickResourceReplacement(resourceID string) (string, error) {
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()

	resource := findResource(index, resourceID)
	if resource == nil {
		return "", httpxBadRequest("resource_not_found", "resource not found")
	}
	dir, _ := targetParts(resource.Target)
	if _, ok := setup.ModelDirKeys[dir]; !ok {
		return "", httpxBadRequest("unsupported_resource", "this resource does not support replacement")
	}
	return m.setup.PickModelFile(dir)
}

// Refresh re-reads the index and returns the new status.
func (m *Manager) Refresh() (*Status, error) {
	m.loadIndex()
	m.mu.Lock()
	m.inventory = map[string]inventoryEntry{}
	m.mu.Unlock()

	// Availability lives in the setup manager; without a rescan the panel would show
	// fresh files but stale "unavailable" verdicts.
	if m.setup != nil {
		if _, err := m.setup.Scan(); err != nil {
			m.logger.Warn("refresh scan failed: %s", err)
		}
	}

	status := m.Status()
	m.logger.Info("resource status refreshed: source=%s, %d resources, %d packs, %d missing",
		status.IndexSource, status.Summary.AllResources, len(status.Packs), status.Summary.Missing)
	return status, nil
}

func findResource(index *Index, id string) *Resource {
	if index == nil {
		return nil
	}
	for _, resource := range index.Resources {
		if resource.ID == id {
			return resource
		}
	}
	return nil
}

// Index returns the loaded index for tests.
func (m *Manager) Index() *Index {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.index
}
