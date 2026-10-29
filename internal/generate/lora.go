package generate

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// modelLoaderClasses are the node types that produce a MODEL output, i.e. the
// places a LoRA chain can be spliced in.
var modelLoaderClasses = map[string]bool{
	"UNETLoader":             true,
	"CheckpointLoaderSimple": true,
	"CheckpointLoader":       true,
}

// LoraOptions configures LoRA injection for one workflow.
type LoraOptions struct {
	// Available is the installed lora file list. An empty list disables the check,
	// which is what happens before the first ComfyUI scan completes.
	Available []string
	// Filter is a regex restricting which LoRAs apply to this workflow.
	Filter string
	// SourceNode pins the MODEL node the chain should attach to.
	SourceNode string
	// LoaderClass is the node type used to represent each LoRA.
	LoaderClass string
}

// AppliedLora records one injected LoRA for logging.
type AppliedLora struct {
	ID       string
	Name     string
	Strength float64
}

// loraRequest is one normalized entry of params.loras.
type loraRequest struct {
	Name     string
	Strength float64
}

// ApplyLoraChain splices params.loras in after the workflow's model loader.
//
// It returns a new graph: the on-disk workflow is never modified, so each request
// gets its own chain and concurrent requests cannot interfere.
func ApplyLoraChain(graph workflow.Graph, params map[string]any, opts LoraOptions) (workflow.Graph, []AppliedLora, error) {
	loras := normalizeLoras(params)
	if len(loras) == 0 {
		return graph, nil, nil
	}

	next := graph.Clone()
	sourceID := findModelSource(next, opts.SourceNode)
	if sourceID == "" {
		return nil, nil, httpx.BadRequest("lora_injection_failed", "cannot inject LoRA: no recognised model loader in the workflow")
	}

	var allowed *regexp.Regexp
	if opts.Filter != "" {
		compiled, err := regexp.Compile("(?i)" + opts.Filter)
		if err != nil {
			return nil, nil, httpx.BadRequest("lora_injection_failed", "invalid LoRA filter: "+opts.Filter)
		}
		allowed = compiled
	}
	for _, lora := range loras {
		if allowed != nil && !allowed.MatchString(lora.Name) {
			return nil, nil, httpx.BadRequest("lora_injection_failed", "LoRA "+lora.Name+" does not apply to this model")
		}
		if len(opts.Available) > 0 && !contains(opts.Available, lora.Name) {
			return nil, nil, httpx.BadRequest("lora_injection_failed", "LoRA not found: "+lora.Name)
		}
	}

	// Every consumer of the model loader's MODEL output must be re-pointed at the
	// end of the chain; otherwise the LoRA would load but never be used.
	type target struct {
		node  workflow.Node
		input string
	}
	var targets []target
	for _, id := range next.IDs() {
		node, ok := next.NodeAt(id)
		if !ok {
			continue
		}
		for _, input := range sortedInputKeys(node.Inputs()) {
			link, ok := node.Inputs()[input].([]any)
			if !ok || len(link) < 2 {
				continue
			}
			source, _ := link[0].(string)
			slot, _ := link[1].(float64)
			if source == sourceID && slot == 0 {
				targets = append(targets, target{node: node, input: input})
			}
		}
	}
	if len(targets) == 0 {
		return nil, nil, httpx.BadRequest("lora_injection_failed", "cannot inject LoRA: the model loader has no downstream consumer")
	}

	loaderClass := opts.LoaderClass
	if loaderClass == "" {
		loaderClass = "LoraLoaderModelOnly"
	}

	last := []any{sourceID, 0}
	applied := make([]AppliedLora, 0, len(loras))
	for _, lora := range loras {
		id := nextLoraNodeID(next)
		next[id] = map[string]any{
			"class_type": loaderClass,
			"inputs": map[string]any{
				"model":          last,
				"lora_name":      lora.Name,
				"strength_model": lora.Strength,
			},
			"_meta": map[string]any{"title": "LoRA " + path.Base(util.NormalizeSlashes(lora.Name))},
		}
		last = []any{id, 0}
		applied = append(applied, AppliedLora{ID: id, Name: lora.Name, Strength: lora.Strength})
	}
	for _, item := range targets {
		item.node.Inputs()[item.input] = last
	}
	return next, applied, nil
}

// normalizeLoras reads params.loras, skipping disabled entries.
func normalizeLoras(params map[string]any) []loraRequest {
	if params == nil {
		return nil
	}
	raw, ok := params["loras"].([]any)
	if !ok {
		return nil
	}
	out := make([]loraRequest, 0, len(raw))
	for _, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if enabled, ok := object["enabled"].(bool); ok && !enabled {
			continue
		}
		name := strings.TrimSpace(util.StringField(object, "name"))
		if name == "" {
			name = strings.TrimSpace(util.StringField(object, "lora_name"))
		}
		if name == "" {
			continue
		}
		strength := 1.0
		if value, ok := object["strength"].(float64); ok {
			strength = value
		} else if value, ok := object["strength_model"].(float64); ok {
			strength = value
		}
		out = append(out, loraRequest{Name: name, Strength: strength})
	}
	return out
}

// nextLoraNodeID allocates an id above every numeric id already in use.
func nextLoraNodeID(graph workflow.Graph) string {
	max := 0
	for _, id := range graph.IDs() {
		if number, err := strconv.Atoi(id); err == nil && number > max {
			max = number
		}
	}
	next := max + 1
	if next < 9000 {
		next = 9000
	}
	for {
		candidate := strconv.Itoa(next)
		if _, exists := graph[candidate]; !exists {
			return candidate
		}
		next++
	}
}

// findModelSource picks the MODEL node a LoRA chain should attach to.
//
// Checkpoint-based graphs can contain several loaders; the one that actually feeds
// downstream nodes wins, with UNETLoader preferred on ties.
func findModelSource(graph workflow.Graph, preferredID string) string {
	if preferredID != "" {
		if _, ok := graph[preferredID]; ok {
			return preferredID
		}
	}

	var candidates []string
	for _, id := range graph.IDs() {
		node, ok := graph.NodeAt(id)
		if ok && modelLoaderClasses[node.ClassType()] {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return ""
	}

	references := map[string]int{}
	for _, id := range candidates {
		references[id] = 0
	}
	for _, id := range graph.IDs() {
		node, ok := graph.NodeAt(id)
		if !ok {
			continue
		}
		for _, value := range node.Inputs() {
			link, ok := value.([]any)
			if !ok || len(link) < 2 {
				continue
			}
			source, _ := link[0].(string)
			slot, _ := link[1].(float64)
			if slot != 0 {
				continue
			}
			if _, tracked := references[source]; tracked {
				references[source]++
			}
		}
	}

	var ranked []string
	for _, id := range candidates {
		if references[id] > 0 {
			ranked = append(ranked, id)
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if references[ranked[i]] != references[ranked[j]] {
			return references[ranked[i]] > references[ranked[j]]
		}
		nodeI, _ := graph.NodeAt(ranked[i])
		nodeJ, _ := graph.NodeAt(ranked[j])
		if nodeI.ClassType() == "UNETLoader" && nodeJ.ClassType() != "UNETLoader" {
			return true
		}
		if nodeJ.ClassType() == "UNETLoader" && nodeI.ClassType() != "UNETLoader" {
			return false
		}
		return ranked[i] < ranked[j]
	})
	if len(ranked) == 0 {
		return ""
	}
	return ranked[0]
}

func sortedInputKeys(inputs map[string]any) []string {
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
