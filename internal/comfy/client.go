// Package comfy talks to ComfyUI over HTTP, plus a minimal WebSocket client used
// only to receive preview frames and progress events.
//
// Zero third-party dependencies is deliberate: the gateway needs HTTP and one
// WebSocket client, which is a couple hundred lines we can own outright. That
// removes a whole class of long-term dependency-rot risk.
package comfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Error is a structured ComfyUI call failure.
type Error struct {
	Status     int
	Code       string
	Message    string
	Details    any
	HTTPStatus int
}

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

func comfyError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Client is a concurrency-safe ComfyUI client.
type Client struct {
	baseURL       string
	timeout       time.Duration
	retryAttempts int
	retryDelay    time.Duration
	http          *http.Client
	logger        *logx.Logger
}

// Options configures a Client.
type Options struct {
	BaseURL       string
	TimeoutMs     int
	RetryAttempts int
	RetryDelayMs  int
	Logger        *logx.Logger
}

// New builds a client.
func New(opts Options) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, errors.New("ComfyUI baseUrl must not be empty")
	}
	attempts := opts.RetryAttempts
	if attempts <= 0 {
		attempts = 1
	}
	delay := opts.RetryDelayMs
	if delay <= 0 {
		delay = 500
	}
	timeout := opts.TimeoutMs
	if timeout <= 0 {
		timeout = 10 * 60 * 1000
	}
	return &Client{
		baseURL:       base,
		timeout:       time.Duration(timeout) * time.Millisecond,
		retryAttempts: attempts,
		retryDelay:    time.Duration(delay) * time.Millisecond,
		// Per-request deadlines live in requestOnce, so the client itself is
		// unbounded; that is what allows a 10-minute video job.
		http:   &http.Client{},
		logger: opts.Logger,
	}, nil
}

// BaseURL returns the normalized ComfyUI address.
func (c *Client) BaseURL() string { return c.baseURL }

// requestOptions describes one call.
type requestOptions struct {
	method  string
	body    []byte
	headers map[string]string
	timeout time.Duration
	raw     bool
}

// rawResponse carries binary payloads.
type rawResponse struct {
	Body        []byte
	ContentType string
}

func (c *Client) requestOnce(ctx context.Context, pathname string, opts requestOptions) (any, *rawResponse, error) {
	timeout := opts.timeout
	if timeout <= 0 {
		timeout = c.timeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	method := opts.method
	if method == "" {
		method = http.MethodGet
	}
	var bodyReader io.Reader
	if opts.body != nil {
		bodyReader = bytes.NewReader(opts.body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+pathname, bodyReader)
	if err != nil {
		return nil, nil, comfyError(http.StatusBadGateway, "comfy_unreachable", "cannot reach ComfyUI: "+err.Error())
	}
	for key, value := range opts.headers {
		req.Header.Set(key, value)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Distinguish "the caller cancelled" from "ComfyUI is down": the first is
		// a normal client-disconnect path, the second is an outage.
		if ctx.Err() != nil {
			return nil, nil, comfyError(http.StatusGatewayTimeout, "comfy_aborted", "ComfyUI request was cancelled or timed out")
		}
		return nil, nil, comfyError(http.StatusBadGateway, "comfy_unreachable", "cannot reach ComfyUI: "+err.Error())
	}
	defer resp.Body.Close()

	if opts.raw {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, nil, &Error{
				Status:     http.StatusBadGateway,
				Code:       "comfy_http_error",
				Message:    fmt.Sprintf("ComfyUI %s returned %d", pathname, resp.StatusCode),
				HTTPStatus: resp.StatusCode,
			}
		}
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, nil, comfyError(http.StatusBadGateway, "comfy_unreachable", "failed to read the ComfyUI response: "+readErr.Error())
		}
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		return nil, &rawResponse{Body: data, ContentType: contentType}, nil
	}

	text, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, nil, comfyError(http.StatusBadGateway, "comfy_unreachable", "failed to read the ComfyUI response: "+readErr.Error())
	}
	var decoded any
	if len(text) > 0 {
		if err := json.Unmarshal(text, &decoded); err != nil {
			decoded = map[string]any{"raw": string(text)}
		}
	} else {
		decoded = map[string]any{}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := extractError(decoded)
		if message == "" {
			message = fmt.Sprintf("ComfyUI %s returned %d", pathname, resp.StatusCode)
		}
		status := http.StatusBadGateway
		if resp.StatusCode == http.StatusBadRequest {
			// A 400 from ComfyUI is usually our fault (the submitted graph is
			// wrong), so surface it as a client error rather than a 502.
			status = http.StatusBadRequest
		}
		return nil, nil, &Error{
			Status:     status,
			Code:       "comfy_http_error",
			Message:    util.Truncate(message, 1200),
			Details:    decoded,
			HTTPStatus: resp.StatusCode,
		}
	}
	return decoded, nil, nil
}

// extractError digs a human-readable message out of a ComfyUI error body.
func extractError(decoded any) string {
	object, ok := decoded.(map[string]any)
	if !ok {
		return ""
	}
	if errObj, ok := object["error"].(map[string]any); ok {
		if message, ok := errObj["message"].(string); ok {
			return message
		}
	}
	if message, ok := object["error"].(string); ok {
		return message
	}
	if raw, ok := object["raw"].(string); ok {
		return raw
	}
	return ""
}

func isRetryable(err error) bool {
	var comfyErr *Error
	if !errors.As(err, &comfyErr) {
		return false
	}
	if comfyErr.Code == "comfy_unreachable" {
		return true
	}
	return comfyErr.Code == "comfy_http_error" && comfyErr.HTTPStatus >= 500
}

// request performs a call. Only GET is retried: retrying a POST can enqueue the
// same job twice, which is unrecoverable for the user.
func (c *Client) request(ctx context.Context, pathname string, opts requestOptions) (any, *rawResponse, error) {
	attempts := 1
	if opts.method == "" || opts.method == http.MethodGet {
		attempts = c.retryAttempts
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		decoded, raw, err := c.requestOnce(ctx, pathname, opts)
		if err == nil {
			return decoded, raw, nil
		}
		lastErr = err
		if attempt >= attempts || !isRetryable(err) || ctx.Err() != nil {
			return nil, nil, err
		}
		c.logger.Warn("ComfyUI %s attempt %d failed, retrying in %dms: %s", pathname, attempt, c.retryDelay.Milliseconds()*int64(attempt), err)
		select {
		case <-time.After(c.retryDelay * time.Duration(attempt)):
		case <-ctx.Done():
			return nil, nil, lastErr
		}
	}
	return nil, nil, lastErr
}

// SubmitResult is the outcome of submitting a workflow.
type SubmitResult struct {
	PromptID string
	ClientID string
	Number   any
	Raw      map[string]any
}

// Submit enqueues an API-format workflow.
func (c *Client) Submit(ctx context.Context, workflow map[string]any, clientID string) (*SubmitResult, error) {
	if clientID == "" {
		clientID = util.RandomID("canvas-")
	}
	payload, err := json.Marshal(map[string]any{"prompt": workflow, "client_id": clientID})
	if err != nil {
		return nil, comfyError(http.StatusInternalServerError, "internal_error", "failed to serialise the workflow")
	}
	decoded, _, err := c.request(ctx, "/prompt", requestOptions{
		method:  http.MethodPost,
		body:    payload,
		headers: map[string]string{"Content-Type": "application/json"},
	})
	if err != nil {
		return nil, err
	}
	data, _ := decoded.(map[string]any)
	if nodeErrors := formatNodeErrors(data["node_errors"]); nodeErrors != "" {
		return nil, &Error{
			Status:  http.StatusBadRequest,
			Code:    "node_errors",
			Message: "ComfyUI rejected the workflow: " + nodeErrors,
			Details: data["node_errors"],
		}
	}
	promptID, _ := data["prompt_id"].(string)
	if promptID == "" {
		encoded, _ := json.Marshal(data)
		return nil, &Error{
			Status:  http.StatusBadGateway,
			Code:    "no_prompt_id",
			Message: "ComfyUI returned no prompt_id: " + util.Truncate(string(encoded), 500),
			Details: data,
		}
	}
	return &SubmitResult{PromptID: promptID, ClientID: clientID, Number: data["number"], Raw: data}, nil
}

// formatNodeErrors flattens ComfyUI's node_errors into a single line.
func formatNodeErrors(value any) string {
	nodeErrors, ok := value.(map[string]any)
	if !ok || len(nodeErrors) == 0 {
		return ""
	}
	var messages []string
	for nodeID, raw := range nodeErrors {
		entry, _ := raw.(map[string]any)
		list, _ := entry["errors"].([]any)
		if len(list) == 0 {
			if entry != nil {
				encoded, _ := json.Marshal(entry)
				messages = append(messages, fmt.Sprintf("node %s: %s", nodeID, util.Truncate(string(encoded), 300)))
			}
			continue
		}
		for _, item := range list {
			detail, _ := item.(map[string]any)
			text, _ := detail["message"].(string)
			if text == "" {
				text, _ = detail["details"].(string)
			}
			if text == "" {
				text = "validation failed"
			}
			messages = append(messages, fmt.Sprintf("node %s: %s", nodeID, text))
		}
	}
	if len(messages) > 8 {
		messages = messages[:8]
	}
	return strings.Join(messages, "；")
}

// History returns one execution record, or nil when ComfyUI has none yet.
func (c *Client) History(ctx context.Context, promptID string) (map[string]any, error) {
	decoded, _, err := c.request(ctx, "/history/"+url.PathEscape(promptID), requestOptions{})
	if err != nil {
		return nil, err
	}
	object, _ := decoded.(map[string]any)
	if entry, ok := object[promptID].(map[string]any); ok {
		return entry, nil
	}
	return nil, nil
}

// ViewedFile is an output file fetched from ComfyUI.
type ViewedFile struct {
	Body        []byte
	ContentType string
}

// View downloads one output file.
func (c *Client) View(ctx context.Context, filename, subfolder, fileType string) (*ViewedFile, error) {
	query := url.Values{}
	query.Set("filename", filename)
	query.Set("subfolder", subfolder)
	if fileType == "" {
		fileType = "output"
	}
	query.Set("type", fileType)
	_, raw, err := c.request(ctx, "/view?"+query.Encode(), requestOptions{raw: true})
	if err != nil {
		return nil, err
	}
	return &ViewedFile{Body: raw.Body, ContentType: raw.ContentType}, nil
}

// UploadedImage references a file already uploaded to ComfyUI's input directory.
type UploadedImage struct {
	Name      string `json:"name"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// UploadImage pushes an in-memory image or video into ComfyUI's input directory.
func (c *Client) UploadImage(ctx context.Context, data []byte, filename, mimeType string) (*UploadedImage, error) {
	if filename == "" {
		filename = "input.png"
	}
	if mimeType == "" {
		mimeType = "image/png"
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="%s"`, escapeQuotes(filename)))
	header.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return nil, comfyError(http.StatusInternalServerError, "internal_error", "failed to build the upload request")
	}
	if _, err := part.Write(data); err != nil {
		return nil, comfyError(http.StatusInternalServerError, "internal_error", "failed to build the upload request")
	}
	if err := writer.WriteField("overwrite", "true"); err != nil {
		return nil, comfyError(http.StatusInternalServerError, "internal_error", "failed to build the upload request")
	}
	if err := writer.Close(); err != nil {
		return nil, comfyError(http.StatusInternalServerError, "internal_error", "failed to build the upload request")
	}

	timeout := c.timeout
	if limit := 120 * time.Second; timeout > limit {
		timeout = limit
	}
	decoded, _, err := c.request(ctx, "/upload/image", requestOptions{
		method:  http.MethodPost,
		body:    buffer.Bytes(),
		headers: map[string]string{"Content-Type": writer.FormDataContentType()},
		timeout: timeout,
	})
	if err != nil {
		return nil, err
	}
	object, _ := decoded.(map[string]any)
	name, _ := object["name"].(string)
	if name == "" {
		encoded, _ := json.Marshal(object)
		return nil, &Error{
			Status:  http.StatusBadGateway,
			Code:    "upload_failed",
			Message: "ComfyUI upload failed: " + util.Truncate(string(encoded), 500),
			Details: object,
		}
	}
	subfolder, _ := object["subfolder"].(string)
	kind, _ := object["type"].(string)
	if kind == "" {
		kind = "input"
	}
	return &UploadedImage{Name: name, Subfolder: subfolder, Type: kind}, nil
}

// Interrupt aborts the currently sampling job.
func (c *Client) Interrupt(ctx context.Context, promptID string) {
	payload := []byte("{}")
	if promptID != "" {
		payload, _ = json.Marshal(map[string]any{"prompt_id": promptID})
	}
	if _, _, err := c.request(ctx, "/interrupt", requestOptions{
		method:  http.MethodPost,
		body:    payload,
		headers: map[string]string{"Content-Type": "application/json"},
		timeout: 5 * time.Second,
	}); err != nil {
		c.logger.Warn("interrupt failed: %s", err)
	}
}

// Cancel deletes the queued item and interrupts sampling. If the job already
// started, deleting fails and interrupt is what stops it.
func (c *Client) Cancel(ctx context.Context, promptID string) {
	done := make(chan struct{}, 2)
	go func() {
		payload, _ := json.Marshal(map[string]any{"delete": []string{promptID}})
		if _, _, err := c.request(ctx, "/queue", requestOptions{
			method:  http.MethodPost,
			body:    payload,
			headers: map[string]string{"Content-Type": "application/json"},
			timeout: 5 * time.Second,
		}); err != nil {
			c.logger.Warn("queue delete failed: %s", err)
		}
		done <- struct{}{}
	}()
	go func() {
		c.Interrupt(ctx, promptID)
		done <- struct{}{}
	}()
	<-done
	<-done
}

// FreeMemory asks ComfyUI to unload models and release VRAM.
func (c *Client) FreeMemory(ctx context.Context) error {
	payload, _ := json.Marshal(map[string]any{"unload_models": true, "free_memory": true})
	_, _, err := c.request(ctx, "/free", requestOptions{
		method:  http.MethodPost,
		body:    payload,
		headers: map[string]string{"Content-Type": "application/json"},
		timeout: 15 * time.Second,
	})
	return err
}

// ObjectInfo fetches every node definition.
func (c *Client) ObjectInfo(ctx context.Context) (map[string]any, error) {
	decoded, _, err := c.request(ctx, "/object_info", requestOptions{timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	object, _ := decoded.(map[string]any)
	return object, nil
}

// SystemStats fetches ComfyUI's own statistics.
func (c *Client) SystemStats(ctx context.Context) (map[string]any, error) {
	decoded, _, err := c.request(ctx, "/system_stats", requestOptions{timeout: 3 * time.Second})
	if err != nil {
		return nil, err
	}
	object, _ := decoded.(map[string]any)
	return object, nil
}

// FolderPaths reads ComfyUI's model directory mapping. Older ComfyUI builds lack
// the endpoint, in which case an empty map means "fall back to models/<type>".
func (c *Client) FolderPaths(ctx context.Context) map[string]any {
	decoded, _, err := c.request(ctx, "/internal/folder_paths", requestOptions{timeout: 5 * time.Second})
	if err != nil {
		c.logger.Warn("failed to read ComfyUI model folders: %s", err)
		return map[string]any{}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return object
}

// Queue reads the current queue.
func (c *Client) Queue(ctx context.Context) (map[string]any, error) {
	decoded, _, err := c.request(ctx, "/queue", requestOptions{timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	object, _ := decoded.(map[string]any)
	return object, nil
}

// IsReachable probes ComfyUI via /system_stats.
func (c *Client) IsReachable(ctx context.Context) bool {
	_, err := c.SystemStats(ctx)
	return err == nil
}

// escapeQuotes escapes a filename for a Content-Disposition header.
func escapeQuotes(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}
