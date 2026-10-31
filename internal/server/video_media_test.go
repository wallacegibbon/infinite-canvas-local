package server

// The media contract is data now, so these tests exercise the route's wiring rather than a
// workflow id: the fixture below is a first/last-frame workflow and nothing in the handler knows
// that. Everything here fails before a run is submitted.

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestVideoCreateEnforcesDeclaredMedia(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := videoFixture()
	srv := newTestGatewayWith(t, fake, file, graph, entry)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, body := postMultipart(t, gateway.URL+"/v1/videos",
		map[string]string{"model": "video-test", "prompt": "a cat"}, "", "", nil)
	if status != http.StatusBadRequest || !strings.Contains(body, `"code":"missing_reference_image"`) {
		t.Fatalf("no media: status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, "at least 1 reference image") {
		t.Errorf("message does not name the image minimum: %s", body)
	}

	status, body = postMultipart(t, gateway.URL+"/v1/videos",
		map[string]string{"model": "video-test", "prompt": "a cat"}, "audio", "clip.mp3", []byte("ID3"))
	if status != http.StatusBadRequest || !strings.Contains(body, `"code":"unsupported_media"`) {
		t.Fatalf("audio on a frame workflow: status = %d, body = %s", status, body)
	}

	// The frame mode decides how many images "both" needs, which is data too.
	status, body = postMultipart(t, gateway.URL+"/v1/videos",
		map[string]string{"model": "video-test", "prompt": "a cat", "params": `{"frameMode":"both"}`},
		"image", "first.png", []byte("PNG"))
	if status != http.StatusBadRequest || !strings.Contains(body, "at least 2 reference image") {
		t.Fatalf("both frames with one image: status = %d, body = %s", status, body)
	}
}

// The `/v1` adapter now shares the run's artifact step, so this pins that a video request with no
// interpolation still answers with the encoded file.
func TestVideoCreateReturnsTheArtifact(t *testing.T) {
	fake := newFakeComfy(t)
	fake.outputs = map[string]any{
		"90": map[string]any{"gifs": []any{map[string]any{"filename": "out.mp4", "subfolder": "", "type": "output"}}},
	}
	fake.viewMime = "video/mp4"
	fake.viewBody = "MP4DATA"
	file, graph, entry := videoFixture()
	srv := newTestGatewayWith(t, fake, file, graph, entry)
	gateway := httptest.NewServer(srv.Handler())
	defer gateway.Close()

	status, body := postMultipart(t, gateway.URL+"/v1/videos",
		map[string]string{"model": "video-test", "prompt": "a cat", "seconds": "5", "params": `{"interpolation":"off"}`},
		"image", "first.png", []byte("PNG"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %s", status, body)
	}

	var queued map[string]any
	if err := json.Unmarshal([]byte(body), &queued); err != nil {
		t.Fatalf("decode: %v", err)
	}
	id, _ := queued["id"].(string)
	if id == "" {
		t.Fatalf("no task id: %s", body)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		response, err := http.Get(gateway.URL + "/v1/videos/" + id + "/content")
		if err != nil {
			t.Fatalf("get content: %v", err)
		}
		raw, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode == http.StatusOK {
			if string(raw) != "MP4DATA" {
				t.Errorf("body = %q", raw)
			}
			if got := response.Header.Get("Content-Type"); got != "video/mp4" {
				t.Errorf("content type = %q", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task never produced a video: %d %s", response.StatusCode, raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
