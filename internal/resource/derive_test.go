package resource

// A derived dependency list is only worth having if it is exact, so these tests pin the three
// shapes that made the hand-written table wrong: a model inside a subdirectory, a loader input that
// names a directory, and a loader class that belongs to a node pack.

import (
	"encoding/json"
	"slices"
	"testing"

	"comfyui-gateway/internal/workflow"
)

const deriveCatalogue = `{
	"schema": 1,
	"resources": [
		{"id": "unet_a", "target": "diffusion_models/a.safetensors"},
		{"id": "lora_moss", "target": "loras/Moss_image/8steps.safetensors"},
		{"id": "lora_root", "target": "loras/8steps.safetensors"},
		{"id": "vdn_model", "target": "vdn/stage-dmd/model.safetensors"},
		{"id": "vdn_spec", "target": "vdn/stage-dmd/model_spec.json"},
		{"id": "vdn_other", "target": "vdn/stage-dmd-2/model.safetensors"},
		{"id": "upscaler", "target": "latent_upscale_models/up.safetensors"},
		{"id": "selflift_node", "target": "custom_nodes/comfyui-SelfLift", "provides": ["SelfLiftH3Sampler"]},
		{"id": "vdn_node", "target": "custom_nodes/ComfyUI-VDN-H3", "provides": ["ApplyVDNH3Advanced"]},
		{"id": "rife_node", "target": "custom_nodes/ComfyUI-Frame-Interpolation", "provides": ["RIFE VFI"]}
	]
}`

func deriveProviders(t *testing.T) *providerIndex {
	t.Helper()
	index := decodeIndex(t, deriveCatalogue)
	return newProviderIndex(index)
}

func deriveGraph(t *testing.T, raw string) workflow.Graph {
	t.Helper()
	var graph workflow.Graph
	if err := json.Unmarshal([]byte(raw), &graph); err != nil {
		t.Fatalf("decode graph: %v", err)
	}
	return graph
}

func TestDeriveMatchesAModelByItsWholeTarget(t *testing.T) {
	providers := deriveProviders(t)

	// The same file name exists at the root of loras/ and under Moss_image/. Only the one the
	// graph names may come back; a basename match would install (and then verify) the wrong file.
	derived := providers.graphDeps(deriveGraph(t, `{
		"1": {"class_type": "LoraLoaderModelOnly", "inputs": {"lora_name": "Moss_image/8steps.safetensors"}}
	}`))
	if !slices.Equal(derived.Resources, []string{"lora_moss"}) {
		t.Fatalf("resources = %v, want [lora_moss]", derived.Resources)
	}

	derived = providers.graphDeps(deriveGraph(t, `{
		"1": {"class_type": "LoraLoaderModelOnly", "inputs": {"lora_name": "8steps.safetensors"}}
	}`))
	if !slices.Equal(derived.Resources, []string{"lora_root"}) {
		t.Fatalf("resources = %v, want [lora_root]", derived.Resources)
	}
}

func TestDeriveTreatsADirectoryInputAsTheWholeFolder(t *testing.T) {
	// `vdn_checkpoint` names a folder, and the folder is one install: the model, its spec, and (in
	// the real catalogue) two adapters plus three configs. A file match would find nothing here.
	derived := deriveProviders(t).graphDeps(deriveGraph(t, `{
		"1": {"class_type": "ApplyVDNH3Advanced", "inputs": {"vdn_checkpoint": "stage-dmd"}}
	}`))
	want := []string{"vdn_model", "vdn_node", "vdn_spec"}
	if !slices.Equal(derived.Resources, want) {
		t.Fatalf("resources = %v, want %v", derived.Resources, want)
	}
	if len(derived.Unresolved) != 0 {
		t.Errorf("unresolved = %v, want none", derived.Unresolved)
	}
}

func TestDeriveKeepsThePackThatOwnsALoaderClass(t *testing.T) {
	// SelfLiftH3Sampler is a loader (it reads upscaler_model) *and* a class that ships with a node
	// pack. Asking for the model and forgetting the node is the bug this covers.
	derived := deriveProviders(t).graphDeps(deriveGraph(t, `{
		"1": {"class_type": "SelfLiftH3Sampler", "inputs": {"upscaler_model": "up.safetensors"}}
	}`))
	want := []string{"selflift_node", "upscaler"}
	if !slices.Equal(derived.Resources, want) {
		t.Fatalf("resources = %v, want %v", derived.Resources, want)
	}
}

func TestDeriveReportsWhatNothingOwns(t *testing.T) {
	// A graph that loads a file no resource provides is a workflow that looks ready and then fails.
	derived := deriveProviders(t).graphDeps(deriveGraph(t, `{
		"1": {"class_type": "UNETLoader", "inputs": {"unet_name": "ghost.safetensors"}}
	}`))
	if len(derived.Resources) != 0 {
		t.Errorf("resources = %v, want none", derived.Resources)
	}
	if !slices.Equal(derived.Unresolved, []string{"ghost.safetensors"}) {
		t.Fatalf("unresolved = %v, want [ghost.safetensors]", derived.Unresolved)
	}
}

func TestDeriveDoesNotBlameTheNodeForItsOwnCheckpoint(t *testing.T) {
	// RIFE names its checkpoint like a loader does, but the pack downloads it into its own folder,
	// so there is no model directory to look in and nothing to report as unresolved.
	derived := deriveProviders(t).graphDeps(deriveGraph(t, `{
		"1": {"class_type": "RIFE VFI", "inputs": {"ckpt_name": "rife49.pth"}}
	}`))
	if !slices.Equal(derived.Resources, []string{"rife_node"}) {
		t.Fatalf("resources = %v, want [rife_node]", derived.Resources)
	}
	if len(derived.Unresolved) != 0 {
		t.Errorf("unresolved = %v, want none", derived.Unresolved)
	}
}

func TestDeriveUnionsEveryVariant(t *testing.T) {
	// Outpaint needs its LoRA; the default edit mode never touches it. Installing only what the
	// default graph names would leave outpaint unable to run.
	entry := &workflow.Entry{
		Workflow: deriveGraph(t, `{
			"1": {"class_type": "UNETLoader", "inputs": {"unet_name": "a.safetensors"}}
		}`),
		Variants: workflow.OrderedMap[*workflow.Variant]{
			Keys: []string{"outpaint"},
			Values: map[string]*workflow.Variant{
				"outpaint": {Workflow: deriveGraph(t, `{
					"2": {"class_type": "LoraLoaderModelOnly", "inputs": {"lora_name": "Moss_image/8steps.safetensors"}}
				}`)},
			},
		},
	}
	derived := deriveProviders(t).entryDeps(entry)
	want := []string{"lora_moss", "unet_a"}
	if !slices.Equal(derived.Resources, want) {
		t.Fatalf("resources = %v, want %v", derived.Resources, want)
	}
}
