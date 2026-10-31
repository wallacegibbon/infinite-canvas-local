package server

// Fixtures and plumbing shared by the run tests: a first/last-frame workflow nothing in the gateway
// knows by name, and a multipart poster for the upload route.

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"testing"
)

func videoFixture() (string, map[string]any, map[string]any) {
	graph := map[string]any{
		"76": map[string]any{"class_type": "MiniMaxH3", "inputs": map[string]any{"prompt": ""}},
		"90": map[string]any{"class_type": "SaveVideo", "inputs": map[string]any{"video": []any{"76", 0}}},
	}
	entry := map[string]any{
		"id":         "video-test",
		"name":       "video-test",
		"type":       "video",
		"file":       "test-video.json",
		"mode":       "frames",
		"parameters": []any{},
		"frameMode":  map[string]any{"default": "byCount", "images": map[string]any{"first": 1, "last": 1, "both": 2}},
		"accepts": map[string]any{
			"images": map[string]any{"min": 1, "max": 2},
			"videos": map[string]any{"max": 0},
			"audios": map[string]any{"max": 0},
			"note":   "first/last frame accepts a first frame, a last frame, or both",
		},
		"bindings": map[string]any{
			"prompt": map[string]any{"node": "76", "input": "prompt"},
			// The first/last wiring is a binding, so a test can see which socket a role reached.
			"frames": map[string]any{
				"node": "76", "input": "first_frame", "from": "image", "frameMode": true,
				"firstInput": "first_frame", "lastInput": "last_frame", "max": 2,
			},
		},
	}
	return "test-video.json", graph, entry
}

// postMultipart sends a form with the given text fields and one optional file.
func postMultipart(t *testing.T, url string, fields map[string]string, fileField, filename string, content []byte) (int, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field %s: %v", key, err)
		}
	}
	if fileField != "" {
		part, err := writer.CreateFormFile(fileField, filename)
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, url, &body)
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
	return response.StatusCode, string(raw)
}
