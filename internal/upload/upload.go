// Package upload stores the media a run references.
//
// An upload is written once and referenced by id, which is the input half of the run protocol
// (docs/protocol.md): the bytes live under <data>/uploads, the index (uploads.json) remembers the
// ComfyUI file they were copied to, so a run does not upload the same clip twice. The bytes stay on
// disk because a run may outlive the ComfyUI input directory (a restart, a cleaned temp folder) and
// because a failed push to ComfyUI has to be retryable.
package upload

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Media kinds. A run's media roles are typed, so an image cannot be passed where a video belongs.
const (
	KindImage = "image"
	KindVideo = "video"
	KindAudio = "audio"
)

// extensionsByKind is the accepted container list per kind. It is a filter, not a converter: the
// gateway never transcodes, so a file whose type it cannot place is refused at upload time instead
// of failing inside ComfyUI later.
var extensionsByKind = map[string][]string{
	KindImage: {".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"},
	KindVideo: {".mp4", ".webm", ".mov", ".mkv", ".avi"},
	KindAudio: {".mp3", ".wav", ".flac", ".ogg", ".opus", ".m4a", ".aac"},
}

// File is one uploaded media file.
type File struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Filename  string `json:"filename"`
	MimeType  string `json:"mimeType"`
	Bytes     int64  `json:"bytes"`
	Path      string `json:"path"`
	CreatedAt string `json:"createdAt"`

	// ComfyName/ComfySubfolder is where the file was copied to in ComfyUI. Empty means the push
	// has not happened or failed; a run retries it from Path.
	ComfyName      string `json:"comfyName,omitempty"`
	ComfySubfolder string `json:"comfySubfolder,omitempty"`
}

// Options configures a Store.
type Options struct {
	DataDir       string
	RetentionDays int
	MaxBytes      int64
	Logger        *logx.Logger
}

// Store owns the upload directory and its index.
type Store struct {
	dir           string
	indexPath     string
	retentionDays int
	maxBytes      int64
	logger        *logx.Logger

	mu    sync.RWMutex
	files map[string]*File
}

// New builds a store and ensures its directory exists.
func New(opts Options) (*Store, error) {
	dir := filepath.Join(opts.DataDir, "uploads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		dir:           dir,
		indexPath:     filepath.Join(opts.DataDir, "uploads.json"),
		retentionDays: opts.RetentionDays,
		maxBytes:      opts.MaxBytes,
		logger:        opts.Logger,
		files:         map[string]*File{},
	}, nil
}

// Load restores the index and drops entries older than the retention window together with their
// bytes, so an abandoned upload cannot sit on the disk forever.
func (s *Store) Load() {
	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		return
	}
	var document struct {
		Uploads []*File `json:"uploads"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		s.warn("failed to read the upload index: %s", err)
		return
	}
	now := time.Now()
	s.mu.Lock()
	for _, file := range document.Uploads {
		if file == nil || file.ID == "" {
			continue
		}
		if s.retentionDays > 0 && expired(file.CreatedAt, now, s.retentionDays) {
			_ = os.Remove(file.Path)
			continue
		}
		s.files[file.ID] = file
	}
	s.mu.Unlock()
	s.persist()
}

// Save writes one upload and returns its record.
func (s *Store) Save(kind, filename, mimeType string, data []byte) (*File, error) {
	if s.maxBytes > 0 && int64(len(data)) > s.maxBytes {
		return nil, ErrTooLarge
	}
	id := util.RandomID("upl_")
	path := filepath.Join(s.dir, id+safeExtension(filename))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return nil, err
	}
	file := &File{
		ID:        id,
		Kind:      kind,
		Filename:  filename,
		MimeType:  mimeType,
		Bytes:     int64(len(data)),
		Path:      path,
		CreatedAt: util.NowIso(),
	}
	s.mu.Lock()
	s.files[id] = file
	s.mu.Unlock()
	s.persist()
	return file, nil
}

// Get returns one upload, or nil.
func (s *Store) Get(id string) *File {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.files[id]
}

// SetComfyReference records where the file landed in ComfyUI, so the next run reuses it.
func (s *Store) SetComfyReference(id, name, subfolder string) {
	s.mu.Lock()
	file, ok := s.files[id]
	if ok {
		file.ComfyName = name
		file.ComfySubfolder = subfolder
	}
	s.mu.Unlock()
	if ok {
		s.persist()
	}
}

// Read returns an upload's bytes for a lazy push to ComfyUI.
func (f *File) Read() ([]byte, error) { return os.ReadFile(f.Path) }

func (s *Store) persist() {
	s.mu.RLock()
	payload := struct {
		Version int     `json:"version"`
		Uploads []*File `json:"uploads"`
	}{Version: 1, Uploads: make([]*File, 0, len(s.files))}
	for _, file := range s.files {
		payload.Uploads = append(payload.Uploads, file)
	}
	s.mu.RUnlock()
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.warn("failed to serialise the upload index: %s", err)
		return
	}
	if err := util.WriteFileAtomic(s.indexPath, append(data, '\n')); err != nil {
		s.warn("failed to save the upload index: %s", err)
	}
}

func (s *Store) warn(format string, args ...any) {
	if s.logger != nil {
		s.logger.Warn(format, args...)
	}
}

func expired(createdAt string, now time.Time, days int) bool {
	parsed := util.ParseIso(createdAt)
	if parsed.IsZero() {
		return false
	}
	return now.Sub(parsed) > time.Duration(days)*24*time.Hour
}

// KindOf places a file by its extension first, then its MIME type. Empty means "not media the
// gateway can run", which is refused at the upload boundary.
func KindOf(mimeType, filename string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	for _, kind := range []string{KindImage, KindVideo, KindAudio} {
		for _, candidate := range extensionsByKind[kind] {
			if extension == candidate {
				return kind
			}
		}
	}
	mime := strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(mime, "image/"):
		return KindImage
	case strings.HasPrefix(mime, "video/"):
		return KindVideo
	case strings.HasPrefix(mime, "audio/"):
		return KindAudio
	}
	return ""
}

// AcceptedExtensions returns the extensions of a kind, for error messages.
func AcceptedExtensions(kind string) []string {
	return append([]string{}, extensionsByKind[kind]...)
}

func safeExtension(filename string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	if extension == "" || strings.ContainsAny(extension, "/\\ ") {
		return ".bin"
	}
	return extension
}

// ErrTooLarge is returned by Save when the payload exceeds the configured limit.
var ErrTooLarge = storeError("upload exceeds the configured size limit")

type storeError string

func (e storeError) Error() string { return string(e) }
