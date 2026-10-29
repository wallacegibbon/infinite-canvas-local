package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Skins records which canvas skins the user has unlocked.
type Skins struct {
	mu       sync.Mutex
	file     string
	logger   *logx.Logger
	unlocked map[string]SkinEntry
}

// SkinEntry is one unlocked skin and when it was unlocked.
type SkinEntry struct {
	SkinID     string `json:"skinId"`
	UnlockedAt string `json:"unlockedAt"`
}

var skinIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

// NewSkins builds the skin store.
func NewSkins(dataDir string, logger *logx.Logger) *Skins {
	return &Skins{
		file:     filepath.Join(dataDir, "skin-unlocks.json"),
		logger:   logger,
		unlocked: map[string]SkinEntry{},
	}
}

// Load reads the persisted unlocks.
func (s *Skins) Load() {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if !os.IsNotExist(err) {
			s.logger.Warn("failed to read skin unlocks: %s", err)
		}
		return
	}
	var document struct {
		Unlocked []json.RawMessage `json:"unlocked"`
	}
	if err := json.Unmarshal(stripBOM(data), &document); err != nil {
		s.logger.Warn("failed to read skin unlocks: %s", err)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, raw := range document.Unlocked {
		var entry SkinEntry
		var id string
		if err := json.Unmarshal(raw, &id); err == nil {
			entry = SkinEntry{SkinID: id, UnlockedAt: util.NowIso()}
		} else if err := json.Unmarshal(raw, &entry); err != nil {
			continue
		}
		entry.SkinID = strings.ToLower(strings.TrimSpace(entry.SkinID))
		if !skinIDPattern.MatchString(entry.SkinID) {
			continue
		}
		if entry.UnlockedAt == "" {
			entry.UnlockedAt = util.NowIso()
		}
		s.unlocked[entry.SkinID] = entry
	}
}

// List returns the unlocked skin ids.
func (s *Skins) List() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.unlocked))
	for id := range s.unlocked {
		out = append(out, id)
	}
	return out
}

// Entries returns the full unlock records.
func (s *Skins) Entries() []SkinEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entriesLocked()
}

// Unlock marks a skin unlocked, returning the existing record when already present.
func (s *Skins) Unlock(value string) (SkinEntry, error) {
	skinID := strings.ToLower(strings.TrimSpace(value))
	if !skinIDPattern.MatchString(skinID) {
		return SkinEntry{}, &InvalidIDError{Message: "invalid skin id", ErrorCode: "invalid_skin_id"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.unlocked[skinID]; ok {
		return entry, nil
	}
	entry := SkinEntry{SkinID: skinID, UnlockedAt: util.NowIso()}
	s.unlocked[skinID] = entry
	if err := s.persistLocked(); err != nil {
		return SkinEntry{}, err
	}
	return entry, nil
}

func (s *Skins) entriesLocked() []SkinEntry {
	out := make([]SkinEntry, 0, len(s.unlocked))
	for _, entry := range s.unlocked {
		out = append(out, entry)
	}
	return out
}

func (s *Skins) persistLocked() error {
	payload, err := json.MarshalIndent(map[string]any{
		"version":  1,
		"unlocked": s.entriesLocked(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(s.file, append(payload, '\n'))
}

// Visibility records which workflows the user hid from the canvas.
type Visibility struct {
	mu     sync.Mutex
	file   string
	logger *logx.Logger
	hidden map[string]bool
}

// NewVisibility builds the visibility store.
func NewVisibility(dataDir string, logger *logx.Logger) *Visibility {
	return &Visibility{
		file:   filepath.Join(dataDir, "workflow-visibility.json"),
		logger: logger,
		hidden: map[string]bool{},
	}
}

// Load reads the hidden set.
func (v *Visibility) Load() {
	data, err := os.ReadFile(v.file)
	if err != nil {
		if !os.IsNotExist(err) {
			v.logger.Warn("failed to read workflow visibility: %s", err)
		}
		return
	}
	var document struct {
		Hidden []string `json:"hidden"`
	}
	if err := json.Unmarshal(stripBOM(data), &document); err != nil {
		v.logger.Warn("failed to read workflow visibility: %s", err)
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, id := range document.Hidden {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			v.hidden[trimmed] = true
		}
	}
}

// IsHidden reports whether a workflow was hidden by the user.
func (v *Visibility) IsHidden(id string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.hidden[strings.TrimSpace(id)]
}

// List returns the hidden workflow ids.
func (v *Visibility) List() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.listLocked()
}

func (v *Visibility) listLocked() []string {
	out := make([]string, 0, len(v.hidden))
	for id := range v.hidden {
		out = append(out, id)
	}
	return out
}

// SetHidden updates one workflow's visibility.
func (v *Visibility) SetHidden(id string, hidden bool) error {
	key := strings.TrimSpace(id)
	if key == "" {
		return &InvalidIDError{Message: "missing workflow id", ErrorCode: "missing_workflow_id"}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if hidden {
		v.hidden[key] = true
	} else {
		delete(v.hidden, key)
	}
	payload, err := json.MarshalIndent(map[string]any{
		"version":   1,
		"hidden":    v.listLocked(),
		"updatedAt": util.NowIso(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(v.file, append(payload, '\n'))
}

// MainModels records the per-workflow main-model override.
//
// Version 2 stores workflow-scoped overrides directly; the version 1 pack-scoped map
// is still read and indexed so existing data files keep working.
type MainModels struct {
	mu       sync.Mutex
	file     string
	logger   *logx.Logger
	packs    map[string]mainModelPack
	workflow map[string]string
	legacy   map[string]string
}

type mainModelPack struct {
	File      string   `json:"file"`
	Workflows []string `json:"workflows"`
}

// NewMainModels builds the store.
func NewMainModels(dataDir string, logger *logx.Logger) *MainModels {
	return &MainModels{
		file:     filepath.Join(dataDir, "main-models.json"),
		logger:   logger,
		packs:    map[string]mainModelPack{},
		workflow: map[string]string{},
		legacy:   map[string]string{},
	}
}

// Load reads the persisted overrides.
func (m *MainModels) Load() {
	data, err := os.ReadFile(m.file)
	if err != nil {
		if !os.IsNotExist(err) {
			m.logger.Warn("failed to read main-model overrides: %s", err)
		}
		return
	}
	var document struct {
		Packs     map[string]mainModelPack `json:"packs"`
		Workflows map[string]string        `json:"workflows"`
	}
	if err := json.Unmarshal(stripBOM(data), &document); err != nil {
		m.logger.Warn("failed to read main-model overrides: %s", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, pack := range document.Packs {
		if strings.TrimSpace(pack.File) == "" {
			continue
		}
		cleaned := make([]string, 0, len(pack.Workflows))
		for _, workflowID := range pack.Workflows {
			if workflowID != "" {
				cleaned = append(cleaned, workflowID)
			}
		}
		m.packs[id] = mainModelPack{File: strings.TrimSpace(pack.File), Workflows: cleaned}
	}
	for workflowID, file := range document.Workflows {
		if workflowID == "" {
			continue
		}
		m.workflow[workflowID] = strings.TrimSpace(file)
	}
	m.rebuildLocked()
}

// rebuildLocked indexes the legacy pack map by workflow id.
func (m *MainModels) rebuildLocked() {
	m.legacy = map[string]string{}
	for _, pack := range m.packs {
		for _, workflowID := range pack.Workflows {
			m.legacy[workflowID] = pack.File
		}
	}
}

// FileForPack returns the override recorded for a pack.
func (m *MainModels) FileForPack(packID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.packs[packID].File
}

// FileForWorkflow returns the override in effect for a workflow.
func (m *MainModels) FileForWorkflow(workflowID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if file, ok := m.workflow[workflowID]; ok {
		return file
	}
	return m.legacy[workflowID]
}

// Snapshot returns the pack map for diagnostics.
func (m *MainModels) Snapshot() map[string]mainModelPack {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]mainModelPack, len(m.packs))
	for id, pack := range m.packs {
		out[id] = mainModelPack{File: pack.File, Workflows: append([]string{}, pack.Workflows...)}
	}
	return out
}

// Set records an override for one pack.
func (m *MainModels) Set(packID, file string, workflows []string) error {
	id := strings.TrimSpace(packID)
	if id == "" {
		return &InvalidIDError{Message: "missing pack id", ErrorCode: "missing_pack_id"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if value := strings.TrimSpace(file); value != "" {
		cleaned := make([]string, 0, len(workflows))
		for _, workflowID := range workflows {
			if workflowID != "" {
				cleaned = append(cleaned, workflowID)
			}
		}
		m.packs[id] = mainModelPack{File: value, Workflows: cleaned}
	} else {
		delete(m.packs, id)
	}
	m.rebuildLocked()
	return m.persistLocked()
}

// SetForWorkflow records an override for a single workflow.
func (m *MainModels) SetForWorkflow(workflowID, file string) error {
	id := strings.TrimSpace(workflowID)
	if id == "" {
		return &InvalidIDError{Message: "missing workflow id", ErrorCode: "missing_workflow_id"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workflow[id] = strings.TrimSpace(file)
	m.rebuildLocked()
	return m.persistLocked()
}

func (m *MainModels) persistLocked() error {
	payload, err := json.MarshalIndent(map[string]any{
		"version":   2,
		"packs":     m.packs,
		"workflows": m.workflow,
		"updatedAt": util.NowIso(),
	}, "", "  ")
	if err != nil {
		return err
	}
	return util.WriteFileAtomic(m.file, append(payload, '\n'))
}

// InvalidIDError reports a rejected identifier, carrying its own error code.
type InvalidIDError struct {
	Message   string
	ErrorCode string
}

// Error implements the error interface.
func (e *InvalidIDError) Error() string { return e.Message }

// Code returns the documented error code for the HTTP layer.
func (e *InvalidIDError) Code() string { return e.ErrorCode }

// stripBOM removes a UTF-8 byte order mark, since some editors add one.
func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		return data[3:]
	}
	return data
}
