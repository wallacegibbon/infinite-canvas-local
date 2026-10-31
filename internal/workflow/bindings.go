package workflow

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/util"
)

// Media is a file that has been uploaded to ComfyUI's input directory.
type Media struct {
	Name      string
	Subfolder string
}

// Reference renders the value a LoadImage-style node expects: subfolder/name.
func (m Media) Reference() string {
	if m.Subfolder != "" {
		return m.Subfolder + "/" + m.Name
	}
	return m.Name
}

// Context is the normalized request, the single input that bindings read from.
type Context struct {
	Prompt         string
	Negative       *string
	Size           *util.Size
	OutputSize     *util.Size
	Count          int
	Quality        map[string]any
	Seconds        *float64
	Mode           string
	Background     string
	Images         []UploadResult
	UploadedImages []Media
	UploadedVideos []Media
	UploadedAudios []Media
	Params         map[string]any

	// SourceVideoFrames is the frame count of the uploaded source video, used by
	// character-swap workflows so output length follows input length.
	SourceVideoFrames int
}

// UploadResult keeps the original upload metadata alongside the ComfyUI reference.
type UploadResult struct {
	Filename     string
	OriginalName string
	Mimetype     string
	Bytes        int
}

// BuildContextParams collects the raw request fields.
type BuildContextParams struct {
	Prompt         string
	Negative       *string
	Size           string
	Count          float64
	Quality        string
	Seconds        float64
	Mode           string
	Background     string
	Images         []UploadResult
	UploadedImages []Media
	UploadedVideos []Media
	UploadedAudios []Media
	Params         map[string]any
	Presets        map[string]any
}

// BuildContext normalizes a request into a Context, mirroring the old buildContext.
func BuildContext(p BuildContextParams) *Context {
	qualityValue := strings.ToLower(strings.TrimSpace(p.Quality))
	if qualityValue == "" {
		qualityValue = "auto"
	}
	quality := map[string]any{"value": qualityValue}
	if preset := qualityPreset(p.Presets, qualityValue); preset != nil {
		for key, value := range preset {
			quality[key] = value
		}
	}

	ctx := &Context{
		Prompt:         p.Prompt,
		Negative:       p.Negative,
		Count:          util.Clamp(int(p.Count), 1, 15),
		Quality:        quality,
		Mode:           p.Mode,
		Background:     p.Background,
		Images:         p.Images,
		UploadedImages: p.UploadedImages,
		UploadedVideos: p.UploadedVideos,
		UploadedAudios: p.UploadedAudios,
		Params:         p.Params,
	}
	if ctx.Params == nil {
		ctx.Params = map[string]any{}
	}
	if p.Count == 0 {
		ctx.Count = 1
	}
	if parsed := util.ParseSize(p.Size); parsed != nil {
		ctx.Size = parsed
	}
	if p.Seconds > 0 {
		seconds := p.Seconds
		ctx.Seconds = &seconds
	}
	if editCanvas, ok := ctx.Params["editCanvasSize"].(string); ok {
		ctx.OutputSize = util.ParseSize(editCanvas)
	}
	return ctx
}

// qualityPreset digs out presets.quality.<value>.
func qualityPreset(presets map[string]any, value string) map[string]any {
	quality, ok := presets["quality"].(map[string]any)
	if !ok {
		return nil
	}
	preset, ok := quality[value].(map[string]any)
	if !ok {
		return nil
	}
	return preset
}

// fields renders the Context as a map so that `from` paths resolve generically.
// Keys explicitly omitted map to JS `undefined`, which bindings treat as "absent".
func (c *Context) fields() map[string]any {
	out := map[string]any{
		"prompt": c.Prompt,
		"count":  c.Count,
		"params": c.Params,
		"images": c.Images,
	}
	out["quality"] = c.Quality
	if c.Negative != nil {
		out["negative"] = *c.Negative
	}
	if c.Size != nil {
		out["size"] = map[string]any{
			"width":  c.Size.Width,
			"height": c.Size.Height,
			"raw":    c.Size.Raw,
		}
	}
	if c.OutputSize != nil {
		out["outputSize"] = map[string]any{
			"width":  c.OutputSize.Width,
			"height": c.OutputSize.Height,
			"raw":    c.OutputSize.Raw,
		}
	}
	if c.Seconds != nil {
		out["seconds"] = *c.Seconds
	}
	if c.Mode != "" {
		out["mode"] = c.Mode
	}
	if c.Background != "" {
		out["background"] = c.Background
	}
	if c.SourceVideoFrames > 0 {
		out["sourceVideoFrames"] = c.SourceVideoFrames
	}
	return out
}

// resolvePath walks a dotted path through the context map.
func resolvePath(root map[string]any, path string) (any, bool) {
	var current any = root
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// randomSeed returns a seed in ComfyUI's accepted range [1, 2147483647).
func randomSeed() int {
	limit := big.NewInt(2147483646)
	value, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return 1
	}
	return int(value.Int64()) + 1
}

// ApplyBindings copies the graph and writes request values into it.
//
// The returned graph is a fresh copy: workflows on disk are never mutated, so two
// concurrent requests cannot observe each other's parameters.
func ApplyBindings(graph Graph, entry *Entry, ctx *Context) (Graph, error) {
	next := graph.Clone()
	context := ctx.fields()

	// Authored defaults, keyed "nodeId.input".
	for key, value := range entry.Defaults {
		nodeID, input, ok := strings.Cut(key, ".")
		if !ok {
			continue
		}
		node, exists := next.NodeAt(nodeID)
		if !exists {
			continue
		}
		inputs := node.Inputs()
		if _, present := inputs[input]; !present {
			continue
		}
		inputs[input] = value
	}

	for _, key := range entry.RawBindings.Keys {
		binding := NewBinding(entry.RawBindings.Values[key].Values)
		if err := applyOneBinding(next, entry, ctx, context, key, binding); err != nil {
			return nil, err
		}
	}

	if err := applyOutputSize(next, entry, ctx, context); err != nil {
		return nil, err
	}
	return next, nil
}

func applyOneBinding(next Graph, entry *Entry, ctx *Context, context map[string]any, key string, binding *Binding) error {
	nodeID := binding.String(keyNode)
	input := binding.String(keyInput)
	if nodeID == "" || input == "" {
		return nil
	}
	node, exists := next.NodeAt(nodeID)
	if !exists {
		return nil
	}
	_, hasInput := node.Inputs()[input]
	isImageBinding := binding.String(keyFrom) == "image" || binding.Has(keyImageIndex)
	isArray := binding.Bool(keyArray)

	if !hasInput && !isImageBinding && !isArray {
		return nil
	}

	switch {
	case binding.Bool(keyFrameMode):
		return applyFrameMode(next, entry, ctx, node, binding)
	case binding.String(keyFrom) == "video" && !isArray:
		return applySingleVideo(entry, ctx, node, binding, input)
	case isArray:
		return applyArrayBinding(next, entry, ctx, node, binding)
	case isImageBinding:
		return applyImageBinding(next, entry, ctx, node, binding, input)
	default:
		return applyScalarBinding(entry, context, key, binding, node, input)
	}
}

// applyFrameMode wires first/last frame inputs by injecting LoadImage nodes.
//
// H3 exposes first_frame/last_frame as IMAGE sockets, so a bare filename will not
// do: each frame needs a real loader node connected to the socket.
func applyFrameMode(next Graph, entry *Entry, ctx *Context, node Node, binding *Binding) error {
	maxImages := binding.Int(keyMax, 2)
	images := ctx.UploadedImages
	if len(images) > maxImages {
		images = images[:maxImages]
	}
	mode := util.StringField(ctx.Params, "frameMode")
	if mode == "" {
		if len(images) >= 2 {
			mode = "both"
		} else {
			mode = "first"
		}
	}

	inputs := node.Inputs()
	firstInput := defaultString(binding.String(keyFirstInput), "first_frame")
	lastInput := defaultString(binding.String(keyLastInput), "last_frame")
	delete(inputs, firstInput)
	delete(inputs, lastInput)

	if mode == "text" {
		return nil
	}

	need := 1
	if mode == "both" {
		need = 2
	}
	if len(images) < need {
		return httpx.BadRequest("missing_reference_image",
			fmt.Sprintf("first/last frame mode needs %d image(s) but the request carried %d.", need, len(images)))
	}

	targets := make([]struct {
		input string
		media Media
	}, 0, 2)
	switch mode {
	case "first":
		targets = append(targets, struct {
			input string
			media Media
		}{firstInput, images[0]})
	case "last":
		targets = append(targets, struct {
			input string
			media Media
		}{lastInput, images[0]})
	case "both":
		targets = append(targets,
			struct {
				input string
				media Media
			}{firstInput, images[0]},
			struct {
				input string
				media Media
			}{lastInput, images[1]},
		)
	}

	for _, target := range targets {
		loadNodeID := nextGatewayLoadID(next)
		next[loadNodeID] = map[string]any{
			"class_type": "LoadImage",
			"inputs":     map[string]any{"image": target.media.Reference()},
			"_meta":      map[string]any{"title": "Gateway Frame " + target.input},
		}
		inputs[target.input] = asLink(loadNodeID, 0)
	}
	return nil
}

// applySingleVideo writes one video reference into a node input.
func applySingleVideo(entry *Entry, ctx *Context, node Node, binding *Binding, input string) error {
	index := binding.Int(keyVideoIndex, 0)
	var reference string
	if index >= 0 && index < len(ctx.UploadedVideos) {
		reference = ctx.UploadedVideos[index].Reference()
	}
	if reference != "" {
		node.Inputs()[input] = reference
		return nil
	}
	if binding.Bool(keyRequired) {
		return httpx.BadRequest("missing_reference_media", fmt.Sprintf("workflow %s requires an input video.", entry.Name))
	}
	return nil
}

// applyArrayBinding wires a list of references (images, videos or audio clips).
func applyArrayBinding(next Graph, entry *Entry, ctx *Context, node Node, binding *Binding) error {
	from := binding.String(keyFrom)
	loadType := binding.String(keyLoadType)
	isVideo := loadType == "video" || from == "video"
	isAudio := loadType == "audio" || from == "audio"

	media := ctx.UploadedImages
	switch {
	case isVideo:
		media = ctx.UploadedVideos
	case isAudio:
		media = ctx.UploadedAudios
	}

	maxItems := binding.Int(keyMax, 8)
	selected := media
	if len(selected) > maxItems {
		selected = selected[:maxItems]
	}
	minItems := binding.Int(keyMin, 0)
	if len(selected) < minItems {
		return httpx.BadRequest("missing_reference_media",
			fmt.Sprintf("the workflow needs at least %d reference media item(s) but the request carried %d.", minItems, len(selected)))
	}

	prefix := binding.String(keyInputPrefix)
	if prefix == "" {
		prefix = binding.String(keyInput)
	}
	if prefix == "" {
		prefix = "ref_images.ref_image_"
	}
	indexBase := binding.Int(keyIndexBase, 0)

	inputs := node.Inputs()
	for name := range inputs {
		if strings.HasPrefix(name, prefix) {
			delete(inputs, name)
		}
	}

	nativeVideoLoader := isVideo && binding.String(keyLoader) == "native"
	audioPrefix := binding.String(keyAudioPrefix)
	sourceLengthInput := binding.String(keySourceLengthInput)
	sourceLengthOutput := binding.Int(keySourceLengthOutput, 1)

	for index, item := range selected {
		loadNodeID := nextGatewayLoadID(next)

		if nativeVideoLoader {
			next[loadNodeID] = map[string]any{
				"class_type": "LoadVideo",
				"inputs":     map[string]any{"file": item.Reference()},
				"_meta":      map[string]any{"title": fmt.Sprintf("Gateway Video %d", index+1)},
			}
			componentsID := nextGatewayLoadID(next)
			next[componentsID] = map[string]any{
				"class_type": "GetVideoComponents",
				"inputs":     map[string]any{"video": asLink(loadNodeID, 0)},
				"_meta":      map[string]any{"title": fmt.Sprintf("Gateway Video Frames %d", index+1)},
			}
			inputs[fmt.Sprintf("%s%d", prefix, indexBase+index)] = asLink(componentsID, 0)
			if sourceLengthInput != "" && ctx.SourceVideoFrames > 0 {
				inputs[sourceLengthInput] = ctx.SourceVideoFrames
			}
			continue
		}

		switch {
		case isVideo:
			next[loadNodeID] = map[string]any{
				"class_type": "VHS_LoadVideo",
				"inputs": map[string]any{
					"video":             item.Reference(),
					"force_rate":        binding.Float(keyForceRate, 0),
					"custom_width":      0,
					"custom_height":     0,
					"frame_load_cap":    0,
					"skip_first_frames": 0,
					"select_every_nth":  1,
					"format":            "AnimateDiff",
				},
				"_meta": map[string]any{"title": fmt.Sprintf("Gateway Video %d", index+1)},
			}
		case isAudio:
			next[loadNodeID] = map[string]any{
				"class_type": "LoadAudio",
				"inputs":     map[string]any{"audio": item.Reference()},
				"_meta":      map[string]any{"title": fmt.Sprintf("Gateway Audio %d", index+1)},
			}
		default:
			next[loadNodeID] = map[string]any{
				"class_type": "LoadImage",
				"inputs":     map[string]any{"image": item.Reference()},
				"_meta":      map[string]any{"title": fmt.Sprintf("Gateway Ref %d", index+1)},
			}
		}

		inputs[fmt.Sprintf("%s%d", prefix, indexBase+index)] = asLink(loadNodeID, 0)
		if isVideo && audioPrefix != "" {
			inputs[fmt.Sprintf("%s%d", audioPrefix, indexBase+index)] = asLink(loadNodeID, 2)
		}
		if isVideo && sourceLengthInput != "" {
			inputs[sourceLengthInput] = asLink(loadNodeID, sourceLengthOutput)
		}
	}
	return nil
}

// applyImageBinding writes one reference image, either as a filename or a link.
func applyImageBinding(next Graph, entry *Entry, ctx *Context, node Node, binding *Binding, input string) error {
	index := binding.Int(keyImageIndex, 0)
	var media *Media
	if index >= 0 && index < len(ctx.UploadedImages) {
		media = &ctx.UploadedImages[index]
	}
	if media == nil {
		if binding.Bool(keyRequired) {
			return httpx.BadRequest("missing_reference_image",
				fmt.Sprintf("workflow %s requires reference image #%d but the request did not provide it.", entry.Name, index+1))
		}
		return nil
	}

	if !binding.Bool(keyAsLink) {
		node.Inputs()[input] = media.Reference()
		return nil
	}

	// asLink: the input is an IMAGE socket, so a loader node is required.
	loadNodeID := binding.String(keyLoadNodeID)
	if loadNodeID == "" {
		loadNodeID = next.NextNodeID(9001)
	}
	next[loadNodeID] = map[string]any{
		"class_type": "LoadImage",
		"inputs":     map[string]any{"image": media.Reference()},
		"_meta":      map[string]any{"title": fmt.Sprintf("Gateway Load %d", index+1)},
	}
	node.Inputs()[input] = asLink(loadNodeID, 0)
	return nil
}

// applyScalarBinding writes a plain value (text, number, enum) into a node input.
func applyScalarBinding(entry *Entry, context map[string]any, key string, binding *Binding, node Node, input string) error {
	var value any
	var present bool
	if from := binding.String(keyFrom); from != "" {
		value, present = resolvePath(context, from)
	} else {
		value, present = context[key]
	}
	if !present {
		value = nil
	}

	missing := isEmpty(value)
	if missing && binding.Bool(keyRandom) {
		value = randomSeed()
		missing = false
	}
	if missing && binding.Has(keyDefault) {
		value = binding.Value(keyDefault)
		missing = isEmpty(value)
	}
	if missing {
		if binding.Bool(keyRequired) {
			return httpx.BadRequest("missing_parameter", "missing required parameter: "+key)
		}
		return nil
	}

	if mapped := binding.Object(keyMap); mapped != nil {
		if replacement, ok := mapped[mapKey(value)]; ok {
			value = replacement
		}
	}
	node.Inputs()[input] = applyTransform(value, binding)
	return nil
}

// applyOutputSize aligns the latent canvas with the requested output size.
func applyOutputSize(next Graph, entry *Entry, ctx *Context, context map[string]any) error {
	if entry.OutputSize == nil {
		return nil
	}
	outputSize := ctx.OutputSize
	if entry.OutputSize.FromSize {
		outputSize = ctx.Size
	}
	if outputSize == nil {
		return nil
	}

	resolution := util.NumberField(ctx.Params, "resolution", 1024)
	nativeResolution := int(math.Max(256, jsRound(resolution/32)*32))
	skipNativeSquare := entry.OutputSize.SkipNativeSquare &&
		outputSize.Width == outputSize.Height &&
		int(jsRound(float64(outputSize.Width)/32)*32) == nativeResolution
	if skipNativeSquare {
		return nil
	}

	sizeNode, okSize := next.NodeAt(entry.OutputSize.Node)
	latentNode, okLatent := next.NodeAt(entry.OutputSize.LatentNode)
	if !okSize || !okLatent {
		return nil
	}
	snap := func(value int) int {
		return int(math.Max(256, jsRound(float64(value)/32)*32))
	}
	sizeNode.Inputs()["width"] = snap(outputSize.Width)
	sizeNode.Inputs()["height"] = snap(outputSize.Height)

	latentInput := defaultString(entry.OutputSize.LatentInput, "latent_image")
	latentSource := defaultString(entry.OutputSize.LatentSource, entry.OutputSize.Node)
	latentNode.Inputs()[latentInput] = asLink(latentSource, 0)
	return nil
}

// applyTransform implements the named numeric coercions used by bindings.
func applyTransform(value any, binding *Binding) any {
	switch binding.String(keyTransform) {
	case "secondsToFrames":
		return secondsToFrames(toFloat(value), binding.Float(keyFPS, 16))
	case "secondsToFrames17":
		return secondsToFrames17(toFloat(value), binding.Float(keyFPS, 24))
	case "snap16":
		return int(math.Max(16, math.Ceil(toFloat(value)/16)*16))
	case "snap32":
		return int(math.Max(32, jsRound(toFloat(value)/32)*32))
	case "round":
		return int(jsRound(toFloat(value)))
	case "int":
		return int(math.Trunc(toFloat(value)))
	case "invert10":
		return 10 - toFloat(value)
	case "multiply":
		// A derived value written into another node (RIFE's output frame rate is the source
		// rate times the multiplier). `fps` is the same idea: a scalar config on the binding.
		return toFloat(value) * binding.Float("factor", 1)
	case "boolean":
		return toBool(value)
	}
	return value
}

// secondsToFrames converts seconds to a 4n+1 frame count, which Wan/LTX require.
func secondsToFrames(seconds, fps float64) int {
	raw := math.Max(1, jsRound(seconds*fps))
	return int(4*jsRound((raw-1)/4) + 1)
}

// secondsToFrames17 converts seconds to a 17k+5 frame count, which H3 requires.
// 124 frames is about 5s at 24fps.
func secondsToFrames17(seconds, fps float64) int {
	raw := math.Max(5, jsRound(seconds*fps))
	remainder := math.Mod(raw, 17)
	return int(raw + math.Mod(5-remainder+17, 17))
}

// jsRound matches JavaScript's Math.round: halves round toward positive infinity,
// unlike Go's math.Round which rounds halves away from zero.
func jsRound(value float64) float64 {
	return math.Floor(value + 0.5)
}

// nextGatewayLoadID allocates a node id for an injected loader, starting at 9101.
func nextGatewayLoadID(graph Graph) string {
	id := 9101
	for {
		candidate := strconv.Itoa(id)
		if _, exists := graph[candidate]; !exists {
			return candidate
		}
		id++
	}
}

// isEmpty reproduces the JS `value === undefined || value === null || value === ""`.
func isEmpty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	}
	return false
}

// mapKey reproduces JS object-key coercion for map lookups.
func mapKey(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		if typed == math.Trunc(typed) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	}
	return fmt.Sprint(value)
}

// toFloat coerces a JS value to a number the way Number() would for our inputs.
func toFloat(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return math.NaN()
		}
		return parsed
	case bool:
		if typed {
			return 1
		}
		return 0
	}
	return math.NaN()
}

func toBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case nil:
		return false
	}
	return true
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
