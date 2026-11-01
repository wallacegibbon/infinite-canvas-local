package resource

// resources.json is the one file a human edits, and the workflow graphs are the thing the gateway
// cannot afford to disagree with. These tests read both and check that they still describe the same
// system — the check the hand-written dependency table never had.

import (
	"regexp"
	"strings"
	"testing"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/workflow"
)

// pinnedCommitPattern is a full git sha, which is the only thing that cannot move under us.
var pinnedCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// catalogueForTests loads the real resources.json and the real workflow registry.
func catalogueForTests(t *testing.T) (*Index, *workflow.Registry) {
	t.Helper()
	index, errText := readIndexFile("../../resources.json")
	if index == nil {
		t.Fatalf("resources.json: %s", errText)
	}
	registry := workflow.NewRegistry(workflow.RegistryOptions{
		Dir:    "../../workflows",
		Logger: logx.NewWithWriter("test", t.Output()),
	})
	if err := registry.Load(); err != nil {
		t.Fatalf("load workflows/registry.json: %v", err)
	}
	return index, registry
}

// Every workflow a pack names has to exist, and every file its graphs load has to be owned by a
// catalogue entry. A renamed workflow or a deleted resource would otherwise surface as a green pack
// or as a run that fails inside ComfyUI.
func TestPackWorkflowsResolveToTheirFiles(t *testing.T) {
	index, registry := catalogueForTests(t)
	providers := newProviderIndex(index)

	for _, pack := range index.Packs {
		if len(pack.Workflows) == 0 {
			continue
		}
		for _, workflowID := range pack.Workflows {
			entry := registry.Resolve(workflowID)
			if entry == nil {
				t.Errorf("pack %s names workflow %s, which the registry does not have", pack.ID, workflowID)
				continue
			}
			derived := providers.entryDeps(entry)
			for _, name := range derived.Unresolved {
				t.Errorf("workflow %s loads %s, which no resource provides", workflowID, name)
			}
			if len(derived.Resources) == 0 {
				t.Errorf("workflow %s needs nothing at all; the graph walk is looking in the wrong place", workflowID)
			}
		}
	}
}

// A pack that runs a workflow must require something, or the panel shows it as complete and offers
// no way to install it. That is exactly what an all-optional hand-written list used to produce.
func TestEveryRunnablePackRequiresSomething(t *testing.T) {
	index, registry := catalogueForTests(t)
	manager := &Manager{registry: registry}
	providers := newProviderIndex(index)

	for _, pack := range index.Packs {
		required, _ := manager.packResourceSplit(pack, providers)
		if len(pack.Workflows) > 0 && len(required) == 0 {
			t.Errorf("pack %s runs %d workflows and requires nothing", pack.ID, len(pack.Workflows))
		}
	}
}

// Nothing in the catalogue is dead weight: every resource is either loaded by a workflow or listed as
// an alternate someone can choose. This is what catches "the feature was deleted, the weights stayed".
func TestEveryCataloguedResourceIsReachable(t *testing.T) {
	index, registry := catalogueForTests(t)
	manager := &Manager{registry: registry}
	providers := newProviderIndex(index)

	reachable := map[string]bool{}
	for _, pack := range index.Packs {
		for _, id := range manager.packModelIDs(pack, providers) {
			reachable[id] = true
		}
	}
	for _, resource := range index.Resources {
		if !reachable[resource.ID] {
			t.Errorf("resource %s (%s) is in no pack and in no graph", resource.ID, resource.Target)
		}
	}
}

// `provides` says which node classes a pack must register. A class ComfyUI ships itself is not one of
// them: listing it makes the pack look broken on any ComfyUI whose version moved the class, and it is
// how one pack came to be blamed for two of the host's own nodes.
func TestPackProvidesOnlyItsOwnClasses(t *testing.T) {
	index, _ := catalogueForTests(t)

	// ComfyUI core classes our graphs use. A pack that claims one of these is claiming the host.
	core := map[string]bool{
		"ModelAttentionBackend":     true,
		"MiniMaxH3SigmaShift":       true,
		"LoraLoaderBypassModelOnly": true,
	}
	for _, resource := range index.Resources {
		for _, class := range resource.Provides {
			if core[class] {
				t.Errorf("resource %s claims %s, which ships with ComfyUI", resource.ID, class)
			}
		}
	}
}

// A node pack downloads source, so it has to name the commit it was tested against. A branch name is a
// moving target — the install would silently become a different pack than the one the workflows were
// verified with.
func TestNodePacksPinACommit(t *testing.T) {
	index, _ := catalogueForTests(t)

	pinned := 0
	for _, resource := range index.Resources {
		if !strings.HasPrefix(resource.Target, "custom_nodes/") {
			continue
		}
		pinned++
		if !strings.HasPrefix(resource.Upstream, "https://github.com/") {
			t.Errorf("node pack %s has no github upstream (%q)", resource.ID, resource.Upstream)
		}
		if !pinnedCommitPattern.MatchString(resource.PinnedCommit) {
			t.Errorf("node pack %s pins %q, which is not a full commit sha", resource.ID, resource.PinnedCommit)
		}
	}
	if pinned == 0 {
		t.Fatal("no node packs were checked; the walk is looking in the wrong place")
	}
}
