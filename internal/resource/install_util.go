package resource

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
)

// fatalInstallError marks a failure that no other mirror can fix, so the caller must
// stop instead of walking the rest of the source list.
//
// A full disk stays full and an unwritable directory stays unwritable; retrying them
// against a second mirror only delays the error and can leave a half-installed tree.
type fatalInstallError struct{ err error }

func (e *fatalInstallError) Error() string { return e.err.Error() }
func (e *fatalInstallError) Unwrap() error { return e.err }

// fatal wraps a failure that should abort the whole job.
func fatal(err error) error { return &fatalInstallError{err: err} }

// isFatal reports whether err must stop the job rather than try another source.
func isFatal(err error) bool {
	var target *fatalInstallError
	return errors.As(err, &target)
}

// targetError reports a failure to write into the folders the install targets.
//
// It is fatal, and that is decided by *which side* failed rather than by inspecting errno:
// the same disk fails the same way for the next item, so a run of identical errors helps
// nobody. "No such file or directory" counts here — a target folder that vanished is a
// deployment problem, and retrying cannot bring it back.
func targetError(err error) error {
	if err == nil {
		return nil
	}
	return fatal(fmt.Errorf("cannot write into ComfyUI's folders: %w", err))
}

// bundledError reports a failure to read something that ships with the gateway (a bundled
// model file or node directory).
//
// It is deliberately not fatal: a release missing one bundled asset must not cancel the
// rest of the install. Same errno as targetError, opposite conclusion — which is why the
// decision lives at the call site instead of in an errno table.
func bundledError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("this release is missing a bundled file: %w", err)
}

// sourceErrors collects one failure per mirror so the user sees the real reason
// instead of a generic "no usable source".
type sourceErrors struct {
	mu    sync.Mutex
	lines []string
	first error
}

// add records a mirror failure, keeping at most three for readability.
func (s *sourceErrors) add(host string, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first == nil {
		s.first = err
	}
	if len(s.lines) >= 3 {
		return
	}
	message := err.Error()
	if host != "" {
		message = host + ": " + message
	}
	s.lines = append(s.lines, message)
}

// err renders the aggregate.
//
// Fatal write failures never reach here: installOne returns them immediately, so every
// error collected below came from a mirror that may be replaced by another one.
func (s *sourceErrors) err(fallback string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lines) == 0 {
		return errors.New(fallback)
	}
	return errors.New(strings.Join(s.lines, "；"))
}

// progressPrinter throttles download progress lines.
//
// downloadFile updates counters every megabyte; logging at that rate would bury the
// rest of the log for a 20 GB file. One line per 10% step (or per 32 MB when the total
// size is unknown) keeps it readable and still proves the transfer is alive.
type progressPrinter struct {
	label     string
	total     int64
	lastBytes int64
	lastShown time.Time
	lastPct   int
}

// newProgressPrinter builds a printer for one download.
func newProgressPrinter(label string, total int64) *progressPrinter {
	return &progressPrinter{label: label, total: total, lastShown: time.Now()}
}

// update emits a progress line when a threshold is crossed.
func (p *progressPrinter) update(logger *logx.Logger, written int64) {
	if logger == nil {
		return
	}
	percent := -1
	if p.total > 0 {
		percent = int(written * 100 / p.total)
	}
	stepDue := percent >= 0 && percent >= p.lastPct+10
	sizeDue := written-p.lastBytes >= 32<<20
	if !stepDue && !sizeDue {
		return
	}

	speed := ""
	if elapsed := time.Since(p.lastShown).Seconds(); elapsed > 0.3 {
		speed = " " + humanBytes(int64(float64(written-p.lastBytes)/elapsed)) + "/s"
	}
	if percent >= 0 {
		logger.Info("%s download %d%% %s/%s%s", p.label, percent, humanBytes(written), humanBytes(p.total), speed)
		p.lastPct = percent
	} else {
		logger.Info("%s download %s%s", p.label, humanBytes(written), speed)
	}
	p.lastBytes = written
	p.lastShown = time.Now()
}

// setItemBytes records live progress on the job item the panel polls.
// setItemBytes records how many bytes of an item have landed, and optionally which
// source they came from. Polling reads this to draw the per-item byte counter.
func (m *Manager) setItemBytes(item *JobItem, written int64, source string) {
	if item == nil {
		return
	}
	m.mu.Lock()
	item.Bytes = written
	if source != "" {
		value := source
		item.Source = &value
	}
	m.mu.Unlock()
}

// setItemStatus moves an item to a later phase so the panel can say what it is doing.
func (m *Manager) setItemStatus(item *JobItem, status string) {
	if item == nil {
		return
	}
	m.mu.Lock()
	item.Status = status
	m.mu.Unlock()
}

// markItemCanceled records an item the user stopped before it could run. A cancelled
// item is an "error" carrying the reason, which is what the panel draws in red.
func (m *Manager) markItemCanceled(item *JobItem) {
	if item == nil {
		return
	}
	m.mu.Lock()
	item.Status = "error"
	item.Error = "cancelled"
	m.mu.Unlock()
}

// displayPath shortens a path relative to the ComfyUI root for log readability.
func (m *Manager) displayPath(target, root string) string {
	if root == "" {
		return target
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || strings.HasPrefix(relative, "..") {
		return target
	}
	return relative
}

// humanBytes renders a byte count for log lines.
func humanBytes(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	for _, unit := range []string{"KB", "MB", "GB", "TB"} {
		value /= 1024
		if value < 1024 || unit == "TB" {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}

// elapsedText renders a duration compactly.
func elapsedText(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(time.Second).String()
}

// hostOf extracts the host from a URL for log lines.
func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	return parsed.Host
}

// shortSha trims a digest for log lines.
func shortSha(value string) string {
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}
