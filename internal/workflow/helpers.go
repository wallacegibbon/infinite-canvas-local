package workflow

import (
	"errors"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"comfyui-gateway/internal/httpx"
)

// containsString reports whether items contains target.
func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// asHTTPError unwraps err into an *httpx.Error.
func asHTTPError(err error, target **httpx.Error) bool {
	return errors.As(err, target)
}

// urlBaseName extracts the last path segment of a URL.
func urlBaseName(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "workflow"
	}
	base := path.Base(parsed.Path)
	if base == "" || base == "/" || base == "." {
		return "workflow"
	}
	if decoded, err := url.PathUnescape(base); err == nil {
		return decoded
	}
	return base
}

// resolveOrJoin resolves a possibly relative path against a base directory.
func resolveOrJoin(base, value string) string {
	if filepath.IsAbs(value) {
		if abs, err := filepath.Abs(value); err == nil {
			return abs
		}
		return value
	}
	return filepath.Join(base, value)
}

// NormalizeModelPath exposes slash normalization for callers comparing model names.
func NormalizeModelPath(value string) string {
	return strings.ReplaceAll(value, `\`, "/")
}
