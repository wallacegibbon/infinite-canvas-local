package resource

import (
	"bytes"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"comfyui-gateway/internal/logx"
)

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:             "0 B",
		999:           "999 B",
		1024:          "1.0 KB",
		6_800_000_000: "6.3 GB",
	}
	for input, want := range cases {
		if got := humanBytes(input); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestElapsedText(t *testing.T) {
	if got := elapsedText(450 * time.Millisecond); got != "450ms" {
		t.Errorf("elapsedText = %q, want 450ms", got)
	}
	if got := elapsedText(92 * time.Second); got != "1m32s" {
		t.Errorf("elapsedText = %q, want 1m32s", got)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://www.modelscope.cn/models/x/resolve/master/a.safetensors": "www.modelscope.cn",
		"https://hf-mirror.com/a": "hf-mirror.com",
		"http://127.0.0.1:8099/x": "127.0.0.1:8099",
		"not a url":               "not a url",
	}
	for input, want := range cases {
		if got := hostOf(input); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestFatalWriteErrors documents which failures abort the job instead of walking the
// rest of the mirror list.
func TestFatalWriteErrors(t *testing.T) {
	fatalCases := []error{syscall.ENOSPC, syscall.EACCES, syscall.EROFS, syscall.EDQUOT}
	for _, cause := range fatalCases {
		if !isFatalWriteError(cause) {
			t.Errorf("%v should be treated as fatal", cause)
		}
		wrapped := &fatalInstallError{err: cause}
		if !isFatal(wrapped) {
			t.Errorf("isFatal did not recognize the wrapped %v", cause)
		}
	}

	// Network and checksum failures are worth retrying against another mirror.
	for _, cause := range []error{errors.New("HTTP 403"), errors.New("sha256 校验失败"), syscall.ECONNRESET} {
		if isFatalWriteError(cause) {
			t.Errorf("%v should not be fatal", cause)
		}
	}
	if isFatal(errors.New("plain")) {
		t.Error("a plain error must not be fatal")
	}
}

// TestSourceErrorsKeepsEveryReason proves the panel gets the real cause instead of a
// generic "no usable source".
func TestSourceErrorsKeepsEveryReason(t *testing.T) {
	failures := &sourceErrors{}
	failures.add("modelscope.cn", errors.New("HTTP 403"))
	// Non-ASCII causes have to survive aggregation byte for byte: mirrors answer in their own
	// language and the panel shows what they said.
	failures.add("hf-mirror.com", errors.New("连接超时"))

	aggregated := failures.err("no usable download source for this resource")
	if aggregated.Error() == "no usable download source for this resource" {
		t.Fatal("the generic fallback replaced the real reasons")
	}
	for _, want := range []string{"modelscope.cn", "HTTP 403", "hf-mirror.com", "连接超时"} {
		if !strings.Contains(aggregated.Error(), want) {
			t.Errorf("aggregate %q is missing %q", aggregated.Error(), want)
		}
	}
}

func TestSourceErrorsFallsBackWhenEmpty(t *testing.T) {
	failures := &sourceErrors{}
	if got := failures.err("no usable source").Error(); got != "no usable source" {
		t.Errorf("fallback = %q", got)
	}
}

func TestSourceErrorsEscalatesDiskFailure(t *testing.T) {
	failures := &sourceErrors{}
	failures.add("modelscope.cn", syscall.ENOSPC)

	aggregated := failures.err("no usable source")
	if !isFatal(aggregated) {
		t.Error("a disk-full failure must escalate to fatal so the job stops")
	}
}

func TestSourceErrorsCapsListLength(t *testing.T) {
	failures := &sourceErrors{}
	for i := 0; i < 6; i++ {
		failures.add("mirror", errors.New("HTTP 500"))
	}
	if got := strings.Count(failures.err("x").Error(), "mirror:"); got != 3 {
		t.Errorf("kept %d reasons, want the first 3", got)
	}
}

// TestProgressPrinterThrottles proves a 20 GB download does not flood the log: one
// line per 10% step instead of one per megabyte.
func TestProgressPrinterThrottles(t *testing.T) {
	var buffer bytes.Buffer
	logger := logx.NewWithWriter("test", &buffer)
	const total = int64(100 << 20) // 100 MB
	printer := newProgressPrinter("sample", total)

	// Walk the whole file 1 MB at a time, like downloadFile does.
	for written := int64(1 << 20); written <= total; written += 1 << 20 {
		printer.update(logger, written)
	}

	lines := strings.Count(strings.TrimSpace(buffer.String()), "\n") + 1
	if buffer.Len() == 0 {
		t.Fatal("no progress was logged at all")
	}
	if lines > 12 {
		t.Errorf("emitted %d progress lines for 100 updates, want it throttled to about 10", lines)
	}
	if !strings.Contains(buffer.String(), "download 100%") {
		t.Errorf("the download never reported completion:\n%s", buffer.String())
	}
	if !strings.Contains(buffer.String(), "MB/") {
		t.Errorf("progress lines should carry a total size and a speed:\n%s", buffer.String())
	}
}

// TestProgressPrinterHandlesUnknownSize covers a server that omits Content-Length.
func TestProgressPrinterHandlesUnknownSize(t *testing.T) {
	var buffer bytes.Buffer
	logger := logx.NewWithWriter("test", &buffer)
	printer := newProgressPrinter("sample", -1)

	// Below the 32 MB fallback threshold nothing is emitted.
	printer.update(logger, 8<<20)
	if buffer.Len() != 0 {
		t.Errorf("logged too early: %s", buffer.String())
	}
	printer.update(logger, 40<<20)
	if !strings.Contains(buffer.String(), "40.0 MB") {
		t.Errorf("expected a size-only progress line, got: %s", buffer.String())
	}
}

func TestDisplayPath(t *testing.T) {
	manager := &Manager{}
	root := "/home/user/comfy/ComfyUI"
	if got := manager.displayPath(root+"/models/unet/a.safetensors", root); got != "models/unet/a.safetensors" {
		t.Errorf("displayPath = %q", got)
	}
	// Outside the root it stays absolute rather than becoming "../..".
	if got := manager.displayPath("/tmp/a.bin", root); got != "/tmp/a.bin" {
		t.Errorf("displayPath = %q", got)
	}
	if got := manager.displayPath("/tmp/a.bin", ""); got != "/tmp/a.bin" {
		t.Errorf("displayPath with no root = %q", got)
	}
}

func TestShortSha(t *testing.T) {
	if got := shortSha("3611a5ccae5f790405eeacf1dfb2c892"); got != "3611a5ccae5f" {
		t.Errorf("shortSha = %q", got)
	}
	if got := shortSha("abc"); got != "abc" {
		t.Errorf("shortSha on a short digest = %q", got)
	}
}
