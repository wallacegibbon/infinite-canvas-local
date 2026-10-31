// Package httpx is the HTTP layer: a single error type, JSON responses, request
// body parsing, and the middleware stack.
//
// The goal is that each route handler only contains business logic and returns an
// error; the mapping from error to JSON lives in one place. The old
// implementation mixed three different error shapes, which made the frontend
// defensive everywhere.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Error is a business error that carries HTTP semantics.
type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

// FailureCode exposes the stable code to packages that persist a failure without
// importing this one (internal/task does exactly that).
func (e *Error) FailureCode() string { return e.Code }

// FailureDetails exposes the structured payload for the same reason.
func (e *Error) FailureDetails() any { return e.Details }

// StatusCode exposes the HTTP status for the same reason.
func (e *Error) StatusCode() int { return e.Status }

// Error implements the error interface.
func (e *Error) Error() string { return e.Message }

// NewError builds an error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// Errorf builds an error with a formatted message.
func Errorf(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithDetails attaches structured details, surfaced verbatim as error.details.
func (e *Error) WithDetails(details any) *Error {
	e.Details = details
	return e
}

// BadRequest builds a 400 error.
func BadRequest(code, message string) *Error {
	return NewError(http.StatusBadRequest, code, message)
}

// NotFound builds a 404 error.
func NotFound(code, message string) *Error {
	return NewError(http.StatusNotFound, code, message)
}

// envelope is the error response body; field names are part of the frontend contract.
type envelope struct {
	Error payload `json:"error"`
}

type payload struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
	Details any    `json:"details,omitempty"`
}

// JSON writes a JSON response. A write failure is unrecoverable for the caller,
// so there is nothing to do beyond returning.
func JSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// OK writes a 200 JSON response.
func OK(w http.ResponseWriter, value any) { JSON(w, http.StatusOK, value) }

// WriteError renders any error into the canonical JSON error body.
func WriteError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := "internal_error"
	message := "Internal error"
	var details any

	var appErr *Error
	if errors.As(err, &appErr) {
		status = appErr.Status
		code = appErr.Code
		message = appErr.Message
		details = appErr.Details
	} else {
		message = err.Error()
	}
	if status < 400 {
		status = http.StatusInternalServerError
	}
	kind := "invalid_request_error"
	if status >= 500 {
		kind = "server_error"
	}
	if code == "" {
		if status >= 500 {
			code = "internal_error"
		} else {
			code = "bad_request"
		}
	}
	JSON(w, status, envelope{Error: payload{Message: message, Type: kind, Code: code, Details: details}})
}

// WriteJSONError writes a bare error message, for middleware that has no *Error.
func WriteJSONError(w http.ResponseWriter, status int, code, message string) {
	WriteError(w, NewError(status, code, message))
}

// Handler is a business handler: returning an error hands rendering to the
// shared exit point.
type Handler func(*Ctx) error

// Ctx carries one request and memoizes the parsed body.
type Ctx struct {
	W         http.ResponseWriter
	R         *http.Request
	RequestID string

	body     map[string]any
	bodyErr  error
	parsed   bool
	formData *FormData
	formErr  error
	formDone bool
}

// MaxJSONBytes corresponds to the old express.json({limit: "50mb"}).
const MaxJSONBytes = 50 << 20

// Body returns the request fields as a map.
//
// The old implementation had three sources for req.body: JSON, urlencoded, and
// multipart text fields. Folding them into one map means handlers never have to
// branch on Content-Type.
func (c *Ctx) Body() (map[string]any, error) {
	if c.parsed {
		return c.body, c.bodyErr
	}
	c.parsed = true
	c.body = map[string]any{}

	contentType := c.R.Header.Get("Content-Type")
	switch {
	case isMultipart(contentType):
		form, err := c.Form()
		if err != nil {
			c.bodyErr = err
			return nil, err
		}
		for key, values := range form.Values {
			switch len(values) {
			case 0:
			case 1:
				c.body[key] = values[0]
			default:
				items := make([]any, len(values))
				for i, value := range values {
					items[i] = value
				}
				c.body[key] = items
			}
		}
		return c.body, nil

	case isFormURLEncoded(contentType):
		if err := c.R.ParseForm(); err != nil {
			c.bodyErr = BadRequest("invalid_form", "failed to parse the form")
			return nil, c.bodyErr
		}
		for key, values := range c.R.PostForm {
			switch len(values) {
			case 0:
			case 1:
				c.body[key] = values[0]
			default:
				items := make([]any, len(values))
				for i, value := range values {
					items[i] = value
				}
				c.body[key] = items
			}
		}
		return c.body, nil

	case c.R.Body == nil:
		return c.body, nil
	}

	raw, err := io.ReadAll(io.LimitReader(c.R.Body, MaxJSONBytes+1))
	if err != nil {
		c.bodyErr = BadRequest("invalid_body", "failed to read the request body")
		return nil, c.bodyErr
	}
	if int64(len(raw)) > MaxJSONBytes {
		c.bodyErr = NewError(http.StatusRequestEntityTooLarge, "entity.too.large", "request body is too large")
		return nil, c.bodyErr
	}
	if len(raw) == 0 {
		return c.body, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		c.bodyErr = BadRequest("invalid_json", "request body is not valid JSON")
		return nil, c.bodyErr
	}
	if decoded != nil {
		c.body = decoded
	}
	return c.body, nil
}

// Form parses a multipart request into text fields and files.
func (c *Ctx) Form() (*FormData, error) {
	if c.formDone {
		return c.formData, c.formErr
	}
	c.formDone = true
	c.formData, c.formErr = ParseMultipart(c.R)
	return c.formData, c.formErr
}

// Files returns the uploaded files, or nil for non-multipart requests.
func (c *Ctx) Files() []File {
	if !isMultipart(c.R.Header.Get("Content-Type")) {
		return nil
	}
	form, err := c.Form()
	if err != nil {
		return nil
	}
	return form.Files
}

// String reads a string field, coercing numbers and booleans.
func String(body map[string]any, key string) string {
	value, ok := body[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return formatNumber(typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case []any:
		if len(typed) > 0 {
			return String(map[string]any{"v": typed[0]}, "v")
		}
	}
	return ""
}

// HasKey reports whether the field is present.
func HasKey(body map[string]any, key string) bool {
	_, ok := body[key]
	return ok
}

// Number reads a numeric field, returning fallback when unparseable.
func Number(body map[string]any, key string, fallback float64) float64 {
	value, ok := body[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		var parsed float64
		if _, err := fmt.Sscanf(typed, "%g", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

// Bool reads a boolean field, accepting "true" and "1".
func Bool(body map[string]any, key string) bool {
	value, ok := body[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		// JavaScript truthiness: any non-empty string is true, including "false".
		// Only the conventional spellings of false are treated as false here, which
		// is what a form submission means in practice.
		trimmed := strings.ToLower(strings.TrimSpace(typed))
		return trimmed != "" && trimmed != "false" && trimmed != "0"
	case float64:
		return typed != 0
	}
	return false
}

// StringSlice reads a string array field.
func StringSlice(body map[string]any, key string) []string {
	value, ok := body[key]
	if !ok || value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// Object reads an object field, returning nil when it is not an object.
func Object(body map[string]any, key string) map[string]any {
	value, ok := body[key]
	if !ok || value == nil {
		return nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return obj
}

func formatNumber(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return fmt.Sprintf("%g", value)
}
