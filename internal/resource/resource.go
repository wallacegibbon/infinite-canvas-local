// Package resource manages the model and node files the workflows need.
//
// It answers one question for the canvas: "given resources.json and what is
// actually on disk, what is installed, what is missing, and how do I install it?"
// Everything here is read-only until an install job is started.
package resource

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/setup"
	"comfyui-gateway/internal/store"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// Source is one place a resource can be downloaded or copied from.
type Source struct {
	Kind  string `json:"kind,omitempty"`
	URL   string `json:"url,omitempty"`
	Path  string `json:"path,omitempty"`
	Label string `json:"label,omitempty"`
}

// Resource is one entry of resources.json.
type Resource struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Label    string         `json:"label"`
	Target   string         `json:"target"`
	Size     int64          `json:"size"`
	Sha256   string         `json:"sha256"`
	License  string         `json:"license"`
	Note     string         `json:"note"`
	Provides []string       `json:"provides"`
	Sources  []any          `json:"sources"`
	Requires []string       `json:"requires"`
	Builder  map[string]any `json:"builder"`
	Files    []string       `json:"files"`
	// A node pack is downloaded source, so the catalogue records which upstream commit the
	// copy came from and which files were changed on top of it. Both are facts about the
	// tree in payload/, not download instructions: the install still copies the
	// bundled directory.
	Upstream     string   `json:"upstream"`
	PinnedCommit string   `json:"pinnedCommit"`
	LocalPatches []string `json:"localPatches"`
}

// Pack is one feature bundle: a set of resources plus the workflows they enable.
type Pack struct {
	ID                string                        `json:"id"`
	Label             string                        `json:"label"`
	Description       string                        `json:"description"`
	Optional          bool                          `json:"optional"`
	OptionalLabel     string                        `json:"optionalLabel"`
	Planned           bool                          `json:"planned"`
	Resources         []string                      `json:"resources"`
	OptionalResources []string                      `json:"optionalResources"`
	Workflows         []string                      `json:"workflows"`
	WorkflowDeps      workflow.OrderedMap[[]string] `json:"workflowDependencies"`
}

// Index is the parsed resources.json document.
type Index struct {
	Schema    int         `json:"schema"`
	Version   any         `json:"version"`
	Channel   string      `json:"channel"`
	UpdatedAt string      `json:"updatedAt"`
	Packs     []*Pack     `json:"packs"`
	Resources []*Resource `json:"resources"`
}

// Item is the wire projection of one resource's status.
type Item struct {
	ID              string         `json:"id"`
	Type            string         `json:"type"`
	Label           string         `json:"label"`
	Target          string         `json:"target"`
	Size            int64          `json:"size"`
	Sha256          string         `json:"sha256"`
	License         string         `json:"license"`
	Note            string         `json:"note"`
	Sources         []string       `json:"sources"`
	Requires        []string       `json:"requires"`
	Builder         map[string]any `json:"builder"`
	Status          string         `json:"status"`
	Present         bool           `json:"present"`
	Registered      bool           `json:"registered"`
	Exists          bool           `json:"exists"`
	MatchedFile     string         `json:"matchedFile"`
	MatchedBy       string         `json:"matchedBy"`
	SizeMismatch    string         `json:"sizeMismatch"`
	ActualSize      int64          `json:"actualSize"`
	Installed       map[string]any `json:"installed"`
	MissingRequires []string       `json:"missingRequires"`
	// MissingClasses names the node classes a pack declares but ComfyUI has not loaded. It is what
	// "the folder is there but nothing works" looks like: a missing python dependency, for instance.
	MissingClasses []string `json:"missingClasses,omitempty"`
	// ManualCommand is set for resources the gateway never installs itself (type
	// "bake"): the exact command that produces the file.
	ManualCommand string `json:"manualCommand,omitempty"`
	Overridden    bool   `json:"overridden,omitempty"`
	OverriddenBy  string `json:"overriddenBy,omitempty"`
}

// MainModelOption is one selectable main model of a pack.
type MainModelOption struct {
	File       string `json:"file"`
	Label      string `json:"label"`
	Installed  bool   `json:"installed"`
	Source     string `json:"source"`
	Matches    bool   `json:"matches,omitempty"`
	ResourceID string `json:"resourceId,omitempty"`
}

// MainModel is a pack's main-model selector state.
type MainModel struct {
	WorkflowIDs       []string          `json:"workflowIds"`
	Files             []string          `json:"files"`
	DefaultFile       string            `json:"defaultFile"`
	DefaultLabel      string            `json:"defaultLabel"`
	Selected          string            `json:"selected"`
	SelectedLabel     string            `json:"selectedLabel"`
	SelectedInstalled bool              `json:"selectedInstalled"`
	Available         bool              `json:"available"`
	Options           []MainModelOption `json:"options"`
}

// WorkflowDependency is the per-workflow resource rollup inside a pack.
type WorkflowDependency struct {
	ID            string             `json:"id"`
	Label         string             `json:"label"`
	Available     bool               `json:"available"`
	Enabled       bool               `json:"enabled"`
	ModelVisible  bool               `json:"modelVisible"`
	VariantLabels map[string]string  `json:"variantLabels"`
	Problems      []setup.Problem    `json:"problems"`
	Recommended   bool               `json:"recommended"`
	MainModel     *WorkflowMainModel `json:"mainModel"`
	Resources     []*Item            `json:"resources"`
	Missing       int                `json:"missing"`
	MissingSize   int64              `json:"missingSize"`
	InstallIDs    []string           `json:"installIds"`
}

// WorkflowMainModel is a single workflow's main-model override state.
//
// The pack-level selector overrides every workflow in the pack; this is what the
// per-workflow row renders, and it lists every option except the workflow's own
// default file, since choosing that would be a no-op.
type WorkflowMainModel struct {
	WorkflowID        string            `json:"workflowId"`
	DefaultFile       string            `json:"defaultFile"`
	DefaultLabel      string            `json:"defaultLabel"`
	Selected          string            `json:"selected"`
	SelectedLabel     string            `json:"selectedLabel"`
	SelectedInstalled bool              `json:"selectedInstalled"`
	Options           []MainModelOption `json:"options"`
}

// PackStatus is the wire projection of one pack.
type PackStatus struct {
	ID              string                `json:"id"`
	Label           string                `json:"label"`
	Description     string                `json:"description"`
	Optional        bool                  `json:"optional"`
	OptionalLabel   string                `json:"optionalLabel"`
	Recommended     bool                  `json:"recommended"`
	Planned         bool                  `json:"planned"`
	Workflows       []string              `json:"workflows"`
	ResourceIDs     []string              `json:"resourceIds"`
	RequiredIDs     []string              `json:"requiredIds"`
	OptionalIDs     []string              `json:"optionalIds"`
	Resources       []*Item               `json:"resources"`
	RequiredItems   []*Item               `json:"requiredItems"`
	OptionalItems   []*Item               `json:"optionalItems"`
	MainModel       *MainModel            `json:"mainModel"`
	WorkflowDeps    []*WorkflowDependency `json:"workflowDependencies"`
	Total           int                   `json:"total"`
	Present         int                   `json:"present"`
	Missing         int                   `json:"missing"`
	MissingSize     int64                 `json:"missingSize"`
	OptionalTotal   int                   `json:"optionalTotal"`
	OptionalPresent int                   `json:"optionalPresent"`
	OptionalMissing int                   `json:"optionalMissing"`
	OptionalSize    int64                 `json:"optionalSize"`
}

// RuntimeDep is a Python package the user must install themselves.
//
// The gateway used to do this, and it regularly broke the user's venv. Telling the
// user exactly what to install is both safer and honest.
type RuntimeDep struct {
	ID       string   `json:"id"`
	Packages []string `json:"packages"`
}

// Summary is the headline counts of the resource panel.
type Summary struct {
	Packs            int   `json:"packs"`
	Resources        int   `json:"resources"`
	Present          int   `json:"present"`
	Missing          int   `json:"missing"`
	MissingSize      int64 `json:"missingSize"`
	AllResources     int   `json:"allResources"`
	RecommendedPacks int   `json:"recommendedPacks"`
}

// Status is the payload of GET /api/resources/status.
type Status struct {
	OK             bool                `json:"ok"`
	LoadedAt       string              `json:"loadedAt"`
	IndexSource    string              `json:"indexSource"`
	IndexError     string              `json:"indexError"`
	IndexWarning   string              `json:"indexWarning"`
	IndexVersion   string              `json:"indexVersion"`
	Channel        string              `json:"channel"`
	ComfyRoot      string              `json:"comfyRoot"`
	ComfyReachable bool                `json:"comfyReachable"`
	ModelInstall   InstallPolicy       `json:"modelInstall"`
	RuntimeDeps    []RuntimeDep        `json:"runtimeDeps"`
	ModelRoots     map[string][]string `json:"modelRoots"`
	Packs          []*PackStatus       `json:"packs"`
	Resources      []*Item             `json:"resources"`
	Summary        Summary             `json:"summary"`
	Lock           map[string]any      `json:"lock"`
}

// Manager owns the resource index, the install lock file, and the job queue.
type Manager struct {
	cfg        *config.Config
	setup      *setup.Manager
	registry   *workflow.Registry
	visibility *store.Visibility
	mainModels *store.MainModels
	logger     *logx.Logger

	indexPath      string
	indexCachePath string
	lockPath       string

	mu           sync.Mutex
	index        *Index
	indexSource  string
	indexError   string
	indexWarning string
	loadedAt     string
	lock         map[string]any
	jobs         map[string]*Job
	inventory    map[string]inventoryEntry
}

type inventoryEntry struct {
	at    time.Time
	items []inventoryItem
}

// Options configures a Manager.
type Options struct {
	Config     *config.Config
	Setup      *setup.Manager
	Registry   *workflow.Registry
	Visibility *store.Visibility
	MainModels *store.MainModels
	Logger     *logx.Logger
}

// New builds a resource manager.
func New(opts Options) *Manager {
	return &Manager{
		cfg:            opts.Config,
		setup:          opts.Setup,
		registry:       opts.Registry,
		visibility:     opts.Visibility,
		mainModels:     opts.MainModels,
		logger:         opts.Logger,
		indexPath:      filepath.Join(opts.Config.RootDir, "resources.json"),
		indexCachePath: filepath.Join(opts.Config.DataDir, "resources-index.json"),
		lockPath:       filepath.Join(opts.Config.DataDir, "resource-lock.json"),
		lock:           map[string]any{"version": 1, "installed": map[string]any{}},
		jobs:           map[string]*Job{},
		inventory:      map[string]inventoryEntry{},
	}
}

// Init loads the lock file and the index, then reconciles main-model scopes.
func (m *Manager) Init() *Status {
	m.loadLock()
	m.loadIndex()
	return m.Status()
}

// loadLock reads data/resource-lock.json.
func (m *Manager) loadLock() {
	data, err := os.ReadFile(m.lockPath)
	if err != nil {
		return
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		return
	}
	if version, _ := document["version"].(float64); version == 1 {
		if _, ok := document["installed"].(map[string]any); ok {
			m.lock = document
		}
	}
}

// saveLock persists the install record.
func (m *Manager) saveLock() {
	m.mu.Lock()
	snapshot := m.lock
	m.mu.Unlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	if err := util.WriteFileAtomic(m.lockPath, append(data, '\n')); err != nil {
		m.logger.Warn("failed to save the install record: %s", err)
	}
}

// loadIndex reads resources.json, falling back to the last good copy.
func (m *Manager) loadIndex() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.indexError = ""
	m.indexWarning = ""
	m.indexSource = "local"

	local, localErr := readIndexFile(m.indexPath)
	if local != nil {
		m.index = local
	} else {
		cached, _ := readIndexFile(m.indexCachePath)
		m.index = cached
		if cached != nil {
			m.indexSource = "cache"
			m.indexWarning = "local index unusable (" + localErr + "), using the cached copy"
		} else {
			m.indexSource = "none"
			m.indexError = "local index unusable: " + localErr
		}
	}
	m.loadedAt = util.NowIso()

	if m.index != nil {
		if data, err := json.MarshalIndent(m.index, "", "  "); err == nil {
			_ = util.WriteFileAtomic(m.indexCachePath, append(data, '\n'))
		}
	}
}

// readIndexFile parses and validates one index document.
func readIndexFile(path string) (*Index, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "file not found"
	}
	var index Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err.Error()
	}
	if err := validateIndex(&index); err != nil {
		return nil, err.Error()
	}
	return &index, ""
}

// A target is "<model dir>/<file>", and the file may sit in a subdirectory
// (for example loras/some-vendor/foo.safetensors).
var targetPattern = regexp.MustCompile(`^[^/]+/.+$`)

// validateIndex rejects an index the UI could not render.
func validateIndex(index *Index) error {
	if index.Schema != 1 {
		return fmt.Errorf("resource index is malformed (schema must be 1)")
	}
	seen := map[string]bool{}
	for _, resource := range index.Resources {
		if resource == nil || resource.ID == "" {
			return fmt.Errorf("a resource is missing its id")
		}
		if seen[resource.ID] {
			return fmt.Errorf("duplicate resource id: %s", resource.ID)
		}
		seen[resource.ID] = true
		if !targetPattern.MatchString(strings.ReplaceAll(resource.Target, `\`, "/")) {
			return fmt.Errorf("resource %s target must be <dir>/<file>", resource.ID)
		}
	}
	known := map[string]bool{}
	for _, resource := range index.Resources {
		known[resource.ID] = true
	}
	for _, pack := range index.Packs {
		for _, id := range pack.Resources {
			if !known[id] {
				return fmt.Errorf("pack %s references unknown resource %s", pack.ID, id)
			}
		}
		for _, id := range pack.OptionalResources {
			if !known[id] {
				return fmt.Errorf("pack %s references unknown resource %s", pack.ID, id)
			}
		}
		for _, mode := range pack.WorkflowDeps.Keys {
			for _, id := range pack.WorkflowDeps.Values[mode] {
				if !known[id] {
					return fmt.Errorf("pack %s workflow %s references unknown resource %s", pack.ID, mode, id)
				}
			}
		}
	}
	return nil
}

// payloadPath is where a resource that ships with the gateway is staged.
//
// The layout mirrors the target, so "bundled" is a convention rather than a field in the
// index: payload/custom_nodes/<node> for a node, payload/<type>/<file> for a model. One
// rule, and the payload can be shipped or embedded without the installer knowing which.
// Nothing is staged there today — every model and node pack downloads — but the rule stays
// so that a file which has to ship inside a release needs no new mechanism.
func (m *Manager) payloadPath(target string) string {
	normalized := util.NormalizeSlashes(target)
	return filepath.Join(m.cfg.RootDir, "payload", filepath.FromSlash(normalized))
}

// targetParts splits a target into its directory and file components.
func targetParts(target string) (dir string, file string) {
	normalized := strings.ReplaceAll(target, `\`, "/")
	index := strings.LastIndex(normalized, "/")
	if index < 0 {
		return "", normalized
	}
	return normalized[:index], normalized[index+1:]
}

// ComfyRoot returns the ComfyUI installation directory, if known.
//
// Empty means ComfyUI never reported its folder layout. Installs refuse in that state
// instead of guessing a path.
func (m *Manager) ComfyRoot() string {
	if m.setup == nil {
		return ""
	}
	return setup.InstallRoot(m.setup.FolderPaths())
}

// modelDirs returns every directory a logical model type may live in.
//
// A nil setup (the gateway before Init, or a unit test that does not need ComfyUI) simply
// means no folder-path overrides: the configured ComfyUI directory still applies.
func (m *Manager) modelDirs(dir, root string) []string {
	var paths map[string]any
	if m.setup != nil {
		paths = m.setup.FolderPaths()
	}
	return setup.CollectModelRoots(dir, paths, root)
}

// loaderClassFor maps a model directory to the loader node that reads it.
func loaderClassFor(dir string) (string, bool) {
	for _, spec := range workflow.ModelLoaderSpecs {
		for _, candidate := range spec.Dirs {
			if candidate == dir {
				return spec.ClassType, true
			}
		}
	}
	return "", false
}

var weightFilePattern = regexp.MustCompile(`(?i)\.(safetensors|sft|ckpt|pt|pth|bin|gguf|onnx|engine)$`)

// inventoryItem is one file discovered under a model root.
type inventoryItem struct {
	rel        string
	abs        string
	size       int64
	fromLoader bool
}

// normalizeRelName lowercases and unifies a relative model path.
func normalizeRelName(value string) string {
	normalized := strings.ToLower(util.NormalizeSlashes(value))
	normalized = strings.TrimPrefix(normalized, "./")
	normalized = strings.TrimPrefix(normalized, "/")
	return strings.TrimSpace(normalized)
}
