package resource

// What a workflow needs is a fact about its graph, not a table someone maintains by hand.
//
// A graph names the files it loads and the node classes it runs, and the catalogue says which
// resource owns each of those names. So the dependency list is derived, and it cannot drift: a
// workflow that starts loading a second LoRA needs no edit to resources.json, and a resource no
// graph names is visibly unused instead of quietly bundled.
//
// Two shapes need care:
//
//   - A model is matched by its whole target, never by its bare file name. The same LoRA name can
//     live in two subdirectories (`loras/Moss_image/…`, `loras/Qwen-功能2.1/…`), and matching on the
//     basename would install the wrong file and report the right one as missing.
//   - `ApplyVDNH3Advanced.vdn_checkpoint` names a *directory* (`stage-dmd-step-250`), and that
//     folder holds a model, two adapters and their configs. It is a prefix match, not a file match.

import (
	"sort"
	"strings"

	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// DerivedDependency is everything one workflow's graphs ask for.
type DerivedDependency struct {
	// Resources are the catalogue ids the graphs name, sorted and unique.
	Resources []string
	// Unresolved are model file names a graph loads that no catalogue entry owns. Empty is the
	// healthy state: a name here is a workflow that reports "installed" and then fails inside
	// ComfyUI.
	Unresolved []string
}

// providerIndex answers "which resource owns this name" for the three ways a graph can use one.
type providerIndex struct {
	// byTarget maps a full model target ("loras/vendor/a.safetensors") to its resource ids.
	byTarget map[string][]string
	// targets are the keys of byTarget, sorted, so a directory match can scan them.
	targets []string
	// byClass maps a node class to the packs that declare it.
	byClass map[string][]string
}

// newProviderIndex flattens the catalogue into the lookups a graph walk needs.
func newProviderIndex(index *Index) *providerIndex {
	builder := &providerIndex{byTarget: map[string][]string{}, byClass: map[string][]string{}}
	if index == nil {
		return builder
	}
	for _, resource := range index.Resources {
		if resource == nil {
			continue
		}
		target := util.NormalizeSlashes(resource.Target)
		if target != "" && !strings.HasPrefix(target, "custom_nodes/") {
			builder.byTarget[target] = append(builder.byTarget[target], resource.ID)
		}
		for _, class := range resource.Provides {
			builder.byClass[class] = append(builder.byClass[class], resource.ID)
		}
	}
	builder.targets = make([]string, 0, len(builder.byTarget))
	for target := range builder.byTarget {
		builder.targets = append(builder.targets, target)
	}
	sort.Strings(builder.targets)
	return builder
}

// filesUnder returns the resources whose target sits in a model directory named by a graph.
func (p *providerIndex) filesUnder(prefix string) []string {
	var out []string
	for _, target := range p.targets {
		if strings.HasPrefix(target, prefix) {
			out = append(out, p.byTarget[target]...)
		}
	}
	return out
}

// modelIDs resolves one loader input to the resources it needs.
//
// A value with a weight extension names a file; anything else names a directory, which is how the
// VDN checkpoint is addressed.
func (p *providerIndex) modelIDs(dir, value string) []string {
	path := dir + "/" + strings.TrimPrefix(value, "/")
	if weightFilePattern.MatchString(value) {
		return p.byTarget[path]
	}
	return p.filesUnder(path + "/")
}

// graphDeps walks one graph: every node class it runs, plus every file its loaders name.
//
// The class lookup runs for loader classes too, because a loader can come from a node pack
// (`SelfLiftH3Sampler` ships with comfyui-SelfLift, `ApplyVDNH3Advanced` with ComfyUI-VDN-H3):
// skipping it would ask for the model and forget the node that reads it.
func (p *providerIndex) graphDeps(graph workflow.Graph) DerivedDependency {
	ids := map[string]bool{}
	unresolved := map[string]bool{}
	for _, id := range graph.IDs() {
		node, ok := graph.NodeAt(id)
		if !ok {
			continue
		}
		class := node.ClassType()
		for _, resourceID := range p.byClass[class] {
			ids[resourceID] = true
		}
		for _, spec := range workflow.ModelLoaderSpecs {
			if spec.ClassType != class || len(spec.Dirs) == 0 {
				continue
			}
			value, _ := node.Inputs()[spec.Input].(string)
			value = strings.TrimPrefix(util.NormalizeSlashes(strings.TrimSpace(value)), "./")
			if value == "" {
				continue
			}
			matched := 0
			for _, dir := range spec.Dirs {
				for _, resourceID := range p.modelIDs(dir, value) {
					ids[resourceID] = true
					matched++
				}
			}
			if matched == 0 {
				unresolved[value] = true
			}
		}
	}
	return DerivedDependency{Resources: sortedKeys(ids), Unresolved: sortedKeys(unresolved)}
}

// entryDeps unions a workflow's graphs.
//
// The variants count: which precision a workflow runs is a request-time choice, but "this
// workflow works" means every one of its modes can find its files — outpaint needs its LoRA even
// when the default mode never touches it.
func (p *providerIndex) entryDeps(entry *workflow.Entry) DerivedDependency {
	ids := map[string]bool{}
	unresolved := map[string]bool{}
	collect := func(graph workflow.Graph) {
		if graph == nil {
			return
		}
		derived := p.graphDeps(graph)
		for _, id := range derived.Resources {
			ids[id] = true
		}
		for _, name := range derived.Unresolved {
			unresolved[name] = true
		}
	}
	if entry == nil {
		return DerivedDependency{Resources: []string{}, Unresolved: []string{}}
	}
	collect(entry.Workflow)
	for _, variant := range entry.Variants.Values {
		if variant != nil {
			collect(variant.Workflow)
		}
	}
	return DerivedDependency{Resources: sortedKeys(ids), Unresolved: sortedKeys(unresolved)}
}

// DerivedDependencies returns what one registered workflow needs, read off its graphs.
//
// It is the single source of truth for a workflow's dependency list; resources.json no longer
// carries one.
func (m *Manager) DerivedDependencies(workflowID string) DerivedDependency {
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()
	return newProviderIndex(index).entryDeps(m.resolvedWorkflow(workflowID))
}

// resolvedWorkflow looks a workflow id up in the registry, tolerating a manager without one (unit
// tests) instead of panicking on a nil registry.
func (m *Manager) resolvedWorkflow(workflowID string) *workflow.Entry {
	if m.registry == nil {
		return nil
	}
	return m.registry.Resolve(workflowID)
}

// sortedKeys returns a map's keys in order, never nil.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// packModelIDs lists every resource a pack contains: what its workflows load, plus its alternates.
func (m *Manager) packModelIDs(pack *Pack, providers *providerIndex) []string {
	ids := map[string]bool{}
	for _, id := range pack.Alternates {
		ids[id] = true
	}
	for _, workflowID := range pack.Workflows {
		for _, id := range providers.entryDeps(m.resolvedWorkflow(workflowID)).Resources {
			ids[id] = true
		}
	}
	return sortedKeys(ids)
}
