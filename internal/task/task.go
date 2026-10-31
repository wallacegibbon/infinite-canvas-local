// Package task implements the asynchronous job store used for video generation.
//
// Video runs for minutes, so the canvas gets a job id immediately and polls. The
// store persists to data/tasks.json and reloads on boot, which is why it also has
// to reconcile records whose output file vanished while the gateway was down.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
	"comfyui-gateway/internal/util"
)

// Terminal statuses never change again.
var terminalStatuses = map[string]bool{"completed": true, "failed": true, "cancelled": true}

// Objects a run can produce. The object travels with the record so the queue, the
// reconciliation pass and the wire projection do not have to assume video.
const (
	ObjectImage = "image"
	ObjectVideo = "video"
	ObjectAudio = "audio"
	ObjectText  = "text"
)

// extensionsByObject is the set of file extensions a finished run of that object can
// have. Reconciliation uses it to find an output whose path was never recorded (older
// records stored no path at all) and to pick a default MIME type.
var extensionsByObject = map[string][]string{
	ObjectImage: {".png", ".jpg", ".jpeg", ".webp"},
	ObjectVideo: {".mp4", ".webm", ".mov"},
	ObjectAudio: {".mp3", ".flac", ".wav", ".opus", ".ogg", ".m4a"},
	ObjectText:  {".txt"},
}

// normalizeObject keeps historical records (which were always video) working.
func normalizeObject(object string) string {
	if object == "" {
		return ObjectVideo
	}
	return object
}

// defaultMimeType is the fallback when a run reports no MIME type of its own.
func defaultMimeType(object string) string {
	switch normalizeObject(object) {
	case ObjectImage:
		return "image/png"
	case ObjectAudio:
		return "audio/mpeg"
	case ObjectText:
		return "text/plain"
	default:
		return "video/mp4"
	}
}

// candidateOutputPaths lists where a reconciled run's file may live, most likely first.
func candidateOutputPaths(outputDir, id, object string) []string {
	extensions := extensionsByObject[normalizeObject(object)]
	paths := make([]string, 0, len(extensions))
	for _, extension := range extensions {
		paths = append(paths, filepath.Join(outputDir, id+extension))
	}
	return paths
}

// TaskOutput is one artifact a finished run produced. A run normally has one; an image
// request with count > 1 has several, and the canvas shows them as a batch.
type TaskOutput struct {
	Index    int    `json:"index"`
	File     string `json:"file"`
	URL      string `json:"url"`
	MimeType string `json:"mimeType"`
	Bytes    int64  `json:"bytes"`
}

// Task is one queued generation job.
type Task struct {
	ID          string         `json:"id"`
	Object      string         `json:"object"`
	Status      string         `json:"status"`
	Progress    float64        `json:"progress"`
	Model       string         `json:"model"`
	Request     map[string]any `json:"request"`
	CreatedAt   string         `json:"createdAt"`
	StartedAt   *string        `json:"startedAt,omitempty"`
	UpdatedAt   string         `json:"updatedAt"`
	CompletedAt *string        `json:"completedAt"`
	PromptID    *string        `json:"promptId"`
	URL         *string        `json:"url"`
	File        *string        `json:"file"`
	MimeType    *string        `json:"mimeType"`
	Bytes       int64          `json:"bytes"`
	// Outputs is the full artifact list. File/URL/MimeType/Bytes above mirror the first
	// one for the legacy single-file endpoints and for records written before this field.
	Outputs []TaskOutput `json:"outputs,omitempty"`
	Error   *string      `json:"error"`
	// Structured failure, kept alongside the human-readable message so a caller can
	// react to a code instead of parsing text. Absent on success.
	ErrorCode    *string `json:"errorCode,omitempty"`
	ErrorDetails any     `json:"errorDetails,omitempty"`
	ErrorStatus  int     `json:"errorStatus,omitempty"`

	// Cancellation plumbing, never serialized.
	cancelled  bool
	cancelFunc context.CancelFunc
}

// CodedFailure is a failure that carries a stable, machine-readable code. The task
// store persists it without importing the package that produced the error.
type CodedFailure interface {
	error
	FailureCode() string
	FailureDetails() any
}

// Update is one progress tick from the runner.
type Update struct {
	Progress float64
	PromptID string
}

// Result is what a finished job produces: one file per output, in request order.
type Result struct {
	Outputs  []ResultFile
	PromptID string
}

// ResultFile is one artifact of a Result, before it is written to disk.
type ResultFile struct {
	Buffer   []byte
	Ext      string
	MimeType string
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
		for _, candidate := range candidateOutputPaths(s.outputDir, task.ID, task.Object) {
			if util.Exists(candidate) {
				value := candidate
				task.File = &value
				break
			}
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

// Create registers a new queued task. The object says what the run produces; it decides
// the fallback MIME type and where reconciliation looks for the output file.
//
// The returned record is the live one the queue will mutate: read it through Get when the value is
// about to be serialised, or the read races the worker.
func (s *Store) Create(object, model string, request map[string]any) *Task {
	now := util.NowIso()
	task := &Task{
		ID:        util.RandomID("task_"),
		Object:    normalizeObject(object),
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
	startedAt := task.UpdatedAt
	task.StartedAt = &startedAt
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
		var coded CodedFailure
		if errors.As(err, &coded) {
			code := coded.FailureCode()
			task.ErrorCode = &code
			task.ErrorDetails = coded.FailureDetails()
		}
		var statusCoded interface{ StatusCode() int }
		if errors.As(err, &statusCoded) {
			task.ErrorStatus = statusCoded.StatusCode()
		}
		completed := util.NowIso()
		task.CompletedAt = &completed
		s.logger.Warn("task %s failed: %s", task.ID, message)
	} else if len(result.Outputs) == 0 {
		message := "the run produced no output file"
		task.Status = "failed"
		task.Error = &message
		completed := util.NowIso()
		task.CompletedAt = &completed
		s.logger.Warn("task %s failed: %s", task.ID, message)
	} else {
		outputs, saveErr := s.saveOutputs(task.ID, task.Object, result.Outputs)
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
			task.Outputs = outputs
			first := outputs[0]
			task.URL = &first.URL
			task.File = &first.File
			task.MimeType = &first.MimeType
			task.Bytes = first.Bytes
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
	// A cancel is reflected immediately, whether the run is still queued or already running: the
	// caller asked for it, and the worker unwinds on its own. Waiting for the worker would make
	// DELETE report "running" for a run the caller just cancelled.
	wasActive := task.Status == "queued" || task.Status == "running"
	cancelFunc := task.cancelFunc
	if wasActive {
		task.Status = "cancelled"
		message := "task cancelled"
		task.Error = &message
		now := util.NowIso()
		if task.CompletedAt == nil {
			task.CompletedAt = &now
		}
		task.UpdatedAt = now
	}
	s.mu.Unlock()

	if cancelFunc != nil {
		cancelFunc()
	}
	if wasActive {
		s.persistNow()
	}
	return s.Get(id)
}

// ContentPath returns the on-disk path of a finished task's first output, or "" when it
// is missing or outside the output directory.
func (s *Store) ContentPath(id string) string {
	return s.OutputPath(id, 0)
}

// OutputPath returns the on-disk path of one output of a finished task, or "" when it is
// missing or outside the output directory.
func (s *Store) OutputPath(id string, index int) string {
	s.mu.RLock()
	stored, ok := s.tasks[id]
	s.mu.RUnlock()
	if !ok {
		return ""
	}
	files := stored.Outputs
	if len(files) == 0 {
		// A record written before multi-output tasks existed carries only the first file.
		if index != 0 || stored.File == nil || *stored.File == "" {
			return ""
		}
		return s.withinOutput(*stored.File)
	}
	if index < 0 || index >= len(files) {
		return ""
	}
	return s.withinOutput(files[index].File)
}

// withinOutput resolves a path and refuses anything outside this store's output directory.
func (s *Store) withinOutput(path string) string {
	resolved, err := filepath.Abs(path)
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

// saveOutputs writes every artifact of a run and returns their public projection.
//
// Names are `<id>.<ext>` for the first output and `<id>-2.<ext>` after that, so a batch is
// recognisable on disk and the first file keeps the historical name.
func (s *Store) saveOutputs(id, object string, files []ResultFile) ([]TaskOutput, error) {
	outputs := make([]TaskOutput, 0, len(files))
	for index, file := range files {
		stem := id
		if index > 0 {
			stem = id + "-" + strconv.Itoa(index+1)
		}
		filename := stem + "." + defaultExt(file.Ext)
		target := filepath.Join(s.outputDir, filename)
		if err := os.WriteFile(target, file.Buffer, 0o644); err != nil {
			return nil, err
		}
		mimeType := file.MimeType
		if mimeType == "" {
			mimeType = defaultMimeType(object)
		}
		outputs = append(outputs, TaskOutput{
			Index:    index,
			File:     target,
			URL:      s.publicURL(filename),
			MimeType: mimeType,
			Bytes:    int64(len(file.Buffer)),
		})
	}
	return outputs, nil
}

// SaveFile stores a one-off file and returns its public URL.
func (s *Store) SaveFile(id string, buffer []byte, ext string) (string, error) {
	url, _, err := s.saveSingle(id, buffer, ext)
	return url, err
}

// saveSingle writes one file outside any run (an image returned as a URL).
func (s *Store) saveSingle(id string, buffer []byte, ext string) (string, string, error) {
	filename := id + "." + defaultExt(ext)
	target := filepath.Join(s.outputDir, filename)
	if err := os.WriteFile(target, buffer, 0o644); err != nil {
		return "", "", err
	}
	return s.publicURL(filename), target, nil
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
		// Snapshot under the lock: the worker mutates the live record, and marshalling it after
		// unlocking raced every progress update.
		payload.Tasks = append(payload.Tasks, task.snapshot())
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
		"object":      normalizeObject(task.Object),
		"status":      task.Status,
		"progress":    task.Progress,
		"model":       task.Model,
		"request":     task.Request,
		"createdAt":   task.CreatedAt,
		"startedAt":   task.StartedAt,
		"updatedAt":   task.UpdatedAt,
		"completedAt": task.CompletedAt,
		"promptId":    task.PromptID,
		"url":         task.URL,
		"file":        task.File,
		"mimeType":    task.MimeType,
		"bytes":       task.Bytes,
		"outputs":     nonNilOutputs(task.Outputs),
		"error":       task.Error,
		"errorCode":   task.ErrorCode,
		"errorDetail": task.ErrorDetails,
		"errorStatus": task.ErrorStatus,
	}
}

// nonNilOutputs keeps the wire projection an array even for a record written before the
// field existed.
func nonNilOutputs(outputs []TaskOutput) []TaskOutput {
	if outputs == nil {
		return []TaskOutput{}
	}
	return outputs
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
