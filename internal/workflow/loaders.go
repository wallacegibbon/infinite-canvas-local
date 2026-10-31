package workflow

// ModelLoaderSpecs is the single list of "inputs that name a model file on disk".
//
// Availability scanning and runtime aliasing both read this table, so adding a new
// loader node happens in exactly one place. Missing entries here used to make the
// gateway report a workflow as ready and then fail inside ComfyUI, so the table is
// deliberately exhaustive rather than curated.
var ModelLoaderSpecs = []struct {
	ClassType string
	Input     string
	Key       string
	Dirs      []string
}{
	{ClassType: "CheckpointLoaderSimple", Input: "ckpt_name", Key: "checkpoints", Dirs: []string{"checkpoints"}},
	{ClassType: "UNETLoader", Input: "unet_name", Key: "unets", Dirs: []string{"diffusion_models"}},
	{ClassType: "UpscaleModelLoader", Input: "model_name", Key: "upscaleModels", Dirs: []string{"upscale_models"}},
	// The SeedVR2 nodes only look in models/SEEDVR2: folder_paths type "seedvr2",
	// see ComfyUI-SeedVR2_VideoUpscaler/src/utils/constants.py: SEEDVR2_FOLDER_NAME.
	{ClassType: "SeedVR2LoadDiTModel", Input: "model", Key: "seedvr2Dit", Dirs: []string{"SEEDVR2"}},
	{ClassType: "SeedVR2LoadVAEModel", Input: "model", Key: "seedvr2Vae", Dirs: []string{"SEEDVR2"}},
	{ClassType: "CLIPLoader", Input: "clip_name", Key: "clips", Dirs: []string{"text_encoders"}},
	{ClassType: "VAELoader", Input: "vae_name", Key: "vaes", Dirs: []string{"vae"}},
	{ClassType: "LoraLoader", Input: "lora_name", Key: "loras", Dirs: []string{"loras"}},
	{ClassType: "LoraLoaderModelOnly", Input: "lora_name", Key: "lorasModelOnly", Dirs: []string{"loras"}},
	// H3 workflows load a turbo LoRA, a latent upscaler and a tiny VAE through these
	// three; omitting them made missing files invisible to the availability check.
	{ClassType: "LoraLoaderBypassModelOnly", Input: "lora_name", Key: "lorasBypassModelOnly", Dirs: []string{"loras"}},
	{ClassType: "SelfLiftH3Sampler", Input: "upscaler_model", Key: "latentUpscalers", Dirs: []string{"latent_upscale_models"}},
	{ClassType: "ModelPreviewOverrideKJ", Input: "tiny_vae", Key: "vaeApprox", Dirs: []string{"vae_approx"}},
	{ClassType: "ApplyVDNH3Advanced", Input: "vdn_checkpoint", Key: "vdn", Dirs: []string{"vdn"}},
}

// SpecFor returns the spec matching a class/input pair.
func SpecFor(classType, input string) (string, []string, bool) {
	for _, spec := range ModelLoaderSpecs {
		if spec.ClassType == classType && spec.Input == input {
			return spec.Key, spec.Dirs, true
		}
	}
	return "", nil, false
}

// SpecForClass returns the first spec matching a class type.
func SpecForClass(classType string) (string, []string, bool) {
	for _, spec := range ModelLoaderSpecs {
		if spec.ClassType == classType {
			return spec.Key, spec.Dirs, true
		}
	}
	return "", nil, false
}

// ResolveMode swaps in a variant's graph, bindings and defaults.
//
// Variant bindings replace the base bindings entirely rather than merging: a
// variant is a different graph with a different wiring, so a partial overlay would
// silently point at nodes that do not exist.
func ResolveMode(entry *Entry, mode string) *Entry {
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
	mergeVariantContract(&merged, variant)
	return &merged
}

// ApplyModelAliases rewrites model file names through the alias resolver.
//
// The resolver maps a name a workflow was authored with onto the name the local
// ComfyUI actually reports, which is how the same registry.json works on machines
// with differently named model files.
//
// The graph is copied lazily: most workflows need no rewriting, and cloning a large
// graph on every request just to leave it unchanged is pure waste.
func ApplyModelAliases(graph Graph, aliasFor func(classType, input, value string) string) Graph {
	if aliasFor == nil {
		return graph
	}
	next := graph
	cloned := false
	for _, spec := range ModelLoaderSpecs {
		for _, id := range next.IDs() {
			node, ok := next.NodeAt(id)
			if !ok || node.ClassType() != spec.ClassType {
				continue
			}
			value, ok := node.Inputs()[spec.Input].(string)
			if !ok {
				continue
			}
			target := aliasFor(spec.ClassType, spec.Input, value)
			if target == "" || target == value {
				continue
			}
			if !cloned {
				next = graph.Clone()
				cloned = true
			}
			updated := map[string]any{}
			for key, item := range node {
				updated[key] = item
			}
			inputs := map[string]any{}
			for key, item := range node.Inputs() {
				inputs[key] = item
			}
			inputs[spec.Input] = target
			updated["inputs"] = inputs
			next[id] = updated
		}
	}
	return next
}

// ApplyMainModelFile points the UNETLoader at a per-workflow main model override.
func ApplyMainModelFile(graph Graph, file string) Graph {
	if file == "" {
		return graph
	}
	target := FindUnetLoader(graph)
	if target == nil || target.File == file {
		return graph
	}
	node, ok := graph.NodeAt(target.NodeID)
	if !ok {
		return graph
	}
	updated := map[string]any{}
	for key, item := range node {
		updated[key] = item
	}
	inputs := map[string]any{}
	for key, item := range node.Inputs() {
		inputs[key] = item
	}
	inputs["unet_name"] = file
	updated["inputs"] = inputs

	next := graph.Clone()
	next[target.NodeID] = updated
	return next
}
