package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"

	"comfyui-gateway/internal/logx"
)

// Session is one agent process, held for as long as the canvas that owns it is open.
//
// Its life is the client's, not the gateway's: a socket starts one, everything the user says goes into
// the same process, and it is released only when that client says so. There is no idle timer and no
// expiry — an agent that forgot its conversation while the user was thinking would be worse than no
// agent, and the history is the whole reason the process exists.
//
// The key is whatever the client called it (the canvas id, in practice). The gateway does not know or
// care what it means: it is a name a process is filed under, and the same name always finds the same
// process.
type Session struct {
	ID string

	proc *Process

	// state is the startup broadcast as the agent last restated it. Only the pump changes it, and a
	// session has one pump, so it needs no lock of its own.
	state startup

	// write keeps frames whole: two messages writing at once would interleave halves of two frames and
	// the agent would read nonsense.
	write sync.Mutex

	// mu guards the watcher. The pump reads the process whether or not anyone is listening — a socket
	// can be closed at any moment, and a reader that was only alive while a socket was would be stuck
	// inside a blocking read nobody can interrupt.
	mu   sync.Mutex
	sink func(Event) error

	// gone releases the session's own entry when the process dies. Set by Connect.
	gone func(reason string)
}

// Connected is what a new connection gets: the session it now owns, and the agent's own startup
// broadcast, which is where the model list comes from.
type Connected struct {
	Session *Session
	Startup Event
	Spawned bool
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
func (s *Sessions) Connect(ctx context.Context, id string, opts SpawnOptions) (Connected, error) {
	if id == "" {
		id = newSessionID()
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.list[id]; ok {
		return Connected{Session: existing, Startup: existing.state.event()}, nil
	}
	proc, err := Spawn(opts)
	if err != nil {
		return Connected{}, err
	}
	state, err := readStartup(proc)
	if err != nil {
		proc.Release()
		return Connected{}, err
	}
	session := &Session{ID: id, proc: proc, state: state}
	session.gone = func(reason string) { s.Close(id, reason) }
	s.list[id] = session
	go session.pump()

	if s.Log != nil {
		s.Log.Info("agent: connected session=%s pid=%d models=%d active=%q", id, proc.Cmd.Process.Pid, len(state.models), activeName(state))
	}
	return Connected{Session: session, Startup: state.event(), Spawned: true}, nil
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
	session.Unwatch()
	session.proc.Release()
	if s.Log != nil {
		s.Log.Info("agent: released session=%s reason=%s", id, reason)
	}
}

// Watch attaches the one connection that draws what this agent says, and reports whether it got the
// seat. One conversation has one set of eyes: two watchers would each half-produce the same answer, and
// the events carry no addressee to tell them apart.
func (s *Session) Watch(sink func(Event) error) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sink != nil {
		return false
	}
	s.sink = sink
	return true
}

// Unwatch detaches the connection. The pump keeps reading: the process is not going anywhere, and a
// reader that stopped with its socket could not be restarted without losing whatever the agent said in
// between.
func (s *Session) Unwatch() {
	s.mu.Lock()
	s.sink = nil
	s.mu.Unlock()
}

// publish hands one event to the watcher, if there is one. A failed write means the connection is gone,
// so it is detached — the pump carries on.
func (s *Session) publish(event Event) {
	s.mu.Lock()
	sink := s.sink
	s.mu.Unlock()
	if sink == nil {
		return
	}
	if err := sink(event); err != nil {
		s.Unwatch()
	}
}

// pump is the session's one reader, for the whole life of the process.
func (s *Session) pump() {
	var answer strings.Builder
	for {
		frame, err := s.proc.Read()
		if err != nil || frame == nil {
			s.publish(Event{Type: "closed"})
			s.gone("exited")
			return
		}
		ended := turnStep(&s.state, frame, true, func(event Event) {
			collect(&answer, event, s.publish)
		})
		if ended {
			s.publish(Event{Type: "done", Text: answer.String()})
			answer.Reset()
		}
	}
}

// Send writes one message into the process. Writing is what a connection does when the user types; the
// answer arrives through the watcher, not here.
func (s *Session) Send(req Request) error {
	s.write.Lock()
	defer s.write.Unlock()
	return writeRequest(s.proc, req)
}

// Cancel asks the agent to wind its current task down. A turn is the agent's to finish, so this is a
// request rather than a kill; the process stays, and its history stays with it.
func (s *Session) Cancel() error {
	s.write.Lock()
	defer s.write.Unlock()
	if err := s.proc.Write(TagCmdInput, `{"command_id":"cancel","name":"cancel"}`); err != nil {
		return ErrGone
	}
	return nil
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
