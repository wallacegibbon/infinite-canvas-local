// Package task implements the asynchronous job store used for video generation.
//
// Video runs for minutes, so the canvas gets a job id immediately and polls. The
// store persists to data/tasks.json and reloads on boot, which is why it also has
// to reconcile records whose output file vanished while the gateway was down.
package task

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Terminal statuses never change again.
var terminalStatuses = map[string]bool{"completed": true, "failed": true, "cancelled": true}

// Task is one queued generation job.
type Task struct {
	ID          string         `json:"id"`
	Object      string         `json:"object"`
	Status      string         `json:"status"`
	Progress    float64        `json:"progress"`
	Model       string         `json:"model"`
	Request     map[string]any `json:"request"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	CompletedAt *string        `json:"completedAt"`
	PromptID    *string        `json:"promptId"`
	URL         *string        `json:"url"`
	File        *string        `json:"file"`
	MimeType    *string        `json:"mimeType"`
	Bytes       int64          `json:"bytes"`
	Error       *string        `json:"error"`

	// Cancellation plumbing, never serialized.
	cancelled  bool
	cancelFunc context.CancelFunc
}

// Update is one progress tick from the runner.
type Update struct {
	Progress float64
	PromptID string
}

// Result is what a finished job produces.
type Result struct {
	Buffer   []byte
	Ext      string
	MimeType string
	PromptID string
}

// RunFunc performs the work of one task.
type RunFunc func(task *Task, progress func(Update), ctx context.Context) (*Result, error)

// Store is the job queue and its on-disk record.
type Store struct {
	dataDir       string
	publicBaseURL string
	logger        *logx.Logger
	retentionDays int
	maxTasks      int

	mu           sync.RWMutex
	tasks        map[string]*Task
	queue        []*Task
	running      bool
	persistTimer *time.Timer
	persistMu    sync.Mutex
	outputDir    string
	file         string

	// Shutdown plumbing so Close can stop the debounced writer and wait for workers.
	closed    chan struct{}
	closeOnce sync.Once
	workerWG  sync.WaitGroup
}

// Options configures a Store.
type Options struct {
	DataDir       string
	PublicBaseURL string
	Logger        *logx.Logger
	RetentionDays int
	MaxTasks      int
}

// New builds a store and ensures the output directory exists.
func New(opts Options) (*Store, error) {
	outputDir := filepath.Join(opts.DataDir, "outputs")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	return &Store{
		dataDir:       opts.DataDir,
		publicBaseURL: opts.PublicBaseURL,
		logger:        opts.Logger,
		retentionDays: opts.RetentionDays,
		maxTasks:      opts.MaxTasks,
		tasks:         map[string]*Task{},
		closed:        make(chan struct{}),
		outputDir:     outputDir,
		file:          filepath.Join(opts.DataDir, "tasks.json"),
	}, nil
}

// Load restores persisted tasks and reconciles them against the output directory.
func (s *Store) Load() {
	data, err := os.ReadFile(s.file)
	if err != nil {
		// First run: no tasks.json yet.
		return
	}
	var document struct {
		Tasks []*Task `json:"tasks"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		s.logger.Warn("failed to read the task records: %s", err)
		return
	}

	s.mu.Lock()
	for _, task := range document.Tasks {
		if task == nil || task.ID == "" {
			continue
		}
		s.reconcileLocked(task)
		s.tasks[task.ID] = task
	}
	s.mu.Unlock()

	s.persistNow()
}

// reconcileLocked repairs a record that no longer matches the filesystem.
//
// Older versions did not store the file path at all, so a completed task could look
// failed after a restart even though its video was still on disk.
func (s *Store) reconcileLocked(task *Task) {
	if task.File == nil || *task.File == "" {
		candidate := filepath.Join(s.outputDir, task.ID+".mp4")
		if util.Exists(candidate) {
			value := candidate
			task.File = &value
		}
	}
	if task.File != nil && util.Exists(*task.File) {
		if task.URL == nil {
			value := s.publicURL(filepath.Base(*task.File))
			task.URL = &value
		}
		if task.Status != "completed" {
			task.Status = "completed"
			task.Progress = 1
			task.Error = nil
			completed := util.NowIso()
			task.CompletedAt = &completed
		}
		return
	}
	if task.Status == "running" || task.Status == "queued" {
		task.Status = "failed"
		message := "the gateway restarted before the task finished"
		task.Error = &message
		completed := util.NowIso()
		task.CompletedAt = &completed
		return
	}
	if task.Status == "completed" {
		task.Status = "failed"
		message := "output file is missing"
		task.Error = &message
		completed := util.NowIso()
		task.CompletedAt = &completed
	}
}

// Create registers a new queued task.
func (s *Store) Create(model string, request map[string]any) *Task {
	now := util.NowIso()
	task := &Task{
		ID:        util.RandomID("task_"),
		Object:    "video",
		Status:    "queued",
		Progress:  0,
		Model:     model,
		Request:   request,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.mu.Lock()
	s.tasks[task.ID] = task
	s.mu.Unlock()
	s.persistNow()
	return task
}

// Enqueue schedules a task; tasks run one at a time, like the old implementation.
func (s *Store) Enqueue(task *Task, run RunFunc) {
	s.mu.Lock()
	s.queue = append(s.queue, task)
	s.mu.Unlock()
	s.startPump(run)
}

// startPump runs the queue worker in a tracked goroutine so Close can wait for it.
func (s *Store) startPump(run RunFunc) {
	s.workerWG.Add(1)
	go func() {
		defer s.workerWG.Done()
		s.pump(run)
	}()
}

// Close stops background persistence and flushes the task list.
//
// Without it the debounced writer can fire after the data directory is considered
// finished with (a test teardown, or a rapid restart), and shutdown has no way to
// know the queue went quiet. The wait is bounded so a long video job cannot hold exit.
func (s *Store) Close() {
	s.closeOnce.Do(func() { close(s.closed) })

	drained := make(chan struct{})
	go func() {
		s.workerWG.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		if s.logger != nil {
			s.logger.Warn("a task was still running at shutdown; skipped waiting for it")
		}
	}

	s.persistMu.Lock()
	if s.persistTimer != nil {
		s.persistTimer.Stop()
		s.persistTimer = nil
	}
	s.persistMu.Unlock()
	s.PersistNow()
}

// pump drains the queue serially. ComfyUI is a single GPU, so parallel jobs would
// only fight over VRAM.
func (s *Store) pump(run RunFunc) {
	select {
	case <-s.closed:
		return
	default:
	}

	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	if len(s.queue) == 0 {
		s.mu.Unlock()
		return
	}
	task := s.queue[0]
	s.queue = s.queue[1:]
	s.running = true
	ctx, cancel := context.WithCancel(context.Background())
	task.cancelFunc = cancel
	task.Status = "running"
	task.Progress = 0.02
	task.UpdatedAt = util.NowIso()
	s.mu.Unlock()
	s.schedulePersist()

	result, err := run(task, func(update Update) {
		s.mu.Lock()
		if update.Progress > task.Progress {
			task.Progress = update.Progress
		}
		if update.PromptID != "" {
			promptID := update.PromptID
			task.PromptID = &promptID
		}
		task.UpdatedAt = util.NowIso()
		s.mu.Unlock()
		s.schedulePersist()
	}, ctx)
	cancel()

	s.mu.Lock()
	if task.cancelled {
		task.Status = "cancelled"
		message := "task cancelled"
		task.Error = &message
	} else if err != nil {
		message := err.Error()
		task.Status = "failed"
		task.Error = &message
		completed := util.NowIso()
		task.CompletedAt = &completed
		s.logger.Warn("task %s failed: %s", task.ID, message)
	} else {
		savedURL, savedFile, saveErr := s.saveOutput(task.ID, result.Buffer, defaultExt(result.Ext))
		if saveErr != nil {
			message := "failed to write the output file: " + saveErr.Error()
			task.Status = "failed"
			task.Error = &message
			completed := util.NowIso()
			task.CompletedAt = &completed
			s.logger.Warn("task %s failed: %s", task.ID, message)
		} else {
			task.Status = "completed"
			task.Progress = 1
			if result.PromptID != "" {
				promptID := result.PromptID
				task.PromptID = &promptID
			}
			task.URL = &savedURL
			task.File = &savedFile
			mimeType := result.MimeType
			if mimeType == "" {
				mimeType = "video/mp4"
			}
			task.MimeType = &mimeType
			task.Bytes = int64(len(result.Buffer))
			completed := util.NowIso()
			task.CompletedAt = &completed
		}
	}
	task.UpdatedAt = util.NowIso()
	task.cancelFunc = nil
	task.cancelled = false
	s.running = false
	s.mu.Unlock()

	s.persistNow()
	s.startPump(run)
}

// snapshot copies a task so callers can read it without racing the worker.
//
// Handing out the live pointer was a data race: /v1/tasks and the video status
// endpoints serialise task fields while the queue goroutine updates progress.
func (t *Task) snapshot() *Task {
	if t == nil {
		return nil
	}
	copied := *t
	return &copied
}

// Get returns a snapshot of a task by id.
func (s *Store) Get(id string) *Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tasks[id].snapshot()
}

// List returns snapshots of all tasks, newest first.
func (s *Store) List() []*Task {
	s.mu.RLock()
	out := make([]*Task, 0, len(s.tasks))
	for _, task := range s.tasks {
		out = append(out, task.snapshot())
	}
	s.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Cancel stops a queued or running task.
func (s *Store) Cancel(id string) *Task {
	s.mu.Lock()
	task, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	if terminalStatuses[task.Status] {
		s.mu.Unlock()
		return task.snapshot()
	}
	task.cancelled = true
	for index, queued := range s.queue {
		if queued == task {
			s.queue = append(s.queue[:index], s.queue[index+1:]...)
			break
		}
	}
	wasQueued := task.Status == "queued"
	cancelFunc := task.cancelFunc
	if wasQueued {
		task.Status = "cancelled"
		message := "task cancelled"
		task.Error = &message
		now := util.NowIso()
		task.CompletedAt = &now
		task.UpdatedAt = now
	}
	s.mu.Unlock()

	if cancelFunc != nil {
		cancelFunc()
	}
	if wasQueued {
		s.persistNow()
	}
	return s.Get(id)
}

// ContentPath returns the on-disk path of a finished task's output, or "" when it
// is missing or outside the output directory.
func (s *Store) ContentPath(id string) string {
	s.mu.RLock()
	task, ok := s.tasks[id]
	s.mu.RUnlock()
	if !ok || task.File == nil || *task.File == "" {
		return ""
	}
	resolved, err := filepath.Abs(*task.File)
	if err != nil {
		return ""
	}
	root, err := filepath.Abs(s.outputDir)
	if err != nil {
		return ""
	}
	if !within(root, resolved) || !util.Exists(resolved) {
		return ""
	}
	return resolved
}

// saveOutput writes a finished artifact and returns its public URL and path.
func (s *Store) saveOutput(id string, buffer []byte, ext string) (string, string, error) {
	filename := id + "." + ext
	target := filepath.Join(s.outputDir, filename)
	if err := os.WriteFile(target, buffer, 0o644); err != nil {
		return "", "", err
	}
	return s.publicURL(filename), target, nil
}

// SaveImage stores a one-off image and returns its public URL.
func (s *Store) SaveImage(id string, buffer []byte, ext string) (string, error) {
	url, _, err := s.saveOutput(id, buffer, defaultExt(ext))
	return url, err
}

func (s *Store) publicURL(filename string) string {
	return s.publicBaseURL + "/files/" + url.PathEscape(filename)
}

// PersistNow writes the task list synchronously.
func (s *Store) PersistNow() { s.persistNow() }

func (s *Store) persistNow() {
	s.mu.RLock()
	payload := struct {
		Version   int     `json:"version"`
		UpdatedAt string  `json:"updatedAt"`
		Tasks     []*Task `json:"tasks"`
	}{Version: 1, UpdatedAt: util.NowIso(), Tasks: make([]*Task, 0, len(s.tasks))}
	for _, task := range s.tasks {
		payload.Tasks = append(payload.Tasks, task)
	}
	s.mu.RUnlock()

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		s.logger.Warn("failed to serialise tasks: %s", err)
		return
	}
	if err := util.WriteFileAtomic(s.file, append(data, '\n')); err != nil {
		s.logger.Warn("failed to save tasks: %s", err)
	}
}

// schedulePersist debounces writes so a progress-heavy job does not hammer the disk.
func (s *Store) schedulePersist() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if s.persistTimer != nil {
		return
	}
	s.persistTimer = time.AfterFunc(250*time.Millisecond, func() {
		s.persistMu.Lock()
		s.persistTimer = nil
		s.persistMu.Unlock()
		s.persistNow()
	})
}

// Cleanup removes finished tasks beyond the retention window or count.
func (s *Store) Cleanup(retentionDays, maxTasks int) (removed int, filesDeleted int) {
	s.mu.Lock()
	var terminal []*Task
	for _, task := range s.tasks {
		if terminalStatuses[task.Status] {
			terminal = append(terminal, task)
		}
	}
	// Map iteration is randomized, so prune in a deterministic order: two tasks that
	// finished in the same millisecond must not make "which one is dropped" a coin flip.
	sort.SliceStable(terminal, func(i, j int) bool {
		left, right := taskTime(terminal[i]), taskTime(terminal[j])
		if left.Equal(right) {
			return terminal[i].ID < terminal[j].ID
		}
		return left.Before(right)
	})
	cutoff := time.Time{}
	if retentionDays > 0 {
		cutoff = time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	}
	drop := map[string]*Task{}
	for _, task := range terminal {
		if !cutoff.IsZero() && taskTime(task).Before(cutoff) {
			drop[task.ID] = task
		}
	}
	if maxTasks > 0 {
		var remaining []*Task
		for _, task := range terminal {
			if _, dropped := drop[task.ID]; !dropped {
				remaining = append(remaining, task)
			}
		}
		sort.SliceStable(remaining, func(i, j int) bool {
			return taskTime(remaining[i]).Before(taskTime(remaining[j]))
		})
		for len(s.tasks)-len(drop) > maxTasks && len(remaining) > 0 {
			drop[remaining[0].ID] = remaining[0]
			remaining = remaining[1:]
		}
	}
	for id := range drop {
		delete(s.tasks, id)
	}
	removed = len(drop)
	s.mu.Unlock()

	if removed == 0 {
		return 0, 0
	}
	for _, task := range drop {
		if task.File == nil {
			continue
		}
		resolved, err := filepath.Abs(*task.File)
		if err != nil {
			continue
		}
		root, err := filepath.Abs(s.outputDir)
		if err != nil || !within(root, resolved) {
			continue
		}
		if err := os.Remove(resolved); err == nil {
			filesDeleted++
		}
	}
	s.persistNow()
	s.logger.Info("pruned %d task(s) and deleted %d output file(s)", removed, filesDeleted)
	return removed, filesDeleted
}

// PublicTask returns the wire projection of a task.
func PublicTask(task *Task) map[string]any {
	if task == nil {
		return nil
	}
	return map[string]any{
		"id":          task.ID,
		"object":      "video",
		"status":      task.Status,
		"progress":    task.Progress,
		"model":       task.Model,
		"request":     task.Request,
		"createdAt":   task.CreatedAt,
		"updatedAt":   task.UpdatedAt,
		"completedAt": task.CompletedAt,
		"promptId":    task.PromptID,
		"url":         task.URL,
		"file":        task.File,
		"mimeType":    task.MimeType,
		"bytes":       task.Bytes,
		"error":       task.Error,
	}
}

func taskTime(task *Task) time.Time {
	for _, value := range []*string{task.CompletedAt, &task.UpdatedAt, &task.CreatedAt} {
		if value == nil || *value == "" {
			continue
		}
		if parsed := util.ParseIso(*value); !parsed.IsZero() {
			return parsed
		}
	}
	return time.Time{}
}

// within reports whether child is inside parent.
func within(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func defaultExt(ext string) string {
	if ext == "" {
		return "mp4"
	}
	return util.SafeFileName(ext)
}
