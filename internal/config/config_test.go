package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateEnv clears every variable the loader reads so tests never inherit the
// developer's shell.
func isolateEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"GATEWAY_PORT", "GATEWAY_HOST", "COMFY_URL", "COMFY_FRONTEND_DIR",
		"INSTALL_INTO_COMFY",
		"SCAN_ON_START",
		"WORKFLOW_DIR", "DATA_DIR", "OUTPUT_DIR", "PUBLIC_BASE_URL", "CORS_ORIGINS",
		"REQUEST_TIMEOUT_MS", "VIDEO_TIMEOUT_MS", "POLL_INTERVAL_MS",
		"COMFY_RETRY_ATTEMPTS", "COMFY_RETRY_DELAY_MS", "TASK_RETENTION_DAYS",
		"MAX_STORED_TASKS", "CLEANUP_INTERVAL_MS",
		"MAX_UPLOAD_BYTES", "MAX_WORKFLOW_BYTES", "MAX_IMAGE_COUNT",
		"RESOURCE_CONCURRENCY", "GATEWAY_TOKEN", "GATEWAY_ROOT",
	} {
		t.Setenv(name, "")
	}
}

func TestDefaults(t *testing.T) {
	isolateEnv(t)
	root := t.TempDir()
	t.Setenv("GATEWAY_ROOT", root)
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OUTPUT_DIR", filepath.Join(root, "data", "outputs"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.GatewayPort != 8190 || cfg.GatewayHost != "127.0.0.1" {
		t.Errorf("listen = %s:%d", cfg.GatewayHost, cfg.GatewayPort)
	}
	if cfg.ComfyURL != "http://127.0.0.1:8188" {
		t.Errorf("ComfyURL = %q", cfg.ComfyURL)
	}
	if cfg.PublicBaseURL != "http://127.0.0.1:8190" {
		t.Errorf("PublicBaseURL = %q", cfg.PublicBaseURL)
	}
	if cfg.RequestTimeoutMs != 10*60*1000 {
		t.Errorf("RequestTimeoutMs = %d", cfg.RequestTimeoutMs)
	}
	// VIDEO_TIMEOUT_MS defaults to 0, meaning "no timeout" for long renders.
	if cfg.VideoTimeoutMs != 0 {
		t.Errorf("VideoTimeoutMs = %d, want 0", cfg.VideoTimeoutMs)
	}
	for _, dir := range []string{cfg.WorkflowDir, cfg.DataDir, cfg.OutputDir} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("directory %q was not created", dir)
		}
	}
}

// GATEWAY_HOST is bound as given. A LAN address is how the workbench is reached from a phone, so there
// is no second switch to remember: the only thing the gateway still refuses is a wildcard CORS
// list, because that hands /v1/* to any page in the browser.
func TestNonLoopbackHostIsAccepted(t *testing.T) {
	isolateEnv(t)
	root := t.TempDir()
	t.Setenv("GATEWAY_ROOT", root)
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OUTPUT_DIR", filepath.Join(root, "out"))
	t.Setenv("GATEWAY_HOST", "192.168.3.232")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with a LAN GATEWAY_HOST: %v", err)
	}
	if cfg.GatewayHost != "192.168.3.232" {
		t.Errorf("gateway host = %q", cfg.GatewayHost)
	}
	if cfg.PublicBaseURL != "http://192.168.3.232:8190" {
		t.Errorf("public base url = %q, want it derived from GATEWAY_HOST", cfg.PublicBaseURL)
	}
}

func TestCorsWildcardIsRejected(t *testing.T) {
	isolateEnv(t)
	root := t.TempDir()
	t.Setenv("GATEWAY_ROOT", root)
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OUTPUT_DIR", filepath.Join(root, "out"))
	t.Setenv("CORS_ORIGINS", "*")

	if _, err := Load(); err == nil {
		t.Fatal("a wildcard CORS origin must be refused")
	}
}

func TestInvalidNumbersFallBack(t *testing.T) {
	isolateEnv(t)
	root := t.TempDir()
	t.Setenv("GATEWAY_ROOT", root)
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OUTPUT_DIR", filepath.Join(root, "out"))
	t.Setenv("GATEWAY_PORT", "not-a-number")
	t.Setenv("POLL_INTERVAL_MS", "-5")
	t.Setenv("MAX_IMAGE_COUNT", "abc")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GatewayPort != 8190 {
		t.Errorf("GatewayPort = %d, want the default", cfg.GatewayPort)
	}
	if cfg.PollIntervalMs != 750 {
		t.Errorf("PollIntervalMs = %d, want the default", cfg.PollIntervalMs)
	}
	if cfg.MaxImageCount != 15 {
		t.Errorf("MaxImageCount = %d, want the default", cfg.MaxImageCount)
	}
}

// Installs are refused unless this is set, so a typo must read as "not set" rather than
// silently enabling them.
func TestInstallIntoComfyIsOptIn(t *testing.T) {
	isolateEnv(t)
	root := t.TempDir()
	t.Setenv("GATEWAY_ROOT", root)
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OUTPUT_DIR", filepath.Join(root, "out"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.InstallIntoComfy {
		t.Error("InstallIntoComfy is on by default; installs must be refused until stated")
	}

	for _, value := range []string{"1", "true", "TRUE", " on "} {
		t.Setenv("INSTALL_INTO_COMFY", value)
		cfg, err = Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !cfg.InstallIntoComfy {
			t.Errorf("INSTALL_INTO_COMFY=%q was not read as true", value)
		}
	}

	for _, value := range []string{"0", "false", "nope", "", "yes please"} {
		t.Setenv("INSTALL_INTO_COMFY", value)
		cfg, err = Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.InstallIntoComfy {
			t.Errorf("INSTALL_INTO_COMFY=%q was read as true", value)
		}
	}
}

func TestCorsListParsing(t *testing.T) {
	isolateEnv(t)
	root := t.TempDir()
	t.Setenv("GATEWAY_ROOT", root)
	t.Setenv("DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("OUTPUT_DIR", filepath.Join(root, "out"))
	t.Setenv("CORS_ORIGINS", " http://a , , http://b ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CorsOrigins) != 2 || cfg.CorsOrigins[0] != "http://a" || cfg.CorsOrigins[1] != "http://b" {
		t.Errorf("CorsOrigins = %v", cfg.CorsOrigins)
	}
}

func TestResolveRootDirPrefersMarker(t *testing.T) {
	// A directory is only accepted as the asset root when it contains a real marker
	// file, so a half-created tree cannot hijack the choice.
	fakeRoot := t.TempDir()
	if hasMarker(fakeRoot) {
		t.Fatal("an empty directory must not look like the asset root")
	}
	if err := os.MkdirAll(filepath.Join(fakeRoot, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasMarker(fakeRoot) {
		t.Fatal("a bare workflows/ directory is not enough of a marker")
	}
	if err := os.WriteFile(filepath.Join(fakeRoot, "workflows", "registry.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasMarker(fakeRoot) {
		t.Fatal("workflows/registry.json must be recognized as the marker")
	}

	resourcesRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(resourcesRoot, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resourcesRoot, "resources", "index.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasMarker(resourcesRoot) {
		t.Fatal("resources/index.json must be recognized as the marker")
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "localhost", "::1", "[::1]", "LOCALHOST"} {
		if !IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = false", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "192.168.1.5", "example.com", ""} {
		if IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = true", host)
		}
	}
}
