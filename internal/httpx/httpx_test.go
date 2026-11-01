package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	return &config.Config{
		CorsOrigins:   []string{"http://localhost:3000"},
		PublicBaseURL: "http://127.0.0.1:8190",
	}
}

func discardLogger() *logx.Logger { return logx.NewWithWriter("test", io.Discard) }

func TestWriteErrorShapes(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteError(recorder, BadRequest("bad_thing", "nope"))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Type != "invalid_request_error" {
		t.Errorf("type = %q", body.Error.Type)
	}
	if body.Error.Code != "bad_thing" || body.Error.Message != "nope" {
		t.Errorf("body = %+v", body.Error)
	}
}

func TestWriteErrorServerErrorType(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteError(recorder, NewError(http.StatusInternalServerError, "internal_error", "boom"))

	var body struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	if body.Error.Type != "server_error" {
		t.Errorf("type = %q, want server_error", body.Error.Type)
	}
}

func TestWriteErrorAcceptsPlainError(t *testing.T) {
	recorder := httptest.NewRecorder()
	WriteError(recorder, errors.New("plain"))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
}

func TestCORSLoopbackIsAllowed(t *testing.T) {
	cfg := testConfig(t)
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/workflows", nil)
	request.Header.Set("Origin", "http://127.0.0.1:3000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:3000" {
		t.Errorf("allow-origin = %q, want the request origin echoed", got)
	}
	if !strings.Contains(recorder.Header().Get("Vary"), "Origin") {
		t.Errorf("missing Vary: Origin")
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	cfg := testConfig(t)
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/workflows", nil)
	request.Header.Set("Origin", "http://evil.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want empty for a foreign origin", got)
	}
}

// The canvas and the gateway are two ports on one address. Requiring that pair to be listed in
// CORS_ORIGINS is what made a LAN address fail with no useful error, so the same-host case is
// allowed by rule; anything served from a different host still needs the list.
func TestCORSSameHostOriginIsAllowed(t *testing.T) {
	cfg := testConfig(t)
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/workflows", nil)
	request.Host = "192.168.3.232:8190"
	request.Header.Set("Origin", "http://192.168.3.232:3000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "http://192.168.3.232:3000" {
		t.Errorf("allow-origin = %q, want the canvas origin echoed", got)
	}
}

func TestCORSRejectsOtherHostOnTheSameNetwork(t *testing.T) {
	cfg := testConfig(t)
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	request := httptest.NewRequest(http.MethodGet, "/api/workflows", nil)
	request.Host = "192.168.3.232:8190"
	request.Header.Set("Origin", "http://192.168.3.99:3000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("allow-origin = %q, want empty for another machine", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	cfg := testConfig(t)
	handler := CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("preflight must not reach the handler")
	}))

	request := httptest.NewRequest(http.MethodOptions, "/api/workflows", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", recorder.Code)
	}
}

func TestGuardBlocksCrossSiteWrite(t *testing.T) {
	cfg := testConfig(t)
	handler := Guard(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a cross-site write must not reach the handler")
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", recorder.Code)
	}
}

func TestGuardAllowsSameSiteWrite(t *testing.T) {
	cfg := testConfig(t)
	called := false
	handler := Guard(cfg)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	request.Header.Set("Sec-Fetch-Site", "same-site")
	request.Header.Set("Origin", "http://localhost:3000")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if !called {
		t.Error("a same-site write should reach the handler")
	}
}

func TestRequestIDIsReusedAndSanitized(t *testing.T) {
	var seen string
	handler := WithRequestID(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDOf(r)
	}))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set("X-Request-Id", "abc<script>")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if seen != "abcscript" {
		t.Errorf("seen id = %q, want unsafe characters stripped", seen)
	}
	if recorder.Header().Get("X-Request-Id") != "abcscript" {
		t.Errorf("response header = %q", recorder.Header().Get("X-Request-Id"))
	}
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	var seen string
	handler := WithRequestID(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDOf(r)
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health", nil))

	if !strings.HasPrefix(seen, "req_") {
		t.Errorf("id = %q, want a generated req_ id", seen)
	}
}

func TestRequestBodyJSON(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"model":"m","n":2}`))
	request.Header.Set("Content-Type", "application/json")
	ctx := &Ctx{R: request, W: httptest.NewRecorder()}

	body, err := ctx.Body()
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if String(body, "model") != "m" {
		t.Errorf("model = %q", String(body, "model"))
	}
	if Number(body, "n", 0) != 2 {
		t.Errorf("n = %v", Number(body, "n", 0))
	}
}

func TestRequestBodyInvalidJSON(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{oops`))
	request.Header.Set("Content-Type", "application/json")
	ctx := &Ctx{R: request, W: httptest.NewRecorder()}

	_, err := ctx.Body()
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != "invalid_json" {
		t.Fatalf("got %v, want invalid_json", err)
	}
}

func TestRequestBodyURLEncoded(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("enabled=false&ids=a"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := &Ctx{R: request, W: httptest.NewRecorder()}

	body, err := ctx.Body()
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if String(body, "enabled") != "false" {
		t.Errorf("enabled = %q", String(body, "enabled"))
	}
}

func buildMultipart(t *testing.T, files int, size int) (io.Reader, string) {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	for i := 0; i < files; i++ {
		part, err := writer.CreateFormFile("image", "file.png")
		if err != nil {
			t.Fatalf("create part: %v", err)
		}
		if _, err := part.Write(bytes.Repeat([]byte("a"), size)); err != nil {
			t.Fatalf("write part: %v", err)
		}
	}
	if err := writer.WriteField("model", "m"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return buffer, writer.FormDataContentType()
}

func TestParseMultipartFieldsAndFiles(t *testing.T) {
	reader, contentType := buildMultipart(t, 2, 16)
	request := httptest.NewRequest(http.MethodPost, "/x", reader)
	request.Header.Set("Content-Type", contentType)

	form, err := ParseMultipart(request)
	if err != nil {
		t.Fatalf("ParseMultipart: %v", err)
	}
	if len(form.Files) != 2 {
		t.Errorf("files = %d, want 2", len(form.Files))
	}
	if form.Files[0].Filename != "file.png" || len(form.Files[0].Bytes) != 16 {
		t.Errorf("file = %+v", form.Files[0])
	}
	if values := form.Values["model"]; len(values) != 1 || values[0] != "m" {
		t.Errorf("model values = %v", values)
	}
}

func TestParseMultipartRejectsTooManyFiles(t *testing.T) {
	reader, contentType := buildMultipart(t, MaxUploadFiles+1, 4)
	request := httptest.NewRequest(http.MethodPost, "/x", reader)
	request.Header.Set("Content-Type", contentType)

	_, err := ParseMultipart(request)
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != "LIMIT_FILE_COUNT" {
		t.Fatalf("got %v, want LIMIT_FILE_COUNT", err)
	}
}

func TestParseMultipartRejectsOversizedFile(t *testing.T) {
	reader, contentType := buildMultipart(t, 1, MaxFileBytes+1024)
	request := httptest.NewRequest(http.MethodPost, "/x", reader)
	request.Header.Set("Content-Type", contentType)

	_, err := ParseMultipart(request)
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != "LIMIT_FILE_SIZE" {
		t.Fatalf("got %v, want LIMIT_FILE_SIZE", err)
	}
	if appErr.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", appErr.Status)
	}
}

func TestRecoverTurnsPanicInto500(t *testing.T) {
	handler := Recover(discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("explode")
	}))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/x", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "internal_error") {
		t.Errorf("body = %s", recorder.Body.String())
	}
}

func TestBoolAndStringSliceHelpers(t *testing.T) {
	body := map[string]any{
		"flag":  true,
		"text":  "yes",
		"ids":   []any{"a", "b", 3},
		"empty": "",
	}
	if !Bool(body, "flag") {
		t.Error("Bool(flag) = false")
	}
	if !Bool(body, "text") {
		t.Error(`Bool("yes") should be true`)
	}
	ids := StringSlice(body, "ids")
	if len(ids) != 2 || ids[0] != "a" {
		t.Errorf("StringSlice = %v, want only the string entries", ids)
	}
}
