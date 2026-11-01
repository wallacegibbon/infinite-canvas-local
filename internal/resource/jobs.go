package resource

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/util"
)

// Install jobs speak the same vocabulary as the canvas resource panel. These strings
// are a wire contract, not cosmetics:
//
//	job.status  : running | done | error | canceled
//	item.status : queued | downloading | verifying | copying | extracting | baking |
//	              done | error | skipped
//	job.progress: 0..100, weighted by bytes
//
// The panel only renders byte counters and transfer speed for "downloading", styles
// "error" red with the item's error text, stops polling and refreshes the list on
// done/error/canceled, and passes progress straight to antd's percent prop — which
// expects 0..100, so a 0..1 fraction shows as an empty bar.
//
// JobItem is one resource inside an install job.
type JobItem struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Bytes   int64   `json:"bytes"`
	Size    int64   `json:"size"`
	Source  *string `json:"source,omitempty"`
	Error   string  `json:"error,omitempty"`
	Hint    string  `json:"hint,omitempty"`
	Skipped bool    `json:"skipped"`
}

// Job is one install run.
type Job struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	Message   string     `json:"message"`
	CreatedAt string     `json:"createdAt"`
	UpdatedAt string     `json:"updatedAt"`
	Items     []*JobItem `json:"items"`
	Completed int        `json:"completed"`
	Total     int        `json:"total"`
	Progress  float64    `json:"progress"`
	Error     string     `json:"error,omitempty"`

	cancel context.CancelFunc
}

// Install selects the requested resources and starts a job.
func (m *Manager) Install(ids, packs []string) (*Job, error) {
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()

	if index == nil {
		return nil, httpxBadRequest("index_unavailable", "resource index unavailable")
	}

	selected := map[string]bool{}
	var ordered []string
	var manual []string
	add := func(id string) {
		if id == "" || selected[id] {
			return
		}
		resource := findResource(index, id)
		if resource == nil {
			return
		}
		selected[id] = true
		// A bake resource is produced by a local merge, so it never becomes a job item.
		// Say so instead of starting a job that is guaranteed to fail.
		if resource.Type == "bake" {
			manual = append(manual, id)
			return
		}
		ordered = append(ordered, id)
	}

	for _, id := range ids {
		add(id)
	}
	// A pack is what its workflows load, plus the alternates it offers: the panel's "install this
	// pack" is the same rule as its per-workflow rows, so the two cannot drift apart.
	providers := newProviderIndex(index)
	for _, packID := range packs {
		for _, pack := range index.Packs {
			if pack.ID != packID {
				continue
			}
			for _, workflowID := range pack.Workflows {
				for _, id := range providers.entryDeps(m.resolvedWorkflow(workflowID)).Resources {
					add(id)
				}
			}
			for _, id := range pack.Alternates {
				add(id)
			}
		}
	}
	root := m.ComfyRoot()
	if len(ordered) == 0 {
		if len(manual) > 0 {
			commands := make([]string, 0, len(manual))
			for _, id := range manual {
				if resource := findResource(index, id); resource != nil {
					commands = append(commands, m.bakeCommand(resource, root))
				}
			}
			return nil, httpxBadRequest("manual_step_required",
				"these resources are produced by a local merge, not downloaded: "+strings.Join(commands, " && "))
		}
		return nil, httpxBadRequest("nothing_selected", "select at least one resource or pack to install")
	}

	// An install writes into ComfyUI's own model folders, so a gateway on another machine can
	// only fill the wrong disk — and silently, since the write itself may well succeed. Installs
	// stay off until the deployment states the folders are reachable.
	if policy := m.InstallPolicy(); !policy.Allowed {
		return nil, httpxBadRequest("model_install_disabled", installRefusedMessage(policy))
	}

	// Writing needs a directory, and only ComfyUI knows which one: its reported custom_nodes
	// folder. When it does not report one, say so instead of inventing a relative path.
	if root == "" {
		return nil, httpxBadRequest("install_root_unknown", m.installRootUnknownMessage())
	}

	items := make([]*JobItem, 0, len(ordered))
	for _, id := range ordered {
		resource := findResource(index, id)
		item := &JobItem{
			ID:     resource.ID,
			Label:  defaultString(resource.Label, resource.ID),
			Type:   defaultString(resource.Type, "model"),
			Status: "queued",
			Size:   resource.Size,
		}
		items = append(items, item)
	}

	job := &Job{
		ID:        util.RandomID("job_"),
		Status:    "running",
		Message:   "starting install",
		CreatedAt: util.NowIso(),
		UpdatedAt: util.NowIso(),
		Items:     items,
		Total:     len(items),
	}
	ctx, cancel := context.WithCancel(context.Background())
	job.cancel = cancel

	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	m.logger.Info("install started job=%s items=%d: %s", job.ID, len(items), strings.Join(ordered, ", "))

	go m.runJob(ctx, cancel, job, index, root)

	// Hand back a snapshot: the runner is already mutating the live job in another
	// goroutine, and the HTTP handler serialises whatever it gets.
	m.mu.Lock()
	defer m.mu.Unlock()
	return job.snapshot(), nil
}

// currentItems builds the status map used to decide what still needs installing.
func (m *Manager) currentItems() map[string]*Item {
	m.mu.Lock()
	index := m.index
	m.mu.Unlock()
	if index == nil {
		return map[string]*Item{}
	}
	root := m.ComfyRoot()
	byID := map[string]*Item{}
	for _, resource := range index.Resources {
		if resource.Type == "bake" {
			continue
		}
		byID[resource.ID] = m.itemStatus(resource, root, map[string]*Item{})
	}
	for _, resource := range index.Resources {
		if resource.Type != "bake" {
			continue
		}
		byID[resource.ID] = m.itemStatus(resource, root, byID)
	}
	return byID
}

// GetJob returns a job snapshot.
// snapshot copies a job and its items so a reader never observes a half-updated
// record. The install workers mutate these fields concurrently with the panel's
// polling requests.
func (j *Job) snapshot() *Job {
	if j == nil {
		return nil
	}
	copied := *j
	copied.Items = make([]*JobItem, 0, len(j.Items))
	for _, item := range j.Items {
		if item == nil {
			continue
		}
		itemCopy := *item
		copied.Items = append(copied.Items, &itemCopy)
	}
	copied.cancel = nil
	copied.Completed, copied.Progress = jobProgress(copied.Items)
	return &copied
}

// jobProgress reports how far an install has come, as the canvas panel wants it:
// completed item count plus a 0..100 percentage.
//
// Weighting by bytes matters more than it looks. A pack with one 6.2 GB model has a
// single item, so counting finished items alone leaves the bar pinned at 0% for the
// whole download and then jumps to 100%. Sizes are known for downloads, so the bar
// follows the bytes; node archives and runtimes whose size is unknown fall back to
// finished-item counting, and "skipped" items stay out of the byte total so an
// already-installed pack does not read as 0%.
func jobProgress(items []*JobItem) (int, float64) {
	finished := 0
	var knownBytes, doneBytes int64
	for _, item := range items {
		if item == nil {
			continue
		}
		switch item.Status {
		case "done", "skipped", "error":
			finished++
		}
		if item.Size <= 0 || item.Status == "skipped" {
			continue
		}
		knownBytes += item.Size
		partial := item.Bytes
		if partial < 0 {
			partial = 0
		}
		if partial > item.Size {
			partial = item.Size
		}
		doneBytes += partial
	}
	if knownBytes > 0 {
		return finished, math.Min(100, math.Round(float64(doneBytes)/float64(knownBytes)*100))
	}
	if len(items) == 0 {
		return finished, 100
	}
	return finished, math.Round(float64(finished) / float64(len(items)) * 100)
}

func (m *Manager) GetJob(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id].snapshot()
}

// CancelJob stops a running job and returns a snapshot of it.
//
// The snapshot is not optional: handing back the live job would let the install
// workers keep mutating it while the HTTP handler serialises it.
func (m *Manager) CancelJob(id string) *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return nil
	}
	if job.Status == "running" {
		if job.cancel != nil {
			job.cancel()
		}
		job.Status = "canceled"
		job.Message = "cancelled"
		job.UpdatedAt = util.NowIso()
		// Outstanding items become errors carrying the reason, which is what the panel
		// draws in red; leaving them "downloading" would show a frozen byte counter.
		for _, item := range job.Items {
			if item == nil {
				continue
			}
			switch item.Status {
			case "queued", "downloading", "verifying", "copying", "extracting", "baking":
				item.Status = "error"
				item.Error = "cancelled"
				item.Hint = ""
			}
		}
	}
	return job.snapshot()
}

// runJob installs every item, at most ResourceConcurrency at a time.
func (m *Manager) runJob(ctx context.Context, cancel context.CancelFunc, job *Job, index *Index, root string) {
	started := time.Now()
	existing := m.currentItems()
	limit := m.cfg.ResourceConcurrency
	if limit <= 0 {
		limit = 2
	}
	semaphore := make(chan struct{}, limit)

	var waitGroup sync.WaitGroup
	for _, item := range job.Items {
		resource := findResource(index, item.ID)
		if resource == nil {
			continue
		}
		if current, ok := existing[item.ID]; ok && current.Status == "present" {
			m.mu.Lock()
			item.Status = "skipped"
			item.Skipped = true
			item.Bytes = current.ActualSize
			m.mu.Unlock()
			continue
		}

		waitGroup.Add(1)
		go func(item *JobItem, resource *Resource) {
			defer waitGroup.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			if ctx.Err() != nil {
				m.markItemCanceled(item)
				return
			}

			m.mu.Lock()
			job.Message = "installing " + item.Label
			job.UpdatedAt = util.NowIso()
			m.mu.Unlock()

			itemStarted := time.Now()
			err := m.installOne(ctx, resource, item, root)
			elapsed := elapsedText(time.Since(itemStarted))

			m.mu.Lock()
			if err != nil {
				item.Status = "error"
				item.Error = err.Error()
				item.Hint = installHint(err)
				// A manual step carries a ready-to-paste command, which is more useful
				// than the generic hint.
				var manual *manualStepError
				if errors.As(err, &manual) {
					item.Hint = manual.command
				}
			} else {
				item.Status = "done"
			}
			job.UpdatedAt = util.NowIso()
			label := item.Label
			installedBytes := item.Bytes
			m.mu.Unlock()

			if err != nil {
				m.logger.Warn("%s install failed after %s: %s", label, elapsed, err)
				if isFatal(err) && cancel != nil {
					// A fatal cause (full disk, no permission) will not improve for the
					// remaining items, so stop them rather than pile up more failures.
					m.logger.Warn("fatal error, stopping the remaining install items")
					cancel()
				}
				return
			}
			m.recordInstalled(resource.ID, installedBytes)
			m.logger.Info("%s installed %s in %s", label, humanBytes(installedBytes), elapsed)
		}(item, resource)
	}
	waitGroup.Wait()

	m.mu.Lock()
	done, skipped, failed := 0, 0, 0
	for _, item := range job.Items {
		switch item.Status {
		case "done":
			done++
		case "skipped":
			skipped++
		case "error":
			failed++
		}
	}
	switch {
	case failed > 0:
		job.Status = "error"
		job.Message = fmt.Sprintf("finished with %d failed item(s)", failed)
	case ctx.Err() != nil:
		job.Status = "canceled"
		job.Message = "cancelled"
	default:
		job.Status = "done"
		job.Message = "install complete"
	}
	job.UpdatedAt = util.NowIso()
	job.cancel = nil
	finalStatus := job.Status
	m.mu.Unlock()

	m.saveLock()
	m.logger.Info("install finished job=%s status=%s done %d / skipped %d / failed %d in %s",
		job.ID, finalStatus, done, skipped, failed, elapsedText(time.Since(started)))

	// ComfyUI only lists new model files after a rescan, and the workflow watcher does
	// not watch model directories. Without this the panel keeps reporting the workflow
	// as unavailable until the user triggers a scan by hand.
	if done > 0 && m.setup != nil {
		go func() {
			if _, err := m.setup.Scan(); err != nil {
				m.logger.Warn("rescan after install failed: %s", err)
			}
		}()
	}
}

// installOne dispatches on resource type.
func (m *Manager) installOne(ctx context.Context, resource *Resource, item *JobItem, root string) error {
	dir, file := targetParts(resource.Target)
	if dir == "" || file == "" {
		return fatal(errors.New("resource target is invalid"))
	}

	switch resource.Type {
	case "bake":
		// Merging two checkpoints needs torch, which this binary does not embed, and the
		// merged file is ~10 GB. Report the exact command instead of a vague instruction.
		m.setItemStatus(item, "baking")
		return m.bakeRequiredError(resource, root)
	case "node":
		return m.installNodeLike(ctx, resource, item, root, file)
	}

	target := m.targetPath(resource, root)
	m.logger.Info("%s install target: %s", item.Label, m.displayPath(target, root))

	// A file staged under payload/<target> ships with the gateway: it is copied
	// in-process, nothing is downloaded and no command runs. This is a convention, not a
	// field, so "bundled" can only ever mean one thing.
	if payload := m.payloadPath(resource.Target); util.Exists(payload) {
		m.setItemStatus(item, "copying")
		written, err := copyFile(ctx, payload, target)
		if err != nil {
			return err
		}
		m.setItemBytes(item, written, "bundled file")
		m.logger.Info("%s copied the bundled file %s (in-process copy, no external command)", item.Label, humanBytes(written))
		return nil
	}

	failures := &sourceErrors{}
	for _, reference := range sourceURLs(resource) {
		err := m.downloadFile(ctx, reference, target, resource.Sha256, item)
		if err == nil {
			return nil
		}
		// A fatal cause cannot be fixed by another mirror, and a cancelled job must not
		// keep pulling bytes: both stop here and surface the real reason.
		if isFatal(err) || ctx.Err() != nil {
			return err
		}
		failures.add(hostOf(reference), err)
	}
	return failures.err("not shipped with this release (payload/" + resource.Target + ") and no download URL works")
}

// installNodeLike installs a custom-node directory or an extracted archive.
func (m *Manager) installNodeLike(ctx context.Context, resource *Resource, item *JobItem, root, file string) error {
	if root == "" {
		return fatal(errors.New("ComfyUI install directory not found"))
	}
	customNodes := filepath.Join(root, "custom_nodes")
	destination := filepath.Join(customNodes, file)
	m.logger.Info("%s install target: %s", item.Label, m.displayPath(destination, root))

	// Same convention as models: a node directory staged under payload/<target>
	// ships with the gateway and is copied as-is.
	if payload := m.payloadPath(resource.Target); util.DirExists(payload) {
		if util.DirExists(destination) {
			m.logger.Info("%s directory already exists, skipping", item.Label)
			return nil
		}
		m.setItemStatus(item, "copying")
		written, err := copyDir(ctx, payload, destination)
		if err != nil {
			return err
		}
		m.setItemBytes(item, written, "bundled directory")
		m.logger.Info("%s copied the bundled directory %s (in-process copy, no external command)", item.Label, humanBytes(written))
		return nil
	}

	if util.DirExists(destination) {
		m.logger.Info("%s directory already exists, skipping", item.Label)
		return nil
	}

	failures := &sourceErrors{}
	for _, reference := range sourceURLs(resource) {
		if err := os.MkdirAll(customNodes, 0o755); err != nil {
			return targetError(err)
		}
		archive := filepath.Join(os.TempDir(), util.RandomID("node-")+filepath.Ext(reference))
		if err := m.downloadFile(ctx, reference, archive, "", item); err != nil {
			if isFatal(err) || ctx.Err() != nil {
				return err
			}
			failures.add(hostOf(reference), err)
			continue
		}
		// Extraction uses archive/zip and archive/tar in-process. No unzip or tar binary
		// is involved, so a missing tool can never fail an install.
		m.setItemStatus(item, "extracting")
		m.logger.Info("%s extracting %s -> %s (in-process, no external command)",
			item.Label, filepath.Base(archive), m.displayPath(destination, root))
		extractErr := extractNodeArchive(ctx, archive, destination)
		_ = os.Remove(archive)
		if extractErr != nil {
			return fmt.Errorf("extraction failed: %w", extractErr)
		}
		return nil
	}
	return failures.err("not shipped with this release (payload/" + resource.Target + ") and no download URL works")
}

// targetPath picks the install destination for a resource.
//
// ComfyUI's real, writable model directory wins, because that is the only place the
// loaders will look. If none is writable, fall back to <ComfyUI>/models/<type>.
func (m *Manager) targetPath(resource *Resource, root string) string {
	dir, file := targetParts(resource.Target)
	if dir == "custom_nodes" {
		return filepath.Join(root, "custom_nodes", file)
	}
	for _, candidate := range m.modelDirs(dir, root) {
		if util.DirExists(candidate) && canWrite(candidate) {
			return filepath.Join(candidate, file)
		}
	}
	return filepath.Join(root, "models", dir, file)
}

// canWrite probes writability without leaving anything behind.
func canWrite(dir string) bool {
	probe := filepath.Join(dir, ".gateway-write-probe")
	file, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return false
	}
	_ = file.Close()
	_ = os.Remove(probe)
	return true
}

// downloadFile streams a URL to disk, verifying the checksum before publishing it.
//
// Bytes land in <target>.part and are only renamed into place once the checksum
// matches, so a half-written model is never visible to ComfyUI.
func (m *Manager) downloadFile(ctx context.Context, rawURL, target, expectedSha string, item *JobItem) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return targetError(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}

	label := "resource"
	if item != nil {
		label = item.Label
	}
	host := hostOf(rawURL)
	m.setItemStatus(item, "downloading")
	m.logger.Info("%s download started <- %s", label, host)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		m.logger.Warn("%s download failed <- %s: %s", label, host, err)
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := fmt.Errorf("HTTP %d", response.StatusCode)
		m.logger.Warn("%s download failed <- %s: %v", label, host, failure)
		return failure
	}

	if item != nil && response.ContentLength > 0 {
		m.mu.Lock()
		item.Size = response.ContentLength
		m.mu.Unlock()
	}

	partial := target + ".part"
	file, err := os.Create(partial)
	if err != nil {
		return targetError(err)
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(partial)
	}()

	hasher := sha256.New()
	writer := io.MultiWriter(file, hasher)
	buffer := make([]byte, 1<<20)
	printer := newProgressPrinter(label, response.ContentLength)

	var written int64
	for {
		if ctx.Err() != nil {
			m.logger.Info("%s download cancelled, partial file removed", label)
			return ctx.Err()
		}
		read, readErr := response.Body.Read(buffer)
		if read > 0 {
			if _, writeErr := writer.Write(buffer[:read]); writeErr != nil {
				return targetError(writeErr)
			}
			written += int64(read)
			m.setItemBytes(item, written, "")
			printer.update(m.logger, written)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			m.logger.Warn("%s download interrupted <- %s: %s", label, host, readErr)
			return readErr
		}
	}

	if expectedSha != "" {
		m.setItemStatus(item, "verifying")
	}
	checksum := "not verified (no sha256 declared)"
	if expectedSha != "" {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(actual, expectedSha) {
			m.logger.Warn("%s checksum mismatch <- %s: expected %s, got %s", label, host, shortSha(expectedSha), shortSha(actual))
			return fmt.Errorf("sha256 mismatch (expected %s..., got %s...)", shortSha(expectedSha), shortSha(actual))
		}
		checksum = "sha256 verified"
	}

	if err := file.Close(); err != nil {
		return targetError(err)
	}
	if err := os.Rename(partial, target); err != nil {
		return targetError(err)
	}
	m.setItemBytes(item, written, host)
	m.logger.Info("%s downloaded %s <- %s, %s", label, humanBytes(written), host, checksum)
	return nil
}

// copyFile copies a bundled file into place and returns the byte count.
func copyFile(ctx context.Context, source, target string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return 0, targetError(err)
	}
	in, err := os.Open(source)
	if err != nil {
		return 0, bundledError(err)
	}
	defer in.Close()
	out, err := os.Create(target)
	if err != nil {
		return 0, targetError(err)
	}
	defer out.Close()
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	written, copyErr := io.Copy(out, in)
	if copyErr != nil {
		return 0, targetError(copyErr)
	}
	return written, nil
}

// copyDir recursively copies a bundled node directory and returns the total bytes.
func copyDir(ctx context.Context, source, target string) (int64, error) {
	var total int64
	err := filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return bundledError(err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return bundledError(err)
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			return targetError(os.MkdirAll(destination, 0o755))
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return targetError(err)
		}
		// Copy in a closure so the descriptors close per file; a defer inside a Walk
		// callback would hold one descriptor per entry until the whole tree finished.
		written, copyErr := func() (int64, error) {
			in, err := os.Open(path)
			if err != nil {
				return 0, bundledError(err)
			}
			defer in.Close()
			out, err := os.Create(destination)
			if err != nil {
				return 0, targetError(err)
			}
			defer out.Close()
			written, copyErr := io.Copy(out, in)
			if copyErr != nil {
				return 0, targetError(copyErr)
			}
			return written, nil
		}()
		if copyErr != nil {
			return copyErr
		}
		total += written
		return nil
	})
	return total, err
}

// extractNodeArchive unpacks a node pack into destination, dropping the single top-level
// directory every GitHub archive wraps its contents in.
//
// ComfyUI does not care what a pack's directory is called; the catalogue does. Install state is read
// from custom_nodes/<target name>, so a pack that landed in custom_nodes/<repo>-<sha> would look
// uninstalled for ever and every click on Install would download it again. This is why the wrapper
// is removed here rather than at the call site.
func extractNodeArchive(ctx context.Context, archive, destination string) error {
	staging, err := os.MkdirTemp("", "node-extract-")
	if err != nil {
		return targetError(err)
	}
	defer os.RemoveAll(staging)

	if err := extractArchive(archive, staging); err != nil {
		return err
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		return targetError(err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("archive is empty: %s", filepath.Base(archive))
	}
	source := staging
	if len(entries) == 1 && entries[0].IsDir() {
		source = filepath.Join(staging, entries[0].Name())
	}
	if util.DirExists(destination) {
		return nil
	}
	if _, err := copyDir(ctx, source, destination); err != nil {
		_ = os.RemoveAll(destination)
		return err
	}
	return nil
}

// extractArchive unpacks a .zip or .tar.gz into a directory.
func extractArchive(archive, destination string) error {
	lowered := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lowered, ".zip"):
		return extractZip(archive, destination)
	case strings.HasSuffix(lowered, ".tar.gz"), strings.HasSuffix(lowered, ".tgz"):
		return extractTarGz(archive, destination)
	}
	return fmt.Errorf("unsupported archive format: %s", filepath.Base(archive))
}

// extractZip unpacks a zip archive, rejecting path traversal.
func extractZip(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		target, err := safeJoin(destination, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		source, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			_ = source.Close()
			return err
		}
		if _, err := io.Copy(out, source); err != nil {
			_ = source.Close()
			_ = out.Close()
			return err
		}
		_ = source.Close()
		_ = out.Close()
	}
	return nil
}

// extractTarGz unpacks a gzipped tarball.
func extractTarGz(archive, destination string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()

	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()

	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, reader); err != nil {
				_ = out.Close()
				return err
			}
			_ = out.Close()
		}
	}
}

// safeJoin prevents an archive entry from escaping the destination directory.
func safeJoin(base, name string) (string, error) {
	cleaned := filepath.Clean("/" + util.NormalizeSlashes(name))
	target := filepath.Join(base, cleaned)
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry escapes the destination: %s", name)
	}
	return target, nil
}

// recordInstalled notes a successful install in data/resource-lock.json.
func (m *Manager) recordInstalled(id string, bytes int64) {
	m.mu.Lock()
	installed, ok := m.lock["installed"].(map[string]any)
	if !ok {
		installed = map[string]any{}
		m.lock["installed"] = installed
	}
	installed[id] = map[string]any{"installedAt": util.NowIso(), "bytes": bytes}
	m.lock["updatedAt"] = util.NowIso()
	m.mu.Unlock()
}

// installHint turns a failure into something the user can act on.
func installHint(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "HTTP 403"), strings.Contains(message, "HTTP 401"):
		return "download rejected: the URL may require authentication; try another source"
	case strings.Contains(message, "HTTP 404"):
		return "dead download URL: update this resource's sources in resources.json"
	case strings.Contains(message, "sha256"):
		return "checksum failed: the download may be truncated, retry"
	case strings.Contains(message, "no space"):
		return "not enough disk space"
	case strings.Contains(message, "quota"):
		return "the disk quota for this volume is exhausted"
	case strings.Contains(message, "cannot write into ComfyUI's folders"):
		// The wrong-machine case is refused before a byte moves (INSTALL_INTO_COMFY), so
		// this is a folder that is missing, read-only, or owned by another user — including
		// a mount that went away, which is why "disk full" was the wrong thing to say.
		return "check that this folder exists, is mounted writable, and belongs to this user (docs/gateway.md)"
	case strings.Contains(message, "missing a bundled file"):
		return "reinstall the gateway: this release is incomplete"
	}
	return ""
}
