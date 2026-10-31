package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"comfyui-gateway/internal/logx"
)

// Session is one agent process, held for as long as the canvas that owns it is open.
//
// Its life is the client's, not the gateway's: a connect starts one, every question after that writes
// into the same process, and it is released only when that client says so. There is no idle timer and
// no expiry — an agent that forgot its conversation while the user was thinking would be worse than no
// agent, and the history is the whole reason the process exists.
//
// The key is whatever the client called it (the canvas id, in practice). The gateway does not know or
// care what it means: it is a name a process is filed under, and the same name always finds the same
// process.
type Session struct {
	ID string

	state startup
	proc  *Process

	// One turn at a time. Two streams reading one stdout would each take half of the frames, and the
	// answers would be split between them; the mutex is what makes a second question wait rather than
	// interleave. Nothing else is shared between turns.
	mu sync.Mutex

	// gone releases the session's own entry when the process dies under it. Set by Connect.
	gone func(reason string)
}

// Sessions holds the connected agents, one per client-chosen name.
type Sessions struct {
	mu   sync.Mutex
	list map[string]*Session
	Log  *logx.Logger
}

func NewSessions(log *logx.Logger) *Sessions {
	return &Sessions{list: map[string]*Session{}, Log: log}
}

// Connect returns the agent filed under this name, starting one if there is none. Connecting twice is
// the same as connecting once: a canvas has one agent, and asking for it again is how a reloaded page
// finds the conversation it left.
//
// The spawn happens under the same lock as the lookup. It is the only way two simultaneous connects
// cannot both decide the name is free and start two processes — and the cost is that a canvas waiting
// on its spawn delays another canvas's connect, which is a handful of milliseconds against a bug that
// would leave an orphan process holding half a conversation.
func (s *Sessions) Connect(ctx context.Context, id string, opts SpawnOptions) (*Session, bool, error) {
	if id == "" {
		id = newSessionID()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.list[id]; ok {
		return existing, false, nil
	}
	proc, err := Spawn(opts)
	if err != nil {
		return nil, false, err
	}
	state, err := readStartup(proc)
	if err != nil {
		proc.Release()
		return nil, false, err
	}
	session := &Session{ID: id, state: state, proc: proc}
	session.gone = func(reason string) { s.Close(id, reason) }
	s.list[id] = session

	if s.Log != nil {
		s.Log.Info("agent: connected session=%s pid=%d models=%d active=%q", id, proc.Cmd.Process.Pid, len(state.models), activeName(state))
	}
	return session, true, nil
}

// Get is the lookup a request does before it starts a stream, so a name that is not connected can be
// refused while a status code still means something.
func (s *Sessions) Get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.list[id]
	return session, ok
}

// Close releases a session and the process with it. Called when the client says so, when the process
// dies under it, and when the gateway is shutting down.
func (s *Sessions) Close(id, reason string) {
	s.mu.Lock()
	session, ok := s.list[id]
	delete(s.list, id)
	s.mu.Unlock()
	if !ok {
		return
	}
	session.proc.Release()
	if s.Log != nil {
		s.Log.Info("agent: released session=%s reason=%s", id, reason)
	}
}

// State is what a connect returns: the agent's own startup broadcast.
func (s *Session) State() Event { return s.state.event() }

// Chat writes one request into the process and streams what it answers, up to the end of the turn.
// The process stays: that is the difference between this and a one-shot question.
//
// A process that ends mid-turn cannot be used again, so the session releases itself and the caller is
// told with ErrGone.
func (s *Session) Chat(ctx context.Context, req Request, emit func(Event)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// The list the connect already showed is re-emitted so a client that reattached still renders it;
	// it is the same startup broadcast.
	emit(s.state.event())
	err := converse(ctx, s.proc, &s.state, req, emit, true)
	if errors.Is(err, ErrGone) {
		s.gone("exited")
	}
	return err
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
