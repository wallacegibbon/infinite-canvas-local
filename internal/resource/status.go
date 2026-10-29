package resource

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"comfyui-gateway/internal/setup"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// matchResult is the outcome of looking for one resource on disk.
type matchResult struct {
	present   bool
	path      string
	rel       string
	size      int64
	matchedBy string
	mismatch  string
}

// inventoryFor lists every weight file under a model type's roots.
//
// Two independent sources are merged: the filesystem walk (which knows sizes and
// subdirectories) and ComfyUI's own loader list (which knows what ComfyUI will
// actually accept). A file can be present on disk yet invisible to ComfyUI, and the
// panel must not claim it is ready.
func (m *Manager) inventoryFor(dir, root string) []inventoryItem {
	roots := m.modelDirs(dir, root)
	key := strings.ToLower(dir + "|" + strings.Join(roots, "|"))

	m.mu.Lock()
	if cached, ok := m.inventory[key]; ok && time.Since(cached.at) < 5*time.Second {
		m.mu.Unlock()
		return cached.items
	}
	m.mu.Unlock()

	var items []inventoryItem
	byRel := map[string]int{}
	add := func(rel, abs string, size int64, fromLoader bool) {
		normalized := normalizeRelName(rel)
		if normalized == "" {
			return
		}
		if existing, ok := byRel[normalized]; ok {
			if abs != "" && items[existing].abs == "" {
				items[existing].abs = abs
				items[existing].size = size
			}
			return
		}
		byRel[normalized] = len(items)
		items = append(items, inventoryItem{rel: normalized, abs: abs, size: size, fromLoader: fromLoader})
	}

	deadline := time.Now().Add(2500 * time.Millisecond)
	for _, base := range roots {
		if !util.DirExists(base) {
			continue
		}
		visited := map[string]bool{}
		type pending struct {
			dir   string
			depth int
		}
		stack := []pending{{dir: base}}
		for len(stack) > 0 && time.Now().Before(deadline) {
			current := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			real, err := filepath.EvalSymlinks(current.dir)
			if err != nil {
				continue
			}
			lowered := strings.ToLower(real)
			if visited[lowered] {
				continue
			}
			visited[lowered] = true

			entries, err := os.ReadDir(current.dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				full := filepath.Join(current.dir, entry.Name())
				if entry.IsDir() {
					if current.depth < 5 {
						stack = append(stack, pending{dir: full, depth: current.depth + 1})
					}
					continue
				}
				if !weightFilePattern.MatchString(entry.Name()) {
					continue
				}
				var size int64
				if info, statErr := entry.Info(); statErr == nil {
					size = info.Size()
				}
				rel, relErr := filepath.Rel(base, full)
				if relErr != nil {
					continue
				}
				add(rel, full, size, false)
			}
		}
	}

	if classType, ok := loaderClassFor(dir); ok {
		for _, name := range m.setup.LoaderOptions()[classType] {
			add(name, "", 0, true)
		}
	}

	m.mu.Lock()
	m.inventory[key] = inventoryEntry{at: time.Now(), items: items}
	m.mu.Unlock()
	return items
}

// modelFileCandidates lists the direct paths a resource could occupy.
func (m *Manager) modelFileCandidates(resource *Resource, root string) []string {
	dir, file := targetParts(resource.Target)
	if dir == "custom_nodes" {
		if root == "" {
			return nil
		}
		return []string{filepath.Join(root, "custom_nodes", file)}
	}
	var out []string
	for _, base := range m.modelDirs(dir, root) {
		out = append(out, filepath.Join(base, file))
	}
	return out
}

// resourceMatch locates a resource on disk.
func (m *Manager) resourceMatch(resource *Resource, root string) matchResult {
	dir, file := targetParts(resource.Target)

	if resource.Type == "dir" {
		for _, base := range m.modelDirs(dir, root) {
			candidate := filepath.Join(base, file)
			if util.DirExists(candidate) &&
				(util.Exists(filepath.Join(candidate, "config.json")) ||
					util.Exists(filepath.Join(candidate, "model.safetensors"))) {
				rel := candidate
				if root != "" {
					if computed, err := filepath.Rel(root, candidate); err == nil {
						rel = computed
					}
				}
				return matchResult{present: true, path: candidate, rel: rel, matchedBy: "path"}
			}
		}
		return matchResult{}
	}

	if dir == "custom_nodes" {
		if root == "" {
			return matchResult{}
		}
		target := filepath.Join(root, "custom_nodes", file)
		if util.DirExists(target) {
			return matchResult{present: true, path: target, rel: file, matchedBy: "path"}
		}
		return matchResult{}
	}

	wanted := normalizeRelName(file)
	wantedBase := wanted
	if index := strings.LastIndex(wanted, "/"); index >= 0 {
		wantedBase = wanted[index+1:]
	}
	expectedSize := resource.Size
	sameSize := func(size int64) bool {
		return expectedSize == 0 || size == 0 || size == expectedSize
	}

	var directMismatch string
	for _, candidate := range m.modelFileCandidates(resource, root) {
		if !util.Exists(candidate) {
			continue
		}
		var candidateSize int64
		if info, err := os.Stat(candidate); err == nil {
			candidateSize = info.Size()
		}
		if sameSize(candidateSize) {
			rel := candidate
			if root != "" {
				if computed, err := filepath.Rel(root, candidate); err == nil {
					rel = computed
				}
			}
			return matchResult{present: true, path: candidate, rel: rel, size: candidateSize, matchedBy: "path"}
		}
		if expectedSize != 0 && candidateSize != 0 {
			directMismatch = sizeMismatchMessage(candidateSize, expectedSize)
		}
	}

	inventory := m.inventoryFor(dir, root)
	var exact, byBase []inventoryItem
	for _, item := range inventory {
		if item.rel == wanted {
			exact = append(exact, item)
			continue
		}
		base := item.rel
		if index := strings.LastIndex(base, "/"); index >= 0 {
			base = base[index+1:]
		}
		if base == wantedBase {
			byBase = append(byBase, item)
		}
	}

	choose := func(items []inventoryItem) *inventoryItem {
		for i := range items {
			if sameSize(items[i].size) {
				return &items[i]
			}
		}
		return nil
	}

	found := choose(exact)
	if found == nil {
		found = choose(byBase)
	}
	matchedBy := ""
	if found != nil {
		if found.rel == wanted {
			matchedBy = "path"
		} else {
			matchedBy = "name"
		}
	}

	var mismatch string
	if found == nil {
		var candidate *inventoryItem
		if len(exact) > 0 {
			candidate = &exact[0]
		} else if len(byBase) > 0 {
			candidate = &byBase[0]
		}
		if candidate != nil {
			// A same-named file of a different size is usually just a name collision.
			// Treating it as present would hide a genuinely missing file and the user
			// would only find out when the run fails.
			if expectedSize != 0 && candidate.size != 0 && candidate.size != expectedSize {
				mismatch = sizeMismatchMessage(candidate.size, expectedSize) + ", possibly a different version"
			} else {
				found = candidate
				if candidate.rel == wanted {
					matchedBy = "path"
				} else {
					matchedBy = "name"
				}
			}
		}
	}

	if found == nil && expectedSize > 0 {
		var bySize []inventoryItem
		for _, item := range inventory {
			if item.size == expectedSize && item.size > 0 {
				bySize = append(bySize, item)
			}
		}
		if len(bySize) == 1 {
			found = &bySize[0]
			matchedBy = "size"
		}
	}

	if found != nil {
		if mismatch == "" {
			mismatch = directMismatch
		}
		return matchResult{
			present:   true,
			path:      found.abs,
			rel:       found.rel,
			size:      found.size,
			matchedBy: matchedBy,
			mismatch:  mismatch,
		}
	}
	return matchResult{mismatch: directMismatch}
}

func sizeMismatchMessage(actual, expected int64) string {
	return "a local file with the same name has a different size (" + util.Itoa(actual) + " vs expected " + util.Itoa(expected) + ")"
}

// resourceAliasFor reports whether the user replaced this resource with a file that
// ComfyUI already sees under a different name.
func (m *Manager) resourceAliasFor(resource *Resource) (string, bool) {
	dir, file := targetParts(resource.Target)
	classType, ok := loaderClassFor(dir)
	if !ok {
		return "", false
	}
	input := ""
	for _, spec := range workflow.ModelLoaderSpecs {
		if spec.ClassType == classType {
			input = spec.Input
			break
		}
	}
	if input == "" {
		return "", false
	}
	// A nil setup means no ComfyUI scan yet (or a unit test): no aliases to resolve.
	if m.setup == nil {
		return "", false
	}
	target := m.setup.ModelAliasFor(classType, input, file)
	if target == "" || target == file {
		return "", false
	}
	return target, true
}

// itemStatus computes the panel row for one resource.
func (m *Manager) itemStatus(resource *Resource, root string, byID map[string]*Item) *Item {
	dir, _ := targetParts(resource.Target)
	isNode := dir == "custom_nodes" || resource.Type == "node"

	item := &Item{
		ID:              resource.ID,
		Type:            defaultString(resource.Type, "model"),
		Label:           defaultString(resource.Label, resource.ID),
		Target:          resource.Target,
		Size:            resource.Size,
		Sha256:          resource.Sha256,
		License:         resource.License,
		Note:            resource.Note,
		Requires:        nonNilStrings(resource.Requires),
		Builder:         resource.Builder,
		Sources:         m.sourcesFor(resource),
		Installed:       m.installedFor(resource.ID),
		MissingRequires: []string{},
	}

	var match matchResult
	alias := ""
	if !isNode {
		if target, ok := m.resourceAliasFor(resource); ok {
			alias = target
			match = matchResult{present: true, matchedBy: "alias"}
		} else {
			match = m.resourceMatch(resource, root)
		}
	} else {
		match = m.resourceMatch(resource, root)
	}

	exists := match.present
	size := match.size
	if size == 0 && match.path != "" {
		if info, err := os.Stat(match.path); err == nil {
			size = info.Size()
		}
	}

	item.Exists = exists
	item.Registered = dir != "custom_nodes" && exists
	item.MatchedFile = match.rel
	item.MatchedBy = match.matchedBy
	item.SizeMismatch = match.mismatch
	item.ActualSize = size

	isBake := resource.Type == "bake"
	if isBake {
		// The panel shows this command instead of a download button: the file is produced
		// by a local merge, never fetched.
		item.ManualCommand = m.bakeCommand(resource, root)
		var missing []string
		for _, id := range resource.Requires {
			dependency, ok := byID[id]
			if !ok || !dependency.Present {
				missing = append(missing, id)
			}
		}
		item.MissingRequires = missing
		switch {
		case exists:
			item.Status = "present"
		case len(missing) > 0:
			item.Status = "blocked"
		default:
			item.Status = "missing"
		}
	} else if exists {
		item.Status = "present"
	} else {
		item.Status = "missing"
	}
	item.Present = item.Status == "present"

	if alias != "" {
		item.Overridden = true
		item.OverriddenBy = alias
		item.MatchedFile = alias
		item.Present = true
		item.Status = "present"
	}
	return item
}

// sourcesFor normalizes a resource's download sources.
//
// index.json mixes two shapes: a bare URL string, and an object with url/kind/path.
// Both are folded into objects, and entries that carry neither a URL nor a bundled
// path are dropped so the UI never renders a dead download button.
func (m *Manager) sourcesFor(resource *Resource) []map[string]any {
	sources := normalizeSources(resource.Sources)
	if resource.BundleDir != "" {
		bundled := map[string]any{"kind": "bundled-dir", "path": resource.BundleDir, "label": "Bundled directory"}
		sources = append([]map[string]any{bundled}, sources...)
	}
	return sources
}

// normalizeSources flattens the two accepted source shapes into objects.
func normalizeSources(raw []any) []map[string]any {
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		var source map[string]any
		switch typed := item.(type) {
		case string:
			source = map[string]any{"url": typed}
		case map[string]any:
			source = make(map[string]any, len(typed)+1)
			for key, value := range typed {
				source[key] = value
			}
		default:
			continue
		}
		source["url"] = strings.TrimSpace(util.StringField(source, "url"))
		kind := util.StringField(source, "kind")
		hasBundledPath := (kind == "bundled" || kind == "bundled-dir") && util.StringField(source, "path") != ""
		if source["url"] != "" || hasBundledPath {
			out = append(out, source)
		}
	}
	return out
}

// installedFor returns the install record of a resource.
func (m *Manager) installedFor(id string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	installed, _ := m.lock["installed"].(map[string]any)
	if installed == nil {
		return nil
	}
	record, _ := installed[id].(map[string]any)
	return record
}

// Status builds the full resource panel payload.
func (m *Manager) Status() *Status {
	root := m.ComfyRoot()

	m.mu.Lock()
	index := m.index
	source := m.indexSource
	indexErr := m.indexError
	indexWarning := m.indexWarning
	loadedAt := m.loadedAt
	lock := m.lock
	m.mu.Unlock()

	installedModels := map[string]bool{}
	for _, name := range m.setup.LoaderOptions()["UNETLoader"] {
		installedModels[name] = true
	}

	if index == nil {
		return &Status{
			OK:           false,
			LoadedAt:     loadedAt,
			IndexSource:  source,
			IndexError:   indexErr,
			IndexWarning: indexWarning,
			Resources:    []*Item{},
			Packs:        []*PackStatus{},
			RuntimeDeps:  runtimeDeps(),
			Lock:         lockView(lock),
		}
	}

	// Pack-level main-model selections mark the resources they shadow as present.
	packMainModels := map[string]*MainModel{}
	overriddenBy := map[string]string{}
	for _, pack := range index.Packs {
		info := m.mainModelInfo(pack, installedModels)
		if info == nil {
			continue
		}
		packMainModels[pack.ID] = info
		if !info.SelectedInstalled {
			continue
		}
		for _, resource := range index.Resources {
			dir, _ := targetParts(resource.Target)
			if dir != "diffusion_models" || resource.Type == "bake" {
				continue
			}
			_, file := targetParts(resource.Target)
			for _, candidate := range info.Files {
				if candidate == file {
					overriddenBy[resource.ID] = info.Selected
					break
				}
			}
		}
	}

	byID := map[string]*Item{}
	byResourceID := map[string]*Resource{}
	for _, resource := range index.Resources {
		byResourceID[resource.ID] = resource
	}

	// Bake dependencies are resolved against other resources, so compute non-bake
	// rows first and fill bakes afterwards.
	var ordered []*Item
	for _, resource := range index.Resources {
		if resource.Type == "bake" {
			continue
		}
		item := m.itemStatus(resource, root, map[string]*Item{})
		byID[resource.ID] = item
		ordered = append(ordered, item)
	}
	for _, resource := range index.Resources {
		if resource.Type != "bake" {
			continue
		}
		item := m.itemStatus(resource, root, byID)
		byID[resource.ID] = item
		ordered = append(ordered, item)
	}

	// Preserve index order in the response.
	resources := make([]*Item, 0, len(index.Resources))
	for _, resource := range index.Resources {
		item := byID[resource.ID]
		if shadow, ok := overriddenBy[resource.ID]; ok {
			item.Status = "present"
			item.Present = true
			item.Overridden = true
			item.OverriddenBy = shadow
		}
		resources = append(resources, item)
	}

	packs := make([]*PackStatus, 0, len(index.Packs))
	for _, pack := range index.Packs {
		packs = append(packs, m.packStatus(pack, byID, byResourceID, packMainModels[pack.ID]))
	}

	workflowResources := m.workflowResources(packs, byID)
	var missing []*Item
	for _, item := range workflowResources {
		if item.Status != "present" {
			missing = append(missing, item)
		}
	}
	var missingSize int64
	for _, item := range missing {
		missingSize += item.Size
	}

	summary := Summary{
		Resources:        len(workflowResources),
		Present:          len(workflowResources) - len(missing),
		Missing:          len(missing),
		MissingSize:      missingSize,
		AllResources:     len(resources),
		RecommendedPacks: 0,
	}
	for _, pack := range packs {
		if !pack.Planned {
			summary.Packs++
		}
		if pack.Recommended {
			summary.RecommendedPacks++
		}
	}

	return &Status{
		OK:             true,
		LoadedAt:       loadedAt,
		IndexSource:    source,
		IndexError:     indexErr,
		IndexWarning:   indexWarning,
		IndexVersion:   util.Stringify(index.Version),
		Channel:        index.Channel,
		ComfyRoot:      root,
		ComfyPython:    m.ComfyPython(),
		ComfyReachable: m.setup.Status().ComfyReachable,
		RuntimeDeps:    runtimeDeps(),
		ModelRoots:     m.modelRootsView(root),
		Packs:          packs,
		Resources:      resources,
		Summary:        summary,
		Lock:           lockView(lock),
	}
}

// packStatus builds one pack's panel row.
func (m *Manager) packStatus(pack *Pack, byID map[string]*Item, byResourceID map[string]*Resource, mainModel *MainModel) *PackStatus {
	referenced := m.packReferencedFiles(pack)

	items := make([]*Item, 0, len(pack.Resources))
	for _, id := range pack.Resources {
		item, ok := byID[id]
		if !ok {
			continue
		}
		if !bakeStillReferenced(item, byResourceID[id], referenced) {
			continue
		}
		items = append(items, item)
	}

	optional := map[string]bool{}
	for _, id := range pack.OptionalResources {
		optional[id] = true
	}
	var requiredItems, optionalItems []*Item
	for _, item := range items {
		if optional[item.ID] {
			optionalItems = append(optionalItems, item)
		} else {
			requiredItems = append(requiredItems, item)
		}
	}
	requiredItems = nonNilItems(requiredItems)
	optionalItems = nonNilItems(optionalItems)

	var requiredMissing, optionalMissing []*Item
	for _, item := range requiredItems {
		if item.Status != "present" {
			requiredMissing = append(requiredMissing, item)
		}
	}
	for _, item := range optionalItems {
		if item.Status != "present" {
			optionalMissing = append(optionalMissing, item)
		}
	}

	status := &PackStatus{
		ID:              pack.ID,
		Label:           defaultString(pack.Label, pack.ID),
		Description:     pack.Description,
		Optional:        pack.Optional,
		OptionalLabel:   pack.OptionalLabel,
		Planned:         pack.Planned,
		Workflows:       nonNilStrings(pack.Workflows),
		ResourceIDs:     nonNilStrings(pack.Resources),
		Resources:       nonNilItems(items),
		RequiredItems:   requiredItems,
		OptionalItems:   optionalItems,
		MainModel:       mainModel,
		Total:           len(requiredItems),
		Present:         len(requiredItems) - len(requiredMissing),
		OptionalTotal:   len(optionalItems),
		OptionalPresent: len(optionalItems) - len(optionalMissing),
		OptionalMissing: len(optionalMissing),
	}
	status.RequiredIDs = itemIDs(requiredItems)
	status.OptionalIDs = itemIDs(optionalItems)
	if !pack.Planned {
		status.Missing = len(requiredMissing)
	}
	for _, item := range requiredMissing {
		status.MissingSize += item.Size
	}
	for _, item := range optionalMissing {
		status.OptionalSize += item.Size
	}

	// Dependencies come from the workflow table when present, so each workflow row
	// can show its own missing files and install button.
	if pack.WorkflowDeps.Len() > 0 {
		status.WorkflowDeps = m.workflowDependencies(pack, byID, byResourceID, mainModel)
	} else {
		status.WorkflowDeps = []*WorkflowDependency{}
	}

	if mainModel != nil {
		mainModel.Available = false
		for _, id := range mainModel.WorkflowIDs {
			if m.setup.WorkflowStatus(id).Available {
				mainModel.Available = true
				break
			}
		}
	}

	// "Recommended" no longer depends on the GPU: it means "optional, and actually
	// missing something". The old VRAM heuristic judged perfectly workable setups
	// (55GB weights offloaded to RAM) as incompatible.
	status.Recommended = !pack.Planned && (status.Missing+status.OptionalMissing) > 0
	return status
}

// workflowDependencies builds the per-workflow rows of a pack.
func (m *Manager) workflowDependencies(pack *Pack, byID map[string]*Item, byResourceID map[string]*Resource, packMainModel *MainModel) []*WorkflowDependency {
	out := make([]*WorkflowDependency, 0, pack.WorkflowDeps.Len())
	for _, workflowID := range pack.WorkflowDeps.Keys {
		entry := m.registry.Resolve(workflowID)
		referenced := m.workflowReferencedFiles(entry)

		var dependencyItems []*Item
		for _, id := range pack.WorkflowDeps.Values[workflowID] {
			item, ok := byID[id]
			if !ok {
				continue
			}
			if !bakeStillReferenced(item, byResourceID[id], referenced) {
				continue
			}
			dependencyItems = append(dependencyItems, item)
		}

		var missingItems []*Item
		for _, item := range dependencyItems {
			if item.Status != "present" {
				missingItems = append(missingItems, item)
			}
		}

		installSet := map[string]bool{}
		var installIDs []string
		for _, item := range missingItems {
			// A bake resource is never installed: it is produced by a local merge. Its
			// inputs still are, so they stay in the list below.
			if item.Type != "bake" && !installSet[item.ID] {
				installSet[item.ID] = true
				installIDs = append(installIDs, item.ID)
			}
			if item.Type != "bake" {
				continue
			}
			for _, requiredID := range item.Requires {
				required, ok := byID[requiredID]
				if !ok || required.Status == "present" || installSet[requiredID] {
					continue
				}
				installSet[requiredID] = true
				installIDs = append(installIDs, requiredID)
			}
		}

		availability := m.setup.WorkflowStatus(workflowID)
		label := workflowID
		variantLabels := map[string]string{}
		modelVisible := true
		if entry != nil {
			label = defaultString(entry.Label, entry.Name)
			variantLabels = entry.VariantLabelsMap()
			modelVisible = !entry.Hidden
		}

		var missingSize int64
		for _, item := range missingItems {
			missingSize += item.Size
		}

		out = append(out, &WorkflowDependency{
			ID:            workflowID,
			Label:         label,
			Available:     availability.Available,
			Enabled:       !m.visibility.IsHidden(workflowID),
			ModelVisible:  modelVisible,
			VariantLabels: variantLabels,
			Problems:      nonNilProblems(availability.Problems),
			Recommended:   false,
			Resources:     nonNilItems(dependencyItems),
			Missing:       len(missingItems),
			MissingSize:   missingSize,
			InstallIDs:    nonNilStrings(installIDs),
			MainModel:     m.workflowMainModel(pack, packMainModel, workflowID),
		})
	}
	return out
}

// workflowMainModel builds the per-workflow main-model selector state.
func (m *Manager) workflowMainModel(pack *Pack, base *MainModel, workflowID string) *WorkflowMainModel {
	if base == nil {
		return nil
	}
	applicable := false
	for _, id := range base.WorkflowIDs {
		if id == workflowID {
			applicable = true
			break
		}
	}
	if !applicable {
		return nil
	}

	entry := m.registry.Resolve(workflowID)
	if entry == nil {
		return nil
	}
	loader := workflow.FindUnetLoader(entry.Workflow)
	if loader == nil || loader.File == "" {
		return nil
	}
	file := loader.File

	raw := ""
	if m.mainModels != nil {
		raw = m.mainModels.FileForWorkflow(workflowID)
	}
	selected := raw
	if raw == file {
		selected = ""
	}

	defaultLabel := stripExtension(file)
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()
	if index != nil {
		for _, resource := range index.Resources {
			if _, name := targetParts(resource.Target); name == file && resource.Label != "" {
				defaultLabel = resource.Label
				break
			}
		}
	}

	selectedLabel := ""
	for _, option := range base.Options {
		if option.File == selected {
			selectedLabel = option.Label
			break
		}
	}

	installed := false
	if selected != "" {
		for _, name := range m.setup.LoaderOptions()["UNETLoader"] {
			if name == selected {
				installed = true
				break
			}
		}
	}

	var options []MainModelOption
	for _, option := range base.Options {
		if option.File == file {
			continue
		}
		options = append(options, option)
	}

	return &WorkflowMainModel{
		WorkflowID:        workflowID,
		DefaultFile:       file,
		DefaultLabel:      defaultLabel,
		Selected:          selected,
		SelectedLabel:     selectedLabel,
		SelectedInstalled: installed,
		Options:           options,
	}
}

// workflowResources collects every resource that some workflow actually depends on.
func (m *Manager) workflowResources(packs []*PackStatus, byID map[string]*Item) []*Item {
	ids := map[string]bool{}
	anyDeps := false
	for _, pack := range packs {
		for _, workflow := range pack.WorkflowDeps {
			anyDeps = true
			for _, item := range workflow.Resources {
				ids[item.ID] = true
				for _, required := range item.Requires {
					ids[required] = true
				}
			}
		}
	}
	if !anyDeps {
		for _, pack := range packs {
			for _, item := range pack.Resources {
				ids[item.ID] = true
			}
		}
	}

	var out []*Item
	for id, item := range byID {
		if ids[id] {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return nonNilItems(out)
}

// modelRootsView lists the existing roots per logical model directory.
func (m *Manager) modelRootsView(root string) map[string][]string {
	out := map[string][]string{}
	for dir := range setup.ModelDirKeys {
		var existing []string
		for _, candidate := range m.modelDirs(dir, root) {
			if util.DirExists(candidate) {
				existing = append(existing, candidate)
			}
		}
		if len(existing) > 0 {
			out[dir] = existing
		}
	}
	return out
}

// runtimeDeps lists the Python packages the user must install themselves.
func runtimeDeps() []RuntimeDep {
	return []RuntimeDep{
		{ID: "h3", Packages: []string{"sageattention"}},
		{ID: "seedvr2", Packages: []string{"diffusers", "omegaconf", "rotary_embedding_torch", "opencv-python", "einops"}},
	}
}

func lockView(lock map[string]any) map[string]any {
	installed, ok := lock["installed"].(map[string]any)
	if !ok {
		installed = map[string]any{}
	}
	updatedAt, _ := lock["updatedAt"].(string)
	return map[string]any{"installed": installed, "updatedAt": updatedAt}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilItems(items []*Item) []*Item {
	if items == nil {
		return []*Item{}
	}
	return items
}

func nonNilProblems(problems []setup.Problem) []setup.Problem {
	if problems == nil {
		return []setup.Problem{}
	}
	return problems
}

func itemIDs(items []*Item) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}
