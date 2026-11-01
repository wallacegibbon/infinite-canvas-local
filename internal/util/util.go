// Package util holds small helpers shared across modules.
//
// These are the counterparts of the old util.js. Their semantics — especially the
// null-vs-absent distinction and the exact timestamp format — must stay identical,
// otherwise the canvas receives different timestamps, file names or MIME types.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// isoLayout is the exact shape produced by JS Date#toISOString().
const isoLayout = "2006-01-02T15:04:05.000Z"

// NowIso returns the current time in the same format as new Date().toISOString().
func NowIso() string {
	return time.Now().UTC().Format(isoLayout)
}

// IsoFromTime formats a time the same way.
func IsoFromTime(t time.Time) string {
	return t.UTC().Format(isoLayout)
}

// ParseIso parses that format, tolerating a missing millisecond part.
// It returns the zero time when the input is not a timestamp.
func ParseIso(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{isoLayout, time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// RandomID returns prefix + UUIDv4, equivalent to the old randomId().
func RandomID(prefix string) string {
	return prefix + uuidV4()
}

func uuidV4() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand does not fail on Linux; fall back to a timestamp so that a
		// pathological kernel never takes the whole gateway down.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buf[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", encoded[0:8], encoded[8:12], encoded[12:16], encoded[16:20], encoded[20:32])
}

// UUIDShort returns 4 random bytes as hex, used as a file dedupe suffix.
func UUIDShort() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()%0xffffffff, 16)
	}
	return hex.EncodeToString(buf[:])
}

// Size is a parsed `size` request parameter.
type Size struct {
	Width  int
	Height int
	Raw    string
}

var sizePattern = regexp.MustCompile(`^(\d+)\s*[xX]\s*(\d+)$`)

// ParseSize parses "1024x768". It returns nil for anything else, matching the
// old null return.
func ParseSize(value string) *Size {
	match := sizePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return nil
	}
	width, errW := strconv.Atoi(match[1])
	height, errH := strconv.Atoi(match[2])
	if errW != nil || errH != nil || width <= 0 || height <= 0 {
		return nil
	}
	return &Size{Width: width, Height: height, Raw: fmt.Sprintf("%dx%d", width, height)}
}

// Clamp constrains value to [min,max].
func Clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

var slugStripper = regexp.MustCompile(`[^a-z0-9._-]+`)
var slugDashRun = regexp.MustCompile(`-+`)

// Slugify produces a URL- and filename-safe identifier, falling back when empty.
func Slugify(value, fallback string) string {
	slug := strings.ToLower(strings.TrimSpace(value))
	slug = slugStripper.ReplaceAllString(slug, "-")
	slug = slugDashRun.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return fallback
	}
	return slug
}

var unsafeFileName = regexp.MustCompile(`[^\w.-]+`)

// SafeFileName collapses any string into a safe single path segment.
func SafeFileName(value string) string {
	if strings.TrimSpace(value) == "" {
		value = "file"
	}
	base := filepath.Base(filepath.ToSlash(value))
	return unsafeFileName.ReplaceAllString(base, "_")
}

// UniqueFileName appends a short random suffix so ComfyUI never overwrites two
// uploads that happen to share a name.
func UniqueFileName(value, suffix string) string {
	if suffix == "" {
		suffix = UUIDShort()
	}
	safe := SafeFileName(value)
	if safe == "file" {
		safe = "image.png"
	}
	ext := filepath.Ext(safe)
	if ext == "" {
		ext = ".png"
	}
	stem := strings.TrimSuffix(filepath.Base(safe), ext)
	if len(stem) > 60 {
		stem = stem[:60]
	}
	if stem == "" {
		stem = "image"
	}
	return SafeFileName(fmt.Sprintf("%s-%s%s", stem, suffix, ext))
}

// MimeFromFilename infers a MIME type from the extension only.
func MimeFromFilename(filename string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".mp4":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".opus":
		return "audio/opus"
	case ".ogg":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	}
	return "application/octet-stream"
}

// ExtFromMime prefers the filename extension, then the MIME type, then fallback.
// The result carries no leading dot, matching the old implementation.
func ExtFromMime(mimeType, filename, fallback string) string {
	if ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), "."); ext != "" {
		return ext
	}
	switch strings.ToLower(mimeType) {
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	case "audio/wav":
		return "wav"
	case "audio/mpeg":
		return "mp3"
	}
	return fallback
}

// Truncate shortens long text with an ellipsis, for logs and error messages.
func Truncate(value string, max int) string {
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

// WriteFileAtomic writes to a temporary file and renames it, so readers never
// observe a half-written document.
func WriteFileAtomic(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", file, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SweepTempFiles removes the leftovers of an interrupted WriteFileAtomic: a temporary file whose name is
// `<something>.<nanoseconds>.tmp`, written next to its target and renamed over it. A process that is
// killed between the two leaves it behind, and nothing would ever read it again — they accumulate in
// DATA_DIR one per kill (five had piled up in this tree before anything looked).
//
// Only files directly in dir are touched, and only ones matching that shape: a directory of user data is
// not a place to run a broad `*.tmp` delete.
func SweepTempFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	temp := regexp.MustCompile(`\.\d{6,}\.tmp$`)
	for _, entry := range entries {
		if entry.IsDir() || !temp.MatchString(entry.Name()) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// Exists reports whether the path is an existing regular file.
func Exists(file string) bool {
	info, err := os.Stat(file)
	return err == nil && info.Mode().IsRegular()
}

// DirExists reports whether the path is an existing directory.
func DirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// NormalizeSlashes converts Windows separators to forward slashes, which is how
// model paths are compared throughout the gateway.
func NormalizeSlashes(value string) string {
	return strings.ReplaceAll(value, `\`, "/")
}

// BaseName returns the last segment after slash normalization.
func BaseName(value string) string {
	return filepath.Base(NormalizeSlashes(value))
}

// StringField reads a string from a decoded JSON object.
//
// These four accessors are the single implementation of "read a field out of
// arbitrary JSON"; httpx and the request params both route through them so the
// coercion rules cannot drift apart.
func StringField(object map[string]any, key string) string {
	if object == nil {
		return ""
	}
	value, ok := object[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	}
	return ""
}

// NumberField reads a number from a decoded JSON object.
func NumberField(object map[string]any, key string, fallback float64) float64 {
	if object == nil {
		return fallback
	}
	value, ok := object[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil {
			return parsed
		}
	}
	return fallback
}

// BoolField reads a boolean from a decoded JSON object.
func BoolField(object map[string]any, key string) bool {
	if object == nil {
		return false
	}
	switch typed := object[key].(type) {
	case bool:
		return typed
	case string:
		return typed == "true" || typed == "1"
	case float64:
		return typed != 0
	}
	return false
}

// Itoa formats an int64 for messages, avoiding strconv imports at call sites.
func Itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

// Stringify renders any JSON value as text for display.
func Stringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	}
	return fmt.Sprint(value)
}

// CommandLine renders an argv as a copy-pasteable shell line.
//
// External commands are logged before they run so a failure can be reproduced by
// pasting the exact line into a shell, instead of guessing which flags were used.
func CommandLine(name string, args ...string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(name))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps an argument in single quotes when it contains shell metacharacters.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\"'\\$`&|;<>()*?[]{}!~#") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ObjectField reads a nested object from a decoded JSON object.
func ObjectField(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	nested, _ := object[key].(map[string]any)
	return nested
}
