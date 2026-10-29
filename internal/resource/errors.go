package resource

import (
	"net/http"

	"comfyui-gateway/internal/httpx"
)

// httpxBadRequest keeps the call sites readable while still using the shared error
// type, so the HTTP layer renders resource failures like any other error.
func httpxBadRequest(code, message string) error {
	return httpx.NewError(http.StatusBadRequest, code, message)
}
