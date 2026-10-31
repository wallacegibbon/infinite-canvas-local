package agent

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
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
	Stderr *stderrTail

	stdinFile *os.File

	waitOnce sync.Once
	waitErr  error
}

// SpawnOptions is what one request may influence. Everything else is the operator's, fixed in the
// gateway's configuration: the config directory, the tool list, the skill container.
type SpawnOptions struct {
	Binary     string
	ConfigPath string
	// System is extra system text (`--system`), the wording for a request that needs some.
	System string
}

// args builds the command line. Built-in tools are always disabled: this route asks an agent for
// words, and a request must not be able to hand itself a shell. The model is not a flag: it is set
// with `CI model_set` once the session is ready, which is where the agent reports the outcome.
func (o SpawnOptions) args() []string {
	args := []string{"--rawio", "--builtin-tools="}
	if o.ConfigPath != "" {
		args = append(args, "--config-path", o.ConfigPath)
	}
	if o.System != "" {
		args = append(args, "--system", o.System)
	}
	return args
}

// Spawn starts the agent. The caller must Release it.
func Spawn(o SpawnOptions) (*Process, error) {
	binary := strings.TrimSpace(o.Binary)
	if binary == "" {
		return nil, errors.New("agent: no binary configured")
	}
	cmd := exec.Command(binary, o.args()...)

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

	in := bufio.NewWriter(stdin)
	return &Process{
		Cmd:       cmd,
		Stdin:     in,
		Stdout:    bufio.NewReaderSize(stdout, 64<<10),
		Stderr:    tail,
		stdinFile: stdin.(*os.File),
	}, nil
}

// Write sends one frame and flushes it. A frame that was never flushed reads as the agent going
// quiet, so every write here is followed by one.
func (p *Process) Write(tag, value string) error {
	if err := WriteFrame(p.Stdin, tag, value); err != nil {
		return err
	}
	return p.Stdin.Flush()
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
	_ = p.Wait()
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
