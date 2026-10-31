package prompt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"comfyui-gateway/internal/logx"
)

// newLoggedOptimizer returns an optimizer whose log output can be inspected.
func newLoggedOptimizer(t *testing.T) (*Optimizer, *bytes.Buffer) {
	t.Helper()
	buffer := &bytes.Buffer{}
	return NewOptimizer(t.TempDir(), logx.NewWithWriter("test", buffer)), buffer
}

func TestChatLogsRequestAndTokenUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The endpoint must be the normalised chat-completions path.
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-key" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"hello"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":4504,"completion_tokens":120,"total_tokens":4624}
		}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	text, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "SYS", "USER", nil,
		Endpoint{BaseURL: server.URL, APIKey: "secret-key", Model: "local-llama"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if text != "hello" {
		t.Errorf("text = %q", text)
	}

	out := logs.String()
	// The resolved URL must be visible: a base URL that normalises to the wrong path
	// is the single most common misconfiguration.
	for _, want := range []string{
		"prompt endpoint request: purpose=optimize/t2va",
		server.URL + "/v1/chat/completions",
		"model=local-llama",
		"prompt endpoint response:",
		"prompt_tokens=4504",
		"completion_tokens=120",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log is missing %q\n--- logs ---\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-key") {
		t.Errorf("the API key leaked into the log:\n%s", out)
	}
}

func TestChatLogsURLNormalisation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"}}]}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	// No /v1 suffix: the gateway appends it, and the change must be logged.
	if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !strings.Contains(logs.String(), "prompt endpoint URL normalised") {
		t.Errorf("normalisation was not logged:\n%s", logs.String())
	}
}

func TestChatLogsEndpointErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// This is the real llama.cpp message when the context window is too small.
		_, _ = w.Write([]byte(`{"error":{"message":"request (4504 tokens) exceeds the available context size (2048 tokens)"}}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	_, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "exceeds the available context size") {
		t.Errorf("error lost the endpoint message: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "returned HTTP 400") {
		t.Errorf("status was not logged:\n%s", out)
	}
	if !strings.Contains(out, "exceeds the available context size") {
		t.Errorf("the endpoint message was not logged:\n%s", out)
	}
}

func TestChatLogsImageRejectionHint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"image input is not supported"}}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	_, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u",
		[]string{"data:image/png;base64,AAAA"},
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "text-only"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "vision-capable") {
		t.Errorf("a 400 with images should hint at a vision model, got: %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "images=1") {
		t.Errorf("image count was not logged:\n%s", out)
	}
}

func TestChatLogsNonJSONBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A reverse proxy or a wrong port often answers with HTML.
		_, _ = w.Write([]byte("<html>404 not found</html>"))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	if _, _, _, err := optimizer.chat(context.Background(), "caption/zh", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err == nil {
		t.Fatal("expected an error for a body with no choices")
	}
	out := logs.String()
	if !strings.Contains(out, "non-JSON") {
		t.Errorf("non-JSON body was not logged:\n%s", out)
	}
	if !strings.Contains(out, "returned no choices") {
		t.Errorf("missing-choices was not logged:\n%s", out)
	}
}

// TestChatReportsEmptyMessage covers max_tokens running out with no answer at all.
func TestChatReportsEmptyMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"   "},"finish_reason":"length"}]}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	_, _, _, err := optimizer.chat(context.Background(), "optimize/i2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err == nil {
		t.Fatal("an empty message must be an error, not a silent empty prompt")
	}
	if !strings.Contains(err.Error(), "length") {
		t.Errorf("the error should name finish_reason: %v", err)
	}
	if !strings.Contains(logs.String(), `empty message`) {
		t.Errorf("empty message was not logged:\n%s", logs.String())
	}
}

// TestChatReportsReasoningOnlyAnswer is the thinking-model failure: everything lands
// in reasoning_content and the answer never starts. The message must say so, because
// "no prompt" alone leaves the user with nothing to act on.
func TestChatReportsReasoningOnlyAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{
			"content":"",
			"reasoning_content":"让我先分析一下画面里的主体和光线，然后再组织字段……"
		},"finish_reason":"length"}]}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	_, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "thinking-model"})
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	for _, want := range []string{"reasoning", "thinking model", "max_tokens"} {
		if !strings.Contains(message, want) {
			t.Errorf("error message is missing %q: %s", want, message)
		}
	}
	if !strings.Contains(logs.String(), "only reasoning_content") {
		t.Errorf("the reasoning-only case was not logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "reasoning_chars=") {
		t.Errorf("reasoning length was not logged:\n%s", logs.String())
	}
}

// TestOptimizeExplainsUnusableAnswer covers a non-empty answer that sanitising empties.
// The user needs to see what the model actually said, not just "no prompt".
func TestOptimizeExplainsUnusableAnswer(t *testing.T) {
	if _, err := os.Stat("../../prompts/skills/h3-prompt-writing"); err != nil {
		t.Skipf("skill files not available: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{
			"content":"思考过程：先分析画面，再写字段。\n\n（然后就停了）"
		},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	buffer := &bytes.Buffer{}
	optimizer := NewOptimizer("../..", logx.NewWithWriter("test", buffer))

	_, err := optimizer.Optimize(context.Background(), OptimizeRequest{
		Prompt:   "一个人在雨里奔跑",
		Mode:     "文生视频(T2VA)",
		Duration: 5,
		Endpoint: Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"},
	})
	if err == nil {
		t.Fatal("expected an error when nothing survives sanitising")
	}
	message := err.Error()
	if !strings.Contains(message, "none of the required H3 fields") {
		t.Errorf("error should name the missing fields: %s", message)
	}
	if !strings.Contains(message, "思考过程") {
		t.Errorf("error should quote what the model actually said: %s", message)
	}
	if !strings.Contains(buffer.String(), "carried no H3 fields") {
		t.Errorf("the sanitising loss was not logged:\n%s", buffer.String())
	}
}

// TestOptimizeAcceptsValidAnswer makes sure the structural guard does not reject a
// real H3 prompt. Without this the guard could break normal operation silently.
func TestOptimizeAcceptsValidAnswer(t *testing.T) {
	if _, err := os.Stat("../../prompts/skills/h3-prompt-writing"); err != nil {
		t.Skipf("skill files not available: %v", err)
	}
	answer := `subject_definitions:
- 主体：一个穿风衣的女人
integrated_multimodal_description:
镜头从背后跟随她穿过雨夜的街道。
overall_soundscape:
雨声、远处车流。`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]any{"content": answer},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 4504, "completion_tokens": 180},
		})
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	buffer := &bytes.Buffer{}
	optimizer := NewOptimizer("../..", logx.NewWithWriter("test", buffer))

	result, err := optimizer.Optimize(context.Background(), OptimizeRequest{
		Prompt:   "一个女人在雨夜独自前行",
		Mode:     "文生视频(T2VA)",
		Duration: 6,
		Endpoint: Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatalf("a valid H3 answer was rejected: %v", err)
	}
	promptText, _ := result["prompt"].(string)
	if !strings.Contains(promptText, "integrated_multimodal_description") {
		t.Errorf("prompt lost its fields: %q", promptText)
	}
	if !strings.Contains(buffer.String(), "prompt endpoint response:") {
		t.Errorf("the successful call was not logged:\n%s", buffer.String())
	}
}

// TestCompleteSendsNoSystemTurnWhenNoneIsGiven pins the one thing this endpoint decides for the
// caller: a caller without a system message must not have an empty one invented for it.
func TestCompleteSendsNoSystemTurnWhenNoneIsGiven(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"an answer"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	optimizer, _ := newLoggedOptimizer(t)
	result, err := optimizer.Complete(context.Background(), ChatRequest{
		Prompt:   "  what is this?  ",
		Endpoint: Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"},
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if result["text"] != "an answer" {
		t.Errorf("text = %v", result["text"])
	}
	if result["model"] != "m" {
		t.Errorf("model = %v", result["model"])
	}

	messages, _ := captured["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %v, want one user turn", messages)
	}
	user, _ := messages[0].(map[string]any)
	// The prompt is trimmed: leading whitespace is not part of what the caller asked.
	if user["role"] != "user" || user["content"] != "what is this?" {
		t.Errorf("user turn = %v", user)
	}
}

func TestCompleteSendsTheSystemMessageFirst(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	optimizer, _ := newLoggedOptimizer(t)
	if _, err := optimizer.Complete(context.Background(), ChatRequest{
		Prompt:   "hello",
		System:   "be brief",
		Endpoint: Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	messages, _ := captured["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want system then user", messages)
	}
	system, _ := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "be brief" {
		t.Errorf("system turn = %v", system)
	}
}

func TestCompleteRejectsABlankPrompt(t *testing.T) {
	optimizer, _ := newLoggedOptimizer(t)
	if _, err := optimizer.Complete(context.Background(), ChatRequest{Prompt: " \n "}); err == nil {
		t.Fatal("expected an error for a blank prompt")
	}
	long := strings.Repeat("字", 8001)
	if _, err := optimizer.Complete(context.Background(), ChatRequest{Prompt: long}); err == nil {
		t.Fatal("expected an error for an oversized prompt")
	}
}

func TestChatLogsMissingConfiguration(t *testing.T) {
	optimizer, logs := newLoggedOptimizer(t)
	_, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: "http://127.0.0.1:8099", Model: "m"})
	if err == nil {
		t.Fatal("expected an error for a missing API key")
	}
	if !strings.Contains(logs.String(), "missing API key") {
		t.Errorf("the missing field was not named:\n%s", logs.String())
	}
}

func TestChatLogsInvalidURL(t *testing.T) {
	optimizer, logs := newLoggedOptimizer(t)
	_, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: "ftp://nope", APIKey: "k", Model: "m"})
	if err == nil {
		t.Fatal("expected an error for a non-http scheme")
	}
	if !strings.Contains(logs.String(), "URL rejected") {
		t.Errorf("rejection was not logged:\n%s", logs.String())
	}
}

func TestChatLogsUnreachableEndpoint(t *testing.T) {
	optimizer, logs := newLoggedOptimizer(t)
	// Port 1 is closed: this is the "llama-server is not running" case.
	_, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"})
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if !strings.Contains(logs.String(), "call failed") {
		t.Errorf("connection failure was not logged:\n%s", logs.String())
	}
}

// TestModeTokenKeepsLogsASCII guards the English-only backend rule: mode names come
// from the frontend and contain Chinese, so they must never reach a log line.
func TestModeTokenKeepsLogsASCII(t *testing.T) {
	modes := []string{
		"全参考模式(Reference to Video)", "文生视频(T2VA)", "首帧图生视频(I2VA)",
		"首尾帧视频(FL2VA)", "尾帧图生视频(L2VA)", "文生图", "图生图", "", "unexpected",
	}
	for _, mode := range modes {
		token := modeToken(mode)
		if token == "" {
			t.Errorf("modeToken(%q) returned an empty token", mode)
		}
		for _, char := range token {
			if char > 127 {
				t.Errorf("modeToken(%q) = %q contains a non-ASCII character", mode, token)
			}
		}
	}
	if got := modeToken("文生视频(T2VA)"); got != "t2va" {
		t.Errorf("modeToken = %q, want t2va", got)
	}
	if got := modeToken(""); got != "unknown" {
		t.Errorf("modeToken(\"\") = %q, want unknown", got)
	}
}

// TestChatPurposeAppearsInLogs checks the purpose tag distinguishes the three routes,
// so a log line can be traced back to the caller.
func TestChatPurposeAppearsInLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	for _, purpose := range []string{"optimize/t2va", "optimize-image/t2i", "caption/en"} {
		optimizer, logs := newLoggedOptimizer(t)
		if _, _, _, err := optimizer.chat(context.Background(), purpose, "s", "u", nil,
			Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err != nil {
			t.Fatalf("chat(%s): %v", purpose, err)
		}
		if !strings.Contains(logs.String(), "purpose="+purpose) {
			t.Errorf("purpose %q missing from logs:\n%s", purpose, logs.String())
		}
	}
}

// TestChatUsageFieldsAreParsedFromRealShape pins the llama.cpp payload shape.
func TestChatUsageFieldsAreParsedFromRealShape(t *testing.T) {
	body := map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]any{"role": "assistant", "content": "done"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 4504, "completion_tokens": 12, "total_tokens": 4516},
	}
	encoded, _ := json.Marshal(body)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	text, model, elapsed, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL + "/v1", APIKey: "k", Model: "qwen3"})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if text != "done" || model != "qwen3" || elapsed < 0 {
		t.Errorf("result = %q/%q/%d", text, model, elapsed)
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("url=%s/v1/chat/completions", server.URL)) {
		t.Errorf("/v1 base URL was not completed to chat/completions:\n%s", logs.String())
	}
}

// captureBody returns a server that records the decoded request body it received.
func captureBody(t *testing.T, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(seen)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
}

// TestChatSendsNoVendorFieldByDefault is the compatibility guarantee: none of the
// thinking switches is part of the OpenAI API, so a fresh endpoint gets a plain
// request that any provider accepts.
func TestChatSendsNoVendorFieldByDefault(t *testing.T) {
	seen := map[string]any{}
	server := captureBody(t, &seen)
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("chat: %v", err)
	}

	for _, key := range []string{"chat_template_kwargs", "enable_thinking", "reasoning_effort"} {
		if _, present := seen[key]; present {
			t.Errorf("%q must not be sent by default: %v", key, seen)
		}
	}
	if !strings.Contains(logs.String(), "thinking=none") {
		t.Errorf("expected thinking=none in the log:\n%s", logs.String())
	}
}

// TestChatThinkingParamVariants covers the other spellings providers accept.
func TestChatThinkingParamVariants(t *testing.T) {
	cases := map[string]func(map[string]any) bool{
		"enable_thinking": func(body map[string]any) bool {
			value, ok := body["enable_thinking"]
			return ok && value == false
		},
		"reasoning_effort": func(body map[string]any) bool {
			value, ok := body["reasoning_effort"]
			return ok && value == "none"
		},
	}
	for param, check := range cases {
		seen := map[string]any{}
		server := captureBody(t, &seen)
		optimizer, _ := newLoggedOptimizer(t)
		optimizer.WithThinkingParam(param)
		if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
			Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err != nil {
			t.Fatalf("%s: chat: %v", param, err)
		}
		server.Close()
		if !check(seen) {
			t.Errorf("%s was not applied: %v", param, seen)
		}
	}
}

// TestChatThinkingParamNoneSendsNothing is the escape hatch for strict providers.
func TestChatThinkingParamNoneSendsNothing(t *testing.T) {
	seen := map[string]any{}
	server := captureBody(t, &seen)
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	optimizer.WithThinkingParam("none")
	if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	for _, key := range []string{"chat_template_kwargs", "enable_thinking", "reasoning_effort"} {
		if _, present := seen[key]; present {
			t.Errorf("%q must not be sent when thinking suppression is off: %v", key, seen)
		}
	}
	if !strings.Contains(logs.String(), "thinking=none") {
		t.Errorf("expected thinking=none in the log:\n%s", logs.String())
	}
}

// TestChatRetriesWithoutThinkingOn400 is the self-healing path: a strict provider that
// rejects the extra parameter must not break every call.
func TestChatRetriesWithoutThinkingOn400(t *testing.T) {
	var attempts []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		attempts = append(attempts, body)
		if _, present := body["chat_template_kwargs"]; present {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unrecognized request argument: chat_template_kwargs"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	optimizer.WithThinkingParam("chat_template_kwargs")
	text, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatalf("the retry should have succeeded: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q", text)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (with then without the hint)", len(attempts))
	}
	if _, present := attempts[1]["chat_template_kwargs"]; present {
		t.Error("the retry still carried the hint")
	}
	if !strings.Contains(logs.String(), "retrying without it") {
		t.Errorf("the fallback was not logged:\n%s", logs.String())
	}
}

// TestChatUnknownThinkingParamBehavesLikeAuto keeps a typo from doing something odd:
// an unrecognised value degrades to the safe default rather than guessing.
func TestChatUnknownThinkingParamBehavesLikeAuto(t *testing.T) {
	seen := map[string]any{}
	server := captureBody(t, &seen)
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	optimizer.WithThinkingParam("enable_thinkin") // typo
	if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil,
		Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	for _, key := range []string{"chat_template_kwargs", "enable_thinking", "reasoning_effort"} {
		if _, present := seen[key]; present {
			t.Errorf("an unknown value must send nothing: %v", seen)
		}
	}
	if !strings.Contains(logs.String(), "unknown PROMPT_THINKING_PARAM") {
		t.Errorf("the fallback was not logged:\n%s", logs.String())
	}
}

// thinkingModel simulates an endpoint that answers only after reasoning, and only if
// the request explicitly disables thinking. It records every request body it sees.
func thinkingModel(t *testing.T, bodies *[]map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		*bodies = append(*bodies, body)

		_, disabled := body["chat_template_kwargs"]
		if !disabled {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"让我想想…"},"finish_reason":"length"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"integrated_multimodal_description: ok"},"finish_reason":"stop"}]}`))
	}))
}

// TestChatAutoLearnsThinkingHint is the whole point of the auto mode: the first call
// detects a thinking model and recovers, and later calls skip the wasted attempt.
func TestChatAutoLearnsThinkingHint(t *testing.T) {
	var bodies []map[string]any
	server := thinkingModel(t, &bodies)
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	endpoint := Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}

	// First call: plain request, gets reasoning-only, retries with the hint, succeeds.
	text, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil, endpoint)
	if err != nil {
		t.Fatalf("first call should have recovered: %v", err)
	}
	if !strings.Contains(text, "integrated_multimodal_description") {
		t.Errorf("unexpected text: %q", text)
	}
	if len(bodies) != 2 {
		t.Fatalf("first call made %d requests, want 2 (plain then hinted)", len(bodies))
	}
	if _, sent := bodies[0]["chat_template_kwargs"]; sent {
		t.Error("the first attempt must be a plain request")
	}
	if _, sent := bodies[1]["chat_template_kwargs"]; !sent {
		t.Error("the retry should have carried the hint")
	}
	if !strings.Contains(logs.String(), "retrying once with") {
		t.Errorf("the recovery was not logged:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "thinking mode set for") {
		t.Errorf("learning was not logged:\n%s", logs.String())
	}

	// Second call: the hint is known, so exactly one request and no wasted round trip.
	bodies = nil
	if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil, endpoint); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(bodies) != 1 {
		t.Errorf("second call made %d requests, want 1 (the hint was learned)", len(bodies))
	}
	if _, sent := bodies[0]["chat_template_kwargs"]; !sent {
		t.Error("the learned hint was not applied")
	}
}

// TestChatFallsBackWhenHintRejected covers a provider that refuses the vendor field:
// the call still succeeds, and the fallback is visible in the log.
func TestChatFallsBackWhenHintRejected(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, present := body["chat_template_kwargs"]; present {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unrecognized request argument"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	optimizer, logs := newLoggedOptimizer(t)
	endpoint := Endpoint{BaseURL: server.URL, APIKey: "k", Model: "m"}

	// Learning only kicks in for auto; start by pinning a hint so the rejection happens.
	optimizer.WithThinkingParam("chat_template_kwargs")
	if _, _, _, err := optimizer.chat(context.Background(), "optimize/t2va", "s", "u", nil, endpoint); err != nil {
		t.Fatalf("the retry should have succeeded: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if !strings.Contains(logs.String(), "retrying without it") {
		t.Errorf("the rejection was not logged:\n%s", logs.String())
	}
	if _, present := bodies[1]["chat_template_kwargs"]; present {
		t.Error("the retry still carried the rejected field")
	}
}

// TestChatAutoDoesNotLearnWhenPinned proves an explicit setting is never overridden by
// observation.
func TestChatAutoDoesNotLearnWhenPinned(t *testing.T) {
	optimizer, logs := newLoggedOptimizer(t)
	optimizer.WithThinkingParam("none")
	if optimizer.learningEnabled() {
		t.Error("an explicit setting must disable learning")
	}
	if got := optimizer.resolveThinking("http://x/v1/chat/completions", "m"); got != "" {
		t.Errorf("resolveThinking = %q, want none", got)
	}
	optimizer.rememberThinking("http://x/v1/chat/completions", "m", "chat_template_kwargs", "test")
	if got := optimizer.resolveThinking("http://x/v1/chat/completions", "m"); got != "" {
		t.Errorf("a pinned setting was overridden: %q", got)
	}
	if logs.Len() != 0 {
		t.Errorf("nothing should be logged here: %s", logs.String())
	}
}
