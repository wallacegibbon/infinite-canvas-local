// Package config turns environment variables into an immutable configuration.
//
// Every default here matches the previous Node implementation: the canvas app,
// the start script and the docs all depend on these ports and paths, so changing
// a default is effectively changing the public contract.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Version is what /health and the startup log report.
const Version = "0.1.0"

// Config holds every runtime parameter of the gateway. It is treated as
// immutable after construction, so it may be shared across goroutines.
type Config struct {
	RootDir string
	Port    int
	Host    string

	ComfyURL            string
	ComfyPython         string
	ComfyModelsShared   bool // COMFY_MODELS_SHARED=1; installs are refused without it (internal/resource/install_policy.go)
	PromptThinkingParam string
	PromptMaxTokens     int
	ComfyFrontendDir    string

	ScanOnStart bool

	WorkflowDir string
	DataDir     string
	OutputDir   string

	PublicBaseURL string
	CorsOrigins   []string

	RequestTimeoutMs   int
	VideoTimeoutMs     int
	PollIntervalMs     int
	ComfyRetryAttempts int
	ComfyRetryDelayMs  int

	TaskRetentionDays int
	MaxStoredTasks    int
	CleanupIntervalMs int
	SessionStaleSec   int

	MaxUploadBytes      int64
	MaxWorkflowBytes    int64
	MaxImageCount       int
	ResourceConcurrency int

	GatewayToken string
}

// Option overrides a single field, mirroring the old loadConfig(overrides).
type Option func(*Config)

// WithPort overrides the listen port.
func WithPort(port int) Option { return func(c *Config) { c.Port = port } }

// WithHost overrides the listen address.
func WithHost(host string) Option { return func(c *Config) { c.Host = host } }

// WithComfyURL overrides the ComfyUI base URL.
func WithComfyURL(url string) Option { return func(c *Config) { c.ComfyURL = url } }

// WithWorkflowDir overrides the gateway workflow directory.
func WithWorkflowDir(dir string) Option { return func(c *Config) { c.WorkflowDir = dir } }

// WithDataDir overrides the data directory.
func WithDataDir(dir string) Option { return func(c *Config) { c.DataDir = dir } }

// WithOutputDir overrides the output directory.
func WithOutputDir(dir string) Option { return func(c *Config) { c.OutputDir = dir } }

// WithComfyPython overrides the ComfyUI interpreter path hint.
func WithComfyPython(path string) Option { return func(c *Config) { c.ComfyPython = path } }

// WithComfyFrontendDir overrides the ComfyUI frontend directory.
func WithComfyFrontendDir(dir string) Option { return func(c *Config) { c.ComfyFrontendDir = dir } }

// WithoutScanOnStart disables the startup scan (tests).
func WithoutScanOnStart() Option {
	return func(c *Config) { c.ScanOnStart = false }
}

// WithRequestTimeout overrides the image generation timeout.
func WithRequestTimeout(ms int) Option { return func(c *Config) { c.RequestTimeoutMs = ms } }

// WithPollInterval overrides the history poll interval.
func WithPollInterval(ms int) Option { return func(c *Config) { c.PollIntervalMs = ms } }

// WithPublicBaseURL overrides the externally visible base URL.
func WithPublicBaseURL(url string) Option { return func(c *Config) { c.PublicBaseURL = url } }

// Load reads the environment, validates it, and returns the configuration.
func Load(opts ...Option) (*Config, error) {
	root := resolveRootDir()

	cfg := &Config{
		RootDir:             root,
		Port:                intValue(os.Getenv("PORT"), 8190),
		Host:                stringOr(os.Getenv("HOST"), "127.0.0.1"),
		ComfyURL:            stringOr(os.Getenv("COMFY_URL"), "http://127.0.0.1:8188"),
		ComfyPython:         strings.TrimSpace(os.Getenv("COMFY_PYTHON")),
		ComfyModelsShared:   boolValue(os.Getenv("COMFY_MODELS_SHARED")),
		PromptThinkingParam: stringOr(os.Getenv("PROMPT_THINKING_PARAM"), "none"),
		PromptMaxTokens:     intValue(os.Getenv("PROMPT_MAX_TOKENS"), 8192),
		ComfyFrontendDir:    strings.TrimSpace(os.Getenv("COMFY_FRONTEND_DIR")),
		ScanOnStart:         os.Getenv("SCAN_ON_START") != "0",
		CorsOrigins: listValue(os.Getenv("CORS_ORIGINS"),
			"http://localhost:3000,http://127.0.0.1:3000,http://localhost:8188,http://127.0.0.1:8188"),
		RequestTimeoutMs:    intValue(os.Getenv("REQUEST_TIMEOUT_MS"), 10*60*1000),
		VideoTimeoutMs:      intOrZero(os.Getenv("VIDEO_TIMEOUT_MS")),
		PollIntervalMs:      intValue(os.Getenv("POLL_INTERVAL_MS"), 750),
		ComfyRetryAttempts:  intValue(os.Getenv("COMFY_RETRY_ATTEMPTS"), 3),
		ComfyRetryDelayMs:   intValue(os.Getenv("COMFY_RETRY_DELAY_MS"), 500),
		TaskRetentionDays:   countValue(os.Getenv("TASK_RETENTION_DAYS")),
		MaxStoredTasks:      countValue(os.Getenv("MAX_STORED_TASKS")),
		CleanupIntervalMs:   intValue(os.Getenv("CLEANUP_INTERVAL_MS"), 60*60*1000),
		SessionStaleSec:     intValue(os.Getenv("SESSION_STALE_SEC"), 120),
		MaxUploadBytes:      int64(intValue(os.Getenv("MAX_UPLOAD_BYTES"), 50*1024*1024)),
		MaxWorkflowBytes:    int64(intValue(os.Getenv("MAX_WORKFLOW_BYTES"), 5*1024*1024)),
		MaxImageCount:       intValue(os.Getenv("MAX_IMAGE_COUNT"), 15),
		ResourceConcurrency: intValue(os.Getenv("RESOURCE_CONCURRENCY"), 2),
		GatewayToken:        strings.TrimSpace(os.Getenv("GATEWAY_TOKEN")),
	}

	cfg.WorkflowDir = absOrJoin(root, os.Getenv("WORKFLOW_DIR"), "workflows")
	cfg.DataDir = absOrJoin(root, os.Getenv("DATA_DIR"), "data")
	cfg.OutputDir = absOrJoin(root, os.Getenv("OUTPUT_DIR"), filepath.Join("data", "outputs"))

	for _, opt := range opts {
		opt(cfg)
	}

	// An option may have replaced a directory, so resolve them all at the end.
	cfg.WorkflowDir = absPath(cfg.WorkflowDir)
	cfg.DataDir = absPath(cfg.DataDir)
	cfg.OutputDir = absPath(cfg.OutputDir)
	cfg.ComfyFrontendDir = absPath(cfg.ComfyFrontendDir)
	cfg.ComfyURL = strings.TrimRight(cfg.ComfyURL, "/")

	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
	}
	cfg.PublicBaseURL = strings.TrimRight(cfg.PublicBaseURL, "/")

	// HOST is bound exactly as given: 192.168.3.232 is as much "local" as 127.0.0.1 for
	// someone who wants their phone on the same network, and a gateway that refuses to start
	// teaches nothing. main.go warns when the bind is reachable from the network and /api/*
	// has no token, which is the part that actually matters.
	if contains(cfg.CorsOrigins, "*") {
		return nil, fmt.Errorf("CORS_ORIGINS must not be a wildcard; list the canvas origins explicitly (a canvas served from the same host as the gateway is allowed automatically)")
	}

	for _, dir := range []string{cfg.WorkflowDir, cfg.DataDir, cfg.OutputDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cannot create directory %s: %w", dir, err)
		}
	}
	return cfg, nil
}

// resolveRootDir locates the asset root: the directory holding workflows/,
// resources/ and prompts/.
//
// The old implementation used __dirname + "..", which was safe because the
// source never moved. Once this ships as a single binary that assumption dies,
// so probe GATEWAY_ROOT first, then the executable's directory, then the working
// directory, accepting the first one that actually looks like the asset root.
func resolveRootDir() string {
	if explicit := strings.TrimSpace(os.Getenv("GATEWAY_ROOT")); explicit != "" {
		return absPath(explicit)
	}
	// The working directory comes first: launch scripts cd into the gateway root,
	// and a binary sitting in bin/ must not treat bin/ as the asset root.
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	for _, dir := range candidates {
		if hasMarker(dir) {
			return dir
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return "."
}

// hasMarker reports whether dir really is the gateway asset root.
//
// A bare "workflows" directory is not enough: the gateway creates its own workflow
// directory on startup, so a wrong guess would leave behind the very marker that
// keeps justifying it. Requiring a known file makes the check self-correcting.
func hasMarker(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "workflows", "registry.json")); err == nil && info.Mode().IsRegular() {
		return true
	}
	if info, err := os.Stat(filepath.Join(dir, "resources", "index.json")); err == nil && info.Mode().IsRegular() {
		return true
	}
	return false
}

// IsLoopbackHost reports whether the host name is a loopback address.
func IsLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

func absOrJoin(root, value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return filepath.Join(root, fallback)
	}
	return value
}

func absPath(value string) string {
	if value == "" {
		return ""
	}
	if abs, err := filepath.Abs(value); err == nil {
		return abs
	}
	return value
}

func stringOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// intValue mirrors the old num(): a positive integer, otherwise the fallback.
func intValue(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// intOrZero allows zero, e.g. VIDEO_TIMEOUT_MS=0 means "no timeout".
func intOrZero(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// countValue mirrors the old count(): zero is valid.
func countValue(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

// boolValue reads an opt-in flag. Anything that is not an accepted spelling is false, which
// is the safe direction here: COMFY_MODELS_SHARED gates a refusal, and a typo must not
// silently enable installs on a host that cannot reach ComfyUI's model folders.
func boolValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func listValue(value, fallback string) []string {
	raw := value
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	var out []string
	for _, item := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
