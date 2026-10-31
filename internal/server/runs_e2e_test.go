package server

// End-to-end cover for the run protocol (docs/protocol.md): one submit shape, one id, outputs off
// that id, and media referenced by upload id.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// postUpload sends one file to an endpoint and decodes the JSON answer.
func postUpload(t *testing.T, url, filename, mimeType string, content []byte) (int, map[string]any) {
	t.Helper()
	status, body := postMultipart(t, url, nil, "file", filename, content)
	if status != http.StatusOK {
		return status, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("upload response is not JSON: %v (%s)", err, body)
	}
	return status, payload
}

// uploadID posts one file and returns the upload id.
func uploadID(t *testing.T, gateway *httptest.Server, filename, mimeType string, content []byte) string {
	t.Helper()
	status, payload := postUpload(t, gateway.URL+"/api/uploads", filename, mimeType, content)
	if status != http.StatusOK {
		t.Fatalf("upload status = %d", status)
	}
	uploaded, _ := payload["upload"].(map[string]any)
	id, _ := uploaded["id"].(string)
	if id == "" {
		t.Fatalf("upload returned no id: %v", payload)
	}
	if uploaded["kind"] != "image" && strings.HasSuffix(filename, ".png") {
		t.Errorf("kind = %v, want image", uploaded["kind"])
	}
	return id
}

// getBytes fetches one URL and returns the status and body.
func getBytes(t *testing.T, url string) (int, []byte) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, body
}

func waitForRun(t *testing.T, gateway *httptest.Server, id string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/runs/"+id, nil)
		if status != http.StatusOK {
			t.Fatalf("run status = %d, body = %v", status, payload)
		}
		switch payload["status"] {
		case "succeeded", "failed", "cancelled":
			return payload
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never finished: %v", payload)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runOutputs(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	raw, _ := payload["outputs"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, _ := item.(map[string]any)
		out = append(out, entry)
	}
	return out
}

func errorText(payload map[string]any) string {
	problem, _ := payload["error"].(map[string]any)
	message, _ := problem["message"].(string)
	return message
}

func TestRunImageProducesEveryOutput(t *testing.T) {
	fake := newFakeComfy(t)
	gateway := httptest.NewServer(newTestGateway(t, fake).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat", "count": 2},
	})
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %v", status, payload)
	}
	// The queue may pick the run up before the response is written, so both states are correct:
	// `status` is the state at that moment, never a promise about the past.
	if status := payload["status"]; status != "queued" && status != "running" {
		t.Errorf("status = %v, want queued or running", status)
	}
	if payload["workflow"] != "image-test" {
		t.Errorf("workflow = %v", payload["workflow"])
	}
	id, _ := payload["id"].(string)
	if id == "" {
		t.Fatalf("no run id: %v", payload)
	}

	finished := waitForRun(t, gateway, id)
	if finished["status"] != "succeeded" {
		t.Fatalf("run failed: %v", finished)
	}
	outputs := runOutputs(t, finished)
	if len(outputs) != 2 {
		t.Fatalf("outputs = %d, want the count the request asked for: %v", len(outputs), finished)
	}
	for index, output := range outputs {
		if output["kind"] != "image" {
			t.Errorf("output %d kind = %v", index, output["kind"])
		}
		if output["mime"] != "image/png" {
			t.Errorf("output %d mime = %v", index, output["mime"])
		}
		wantURL := "/api/runs/" + id + "/outputs/" + strconv.Itoa(index)
		if output["url"] != wantURL {
			t.Errorf("output %d url = %v, want %s", index, output["url"], wantURL)
		}
		status, body := getBytes(t, gateway.URL+wantURL)
		if status != http.StatusOK || string(body) != "PNGDATA" {
			t.Errorf("output %d = %d %q", index, status, body)
		}
	}

	// The list endpoint sees the same run, and completed is reported as succeeded.
	status, list := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/runs?status=succeeded", nil)
	if status != http.StatusOK {
		t.Fatalf("list status = %d", status)
	}
	runs, _ := list["runs"].([]any)
	if len(runs) != 1 {
		t.Errorf("runs = %d, want 1: %v", len(runs), list)
	}
}

func TestRunResolvesUploadedMediaByRole(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	// A reference-image workflow: the array binding injects a LoadImage node.
	graph["76"] = map[string]any{"class_type": "RefNode", "inputs": map[string]any{}}
	entry["accepts"] = map[string]any{"images": map[string]any{"max": 1}}
	entry["bindings"] = map[string]any{
		"prompt": map[string]any{"node": "4", "input": "text"},
		"refs": map[string]any{
			"node": "76", "input": "ref_images", "from": "image", "array": true,
			"inputPrefix": "ref_images.ref_image_", "min": 1, "max": 1,
		},
	}
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	id := uploadID(t, gateway, "reference.png", "image/png", []byte("PNG"))

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat"},
		"media":    map[string]any{"reference": []string{id}},
	})
	if status != http.StatusAccepted {
		t.Fatalf("submit status = %d, body = %v", status, payload)
	}
	finished := waitForRun(t, gateway, payload["id"].(string))
	if finished["status"] != "succeeded" {
		t.Fatalf("run failed: %v", finished)
	}

	submitted := fake.submittedGraph()
	if submitted == nil {
		t.Fatal("no graph was submitted")
	}
	injected, _ := submitted["9101"].(map[string]any)
	if injected == nil || injected["class_type"] != "LoadImage" {
		t.Fatalf("no LoadImage node was injected: %v", submitted)
	}
	inputs, _ := injected["inputs"].(map[string]any)
	if inputs["image"] != "uploaded.png" {
		t.Errorf("injected image = %v, want the ComfyUI name from the upload", inputs["image"])
	}
	refNode, _ := submitted["76"].(map[string]any)
	refInputs, _ := refNode["inputs"].(map[string]any)
	if _, ok := refInputs["ref_images.ref_image_0"]; !ok {
		t.Errorf("the role did not reach the binding: %v", refInputs)
	}
}

func TestRunRejectsBadMedia(t *testing.T) {
	fake := newFakeComfy(t)
	gateway := httptest.NewServer(newTestGateway(t, fake).Handler())
	defer gateway.Close()

	audio := uploadID(t, gateway, "clip.mp3", "audio/mpeg", []byte("ID3"))

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat"},
		"media":    map[string]any{"plain": []string{audio}},
	})
	if status != http.StatusBadRequest || !strings.Contains(errorText(payload), "media role") {
		t.Errorf("unknown role: status = %d, body = %v", status, payload)
	}

	status, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat"},
		"media":    map[string]any{"reference": []string{audio}},
	})
	if status != http.StatusBadRequest || !strings.Contains(errorText(payload), "needs image") {
		t.Errorf("kind mismatch: status = %d, body = %v", status, payload)
	}

	status, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat"},
		"media":    map[string]any{"reference": []string{"upl_missing"}},
	})
	if status != http.StatusNotFound {
		t.Errorf("missing upload: status = %d, body = %v", status, payload)
	}

	status, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "nope",
	})
	if status != http.StatusNotFound {
		t.Errorf("unknown workflow: status = %d, body = %v", status, payload)
	}
}

// A workflow with variants picks the first mode when the request omits one, and the variant's
// contract is what applies. Without the default, a media rule declared on a variant would never
// fire for a request that forgot `mode`.
func TestRunUsesFirstVariantByDefault(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	entry["variants"] = map[string]any{
		"edit": map[string]any{
			"file":    file,
			"accepts": map[string]any{"images": map[string]any{"max": 0}},
		},
	}
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	imageID := uploadID(t, gateway, "reference.png", "image/png", []byte("PNG"))

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat"},
		"media":    map[string]any{"reference": []string{imageID}},
	})
	if status != http.StatusBadRequest || !strings.Contains(errorText(payload), "accepts no reference images") {
		t.Fatalf("the default variant's contract was not applied: status = %d, body = %v", status, payload)
	}

	status, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a cat", "mode": "nope"},
	})
	if status != http.StatusBadRequest || !strings.Contains(errorText(payload), "modes are") {
		t.Fatalf("unknown mode: status = %d, body = %v", status, payload)
	}
}

// Submission is where a wrong request must fail: a binding error would surface minutes later as a
// failed run, and a typo in a key would be ignored entirely.
func TestRunValidatesTypedInputs(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	entry["parameters"] = []any{
		map[string]any{"key": "steps", "type": "number", "min": 1, "max": 8},
	}
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	cases := []struct {
		name   string
		inputs map[string]any
		code   string
	}{
		{"unknown key", map[string]any{"prompt": "x", "setps": 4}, "unknown_input"},
		{"missing prompt", map[string]any{}, "missing_input"},
		{"bad size", map[string]any{"prompt": "x", "size": "huge"}, "invalid_input"},
		{"count out of range", map[string]any{"prompt": "x", "count": 0}, "invalid_input"},
		{"declared range", map[string]any{"prompt": "x", "steps": 0}, "invalid_input"},
	}
	for _, testCase := range cases {
		status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
			"workflow": "image-test",
			"inputs":   testCase.inputs,
		})
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%v)", testCase.name, status, payload)
			continue
		}
		problem, _ := payload["error"].(map[string]any)
		if problem["code"] != testCase.code {
			t.Errorf("%s: code = %v, want %s", testCase.name, problem["code"], testCase.code)
		}
	}

	// The same request with valid values is accepted.
	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "x", "size": "1024x1024", "count": 1, "steps": 4},
	})
	if status != http.StatusAccepted {
		t.Fatalf("valid request rejected: %d %v", status, payload)
	}
}

// The schema endpoint is the contract a client builds a form from.
func TestWorkflowSchemaListsInputs(t *testing.T) {
	fake := newFakeComfy(t)
	gateway := httptest.NewServer(newTestGateway(t, fake).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodGet, gateway.URL+"/api/workflows/image-test/schema", nil)
	if status != http.StatusOK {
		t.Fatalf("schema status = %d", status)
	}
	inputs, _ := payload["schema"].(map[string]any)["inputs"].([]any)
	if len(inputs) == 0 {
		t.Fatalf("schema has no inputs: %v", payload)
	}
	found := false
	for _, raw := range inputs {
		spec, _ := raw.(map[string]any)
		if spec["key"] == "prompt" {
			found = true
			if spec["required"] != true {
				t.Errorf("prompt must be required: %v", spec)
			}
		}
	}
	if !found {
		t.Errorf("prompt is missing from the schema: %v", inputs)
	}
}

// An encoder writes a fixed set of containers. A request for another one must fail at submission,
// with the accepted set in the message, rather than inside ComfyUI a minute later.
func TestRunRejectsUndeclaredOutputContainer(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	// A container is only meaningful when a binding carries it into the graph.
	save, _ := graph["9"].(map[string]any)
	saveInputs, _ := save["inputs"].(map[string]any)
	saveInputs["format"] = "png"
	bindings, _ := entry["bindings"].(map[string]any)
	bindings["format"] = map[string]any{"node": "9", "input": "format", "default": "png"}
	entry["outputs"] = map[string]any{
		"kind":        "image",
		"formatParam": "format",
		"formats":     map[string]any{"png": map[string]any{}},
	}
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "x", "format": "jpg"},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%v)", status, payload)
	}
	problem, _ := payload["error"].(map[string]any)
	if problem["code"] != "unsupported_output_format" {
		t.Errorf("code = %v", problem["code"])
	}
	if !strings.Contains(asString(problem["message"]), "writes png") {
		t.Errorf("message = %v", problem["message"])
	}

	status, payload = doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "x", "format": "png"},
	})
	if status != http.StatusAccepted {
		t.Fatalf("declared container rejected: %d %v", status, payload)
	}
	// Let the run finish before the temporary data directory is removed: the queue persists its
	// record asynchronously and would otherwise write into a directory that is already gone.
	if finished := waitForRun(t, gateway, asString(payload["id"])); finished["status"] != "succeeded" {
		t.Fatalf("run did not succeed: %v", finished)
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

// A transparent background rewrites the prompt in the `/v1` image routes; the run route has to do
// the same, or the same request would render differently depending on which route produced it.
func TestRunAppliesBackgroundPrompt(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	entry["backgroundPrompt"] = "rgba hint"
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs":   map[string]any{"prompt": "a glass", "background": "transparent"},
	})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	finished := waitForRun(t, gateway, asString(payload["id"]))
	if finished["status"] != "succeeded" {
		t.Fatalf("run did not succeed: %v", finished)
	}
	submitted := fake.submittedGraph()
	encoder, _ := submitted["4"].(map[string]any)
	inputs, _ := encoder["inputs"].(map[string]any)
	prompt, _ := inputs["text"].(string)
	if !strings.Contains(prompt, "alpha channel") || !strings.Contains(prompt, "a glass") {
		t.Errorf("background prompt not applied: %q", prompt)
	}
}

// A MOSS layout is rendered into the prompt by one function; a run must call it too.
func TestRunAppliesMossLayoutPrompt(t *testing.T) {
	fake := newFakeComfy(t)
	file, graph, entry := imageFixture()
	entry["promptInject"] = "moss-layout"
	gateway := httptest.NewServer(newTestGatewayWith(t, fake, file, graph, entry).Handler())
	defer gateway.Close()

	status, payload := doJSON(t, gateway.Client(), http.MethodPost, gateway.URL+"/api/runs", map[string]any{
		"workflow": "image-test",
		"inputs": map[string]any{
			"prompt":        "a quiet room",
			"layoutEnabled": true,
			"layout": map[string]any{
				"width":   1024,
				"height":  1024,
				"summary": "a room",
				"regions": []any{
					map[string]any{"label": "character", "caption": "a woman", "bbox": map[string]any{"x": 0.1, "y": 0.2, "width": 0.3, "height": 0.4}},
				},
			},
		},
	})
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, body = %v", status, payload)
	}
	finished := waitForRun(t, gateway, asString(payload["id"]))
	if finished["status"] != "succeeded" {
		t.Fatalf("run did not succeed: %v", finished)
	}
	submitted := fake.submittedGraph()
	encoder, _ := submitted["4"].(map[string]any)
	inputs, _ := encoder["inputs"].(map[string]any)
	prompt, _ := inputs["text"].(string)
	for _, want := range []string{`"summary"`, `"label"`, "a woman"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("layout prompt missing %s: %q", want, prompt)
		}
	}
}
