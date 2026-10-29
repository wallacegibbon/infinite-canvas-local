package comfy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"comfyui-gateway/internal/logx"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Options{
		BaseURL:       server.URL,
		TimeoutMs:     5000,
		RetryAttempts: 3,
		RetryDelayMs:  1,
		Logger:        logx.NewWithWriter("test", io.Discard),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func TestGetIsRetried(t *testing.T) {
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		_, _ = w.Write([]byte(`{"system":{}}`))
	})

	if _, err := client.SystemStats(context.Background()); err != nil {
		t.Fatalf("SystemStats after transient failures: %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (two failures then success)", calls.Load())
	}
}

func TestPostIsNotRetried(t *testing.T) {
	// Retrying a submit would enqueue the same job twice, which is unrecoverable.
	var calls atomic.Int32
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})

	_, err := client.Submit(context.Background(), map[string]any{"1": map[string]any{}}, "c1")
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want exactly 1 for a POST", calls.Load())
	}
}

func TestUnreachableIsStructured(t *testing.T) {
	client, err := New(Options{
		BaseURL:   "http://127.0.0.1:1",
		Logger:    logx.NewWithWriter("test", io.Discard),
		TimeoutMs: 200,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.SystemStats(context.Background())
	var comfyErr *Error
	if !errors.As(err, &comfyErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if comfyErr.Code != "comfy_unreachable" {
		t.Errorf("code = %q", comfyErr.Code)
	}
	if !isRetryable(comfyErr) {
		t.Error("an unreachable ComfyUI must be retryable")
	}
}

func TestSubmitReturnsPromptID(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prompt" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != "c1" {
			t.Errorf("client_id = %v", body["client_id"])
		}
		_, _ = w.Write([]byte(`{"prompt_id":"p1","number":7}`))
	})

	result, err := client.Submit(context.Background(), map[string]any{"1": map[string]any{}}, "c1")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if result.PromptID != "p1" {
		t.Errorf("promptID = %q", result.PromptID)
	}
}

func TestSubmitNodeErrorsAreClientErrors(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"node_errors":{"8":{"errors":[{"message":"bad seed"}]}}}`))
	})

	_, err := client.Submit(context.Background(), map[string]any{}, "c1")
	var comfyErr *Error
	if !errors.As(err, &comfyErr) {
		t.Fatalf("error type = %T", err)
	}
	if comfyErr.Status != http.StatusBadRequest || comfyErr.Code != "node_errors" {
		t.Errorf("status/code = %d/%q, want 400/node_errors", comfyErr.Status, comfyErr.Code)
	}
	if !strings.Contains(comfyErr.Message, "bad seed") {
		t.Errorf("message = %q, want the node error included", comfyErr.Message)
	}
}

func TestHistoryUnwrapsPromptID(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/history/p1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"p1":{"status":{"completed":true},"outputs":{"9":{"images":[]}}}}`))
	})

	history, err := client.History(context.Background(), "p1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if history == nil {
		t.Fatal("history is nil")
	}
	if _, ok := history["status"]; !ok {
		t.Errorf("history was not unwrapped: %v", history)
	}
}

func TestHistoryMissingReturnsNil(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	history, err := client.History(context.Background(), "p1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if history != nil {
		t.Errorf("history = %v, want nil", history)
	}
}

func TestViewReturnsBytes(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/view" {
			t.Errorf("path = %q", r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("filename") != "out.png" || query.Get("type") != "output" {
			t.Errorf("query = %v", query)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNG"))
	})

	viewed, err := client.View(context.Background(), "out.png", "", "output")
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	if string(viewed.Body) != "PNG" || viewed.ContentType != "image/png" {
		t.Errorf("viewed = %q / %q", viewed.Body, viewed.ContentType)
	}
}

func TestUploadImageSendsMultipart(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload/image" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		file, header, err := r.FormFile("image")
		if err != nil {
			t.Fatalf("form file: %v", err)
		}
		defer file.Close()
		data, _ := io.ReadAll(file)
		if string(data) != "BYTES" {
			t.Errorf("uploaded bytes = %q", data)
		}
		if header.Filename != "in.png" {
			t.Errorf("filename = %q", header.Filename)
		}
		if r.FormValue("overwrite") != "true" {
			t.Errorf("overwrite = %q", r.FormValue("overwrite"))
		}
		_, _ = w.Write([]byte(`{"name":"in.png","subfolder":"sub","type":"input"}`))
	})

	uploaded, err := client.UploadImage(context.Background(), []byte("BYTES"), "in.png", "image/png")
	if err != nil {
		t.Fatalf("UploadImage: %v", err)
	}
	if uploaded.Name != "in.png" || uploaded.Subfolder != "sub" {
		t.Errorf("uploaded = %+v", uploaded)
	}
}

func TestUploadImageMissingNameIsAnError(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := client.UploadImage(context.Background(), []byte("x"), "in.png", "image/png"); err == nil {
		t.Fatal("expected an error when ComfyUI omits the uploaded name")
	}
}

func TestFolderPathsFailureIsNonFatal(t *testing.T) {
	// Older ComfyUI builds lack this endpoint; the gateway must fall back silently.
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	paths := client.FolderPaths(context.Background())
	if paths == nil || len(paths) != 0 {
		t.Errorf("paths = %v, want an empty map", paths)
	}
}

func TestIsReachable(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"system":{}}`))
	})
	if !client.IsReachable(context.Background()) {
		t.Error("expected the server to be reachable")
	}
}
