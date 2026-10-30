package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/comfy"
	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// Problem is one reason a workflow is not usable right now.
type Problem struct {
	Type       string   `json:"type"`
	Message    string   `json:"message"`
	ClassType  string   `json:"classType,omitempty"`
	Input      string   `json:"input,omitempty"`
	Value      string   `json:"value,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

// Availability is the cached verdict for one workflow.
type Availability struct {
	Available bool      `json:"available"`
	Problems  []Problem `json:"problems"`
}

// WorkflowState is one row of the scan summary.
type WorkflowState struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Available bool      `json:"available"`
	Problems  []Problem `json:"problems"`
}

// Counts summarizes availability.
type Counts struct {
	Total       int `json:"total"`
	Available   int `json:"available"`
	Unavailable int `json:"unavailable"`
}

// Missing enumerates what the scan could not satisfy.
type Missing struct {
	Models []string `json:"models"`
	Nodes  []string `json:"nodes"`
}

// Status is the payload of /api/setup/status and /health.setup.
type Status struct {
	Phase          string          `json:"phase"`
	ComfyURL       string          `json:"comfyUrl"`
	ComfyVersion   string          `json:"comfyVersion"`
	ComfyReachable bool            `json:"comfyReachable"`
	ScanStartedAt  *string         `json:"scanStartedAt"`
	ScannedAt      *string         `json:"scannedAt"`
	ScanDurationMs int64           `json:"scanDurationMs"`
	LastError      *string         `json:"lastError"`
	Counts         Counts          `json:"counts"`
	Workflows      []WorkflowState `json:"workflows"`
	Loras          []string        `json:"loras"`
	Missing        Missing         `json:"missing"`
	Scanning       bool            `json:"scanning"`
}

// Manager keeps the cached view of which nodes and models ComfyUI has.
type Manager struct {
	cfg           *config.Config
	registry      *workflow.Registry
	comfy         *comfy.Client
	logger        *logx.Logger
	mainModelFile func(workflowID string) string

	statusFile string
	aliasFile  string

	mu        sync.Mutex
	status    Status
	aliases   map[string]string
	auto      map[string]map[string]string
	available map[string]*Availability

	loaderCatalog     map[string][]string
	folderPathCatalog map[string]any
	nodeClasses       map[string]bool

	hasScanned bool
	scanning   bool
	scanMu     sync.Mutex
}

// Options configures a Manager.
type Options struct {
	Config        *config.Config
	Registry      *workflow.Registry
	Comfy         *comfy.Client
	Logger        *logx.Logger
	MainModelFile func(workflowID string) string
}

// New builds a manager and reads its persisted state.
func New(opts Options) *Manager {
	mainModelFile := opts.MainModelFile
	if mainModelFile == nil {
		mainModelFile = func(string) string { return "" }
	}
	manager := &Manager{
		cfg:               opts.Config,
		registry:          opts.Registry,
		comfy:             opts.Comfy,
		logger:            opts.Logger,
		mainModelFile:     mainModelFile,
		statusFile:        filepath.Join(opts.Config.DataDir, "setup-status.json"),
		aliasFile:         filepath.Join(opts.Config.DataDir, "model-aliases.json"),
		aliases:           map[string]string{},
		auto:              map[string]map[string]string{},
		available:         map[string]*Availability{},
		loaderCatalog:     map[string][]string{},
		folderPathCatalog: map[string]any{},
		nodeClasses:       map[string]bool{},
		status: Status{
			Phase:     "idle",
			ComfyURL:  opts.Config.ComfyURL,
			Workflows: []WorkflowState{},
			Loras:     []string{},
			Missing:   Missing{Models: []string{}, Nodes: []string{}},
		},
	}
	manager.aliases = readAliasFile(manager.aliasFile)
	return manager
}

// readAliasFile loads the persisted alias table.
func readAliasFile(file string) map[string]string {
	data, err := os.ReadFile(file)
	if err != nil {
		return map[string]string{}
	}
	var document struct {
		Aliases map[string]string `json:"aliases"`
	}
	if err := json.Unmarshal(data, &document); err != nil || document.Aliases == nil {
		return map[string]string{}
	}
	return document.Aliases
}

// aliasKey builds the composite key used by the alias table.
func aliasKey(classType, input, from string) string {
	return classType + "\x00" + input + "\x00" + util.NormalizeSlashes(from)
}

func normalizeBaseName(value string) string {
	normalized := util.NormalizeSlashes(value)
	if index := strings.LastIndex(normalized, "/"); index >= 0 {
		normalized = normalized[index+1:]
	}
	return strings.ToLower(strings.TrimSpace(normalized))
}

func isModelOption(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && !strings.HasPrefix(trimmed, "<")
}

// Status returns the current scan state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.status
	out.Scanning = m.scanning
	return out
}

// HasScanned reports whether at least one scan completed.
func (m *Manager) HasScanned() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasScanned
}

// IsAvailable reports whether a workflow can run right now.
//
// Unknown ids are treated as unavailable: a workflow added to the registry after the
// last scan must not be advertised as ready, because the user will only discover the
// missing models when the generation fails inside ComfyUI.
func (m *Manager) IsAvailable(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.hasScanned {
		return true
	}
	entry, ok := m.available[id]
	if !ok {
		return false
	}
	return entry.Available
}

// WorkflowStatus returns the cached verdict, defaulting to a "not scanned" problem.
func (m *Manager) WorkflowStatus(id string) Availability {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry, ok := m.available[id]; ok {
		return *entry
	}
	return Availability{
		Available: false,
		Problems:  []Problem{{Type: "scan", Message: "this workflow has not been scanned yet"}},
	}
}

// LoraNames returns the installed LoRA files.
func (m *Manager) LoraNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.status.Loras))
	copy(out, m.status.Loras)
	return out
}

// LoaderOptions returns the installed files per loader class.
func (m *Manager) LoaderOptions() workflow.LoaderOptions {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(workflow.LoaderOptions, len(m.loaderCatalog))
	for key, value := range m.loaderCatalog {
		out[key] = value
	}
	return out
}

// FolderPaths returns ComfyUI's model directory mapping.
func (m *Manager) FolderPaths() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]any, len(m.folderPathCatalog))
	for key, value := range m.folderPathCatalog {
		out[key] = value
	}
	return out
}

// ModelAliasFor maps an authored model name onto an installed one.
func (m *Manager) ModelAliasFor(classType, input, value string) string {
	if value == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if user, ok := m.aliases[aliasKey(classType, input, value)]; ok && user != "" {
		if actual := m.findInstalledOptionLocked(classType, user); actual != "" {
			return actual
		}
		if m.aliasTargetExistsLocked(classType, user) {
			return util.NormalizeSlashes(user)
		}
	}
	if byBase, ok := m.auto[classType]; ok {
		if auto, ok := byBase[normalizeBaseName(value)]; ok {
			if util.NormalizeSlashes(auto) != util.NormalizeSlashes(value) {
				return auto
			}
		}
	}
	return ""
}

// ModelCandidates suggests installed alternatives for a missing model.
func (m *Manager) ModelCandidates(classType, value string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modelCandidatesLocked(classType, value)
}

func (m *Manager) modelCandidatesLocked(classType, value string) []string {
	base := normalizeBaseName(value)
	var candidates []string
	for _, item := range m.loaderCatalog[classType] {
		if !isModelOption(item) || util.NormalizeSlashes(item) == util.NormalizeSlashes(value) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		sameI := normalizeBaseName(candidates[i]) == base
		sameJ := normalizeBaseName(candidates[j]) == base
		if sameI != sameJ {
			return sameI
		}
		// localeCompare in the old implementation folded case; match that so the
		// suggestion list stays stable and readable.
		left := strings.ToLower(candidates[i])
		right := strings.ToLower(candidates[j])
		if left != right {
			return left < right
		}
		return candidates[i] < candidates[j]
	})
	return candidates
}

func (m *Manager) findInstalledOptionLocked(classType, value string) string {
	normalized := util.NormalizeSlashes(value)
	for _, item := range m.loaderCatalog[classType] {
		if isModelOption(item) && util.NormalizeSlashes(item) == normalized {
			return item
		}
	}
	return ""
}

// aliasTargetExistsLocked reports whether an alias target is on disk even though
// ComfyUI does not list it yet (for instance right after a manual download).
func (m *Manager) aliasTargetExistsLocked(classType, value string) bool {
	if m.findInstalledOptionLocked(classType, value) != "" {
		return true
	}
	normalized := util.NormalizeSlashes(value)
	if normalized == "" || filepath.IsAbs(normalized) || strings.Contains(normalized, "..") {
		return false
	}
	_, dirs, ok := workflow.SpecForClass(classType)
	if !ok {
		return false
	}
	for _, dir := range dirs {
		for _, root := range m.modelRootsLocked(dir) {
			if util.Exists(filepath.Join(root, normalized)) {
				return true
			}
		}
	}
	return false
}

// modelRootsLocked resolves the directories a logical model type lives in.
func (m *Manager) modelRootsLocked(dir string) []string {
	return CollectModelRoots(dir, m.folderPathCatalog, m.cfg.ComfyDir, m.comfyRootLocked())
}

func (m *Manager) comfyRootLocked() string {
	return InstallRoot(m.cfg.ComfyDir, m.folderPathCatalog)
}

// SetModelAlias records one alias override and rescans.
func (m *Manager) SetModelAlias(entry map[string]any) (Status, error) {
	classType := util.StringField(entry, "classType")
	input := util.StringField(entry, "input")
	if _, _, ok := workflow.SpecFor(classType, input); !ok {
		return m.Status(), httpx.BadRequest("unsupported_input", "unsupported model input")
	}
	source := strings.TrimSpace(util.StringField(entry, "from"))
	if source == "" {
		return m.Status(), httpx.BadRequest("missing_source", "missing the source model name")
	}
	target := strings.TrimSpace(util.StringField(entry, "to"))
	key := aliasKey(classType, input, source)

	m.mu.Lock()
	if target == "" || util.NormalizeSlashes(target) == util.NormalizeSlashes(source) {
		delete(m.aliases, key)
	} else if actual := m.findInstalledOptionLocked(classType, target); actual != "" {
		m.aliases[key] = actual
	} else if m.aliasTargetExistsLocked(classType, target) {
		m.aliases[key] = util.NormalizeSlashes(target)
	} else {
		m.mu.Unlock()
		return m.Status(), httpx.BadRequest("alias_target_missing", "the selected model is not installed or not listed by ComfyUI: "+target)
	}
	payload, err := json.MarshalIndent(map[string]any{
		"version":   1,
		"aliases":   m.aliases,
		"updatedAt": util.NowIso(),
	}, "", "  ")
	m.mu.Unlock()
	if err != nil {
		return m.Status(), err
	}
	if err := util.WriteFileAtomic(m.aliasFile, append(payload, '\n')); err != nil {
		return m.Status(), err
	}
	return m.Scan()
}

// PickModelFile opens a native file chooser and returns a model-relative path.
//
// zenity is used rather than a bespoke dialog: it is present on any desktop Linux
// install, and failing loudly beats silently returning nothing.
func (m *Manager) PickModelFile(dir string) (string, error) {
	if dir == "" {
		return "", httpx.BadRequest("invalid_dir", "unsupported model directory")
	}
	if _, ok := ModelDirKeys[dir]; !ok {
		return "", httpx.BadRequest("invalid_dir", "unsupported model directory")
	}

	m.mu.Lock()
	roots := m.modelRootsLocked(dir)
	m.mu.Unlock()

	var usable []string
	for _, root := range roots {
		if util.DirExists(root) {
			usable = append(usable, root)
		}
	}
	if len(usable) == 0 {
		return "", httpx.BadRequest("no_model_root", "no ComfyUI model directory found")
	}

	initial := preferredRoot(usable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	args := []string{
		"--file-selection",
		"--title=Select a model file",
		"--filename=" + strings.TrimRight(initial, "/") + "/",
		"--file-filter=Model files | *.safetensors *.sft *.ckpt *.pt *.pth *.bin *.gguf *.onnx *.engine",
	}
	// Log the exact argv: if the dialog fails, the user can paste this into a shell.
	m.logger.Info("running external command: %s", util.CommandLine("zenity", args...))
	out, err := exec.CommandContext(ctx, "zenity", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case asExitError(err, &exitErr) && exitErr.ExitCode() == 1:
			// The user closed the dialog on purpose.
			return "", nil
		case errors.Is(err, exec.ErrNotFound):
			m.logger.Warn("zenity is not installed; cannot open the file picker")
			return "", httpx.BadRequest("dialog_unavailable",
				"zenity was not found; type the model's relative path instead (for example moss_image_sft_bf16.safetensors), or install zenity")
		default:
			m.logger.Warn("zenity failed: %s", err)
			return "", httpx.BadRequest("dialog_failed", "file picker failed: "+err.Error())
		}
	}

	selected := strings.TrimSpace(string(out))
	if selected == "" {
		return "", nil
	}
	relative, _, ok := ModelRelativePath(usable, selected)
	if !ok {
		return "", httpx.BadRequest("outside_model_root",
			fmt.Sprintf("the selected file is outside ComfyUI's model directories, so ComfyUI cannot see it. Available roots: %s", strings.Join(usable, "、")))
	}
	return relative, nil
}

// preferredRoot picks the first model root that actually contains files.
//
// ComfyUI often ships an empty models/unet next to the real models/diffusion_models,
// so the empty one must not be offered first in the file dialog.
func preferredRoot(roots []string) string {
	modelFilePattern := regexp.MustCompile(`(?i)\.(safetensors|sft|ckpt|pt|pth|bin|gguf|onnx|engine)$`)
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if entry.IsDir() || modelFilePattern.MatchString(entry.Name()) {
				return root
			}
		}
	}
	return roots[0]
}

// Start kicks off the initial scan.
//
// There is no background poller: the scan is expensive (it asks ComfyUI for its whole
// object_info) and, because Registry.Scan registers the graphs it finds, it also rewrites
// workflows/registry.json — which must not happen behind the back of whoever is editing
// that file. A rescan happens at boot, on POST /api/resources/refresh, after a resource install or
// a model switch, and when the panel's refresh button is used.
func (m *Manager) Start() {
	go func() {
		if _, err := m.Scan(); err != nil {
			m.logger.Warn("initial setup scan failed: %s", err)
		}
	}()
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}
