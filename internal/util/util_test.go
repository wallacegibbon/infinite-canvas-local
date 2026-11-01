package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSize(t *testing.T) {
	parsed := ParseSize(" 1024 x 768 ")
	if parsed == nil || parsed.Width != 1024 || parsed.Height != 768 {
		t.Fatalf("ParseSize = %+v", parsed)
	}
	if parsed.Raw != "1024x768" {
		t.Errorf("Raw = %q", parsed.Raw)
	}
	for _, input := range []string{"", "1024", "x", "0x10", "10x0", "axb", "1024x768x2"} {
		if got := ParseSize(input); got != nil {
			t.Errorf("ParseSize(%q) = %+v, want nil", input, got)
		}
	}
}

func TestClamp(t *testing.T) {
	if got := Clamp(5, 1, 15); got != 5 {
		t.Errorf("Clamp in range = %d", got)
	}
	if got := Clamp(0, 1, 15); got != 1 {
		t.Errorf("Clamp below = %d", got)
	}
	if got := Clamp(99, 1, 15); got != 15 {
		t.Errorf("Clamp above = %d", got)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Image Z-Image Turbo": "image-z-image-turbo",
		"  spaced  ":          "spaced",
		"a__b":                "a__b",
		"中文":                  "workflow",
		"":                    "workflow",
	}
	for input, want := range cases {
		if got := Slugify(input, "workflow"); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSafeFileName(t *testing.T) {
	if got := SafeFileName("../../etc/passwd"); got != "passwd" {
		t.Errorf("SafeFileName escaped traversal: %q", got)
	}
	if got := SafeFileName("a b/c?.png"); got != "c_.png" {
		t.Errorf("SafeFileName = %q", got)
	}
}

func TestUniqueFileName(t *testing.T) {
	got := UniqueFileName("My Photo.PNG", "abcd1234")
	if got != "My_Photo-abcd1234.PNG" {
		t.Errorf("UniqueFileName = %q", got)
	}
	// A name without an extension still gets one, because ComfyUI infers the type.
	if got := UniqueFileName("noext", "x"); filepath.Ext(got) != ".png" {
		t.Errorf("UniqueFileName(%q) = %q, want a .png extension", "noext", got)
	}
}

func TestExtFromMimePrefersFilename(t *testing.T) {
	if got := ExtFromMime("image/png", "weird.jpeg", "bin"); got != "jpeg" {
		t.Errorf("ExtFromMime = %q, want the filename extension", got)
	}
	if got := ExtFromMime("video/mp4", "", "bin"); got != "mp4" {
		t.Errorf("ExtFromMime = %q, want mp4", got)
	}
	if got := ExtFromMime("application/octet-stream", "", "bin"); got != "bin" {
		t.Errorf("ExtFromMime fallback = %q", got)
	}
}

func TestMimeFromFilename(t *testing.T) {
	cases := map[string]string{
		"a.mp4":  "video/mp4",
		"a.webm": "video/webm",
		"a.PNG":  "image/png",
		"a.jpg":  "image/jpeg",
		"a.wav":  "audio/wav",
		"a.xyz":  "application/octet-stream",
	}
	for input, want := range cases {
		if got := MimeFromFilename(input); got != want {
			t.Errorf("MimeFromFilename(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("hello", 10); got != "hello" {
		t.Errorf("Truncate = %q", got)
	}
	if got := Truncate("hello world", 5); got != "hello…" {
		t.Errorf("Truncate = %q", got)
	}
	// Must count runes, not bytes, or CJK text gets chopped mid-character.
	if got := Truncate("中文中文中文", 3); got != "中文中…" {
		t.Errorf("Truncate = %q", got)
	}
}

func TestNormalizeSlashes(t *testing.T) {
	if got := NormalizeSlashes(`a\b\c`); got != "a/b/c" {
		t.Errorf("NormalizeSlashes = %q", got)
	}
	if got := BaseName(`x\y\model.safetensors`); got != "model.safetensors" {
		t.Errorf("BaseName = %q", got)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.json")
	if err := WriteFileAtomic(path, []byte("{}")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{}" {
		t.Fatalf("read back = %q, err %v", data, err)
	}
	// No temporary files may be left behind.
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover files: %v", entries)
	}
}

func TestIsoRoundTrip(t *testing.T) {
	now := NowIso()
	parsed := ParseIso(now)
	if parsed.IsZero() {
		t.Fatalf("ParseIso(%q) returned zero", now)
	}
	if got := IsoFromTime(parsed); got != now {
		t.Errorf("round trip = %q, want %q", got, now)
	}
	if !ParseIso("not a date").IsZero() {
		t.Error("ParseIso should return zero for garbage")
	}
}

func TestMapAccessors(t *testing.T) {
	object := map[string]any{
		"text":   "value",
		"number": 42.0,
		"flag":   true,
		"nested": map[string]any{"k": "v"},
	}
	if got := StringField(object, "text"); got != "value" {
		t.Errorf("StringField = %q", got)
	}
	if got := StringField(object, "number"); got != "42" {
		t.Errorf("StringField(number) = %q", got)
	}
	if got := NumberField(object, "number", 0); got != 42 {
		t.Errorf("NumberField = %v", got)
	}
	if got := NumberField(object, "missing", 7); got != 7 {
		t.Errorf("NumberField fallback = %v", got)
	}
	if !BoolField(object, "flag") {
		t.Error("BoolField = false")
	}
	if got := ObjectField(object, "nested"); got["k"] != "v" {
		t.Errorf("ObjectField = %v", got)
	}
	if got := ObjectField(object, "text"); got != nil {
		t.Errorf("ObjectField on a non-object = %v", got)
	}
}

func TestRandomIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := RandomID("req_")
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		if len(id) != len("req_")+36 {
			t.Fatalf("id %q does not look like a UUIDv4", id)
		}
	}
}

// A killed process leaves `<name>.<nanoseconds>.tmp` next to a target nothing reads again. The sweep takes
// exactly those, and nothing else in the directory.
func TestSweepTempFilesTakesOnlyAtomicLeftovers(t *testing.T) {
	dir := t.TempDir()
	keep := []string{"tasks.json", "notes.tmp", "output.png", "status.json.bak"}
	drop := []string{"tasks.json.1793441001953777902.tmp", "setup-status.json.1793466186600214164.tmp"}
	for _, name := range append(append([]string{}, keep...), drop...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.123456.tmp"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	SweepTempFiles(dir)

	for _, name := range drop {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the sweep", name)
		}
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s should have been left alone: %v", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "sub.123456.tmp")); err != nil || !info.IsDir() {
		t.Error("a directory that happens to match the pattern must not be removed")
	}
}
