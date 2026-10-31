package agent

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
)

// Process is one running `alayacore --rawio`, owned by one request.
//
// Nothing keeps it alive beyond that request: run.go releases it when the answer ends, and a
// cancelled request sends `CI cancel` so the agent winds its own task down. The gateway does not
// layer a timeout of its own on top — the process is what knows what a half-finished step means.
type Process struct {
	Cmd    *exec.Cmd
	Stdin  *bufio.Writer
	Stdout *bufio.Reader
	// Stderr keeps the child's last lines. A core that dies at startup says why only there: a
	// refused session file, an unknown model, a bad config. Losing that turns "your model name is
	// wrong" into "connection closed".
	Stderr    *stderrTail
	Log       *logx.Logger
	startedAt time.Time

	stdinFile *os.File

	waitOnce sync.Once
	waitErr  error
}

// SpawnOptions is what one request may influence. Everything else is the operator's, fixed in the
// gateway's configuration: the config directory, the tool list, the skill container.
type SpawnOptions struct {
	Binary     string
	ConfigPath string
	// Skills is the container of skill directories passed as `--skill`. The agent reads them itself;
	// the gateway only says where they are.
	Skills string
	// Tools is the `--builtin-tools` list. Read-only file access is what a skill needs in order to be
	// activated at all (the agent loads a skill by reading its SKILL.md); no other tool is enabled.
	Tools string
	// Log records what happened in terms someone who knows AlayaCore can follow: the command line,
	// every frame in both directions, and how the process ended.
	Log *logx.Logger
}

// args builds the command line. Built-in tools are always disabled: this route asks an agent for
// words, and a request must not be able to hand itself a shell. The model is not a flag: it is set
// with `CI model_set` once the session is ready, which is where the agent reports the outcome.
func (o SpawnOptions) args() []string {
	args := []string{"--rawio", "--builtin-tools=" + o.Tools}
	if o.ConfigPath != "" {
		args = append(args, "--config-path", o.ConfigPath)
	}
	if o.Skills != "" {
		args = append(args, "--skill", o.Skills)
	}
	return args
}

// findBinary locates alayacore: the configured path, then PATH, then next to this executable — the
// same order the rest of the ecosystem uses, so a bundled copy wins over a stale one on PATH.
func findBinary(configured string) string {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured
	}
	if path, err := exec.LookPath("alayacore"); err == nil {
		return path
	}
	if exe, err := os.Executable(); err == nil {
		beside := filepath.Join(filepath.Dir(exe), "alayacore")
		if info, err := os.Stat(beside); err == nil && info.Size() > 0 {
			return beside
		}
	}
	return "alayacore"
}

// Spawn starts the agent. The caller must Release it.
func Spawn(o SpawnOptions) (*Process, error) {
	cmd := exec.Command(findBinary(o.Binary), o.args()...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// stderr is an explicit pipe rather than cmd.StderrPipe(), because Wait closes a pipe it owns
	// itself and the tail is still being read on the release path. With our own descriptors, Wait
	// cannot touch the read end.
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = stderrWrite

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderrRead.Close()
		_ = stderrWrite.Close()
		return nil, err
	}
	// The parent's copy of the write end is not ours to keep: holding it open would mean the pipe
	// never reaches EOF after the child exits.
	_ = stderrWrite.Close()

	tail := &stderrTail{}
	go tail.drain(stderrRead)

	if o.Log != nil {
		o.Log.Info("agent: spawn pid=%d binary=%s config=%s skills=%s tools=%q", cmd.Process.Pid, cmd.Path, o.ConfigPath, o.Skills, o.Tools)
	}

	in := bufio.NewWriter(stdin)
	return &Process{
		Cmd:       cmd,
		Stdin:     in,
		Stdout:    bufio.NewReaderSize(stdout, 64<<10),
		Stderr:    tail,
		stdinFile: stdin.(*os.File),
		Log:       o.Log,
		startedAt: time.Now(),
	}, nil
}

// Write sends one frame and flushes it. A frame that was never flushed reads as the agent going
// quiet, so every write here is followed by one.
func (p *Process) Write(tag, value string) error {
	p.logFrame(">>", tag, value)
	if err := WriteFrame(p.Stdin, tag, value); err != nil {
		return err
	}
	return p.Stdin.Flush()
}

// Read returns the next frame, logging it. Everything the agent says passes through here, so the log
// is the transcript: a reader who knows AlayaCore can see the frames in order.
func (p *Process) Read() (*Frame, error) {
	frame, err := ReadFrame(p.Stdout)
	if frame != nil {
		p.logFrame("<<", frame.Tag, frame.Value)
	}
	return frame, err
}

func (p *Process) logFrame(direction, tag, value string) {
	if p.Log == nil {
		return
	}
	p.Log.Info("agent %s %s %s", direction, tag, truncateFrame(redactKeys(value)))
}

// reAPIKey matches the value of an api_key field in a frame body. `SM model_list` carries every key
// from model.conf in cleartext; the transcript is worth having, the keys in it are not.
var reAPIKey = regexp.MustCompile(`("api_key":\s*)"(?:[^"\\]|\\.)*"`)

func redactKeys(value string) string {
	return reAPIKey.ReplaceAllString(value, `$1"***"`)
}

// truncateFrame keeps a frame's head. A rewrite of an H3 prompt is thousands of characters and is not
// more informative for it; the shape of the beginning is what tells a reader what the frame is.
func truncateFrame(value string) string {
	const limit = 400
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + fmt.Sprintf("… (%d runes)", len(runes))
}

// Wait reaps the child once and returns its exit error. Callers that may race (the reader's end of
// stream and Release) get the same answer from the sync.Once.
func (p *Process) Wait() error {
	p.waitOnce.Do(func() { p.waitErr = p.Cmd.Wait() })
	return p.waitErr
}

// Release ends the process: close stdin so it sees end of input, then wait. Closing is the
// cooperative half; the agent drains the task it is on and exits. If it does not exit on its own
// the OS still reaps it — Wait blocks until it does, which is why the caller does this in the
// background rather than on the response path.
func (p *Process) Release() {
	if p.stdinFile != nil {
		_ = p.stdinFile.Close()
	}
	err := p.Wait()
	if p.Log != nil {
		// The tail is the reason a spawn failed, so it is worth a line of its own when it exists.
		if tail := p.Stderr.Last(); tail != "" {
			p.Log.Warn("agent: stderr %s", truncateFrame(tail))
		}
		p.Log.Info("agent: exit pid=%d err=%v lived=%dms", p.Cmd.Process.Pid, err, time.Since(p.startedAt).Milliseconds())
	}
}

// stderrTail keeps the last few lines the child wrote. Bounded: a chatty child must not grow the
// gateway's memory, and only the last line is ever quoted (that is where the reason is).
type stderrTail struct {
	mu    sync.Mutex
	lines []string
}

const stderrTailLines = 20

func (t *stderrTail) drain(r *os.File) {
	defer r.Close()
	buf := make([]byte, 4096)
	var pending string
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending += string(buf[:n])
			for {
				index := strings.IndexByte(pending, '\n')
				if index < 0 {
					break
				}
				t.push(pending[:index])
				pending = pending[index+1:]
			}
		}
		if err != nil {
			if strings.TrimSpace(pending) != "" {
				t.push(pending)
			}
			return
		}
	}
}

func (t *stderrTail) push(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > stderrTailLines {
		t.lines = t.lines[len(t.lines)-stderrTailLines:]
	}
}

// Last returns the most recent line, which for a startup failure is the whole reason.
func (t *stderrTail) Last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return ""
	}
	return t.lines[len(t.lines)-1]
}
