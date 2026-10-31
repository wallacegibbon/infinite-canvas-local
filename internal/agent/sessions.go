package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
)

// Session is one agent process held between two requests: the one that connects (and reads the model
// list) and the one that asks (and releases it).
//
// The lifetime is the user's: the connect button starts it, the send button ends it, and nothing else
// keeps it alive. The idle timeout exists only so a closed tab cannot leave a process behind.
type Session struct {
	ID string

	state   startup
	proc    *Process
	opts    SpawnOptions
	mu      sync.Mutex
	expires *time.Timer
}

// Sessions holds the connected agents, one per connect.
type Sessions struct {
	mu   sync.Mutex
	list map[string]*Session
	Log  *logx.Logger
}

// idleSession is how long a connected agent waits for its prompt before it is released. Generous on
// purpose: the user may be reading the model list or typing.
const idleSession = 5 * time.Minute

func NewSessions(log *logx.Logger) *Sessions {
	return &Sessions{list: map[string]*Session{}, Log: log}
}

// Connect spawns an agent and keeps it, returning the state it reported at startup.
func (s *Sessions) Connect(ctx context.Context, opts SpawnOptions) (*Session, error) {
	proc, err := Spawn(opts)
	if err != nil {
		return nil, err
	}
	state, err := readStartup(proc)
	if err != nil {
		proc.Release()
		return nil, err
	}
	session := &Session{ID: newSessionID(), state: state, proc: proc, opts: opts}
	session.expires = time.AfterFunc(idleSession, func() { s.Close(session.ID, "idle") })

	s.mu.Lock()
	s.list[session.ID] = session
	s.mu.Unlock()

	if s.Log != nil {
		s.Log.Info("agent: connected session=%s pid=%d models=%d active=%q", session.ID, proc.Cmd.Process.Pid, len(state.models), activeName(state))
	}
	return session, nil
}

// State is what the connect response carries: the agent's own startup broadcast.
func (s *Session) State() Event { return s.state.event() }

// Peek returns the session without consuming it, so a request can be refused before its stream starts.
func (s *Sessions) Peek(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.list[id]
	return session, ok
}

// Touch restarts the idle window: the connection is still in use, just not for a question.
func (s *Sessions) Touch(id string) {
	s.mu.Lock()
	session, ok := s.list[id]
	s.mu.Unlock()
	if ok && session.expires != nil {
		session.expires.Reset(idleSession)
	}
}

// Command runs one command on a connected agent and leaves it connected. Changing a setting is not
// asking a question, so it must not spend the connection the way a prompt does.
func (s *Session) Command(ctx context.Context, req Request, emit func(Event)) error {
	// The session's own state is updated by what the agent announces (SM model, SM reasoning_level),
	// so the next prompt's re-emitted model list is the current one.
	return converse(ctx, s.proc, &s.state, req, emit, true)
}

// Take removes the session and hands over its process: one connect answers exactly one prompt. The
// caller owns the process afterwards and must release it.
func (s *Sessions) Take(id string) (*Process, Startup, bool) {
	s.mu.Lock()
	session, ok := s.list[id]
	delete(s.list, id)
	s.mu.Unlock()
	if !ok {
		return nil, Startup{}, false
	}
	if session.expires != nil {
		session.expires.Stop()
	}
	return session.proc, session.state, true
}

// Close releases a session the client abandoned, or one that idled out.
func (s *Sessions) Close(id, reason string) {
	s.mu.Lock()
	session, ok := s.list[id]
	delete(s.list, id)
	s.mu.Unlock()
	if !ok {
		return
	}
	if session.expires != nil {
		session.expires.Stop()
	}
	session.proc.Release()
	if s.Log != nil {
		s.Log.Info("agent: released session=%s reason=%s", id, reason)
	}
}

func newSessionID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return "session"
	}
	return hex.EncodeToString(buffer)
}

func activeName(state startup) string {
	if state.active == nil {
		return ""
	}
	return state.active.Name
}

// readStartup reads the frames an agent emits before it accepts input.
func readStartup(proc *Process) (startup, error) {
	state := startup{}
	err := waitReady(proc, &state, func(Event) {}, func() error { return nil })
	return state, err
}
