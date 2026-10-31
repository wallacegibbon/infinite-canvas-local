package workflow

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Patterns used when inferring a type and a binding set from a bare graph.
var (
	reVideoClass   = regexp.MustCompile(`(?i)SaveVideo|VHS_VideoCombine|SaveWebM|WanImageToVideo|WanFirstLastFrame|WanVace|MiniMaxH3|MinimaxH3|H3.*(?:Video|Ref)|VDN-H3`)
	reImageClass   = regexp.MustCompile(`(?i)SaveImage|KSampler|Empty.*LatentImage`)
	reAudioClass   = regexp.MustCompile(`(?i)SaveAudio|VAE.*Audio|Audio`)
	reTextNode     = regexp.MustCompile(`(?i)CLIPTextEncode`)
	rePositiveTitl = regexp.MustCompile(`(?i)positive|正向|正面`)
	reNegativeTitl = regexp.MustCompile(`(?i)negative|负向|负面`)
	reLatent       = regexp.MustCompile(`(?i)Empty.*LatentImage`)
	reKSampler     = regexp.MustCompile(`(?i)KSampler`)
	reRandomNoise  = regexp.MustCompile(`(?i)RandomNoise`)
	reBasicSched   = regexp.MustCompile(`(?i)BasicScheduler`)
	reLoadImage    = regexp.MustCompile(`(?i)^LoadImage`)
	reVideoNode    = regexp.MustCompile(`(?i)WanImageToVideo|WanFirstLastFrame|WanVace|MiniMaxH3|MinimaxH3|VDN-H3|H3`)
	reH3Node       = regexp.MustCompile(`(?i)MiniMaxH3|MinimaxH3|H3`)
	reRefImage     = regexp.MustCompile(`^ref_images\.ref_image_(\d+)$`)
	reHasSave      = regexp.MustCompile(`(?i)SaveImage|SaveVideo|VHS_VideoCombine|SaveWebM`)
	rePromptInput  = regexp.MustCompile(`(?i)^(prompt|positive|text)$`)
)

// InferType guesses the capability of a graph, honoring an explicit hint.
func InferType(graph Graph, hint string) string {
	switch hint {
	case "image", "video", "audio", "text":
		return hint
	}
	var classes strings.Builder
	for _, node := range graph {
		if object, ok := node.(map[string]any); ok {
			classes.WriteString(Node(object).ClassType())
			classes.WriteByte(' ')
		}
	}
	joined := classes.String()
	if reVideoClass.MatchString(joined) {
		return "video"
	}
	// Audio is checked before image because audio graphs still sample with a KSampler,
	// which would otherwise classify MiniMax Music workflows as image-to-image graphs.
	if reAudioClass.MatchString(joined) {
		return "audio"
	}
	if reImageClass.MatchString(joined) {
		return "image"
	}
	return "text"
}

// TypePrefixes maps a capability to the filename/name prefix it should carry.
var TypePrefixes = map[string]string{
	"image": "image",
	"video": "video",
	"audio": "audio",
	"text":  "text",
}

// EnsureTypePrefix forces a workflow name to start with its type prefix.
func EnsureTypePrefix(name, workflowType string) string {
	prefix := TypePrefixes[workflowType]
	if prefix == "" {
		prefix = "text"
	}
	value := strings.TrimSpace(name)
	if value == "" {
		value = "workflow"
	}
	if strings.HasPrefix(strings.ToLower(value), prefix+"-") {
		return value
	}
	return prefix + "-" + value
}

// bindingBuilder accumulates bindings while preserving insertion order, because
// the resulting JSON is written back to registry.json and diffs should be stable.
type bindingBuilder struct {
	keys   []string
	values map[string]OrderedMap[any]
}

func newBindingBuilder() *bindingBuilder {
	return &bindingBuilder{values: map[string]OrderedMap[any]{}}
}

func (b *bindingBuilder) Has(key string) bool {
	_, ok := b.values[key]
	return ok
}

func (b *bindingBuilder) Get(key string) (OrderedMap[any], bool) {
	value, ok := b.values[key]
	return value, ok
}

// Set inserts or replaces a binding; replacing keeps the original position, which
// matches JavaScript object assignment semantics.
func (b *bindingBuilder) Set(key string, value OrderedMap[any]) {
	if _, exists := b.values[key]; !exists {
		b.keys = append(b.keys, key)
	}
	b.values[key] = value
}

// Result converts the accumulated bindings into the registry representation.
func (b *bindingBuilder) Result() OrderedMap[OrderedMap[any]] {
	out := OrderedMap[OrderedMap[any]]{Values: map[string]OrderedMap[any]{}}
	for _, key := range b.keys {
		out.Keys = append(out.Keys, key)
		out.Values[key] = b.values[key]
	}
	return out
}

// kv builds an ordered object from alternating key/value arguments.
func kv(pairs ...any) OrderedMap[any] {
	out := OrderedMap[any]{Values: map[string]any{}}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, _ := pairs[i].(string)
		out.Keys = append(out.Keys, key)
		out.Values[key] = pairs[i+1]
	}
	return out
}

// InferBindings derives a workable binding set from a graph.
//
// This runs when a workflow is imported from ComfyUI without hand-authored
// bindings. It is best-effort by design: the result is reviewed and adjusted in
// registry.json, so it should pick obvious nodes and stay silent otherwise.
func InferBindings(graph Graph) OrderedMap[OrderedMap[any]] {
	nodes := graph.SortedNodes()
	out := newBindingBuilder()

	// Text encoders: pick a positive and a negative encoder when present.
	var textNodes []struct {
		ID   string
		Node Node
	}
	for _, entry := range nodes {
		if reTextNode.MatchString(entry.Node.ClassType()) {
			textNodes = append(textNodes, entry)
		}
	}
	if len(textNodes) > 0 {
		positive := textNodes[0]
		for _, candidate := range textNodes {
			if rePositiveTitl.MatchString(candidate.Node.Title()) {
				positive = candidate
				break
			}
		}
		var negative *struct {
			ID   string
			Node Node
		}
		for i := range textNodes {
			if reNegativeTitl.MatchString(textNodes[i].Node.Title()) {
				negative = &textNodes[i]
				break
			}
		}
		if negative == nil {
			for i := range textNodes {
				if textNodes[i].ID != positive.ID {
					negative = &textNodes[i]
					break
				}
			}
		}

		positiveInputs := positive.Node.Inputs()
		if _, ok := positiveInputs["text"].(string); ok {
			out.Set("prompt", kv(keyNode, positive.ID, "input", "text"))
		} else if _, ok := positiveInputs["text_g"].(string); ok {
			out.Set("prompt", kv(keyNode, positive.ID, "input", "text_g"))
			out.Set("prompt2", kv(keyNode, positive.ID, "input", "text_l"))
		}

		if negative != nil {
			negativeInputs := negative.Node.Inputs()
			if _, ok := negativeInputs["text"].(string); ok {
				out.Set("negative", kv(keyNode, negative.ID, "input", "text", keyDefault, ""))
			} else if _, ok := negativeInputs["text_g"].(string); ok {
				out.Set("negative", kv(keyNode, negative.ID, "input", "text_g", keyDefault, ""))
				out.Set("negative2", kv(keyNode, negative.ID, "input", "text_l", keyDefault, ""))
			}
		}
	}

	if latent := firstNode(nodes, reLatent); latent != nil {
		inputs := latent.Node.Inputs()
		if width, ok := inputs["width"].(float64); ok {
			out.Set("width", kv(keyNode, latent.ID, "input", "width", keyFrom, "size.width", keyDefault, width))
		}
		if height, ok := inputs["height"].(float64); ok {
			out.Set("height", kv(keyNode, latent.ID, "input", "height", keyFrom, "size.height", keyDefault, height))
		}
		if _, ok := inputs["batch_size"].(float64); ok {
			out.Set("batch", kv(keyNode, latent.ID, "input", "batch_size", keyFrom, "count", keyDefault, inputs["batch_size"]))
		}
	}

	if sampler := firstNode(nodes, reKSampler); sampler != nil {
		inputs := sampler.Node.Inputs()
		if seed, ok := inputs["seed"].(float64); ok {
			out.Set("seed", kv(keyNode, sampler.ID, "input", "seed", keyRandom, true, keyDefault, seed))
		}
		if steps, ok := inputs["steps"].(float64); ok {
			out.Set("steps", kv(keyNode, sampler.ID, "input", "steps", keyFrom, "quality.steps", keyDefault, steps))
		}
		if cfg, ok := inputs["cfg"].(float64); ok {
			out.Set("cfg", kv(keyNode, sampler.ID, "input", "cfg", keyFrom, "quality.cfg", keyDefault, cfg))
		}
		if denoise, ok := inputs["denoise"].(float64); ok && hasLoadImage(nodes) {
			out.Set("denoise", kv(keyNode, sampler.ID, "input", "denoise", keyFrom, "params.denoise", keyDefault, denoise))
		}
	}

	if _, exists := out.Get("seed"); !exists {
		if noise := firstNode(nodes, reRandomNoise); noise != nil {
			if _, has := noise.Node.Inputs()["noise_seed"]; has {
				out.Set("seed", kv(keyNode, noise.ID, "input", "noise_seed", keyRandom, true))
			}
		}
	}
	if _, exists := out.Get("steps"); !exists {
		if scheduler := firstNode(nodes, reBasicSched); scheduler != nil {
			if steps, ok := scheduler.Node.Inputs()["steps"].(float64); ok {
				out.Set("steps", kv(keyNode, scheduler.ID, "input", "steps", keyFrom, "quality.steps", keyDefault, steps))
			}
		}
	}

	var loadImages []struct {
		ID   string
		Node Node
	}
	for _, entry := range nodes {
		if reLoadImage.MatchString(entry.Node.ClassType()) {
			loadImages = append(loadImages, entry)
		}
	}
	if len(loadImages) > 0 {
		out.Set("image", kv(keyNode, loadImages[0].ID, "input", "image", keyFrom, "image", keyImageIndex, 0))
	}
	if len(loadImages) > 1 {
		out.Set("image2", kv(keyNode, loadImages[1].ID, "input", "image", keyFrom, "image", keyImageIndex, 1))
	}

	// Video nodes carry size, length and reference media inputs under varying names.
	for _, entry := range nodes {
		if !reVideoNode.MatchString(entry.Node.ClassType()) {
			continue
		}
		isH3 := reH3Node.MatchString(entry.Node.ClassType())
		inputs := entry.Node.Inputs()
		nodeFPS, hasFPS := inputs["fps"].(float64)
		if !hasFPS {
			nodeFPS, hasFPS = inputs["frame_rate"].(float64)
		}

		for _, input := range sortedKeys(inputs) {
			low := strings.ToLower(input)
			value := inputs[input]

			if low == "width" || low == "height" {
				if _, exists := out.Get(low); !exists || isH3 {
					pairs := []any{keyNode, entry.ID, "input", input, keyFrom, "size." + low}
					if number, ok := value.(float64); ok {
						pairs = append(pairs, keyDefault, number)
					}
					if isH3 {
						pairs = append(pairs, keyTransform, "snap32")
					}
					out.Set(low, kv(pairs...))
				}
			}

			if low == "length" || low == "num_frames" || low == "frames" {
				if _, exists := out.Get("length"); !exists {
					transform := "secondsToFrames"
					fps := 16.0
					if isH3 {
						transform = "secondsToFrames17"
						fps = 24
					}
					if hasFPS {
						fps = nodeFPS
					}
					pairs := []any{keyNode, entry.ID, "input", input, keyFrom, "seconds", keyTransform, transform, keyFPS, fps}
					if number, ok := value.(float64); ok {
						pairs = append(pairs, keyDefault, number)
					}
					out.Set("length", kv(pairs...))
				}
			}

			if low == "fps" || low == "frame_rate" {
				if _, exists := out.Get("fps"); !exists {
					fallback := 16.0
					if isH3 {
						fallback = 24
					}
					pairs := []any{keyNode, entry.ID, "input", input}
					if hasFPS {
						pairs = append(pairs, keyDefault, nodeFPS)
					} else {
						pairs = append(pairs, keyDefault, fallback)
					}
					out.Set("fps", kv(pairs...))
				}
			}

			if low == "start_image" || low == "start_frame" || low == "first_frame" {
				if _, exists := out.Get("image"); !exists {
					out.Set("image", kv(keyNode, entry.ID, "input", input, keyFrom, "image", keyImageIndex, 0))
				}
			}
			if low == "end_image" || low == "last_frame" {
				if _, exists := out.Get("image2"); !exists {
					out.Set("image2", kv(keyNode, entry.ID, "input", input, keyFrom, "image", keyImageIndex, 1))
				}
			}

			if match := reRefImage.FindStringSubmatch(low); match != nil {
				index, _ := strconv.Atoi(match[1])
				key := "image"
				if index != 0 {
					key = "image" + strconv.Itoa(index+1)
				}
				if _, exists := out.Get(key); !exists {
					out.Set(key, kv(keyNode, entry.ID, "input", input, keyFrom, "image", keyImageIndex, index))
				}
			}
		}
	}

	if _, exists := out.Get("prompt"); !exists {
		for _, entry := range nodes {
			found := false
			for _, input := range sortedKeys(entry.Node.Inputs()) {
				if _, isString := entry.Node.Inputs()[input].(string); !isString {
					continue
				}
				if rePromptInput.MatchString(input) {
					out.Set("prompt", kv(keyNode, entry.ID, "input", input))
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}

	return out.Result()
}

// firstNode returns the first node whose class matches, in sorted id order.
func firstNode(nodes []struct {
	ID   string
	Node Node
}, pattern *regexp.Regexp) *struct {
	ID   string
	Node Node
} {
	for i := range nodes {
		if pattern.MatchString(nodes[i].Node.ClassType()) {
			return &nodes[i]
		}
	}
	return nil
}

func hasLoadImage(nodes []struct {
	ID   string
	Node Node
}) bool {
	for _, entry := range nodes {
		if reLoadImage.MatchString(entry.Node.ClassType()) {
			return true
		}
	}
	return false
}

// SortedKeys returns the keys of a JSON object in sorted order.
func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	// Deterministic order matters: which input wins inference should not depend
	// on Go's randomized map iteration.
	sort.Strings(keys)
	return keys
}

// OutputWarnings reports graphs that will produce nothing retrievable.
func OutputWarnings(graph Graph) []string {
	var warnings []string
	hasSave := false
	for _, node := range graph {
		if object, ok := node.(map[string]any); ok && reHasSave.MatchString(Node(object).ClassType()) {
			hasSave = true
			break
		}
	}
	if !hasSave {
		warnings = append(warnings, "no SaveImage / SaveVideo / VHS_VideoCombine output node found; results may not be retrievable.")
	}
	return warnings
}

// BindingWarnings reports bindings that cannot possibly have an effect.
func BindingWarnings(graph Graph, bindings OrderedMap[OrderedMap[any]]) []string {
	var warnings []string
	for _, key := range bindings.Keys {
		binding := NewBinding(bindings.Values[key].Values)
		nodeID := binding.String(keyNode)
		input := binding.String(keyInput)
		if nodeID == "" || input == "" {
			warnings = append(warnings, "binding "+key+" has no node/input.")
			continue
		}
		node, ok := graph.NodeAt(nodeID)
		if !ok {
			warnings = append(warnings, "binding "+key+" points at missing node "+nodeID+".")
			continue
		}
		if _, exists := node.Inputs()[input]; !exists {
			warnings = append(warnings, "binding "+key+" points at "+nodeID+"."+input+" but that input does not exist.")
		}
	}
	return warnings
}
