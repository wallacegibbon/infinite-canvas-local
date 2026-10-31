package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Task modes accepted by /api/prompts/optimize. These are H3 task types, and the
// prompt template differs per mode, so the set is closed.
var taskModes = map[string]bool{
	"全参考模式(Reference to Video)": true,
	"文生视频(T2VA)":                true,
	"首帧图生视频(I2VA)":              true,
	"首尾帧视频(FL2VA)":              true,
	"尾帧图生视频(L2VA)":              true,
}

var (
	// Go's regexp engine has no backreferences, so the closing tag is spelled out as
	// an alternation. Matching a mismatched pair is harmless: these blocks never nest.
	reThinkBlock  = regexp.MustCompile(`(?is)<(?:think|thinking|analysis|reasoning|scratchpad)(?:\s[^>]*)?>[\s\S]*?</(?:think|thinking|analysis|reasoning|scratchpad)>`)
	reSpecialTok  = regexp.MustCompile(`<\|[^|]{1,60}\|>`)
	reFenceOpen   = regexp.MustCompile(`^\s*` + "```" + `[a-zA-Z0-9_-]*\s*\n?`)
	reFenceClose  = regexp.MustCompile("\n?```\\s*$")
	reMetaLine    = regexp.MustCompile(`(?i)^(?:[-*#>\s]*)?(?:\*\*)?(?:思考过程|思考|分析|推理|说明|解释|备注|注意|Thinking|Analysis|Reasoning|Explanation|Notes?)(?:\*\*)?\s*[:：]`)
	reSubjectDef  = regexp.MustCompile(`(?i)subject_definitions\s*:`)
	reIntegrated  = regexp.MustCompile(`(?i)integrated_multimodal_description\s*:`)
	reSoundscape  = regexp.MustCompile(`(?i)overall_soundscape\s*:|non_diegetic_music\s*:`)
	reTailMeta    = regexp.MustCompile(`(?is)\n{2,}(?:思考过程|分析|推理|解释|备注|说明)\s*[:：][\s\S]*$`)
	reFinalPrefix = regexp.MustCompile(`(?i)^(最终提示词|输出结果|输出|结果|Prompt)\s*[:：]\s*`)
	reShotIndex   = regexp.MustCompile(`(?i)\[Shot\s+(\d+)\]`)
	reAlignPrefix = regexp.MustCompile(`(?i)^(?:For the target video, at 0\.00 seconds[^\n]*|How the reference pictures align[^\n]*)\s*\n+`)
	reFenceAny    = regexp.MustCompile("^```[a-zA-Z]*\\s*")
	reFenceEnd    = regexp.MustCompile("```$")
	reWrapping    = regexp.MustCompile(`^["“「']+|["”」']+$`)
	reCaptionPfx  = regexp.MustCompile(`(?i)^(?:画面描述|图片描述|描述|Caption)\s*[:：]\s*`)
	reMetaPara    = regexp.MustCompile(`(?i)^(?:思考|分析|推理|我们|首先|用户|需求|要求|说明|注|最终|The user|We need|Let me|I need|Analysis|Reasoning|Explanation|Here'?s|Okay|Sure)`)
)

// Endpoint is where a prompt request goes: an OpenAI-compatible chat endpoint.
//
// Pointing this at a local llama.cpp is a supported and tested configuration, which
// is why there is no allow-list of hosts: the gateway binds to loopback by default,
// and that is the actual security boundary.
type Endpoint struct {
	BaseURL string
	APIKey  string
	Model   string
}

// ChatRequest is the normalized input of a plain chat completion: a prompt, optionally a
// system message and images, and where to send it.
//
// Nothing here is a skill: the gateway owns no wording for this endpoint. That is what makes it
// usable by anything in the canvas that needs a model's words rather than an image.
type ChatRequest struct {
	Prompt   string
	System   string
	Images   []string
	Endpoint Endpoint
}

// OptimizeRequest is the normalized input of prompt optimization.
type OptimizeRequest struct {
	Prompt        string
	Mode          string
	Duration      float64
	ReferenceNote string
	Language      string
	Images        []string
	ImageCount    int
	Endpoint      Endpoint
}

// Optimizer owns the H3 skill text and the chat client.
type Optimizer struct {
	skillDir string
	logger   *logx.Logger

	// thinkingParam pins the mechanism; "auto" learns it per endpoint.
	thinkingParam string

	mu      sync.Mutex
	learned map[string]string

	unknownValueWarned sync.Once

	// maxTokens is the completion budget. Thinking models spend most of it on
	// reasoning, so the answer only starts once they stop.
	maxTokens int

	skill     string
	baseEnv   string
	reference string
	loadErr   error
	loaded    bool
}

// NewOptimizer builds an optimizer rooted at the gateway's asset directory.
func NewOptimizer(rootDir string, logger *logx.Logger) *Optimizer {
	return &Optimizer{
		skillDir:      filepath.Join(rootDir, "prompts", "skills", "h3-prompt-writing"),
		logger:        logger,
		thinkingParam: "auto", // config overrides this; see PROMPT_THINKING_PARAM
		learned:       map[string]string{},
		maxTokens:     8192,
	}
}

// thinkingMechanisms are the request parameters that ask an endpoint for a direct
// answer instead of a chain of thought.
//
// None of them belongs to the OpenAI API: chat_template_kwargs is vLLM/SGLang,
// enable_thinking is the llama.cpp/Qwen convention, and reasoning_effort is only
// semi-standard. That is why the gateway sends none of them unless it has to.
var thinkingMechanisms = map[string]func(map[string]any){
	"chat_template_kwargs": func(body map[string]any) {
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": false}
	},
	"enable_thinking":  func(body map[string]any) { body["enable_thinking"] = false },
	"reasoning_effort": func(body map[string]any) { body["reasoning_effort"] = "none" },
}

// defaultThinkingMechanism is what the gateway tries when it detects a thinking model.
const defaultThinkingMechanism = "chat_template_kwargs"

// WithMaxTokens sets the completion budget.
//
// A thinking model writes its chain of thought first, so this budget has to cover
// reasoning plus the answer. Too small and the answer never starts; measured on
// Qwen3.8-27B the same H3 rewrite used ~900 reasoning tokens typically, with a long
// tail, which is why the default is well above the answer's own size.
func (o *Optimizer) WithMaxTokens(tokens int) *Optimizer {
	if tokens > 0 {
		o.maxTokens = tokens
	}
	return o
}

// WithThinkingParam pins the thinking mechanism.
//
// Accepted values: "auto" (default), "none", or one of thinkingMechanisms. "auto"
// sends no vendor field until the endpoint proves it is a thinking model.
func (o *Optimizer) WithThinkingParam(name string) *Optimizer {
	o.thinkingParam = strings.TrimSpace(name)
	return o
}

// thinkingCacheKey identifies one endpoint+model pair.
func thinkingCacheKey(endpoint, model string) string { return endpoint + "|" + model }

// resolveThinking returns the mechanism to use for this endpoint, or "" for none.
func (o *Optimizer) resolveThinking(endpoint, model string) string {
	switch strings.ToLower(strings.TrimSpace(o.thinkingParam)) {
	case "none", "off":
		return ""
	case "chat_template_kwargs", "enable_thinking", "reasoning_effort":
		return strings.ToLower(strings.TrimSpace(o.thinkingParam))
	}
	// "auto" and anything unrecognised: use what worked before, else send nothing.
	value := strings.ToLower(strings.TrimSpace(o.thinkingParam))
	if value != "" && value != "auto" {
		o.unknownValueWarned.Do(func() {
			o.logger.Warn("unknown PROMPT_THINKING_PARAM %q; treating it as auto (send no vendor field until the endpoint asks for one)", o.thinkingParam)
		})
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.learned[thinkingCacheKey(endpoint, model)]
}

// pinnedThinking returns the explicitly configured mechanism, or "" for auto.
func (o *Optimizer) pinnedThinking() string {
	switch strings.ToLower(strings.TrimSpace(o.thinkingParam)) {
	case "chat_template_kwargs", "enable_thinking", "reasoning_effort":
		return strings.ToLower(strings.TrimSpace(o.thinkingParam))
	}
	return ""
}

// learningEnabled reports whether the gateway may adapt per endpoint.
func (o *Optimizer) learningEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(o.thinkingParam)) {
	case "none", "off", "chat_template_kwargs", "enable_thinking", "reasoning_effort":
		return false
	}
	return true
}

// rememberThinking records what this endpoint needs, so the next call is free of the
// failed attempt. Passing "" records that no hint should be sent.
func (o *Optimizer) rememberThinking(endpoint, model, mechanism, reason string) {
	// An explicit setting always wins: observation must not silently override it.
	if !o.learningEnabled() {
		return
	}
	key := thinkingCacheKey(endpoint, model)
	o.mu.Lock()
	previous, seen := o.learned[key]
	o.learned[key] = mechanism
	o.mu.Unlock()
	if seen && previous == mechanism {
		return
	}
	label := mechanism
	if label == "" {
		label = "none"
	}
	o.logger.Info("prompt endpoint thinking mode set for %s (model=%s): %s — %s", endpoint, model, label, reason)
}

// loadSkills reads the H3 writing guide once.
//
// It is roughly 4500 tokens of context, which is why it is read from disk rather
// than embedded in the binary: users can improve it without a rebuild.
func (o *Optimizer) loadSkills() {
	if o.loaded {
		return
	}
	o.loaded = true

	read := func(name string) (string, error) {
		data, err := os.ReadFile(filepath.Join(o.skillDir, name))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	skill, err := read("SKILL.md")
	if err != nil {
		o.loadErr = err
		return
	}
	base, err := read(filepath.Join("references", "base-en.txt"))
	if err != nil {
		o.loadErr = err
		return
	}
	reference, err := read(filepath.Join("references", "ref-en.txt"))
	if err != nil {
		o.loadErr = err
		return
	}
	o.skill, o.baseEnv, o.reference = skill, base, reference
}

// buildSystem composes the H3 system prompt for one task.
func (o *Optimizer) buildSystem(mode string, seconds int, language string) (string, error) {
	o.loadSkills()
	if o.loadErr != nil {
		return "", httpx.NewError(http.StatusInternalServerError, "missing_h3_skill",
			"the H3 prompt skill files are missing; check gateway/prompts/skills/h3-prompt-writing")
	}
	guide := o.baseEnv
	if strings.Contains(mode, "全参考") || strings.Contains(mode, "Ref2VA") || strings.Contains(mode, "Reference to Video") {
		guide = o.baseEnv + "\n\n" + o.reference
	}
	return fmt.Sprintf(`%s

%s

## 本次任务
模式：%s
时长：%d 秒
输出语言：%s
参考图片（若有）已随消息附上：以图片内容为准，不要编造图中不存在的室内外、家具、陈设或光线。
对齐句由系统在正文前补充，正文不要另写。
只输出最终 H3 提示词正文，不要输出思考过程、分析、解释、Markdown 代码块或前后缀。`,
		o.skill, guide, mode, seconds, languageDirective(language)), nil
}

func languageDirective(language string) string {
	if normalizeLanguage(language) == "en" {
		return "Write every field body in English. Keep dialogue, lyrics, and on-screen visible text in their original language; Chinese dialogue stays Chinese inside <d>[Chinese]...</d>. Official field names stay English."
	}
	return "字段正文全部使用中文（覆盖 skill 默认英文）；台词、旁白、歌词保留原文语言（中文仍为中文）；官方字段名保留英文。"
}

// modeToken maps a task mode to an ASCII token for log lines.
//
// The mode values are protocol strings the frontend sends and they contain Chinese;
// embedding them in a log would break the English-only backend rule.
func modeToken(mode string) string {
	switch strings.TrimSpace(mode) {
	case "全参考模式(Reference to Video)":
		return "reference"
	case "文生视频(T2VA)":
		return "t2va"
	case "首帧图生视频(I2VA)":
		return "i2va"
	case "首尾帧视频(FL2VA)":
		return "fl2va"
	case "尾帧图生视频(L2VA)":
		return "l2va"
	case "文生图":
		return "t2i"
	case "图生图":
		return "i2i"
	}
	if strings.TrimSpace(mode) == "" {
		return "unknown"
	}
	return "other"
}

func normalizeLanguage(value string) string {
	if strings.ToLower(strings.TrimSpace(value)) == "en" {
		return "en"
	}
	return "zh"
}

// Optimize rewrites a user prompt into an H3 prompt.
func (o *Optimizer) Optimize(ctx context.Context, req OptimizeRequest) (map[string]any, error) {
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		return nil, httpx.BadRequest("missing_prompt", "prompt is required")
	}
	if len([]rune(text)) > 8000 {
		return nil, httpx.BadRequest("prompt_too_long", "prompt is too long")
	}
	taskType := strings.TrimSpace(req.Mode)
	if taskType == "" {
		taskType = "文生视频(T2VA)"
	}
	if !taskModes[taskType] {
		return nil, httpx.BadRequest("invalid_mode", "unsupported task type: "+taskType)
	}

	seconds := req.Duration
	if seconds <= 0 {
		seconds = 5
	}
	if seconds > 15 {
		seconds = 15
	}
	if seconds < 1 {
		seconds = 1
	}
	language := normalizeLanguage(req.Language)

	images := req.Images
	if len(images) > 8 {
		images = images[:8]
	}
	system, err := o.buildSystem(taskType, int(seconds), language)
	if err != nil {
		return nil, err
	}

	userText := buildOptimizeUserText(req, taskType, language, len(images))
	content, model, elapsed, err := o.chat(ctx, "optimize/"+modeToken(taskType), system, userText, images, req.Endpoint)
	if err != nil {
		return nil, err
	}

	sanitized := sanitizeH3(content)
	if !looksLikeH3Prompt(sanitized) {
		o.logger.Warn("prompt optimize: the reply carried no H3 fields after sanitising (purpose=optimize/%s, head): %s",
			modeToken(taskType), util.Truncate(strings.TrimSpace(content), 300))
		return nil, httpx.NewError(http.StatusBadGateway, "no_prompt_output",
			fmt.Sprintf("the model answered (%d chars) but the reply contained none of the required H3 fields "+
				"(subject_definitions / integrated_multimodal_description). First 160 chars: %s",
				len([]rune(content)), util.Truncate(strings.TrimSpace(content), 160)))
	}
	speaker := EnsureSpeakerTags(sanitized)

	return map[string]any{
		"prompt":    ensureFrameAlignmentPrompt(speaker.Prompt, taskType, seconds),
		"mode":      taskType,
		"duration":  seconds,
		"model":     model,
		"elapsedMs": elapsed,
		"speakerTags": map[string]any{
			"fixed":  speaker.Fixed,
			"issues": speaker.Issues,
		},
	}, nil
}

// buildOptimizeUserText assembles the user turn for prompt optimization.
func buildOptimizeUserText(req OptimizeRequest, mode, language string, imageCount int) string {
	var lines []string
	lines = append(lines, "任务类型："+mode)
	lines = append(lines, "视频时长："+util.Stringify(req.Duration)+" 秒")
	if language == "en" {
		lines = append(lines, "输出语言：英文（正文英文，台词保留原文语言）")
	} else {
		lines = append(lines, "输出语言：中文")
	}

	switch {
	case strings.Contains(mode, "I2VA"):
		lines = append(lines, "画面路径：首帧图生视频：从 <Picture 1> 的 0.00 秒首帧开始向后发展，不要换成别的底图。")
	case strings.Contains(mode, "FL2VA"):
		lines = append(lines, "画面路径：首尾帧视频：从 Picture 1 连续过渡到 Picture 2，优先单镜头，最后一镜落到尾帧。")
	case strings.Contains(mode, "L2VA"):
		lines = append(lines, "画面路径：尾帧图生视频：从合理的前置状态收敛到结尾的 <Picture 1>。")
	}

	if note := strings.TrimSpace(req.ReferenceNote); note != "" {
		lines = append(lines, "参考素材说明："+note)
	}
	if total := maxIntInt(imageCount, req.ImageCount); total > 0 {
		if imageCount > 0 {
			lines = append(lines, fmt.Sprintf("参考图片：%d 张（已随消息附上，以图片为准；不要编造图中不存在的场景、陈设、家具或光线）", total))
		} else {
			lines = append(lines, fmt.Sprintf("参考图片：%d 张（未附图片，仅使用文字说明；不要编造未看到的场景细节）", total))
		}
	}
	lines = append(lines, "用户原始提示词：\n"+req.Prompt)
	return strings.Join(lines, "\n")
}

// OptimizeImage rewrites a prompt for image generation. Images are never attached:
// the image endpoint is text-only by design.
func (o *Optimizer) OptimizeImage(ctx context.Context, req OptimizeRequest) (map[string]any, error) {
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		return nil, httpx.BadRequest("missing_prompt", "prompt is required")
	}
	if len([]rune(text)) > 8000 {
		return nil, httpx.BadRequest("prompt_too_long", "prompt is too long")
	}

	mode := "文生图"
	if strings.EqualFold(strings.TrimSpace(req.Mode), "image") || req.ImageCount > 0 {
		mode = "图生图"
	}

	var lines []string
	lines = append(lines, "任务模式："+mode)
	if normalizeLanguage(req.Language) == "en" {
		lines = append(lines, "输出语言：英文")
	} else {
		lines = append(lines, "输出语言：中文")
	}
	if req.ImageCount > 0 {
		lines = append(lines, fmt.Sprintf("参考图片：%d 张（仅使用文字说明）", req.ImageCount))
	}
	lines = append(lines, "用户原始提示词：\n"+req.Prompt)

	content, model, elapsed, err := o.chat(ctx, "optimize-image/"+modeToken(mode), imageSystemPrompt, strings.Join(lines, "\n"), nil, req.Endpoint)
	if err != nil {
		return nil, err
	}
	result := sanitizeImage(content)
	if result == "" {
		o.logger.Warn("image prompt optimize: the whole response was stripped by sanitising (head): %s",
			util.Truncate(strings.TrimSpace(content), 300))
		return nil, httpx.NewError(http.StatusBadGateway, "no_prompt_output",
			fmt.Sprintf("the model answered (%d chars) but no usable prompt remained after sanitising. First 160 chars: %s",
				len([]rune(content)), util.Truncate(strings.TrimSpace(content), 160)))
	}

	return map[string]any{
		"prompt":    result,
		"mode":      mode,
		"model":     model,
		"elapsedMs": elapsed,
	}, nil
}

// Caption reverse-engineers a prompt from one image.
func (o *Optimizer) Caption(ctx context.Context, images []string, language, instruction string, endpoint Endpoint) (map[string]any, error) {
	if len(images) == 0 {
		return nil, httpx.BadRequest("missing_reference_image", "captioning needs one image")
	}
	english := normalizeLanguage(language) == "en"
	system := strings.TrimSpace(instruction)
	if system == "" {
		if english {
			system = captionSystemEn
		} else {
			system = captionSystemZh
		}
	}
	userText := captionUserZh
	if english {
		userText = captionUserEn
	}

	content, model, elapsed, err := o.chat(ctx, "caption/"+normalizeLanguage(language), system, userText, images, endpoint)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(content) == "" {
		return nil, httpx.NewError(http.StatusBadGateway, "no_prompt_output", "the prompt endpoint returned no prompt")
	}

	return map[string]any{
		"prompt":    content,
		"model":     model,
		"elapsedMs": elapsed,
	}, nil
}

// Complete answers one prompt. The caller supplies the system message, or none at all.
//
// This is the endpoint the canvas's text node and plugins use: the answer is the model's own
// words, so there is no sanitiser and no H3 field check here — only the two limits that keep a
// stray binding from turning into an unbounded request.
func (o *Optimizer) Complete(ctx context.Context, req ChatRequest) (map[string]any, error) {
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		return nil, httpx.BadRequest("missing_prompt", "prompt is required")
	}
	if len([]rune(text)) > 8000 {
		return nil, httpx.BadRequest("prompt_too_long", "prompt is too long")
	}
	images := req.Images
	if len(images) > 8 {
		images = images[:8]
	}

	// An empty system message is dropped in chat() rather than here: the omission belongs to the
	// request shape, not to this endpoint.
	content, model, elapsed, err := o.chat(ctx, "chat", req.System, text, images, req.Endpoint)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"text":      content,
		"model":     model,
		"elapsedMs": elapsed,
	}, nil
}

// chat calls the configured OpenAI-compatible endpoint.
//
// Every call is logged with the resolved URL, and every failure carries the
// endpoint's own message. This is the most easily misconfigured part of the system:
// a base URL that normalises to the wrong path, a model that rejects images, or a
// context window smaller than the H3 skill otherwise surface only as a vague error.
func (o *Optimizer) chat(ctx context.Context, purpose, system, userText string, images []string, endpoint Endpoint) (string, string, int64, error) {
	baseURL := strings.TrimSpace(endpoint.BaseURL)
	apiKey := strings.TrimSpace(endpoint.APIKey)
	model := strings.TrimSpace(endpoint.Model)
	if baseURL == "" || apiKey == "" || model == "" {
		var missing []string
		if baseURL == "" {
			missing = append(missing, "base URL")
		}
		if apiKey == "" {
			missing = append(missing, "API key")
		}
		if model == "" {
			missing = append(missing, "model")
		}
		o.logger.Warn("prompt endpoint not configured (purpose=%s): missing %s", purpose, strings.Join(missing, ", "))
		return "", "", 0, httpx.BadRequest("prompt_not_configured", "prompt endpoint is not configured: set the base URL, API key and model in the settings first")
	}

	chatURL, err := chatEndpoint(baseURL)
	if err != nil {
		o.logger.Warn("prompt endpoint URL rejected (purpose=%s, base_url=%q): %s", purpose, baseURL, err)
		return "", "", 0, err
	}
	if trimmed := strings.TrimRight(baseURL, "/"); trimmed != chatURL {
		o.logger.Info("prompt endpoint URL normalised: %q -> %q", trimmed, chatURL)
	}

	imageBytes := 0
	for _, image := range images {
		imageBytes += len(image)
	}

	var content any = userText
	if len(images) > 0 {
		parts := []any{map[string]any{"type": "text", "text": userText}}
		for _, image := range images {
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": image},
			})
		}
		content = parts
	}

	requested := map[string]any{
		"model":       model,
		"messages":    chatMessages(system, content),
		"temperature": 0.6,
		"max_tokens":  o.maxTokens,
	}

	mechanism := o.resolveThinking(chatURL, model)
	thinkingLabel := mechanism
	if thinkingLabel == "" {
		thinkingLabel = "none"
	}
	o.logger.Info("prompt endpoint request: purpose=%s url=%s model=%s images=%d image_payload=%dKB prompt_chars=%d system_chars=%d thinking=%s",
		purpose, chatURL, model, len(images), imageBytes/1024, len([]rune(userText)), len([]rune(system)), thinkingLabel)

	requestCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	started := time.Now()
	var call *chatExchange
	for attempt := 0; attempt < 2; attempt++ {
		payload, marshalErr := json.Marshal(withThinkingHint(requested, mechanism))
		if marshalErr != nil {
			o.logger.Warn("prompt endpoint request could not be built (purpose=%s): %s", purpose, marshalErr)
			return "", "", 0, httpx.NewError(http.StatusInternalServerError, "internal_error", "failed to build the request")
		}

		exchange, err := o.exchange(requestCtx, chatURL, apiKey, purpose, payload)
		if err != nil {
			elapsed := time.Since(started).Milliseconds()
			if ctx.Err() != nil {
				o.logger.Warn("prompt endpoint call timed out after %dms (purpose=%s, url=%s)", elapsed, purpose, endpoint)
				return "", "", 0, httpx.NewError(http.StatusGatewayTimeout, "prompt_api_timeout", "prompt endpoint request was cancelled or timed out")
			}
			o.logger.Warn("prompt endpoint call failed after %dms (purpose=%s, url=%s): %s", elapsed, purpose, endpoint, err)
			return "", "", 0, httpx.NewError(http.StatusBadGateway, "prompt_api_failed", "prompt endpoint request failed: "+err.Error())
		}
		call = exchange

		// A strict provider rejects unknown parameters outright. Drop the hint, remember
		// that this endpoint wants none, and retry once.
		if call.status == http.StatusBadRequest && mechanism != "" {
			o.logger.Warn("prompt endpoint returned HTTP 400 with %q; retrying without it", mechanism)
			o.rememberThinking(chatURL, model, "", "the endpoint rejected the thinking parameter")
			mechanism = ""
			continue
		}
		if call.status < 200 || call.status >= 300 {
			break
		}
		if strings.TrimSpace(call.text) != "" {
			break
		}
		// Empty answer but the model reasoned: a thinking model ran out of budget. Worth
		// exactly one automatic retry, and only when the gateway is allowed to adapt.
		if call.reasoning == "" || mechanism != "" || attempt == 1 || !o.learningEnabled() {
			break
		}
		o.logger.Warn("prompt endpoint answered with reasoning only; retrying once with %q", defaultThinkingMechanism)
		mechanism = defaultThinkingMechanism
	}

	elapsed := time.Since(started).Milliseconds()
	if call == nil {
		return "", "", 0, httpx.NewError(http.StatusBadGateway, "prompt_api_failed", "prompt endpoint request failed")
	}

	// Remember a hint that was needed, so the next call skips the failed attempt.
	if call.status >= 200 && call.status < 300 && strings.TrimSpace(call.text) != "" &&
		mechanism != "" && o.pinnedThinking() == "" && o.learningEnabled() {
		o.rememberThinking(chatURL, model, mechanism, "the answer needed a thinking hint")
	}

	if call.status < 200 || call.status >= 300 {
		hint := ""
		if len(images) > 0 && (call.status == 400 || call.status == 415 || call.status == 422) {
			hint = " (if the model does not accept image input, switch to a vision-capable one)"
		}
		o.logger.Warn("prompt endpoint returned HTTP %d (purpose=%s, %dms): %s",
			call.status, purpose, elapsed, util.Truncate(call.errMessage, 500))
		return "", "", 0, httpx.NewError(http.StatusBadGateway, "prompt_api_failed",
			"prompt endpoint returned an error: "+util.Truncate(call.errMessage, 800)+hint)
	}

	if !call.hasChoices {
		o.logger.Warn("prompt endpoint returned no choices (purpose=%s, status=%d, %dms): %s",
			purpose, call.status, elapsed, util.Truncate(call.rawBody, 300))
		return "", "", 0, httpx.NewError(http.StatusBadGateway, "prompt_api_failed", "the prompt endpoint returned no content")
	}

	if call.reasoning != "" {
		o.logger.Info("prompt endpoint response carried reasoning_content (%d chars)", len([]rune(call.reasoning)))
	}
	o.logger.Info("prompt endpoint response: purpose=%s status=%d %dms chars=%d reasoning_chars=%d finish_reason=%q prompt_tokens=%d completion_tokens=%d",
		purpose, call.status, elapsed, len([]rune(call.text)), len([]rune(call.reasoning)),
		call.finishReason, call.promptTokens, call.completionTokens)

	if strings.TrimSpace(call.text) == "" {
		if call.reasoning != "" {
			o.logger.Warn("prompt endpoint returned only reasoning_content (purpose=%s, %dms); the model never reached its answer",
				purpose, elapsed)
			return "", "", elapsed, httpx.NewError(http.StatusBadGateway, "prompt_reasoning_only",
				fmt.Sprintf("the model returned %d chars of reasoning but no answer (finish_reason=%s). "+
					"It is a thinking model that exhausted max_tokens: raise PROMPT_MAX_TOKENS (currently %d) "+
					"or set PROMPT_THINKING_PARAM=chat_template_kwargs to request a direct answer",
					len([]rune(call.reasoning)), call.finishReason, o.maxTokens))
		}
		o.logger.Warn("prompt endpoint returned an empty message (purpose=%s, %dms, finish_reason=%q)",
			purpose, elapsed, call.finishReason)
		return "", "", elapsed, httpx.NewError(http.StatusBadGateway, "no_prompt_output",
			fmt.Sprintf("the prompt endpoint returned an empty message (finish_reason=%s)", call.finishReason))
	}
	return call.text, model, elapsed, nil
}

// chatMessages builds the turn list. An empty system message is omitted rather than sent as an
// empty turn: /api/prompts/chat has no wording of its own, and a model that receives a blank
// system role may answer it instead of the prompt.
func chatMessages(system string, content any) []any {
	messages := make([]any, 0, 2)
	if trimmed := strings.TrimSpace(system); trimmed != "" {
		messages = append(messages, map[string]any{"role": "system", "content": trimmed})
	}
	return append(messages, map[string]any{"role": "user", "content": content})
}

// chatExchange is one parsed request/response round trip.
type chatExchange struct {
	status           int
	text             string
	reasoning        string
	finishReason     string
	promptTokens     int
	completionTokens int
	errMessage       string
	rawBody          string
	hasChoices       bool
}

// withThinkingHint copies the base request and applies the requested hint, if any.
func withThinkingHint(base map[string]any, mechanism string) map[string]any {
	body := make(map[string]any, len(base)+1)
	for key, value := range base {
		body[key] = value
	}
	if apply, ok := thinkingMechanisms[mechanism]; ok {
		apply(body)
	}
	return body
}

// exchange performs one HTTP round trip and parses the reply.
func (o *Optimizer) exchange(ctx context.Context, endpoint, apiKey, purpose string, payload []byte) (*chatExchange, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+apiKey)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if readErr != nil {
		o.logger.Warn("prompt endpoint response could not be read (purpose=%s, status=%d): %s", purpose, response.StatusCode, readErr)
		return nil, readErr
	}

	var decoded struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
				// Thinking models put their chain of thought here and only start
				// writing content after they finish reasoning.
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if parseErr := json.Unmarshal(raw, &decoded); parseErr != nil {
		o.logger.Warn("prompt endpoint returned non-JSON (purpose=%s, status=%d): %s",
			purpose, response.StatusCode, util.Truncate(string(raw), 300))
	}

	call := &chatExchange{status: response.StatusCode, rawBody: string(raw)}
	if len(decoded.Choices) > 0 {
		choice := decoded.Choices[0]
		call.text = contentText(choice.Message.Content)
		call.reasoning = strings.TrimSpace(choice.Message.ReasoningContent)
		call.finishReason = choice.FinishReason
		call.hasChoices = true
	}
	call.promptTokens = decoded.Usage.PromptTokens
	call.completionTokens = decoded.Usage.CompletionTokens
	call.errMessage = decoded.Error.Message
	if call.errMessage == "" {
		call.errMessage = decoded.Message
	}
	if call.errMessage == "" {
		call.errMessage = string(raw)
	}
	return call, nil
}

// contentText flattens the two content shapes OpenAI-compatible servers return.
func contentText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var builder strings.Builder
		for _, part := range typed {
			if text, ok := part.(string); ok {
				builder.WriteString(text)
				continue
			}
			if object, ok := part.(map[string]any); ok {
				builder.WriteString(util.StringField(object, "text"))
			}
		}
		return builder.String()
	}
	return ""
}

// chatEndpoint normalizes a base URL into a chat-completions endpoint.
func chatEndpoint(baseURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return "", httpx.BadRequest("prompt_not_configured", "prompt endpoint base URL is not configured")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", httpx.BadRequest("prompt_invalid_url", "invalid base URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", httpx.BadRequest("prompt_invalid_url", "the base URL must be http or https")
	}
	lowered := strings.ToLower(trimmed)
	if strings.HasSuffix(lowered, "/chat/completions") {
		return trimmed, nil
	}
	if regexp.MustCompile(`/v\d+$`).MatchString(lowered) {
		return trimmed + "/chat/completions", nil
	}
	return trimmed + "/v1/chat/completions", nil
}

// stripReasoning removes leaked chain-of-thought and code fences.
func stripReasoning(text string) string {
	out := reThinkBlock.ReplaceAllString(text, "")
	out = reSpecialTok.ReplaceAllString(out, "")
	out = reFenceOpen.ReplaceAllString(out, "")
	out = reFenceClose.ReplaceAllString(out, "")

	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if reMetaLine.MatchString(strings.TrimSpace(line)) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// h3FieldMarker matches the top-level field names an H3 prompt must contain.
var h3FieldMarker = regexp.MustCompile(`(?i)subject_definitions\s*:|integrated_multimodal_description\s*:|overall_soundscape\s*:|non_diegetic_music\s*:`)

// looksLikeH3Prompt reports whether text still carries H3 fields after sanitising.
//
// A thinking model can leave a trailing sentence behind once its reasoning lines are
// stripped. That text is non-empty, so an "is it empty" check alone would let it be
// returned as a prompt and rendered as garbage on the canvas.
func looksLikeH3Prompt(text string) bool {
	return h3FieldMarker.MatchString(text)
}

// sanitizeH3 trims an H3 prompt down to its first real field.
func sanitizeH3(text string) string {
	out := stripReasoning(text)
	reference := loc(reSubjectDef, out)
	main := loc(reIntegrated, out)
	switch {
	case reference >= 0 && (main < 0 || reference < main):
		out = out[reference:]
	case main > 0:
		out = out[main:]
	case main < 0:
		if alt := loc(reSoundscape, out); alt > 0 {
			out = out[alt:]
		}
	}
	out = reTailMeta.ReplaceAllString(out, "")
	out = reFinalPrefix.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

// sanitizeImage trims an image prompt to the last labelled prompt, if any.
func sanitizeImage(text string) string {
	out := stripReasoning(text)
	markers := []string{"最终提示词：", "最终提示词:", "提示词：", "提示词:", "**Prompt:**", "Prompt:", "输出：", "输出:"}
	for _, marker := range markers {
		if index := strings.LastIndex(out, marker); index >= 0 {
			out = strings.TrimSpace(out[index+len(marker):])
		}
	}
	paragraphs := splitParagraphs(out)
	for len(paragraphs) > 1 && reMetaPara.MatchString(paragraphs[0]) {
		paragraphs = paragraphs[1:]
	}
	out = strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
	out = reFenceAny.ReplaceAllString(out, "")
	out = reFenceEnd.ReplaceAllString(out, "")
	out = reWrapping.ReplaceAllString(out, "")
	return strings.TrimSpace(out)
}

// sanitizeCaption trims a caption down to its description paragraphs.
func sanitizeCaption(text string) string {
	out := stripReasoning(text)
	out = reCaptionPfx.ReplaceAllString(out, "")
	paragraphs := splitParagraphs(out)
	for len(paragraphs) > 1 && reMetaPara.MatchString(paragraphs[0]) {
		paragraphs = paragraphs[1:]
	}
	return strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
}

func splitParagraphs(text string) []string {
	var out []string
	for _, block := range regexp.MustCompile(`\n{2,}`).Split(text, -1) {
		if trimmed := strings.TrimSpace(block); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ensureFrameAlignmentPrompt prefixes the alignment sentence the renderer expects.
func ensureFrameAlignmentPrompt(text, mode string, seconds float64) string {
	body := strings.TrimSpace(text)
	if body == "" {
		return body
	}
	lowered := strings.ToLower(mode)
	if !strings.Contains(lowered, "i2va") && !strings.Contains(lowered, "fl2va") &&
		!strings.Contains(lowered, "l2va") && !strings.Contains(mode, "首帧") &&
		!strings.Contains(mode, "首尾帧") && !strings.Contains(mode, "尾帧") {
		return body
	}
	content := strings.TrimSpace(reAlignPrefix.ReplaceAllString(body, ""))
	if seconds <= 0 {
		seconds = 5
	}
	timeText := formatSeconds(seconds)
	last := lastShotIndex(content)

	switch {
	case strings.Contains(mode, "首帧") || strings.Contains(lowered, "i2va"):
		return "For the target video, at 0.00 seconds into the target video, <Picture 1> (from [Shot 1]) is fully referenced.\n\n" + content
	case strings.Contains(mode, "首尾帧") || strings.Contains(lowered, "fl2va"):
		return fmt.Sprintf("How the reference pictures align with the target video — Picture 1 (from Shot 1) aligns with the 0.00-second mark of the target video; Picture 2 (from Shot %d) aligns with the %s-second mark of the target video.\n\n%s",
			last, timeText, content)
	}
	return fmt.Sprintf("How the reference pictures align with the target video — <Picture 1> (from [Shot %d]) aligns with the %s-second mark of the target video.\n\n%s",
		last, timeText, content)
}

func formatSeconds(value float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.2f", value), "0"), ".")
}

// lastShotIndex finds the highest [Shot N] marker, defaulting to 1.
func lastShotIndex(text string) int {
	last := 1
	for _, match := range reShotIndex.FindAllStringSubmatch(text, -1) {
		if value := atoi(match[1]); value > last {
			last = value
		}
	}
	return last
}

// loc returns the byte offset of a pattern, or -1.
func loc(pattern *regexp.Regexp, text string) int {
	index := pattern.FindStringIndex(text)
	if index == nil {
		return -1
	}
	return index[0]
}

func atoi(value string) int {
	number := 0
	for _, char := range value {
		if char < '0' || char > '9' {
			return number
		}
		number = number*10 + int(char-'0')
	}
	return number
}

func maxIntInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

const imageSystemPrompt = `你是 AI 生图提示词专家。请把用户输入改写为一条可直接使用的生图/图生图提示词，只输出最终提示词本身，不要输出任何思考过程、分析、解释、Markdown 代码块、标题或前后缀。

要求：主体明确，构图、镜头、光线、材质与风格具体；保留用户的主体和意图，不要编造品牌或画面文字；图生图时说明需要保留与需要改变的部分；输出语言与“输出语言”字段一致。`

const captionSystemZh = "你是一位专业的AI图像生成提示词工程师。请详细描述这张图像主体、前景、中景、背景、构图、视觉引导、光影氛围等细节并创作出具有深度、氛围和艺术感或日常业余设备拍摄的图像提示词。\n\n要求：中文提示词，不用出现对图像水印的描述，不要出现无关的文字和符号，不需要总结，限制在800字以内"

const captionUserZh = "请把这张图片反推成可直接用于 AI 生图的中文提示词。"

const captionSystemEn = "You are a professional prompt engineer for AI image generation. Describe the subject, foreground, middle ground, background, composition, visual guidance, lighting and atmosphere, then write a detailed prompt with depth and mood. Requirements: English prompt only; do not describe watermarks; no irrelevant text or symbols; no summary; at most 800 words."

const captionUserEn = "Write a detailed English prompt that could recreate this image."
