package server

// POST /api/prompts/chat, the one prompt route that has no wording of its own.
//
// The canvas's text node and plugins call it, so what a test has to pin is not the H3 skill (that
// belongs to /api/prompts/optimize) but the shape of the request the gateway builds: where the
// endpoint comes from, what happens when it is missing, and that an empty system message is not
// sent as an empty turn.

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync"
	"testing"
)

// fakeChat is an OpenAI-compatible chat endpoint that records what it was asked.
type fakeChat struct {
	server *httptest.Server

	mu      sync.Mutex
	path    string
	payload map[string]any
	calls   int

	answer string
	status int
	body   string
}

func newFakeChat(t *testing.T, answer string) *fakeChat {
	t.Helper()
	fake := &fakeChat{answer: answer}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.path = r.URL.Path
		fake.calls++
		var decoded map[string]any
		if err := json.NewDecoder(r.Body).Decode(&decoded); err == nil {
			fake.payload = decoded
		}
		status, body := fake.status, fake.body
		fake.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		writeJSON(w, map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": answer}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 3},
		})
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// lastPayload returns the decoded body of the last request, plus the path it went to.
func (f *fakeChat) lastPayload() (string, map[string]any, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.path, f.payload, f.calls
}

// chatMessagesOf returns the request's messages, lowercased roles included.
func chatMessagesOf(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("request carried no messages: %v", payload)
	}
	messages := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		message, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("message is not an object: %v", item)
		}
		messages = append(messages, message)
	}
	return messages
}

// postPromptChat posts a multipart form to the chat route and decodes the reply. The image part
// carries a browser's own Content-Type, because that is what decides the data URL's mime.
func postPromptChat(t *testing.T, url string, fields map[string]string, withImage bool) (int, map[string]any) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if withImage {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="image"; filename="shot.png"`)
		header.Set("Content-Type", "image/png")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write([]byte("PNGDATA")); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, url+"/api/prompts/chat", &body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("response is not JSON (status %d): %s", response.StatusCode, raw)
	}
	return response.StatusCode, decoded
}

func startChatGateway(t *testing.T) *httptest.Server {
	t.Helper()
	gateway := httptest.NewServer(newTestGateway(t, newFakeComfy(t)).Handler())
	t.Cleanup(gateway.Close)
	return gateway
}

// The endpoint, key and model travel with the request; the answer travels back as text.
func TestPromptChatAnswersWithTheConfiguredEndpoint(t *testing.T) {
	chat := newFakeChat(t, "the teapot is on the left")
	gateway := startChatGateway(t)

	status, payload := postPromptChat(t, gateway.URL, map[string]string{
		"prompt":        "describe the scene",
		"promptBaseUrl": chat.server.URL,
		"promptApiKey":  "secret-key",
		"promptModel":   "qwen3.8-27b",
	}, false)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	if payload["text"] != "the teapot is on the left" {
		t.Errorf("text = %v", payload["text"])
	}
	if payload["model"] != "qwen3.8-27b" {
		t.Errorf("model = %v", payload["model"])
	}

	path, request, _ := chat.lastPayload()
	// A base URL without a path normalises to chat-completions, as it does for every prompt route.
	if path != "/v1/chat/completions" {
		t.Errorf("endpoint path = %q, want /v1/chat/completions", path)
	}
	if request["model"] != "qwen3.8-27b" {
		t.Errorf("request model = %v", request["model"])
	}
	messages := chatMessagesOf(t, request)
	// No system field was sent, so no system turn is sent on: an empty one is not the same request.
	if len(messages) != 1 {
		t.Fatalf("messages = %v, want one user turn", messages)
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "describe the scene" {
		t.Errorf("message = %v", messages[0])
	}
}

// A caller with a system message gets it first, and its images are attached to the user turn.
func TestPromptChatCarriesSystemMessageAndImages(t *testing.T) {
	chat := newFakeChat(t, "two chairs")
	gateway := startChatGateway(t)

	status, payload := postPromptChat(t, gateway.URL, map[string]string{
		"prompt":        "how many chairs?",
		"system":        "Answer in three words.",
		"promptBaseUrl": chat.server.URL,
		"promptApiKey":  "secret-key",
		"promptModel":   "vision-model",
	}, true)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %v", status, payload)
	}

	_, request, _ := chat.lastPayload()
	messages := chatMessagesOf(t, request)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want system then user", messages)
	}
	if messages[0]["role"] != "system" || messages[0]["content"] != "Answer in three words." {
		t.Errorf("system turn = %v", messages[0])
	}

	parts, ok := messages[1]["content"].([]any)
	if !ok {
		t.Fatalf("user turn is not a part list: %v", messages[1]["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %v, want text and one image", parts)
	}
	text, _ := parts[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "how many chairs?" {
		t.Errorf("text part = %v", text)
	}
	image, _ := parts[1].(map[string]any)
	imageURL, _ := image["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	if image["type"] != "image_url" || !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Errorf("image part = %v", image)
	}
}

// With no endpoint configured the gateway says which settings are missing instead of calling
// something. This is the state a fresh install is in, so the message has to be actionable.
func TestPromptChatWithoutConfigurationNamesTheMissingSettings(t *testing.T) {
	chat := newFakeChat(t, "never asked")
	gateway := startChatGateway(t)

	status, payload := postPromptChat(t, gateway.URL, map[string]string{"prompt": "hello"}, false)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	errPayload, _ := payload["error"].(map[string]any)
	if errPayload["code"] != "prompt_not_configured" {
		t.Errorf("error = %v", errPayload)
	}
	if message, _ := errPayload["message"].(string); !strings.Contains(message, "not configured") {
		t.Errorf("message does not say the endpoint is unconfigured: %v", errPayload)
	}
	if _, _, calls := chat.lastPayload(); calls != 0 {
		t.Errorf("the endpoint was called %d times without a configuration", calls)
	}
}

// The endpoint's own error reaches the caller, with the endpoint's wording.
func TestPromptChatPassesThroughTheEndpointError(t *testing.T) {
	chat := newFakeChat(t, "")
	chat.status = http.StatusUnauthorized
	chat.body = `{"error":{"message":"invalid api key"}}`
	gateway := startChatGateway(t)

	status, payload := postPromptChat(t, gateway.URL, map[string]string{
		"prompt":        "hello",
		"promptBaseUrl": chat.server.URL,
		"promptApiKey":  "wrong",
		"promptModel":   "m",
	}, false)
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	errPayload, _ := payload["error"].(map[string]any)
	if message, _ := errPayload["message"].(string); !strings.Contains(message, "invalid api key") {
		t.Errorf("error lost the endpoint message: %v", errPayload)
	}
}

// An empty prompt is refused before any request is made.
func TestPromptChatRequiresAPrompt(t *testing.T) {
	chat := newFakeChat(t, "never asked")
	gateway := startChatGateway(t)

	status, payload := postPromptChat(t, gateway.URL, map[string]string{
		"prompt":        "   ",
		"promptBaseUrl": chat.server.URL,
		"promptApiKey":  "k",
		"promptModel":   "m",
	}, false)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	errPayload, _ := payload["error"].(map[string]any)
	if errPayload["code"] != "missing_prompt" {
		t.Errorf("error = %v", errPayload)
	}
	if _, _, calls := chat.lastPayload(); calls != 0 {
		t.Errorf("the endpoint was called %d times for an empty prompt", calls)
	}
}
