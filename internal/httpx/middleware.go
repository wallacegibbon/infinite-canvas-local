package httpx

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Middleware is a standard HTTP middleware.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first entry ends up outermost.
func Chain(handler http.Handler, middleware ...Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		handler = middleware[i](handler)
	}
	return handler
}

var requestIDStripper = regexp.MustCompile(`[^\w.:-]`)

// WithRequestID reuses or generates a request ID, exposes it as a response
// header, and logs one access line per request.
func WithRequestID(logger *logx.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := requestIDStripper.ReplaceAllString(r.Header.Get("X-Request-Id"), "")
			if len(id) > 100 {
				id = id[:100]
			}
			if id == "" {
				id = util.RandomID("req_")
			}
			w.Header().Set("X-Request-Id", id)

			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			started := time.Now()
			next.ServeHTTP(recorder, r.WithContext(withRequestID(r.Context(), id)))
			logger.Info("%s %s %d %dms %s", r.Method, r.URL.RequestURI(), recorder.status, time.Since(started).Milliseconds(), id)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.wroteHeader {
		return
	}
	s.wroteHeader = true
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(data []byte) (int, error) {
	if !s.wroteHeader {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(data)
}

// CORS echoes the configured cross-origin headers and answers preflight with 204.
func CORS(cfg *config.Config) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			wildcard := containsString(cfg.CorsOrigins, "*")
			allowed := wildcard || isAllowedOrigin(cfg, r, origin)
			if allowed {
				if wildcard {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				}
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With, X-Request-Id")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-Id")
			w.Header().Set("Access-Control-Max-Age", "86400")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Guard enforces two protections: a token on /api/* when GATEWAY_TOKEN is set, and a
// block on cross-site browser writes.
func Guard(cfg *config.Config) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.GatewayToken != "" && strings.HasPrefix(r.URL.Path, "/api/") {
				if r.Header.Get("X-Gateway-Token") != cfg.GatewayToken {
					writeOkFalse(w, http.StatusUnauthorized, "missing a valid gateway token")
					return
				}
			}
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				origin := r.Header.Get("Origin")
				site := r.Header.Get("Sec-Fetch-Site")
				if site == "cross-site" || !isAllowedOrigin(cfg, r, origin) {
					writeOkFalse(w, http.StatusForbidden, "cross-site request rejected")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Recover keeps a single panicking request from taking down the process.
func Recover(logger *logx.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic %s %s: %v", r.Method, r.URL.Path, recovered)
					WriteError(w, NewError(http.StatusInternalServerError, "internal_error", "internal server error"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// writeOkFalse emits {ok:false, error:{message}}, the shape middleware uses.
func writeOkFalse(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]any{
		"ok":    false,
		"error": map[string]any{"message": message},
	})
}

func isLoopbackOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return config.IsLoopbackHost(parsed.Hostname())
}

// sameHostOrigin reports whether the canvas and the gateway were reached under the same host
// name, which is the normal single-machine layout (canvas on :3000, gateway on :8190, one
// address). That is what makes a LAN address work without maintaining a CORS_ORIGINS list per
// ip:port pair, and it keeps the list meaningful: a page served from another host still has to
// be listed explicitly.
func sameHostOrigin(r *http.Request, origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" {
		return false
	}
	host := r.Host
	if name, _, splitErr := net.SplitHostPort(r.Host); splitErr == nil {
		host = name
	}
	return strings.EqualFold(strings.Trim(host, "[]"), parsed.Hostname())
}

func isAllowedOrigin(cfg *config.Config, r *http.Request, origin string) bool {
	if origin == "" {
		return true
	}
	if isLoopbackOrigin(origin) || sameHostOrigin(r, origin) {
		return true
	}
	return containsString(cfg.CorsOrigins, origin) ||
		containsString(cfg.CorsOrigins, "*") ||
		origin == cfg.PublicBaseURL
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// ctxKey identifies values this package puts into a request context.
type ctxKey int

const requestIDKey ctxKey = iota

func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDOf returns the request ID, or an empty string when unset.
func RequestIDOf(r *http.Request) string {
	if id, ok := r.Context().Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}
