package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"comfyui-gateway/internal/logx"
)

// Session is one agent process, held for as long as the connection that owns it is open.
//
// Its life is the connection's: a socket starts one, everything the user says goes into the same
// process, and it is released when that socket ends. Nothing is lost by that, because the conversation
// is not in the process — AlayaCore keeps it in the file named by `--session`, and the next process
// replays it. Memory lives in the file; the process is only the hands that work on it.
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

	// pending is the tool calls waiting for the canvas to answer them, by the id the gateway gave them,
	// and calls counts the ones asked so far. A tool call is a question only the page can answer, so it
	// leaves through the same sink and comes back as a message on the same socket.
	pending map[string]chan toolAnswer
	calls   int

	// gone releases the session's own entry when the process dies. Set by Connect.
	gone func(reason string)
}

// Connected is what a new connection gets: the session it now owns, and the agent's own startup
// broadcast, which is where the model list comes from.
type Connected struct {
	Session *Session
	Startup Event
	// History is the conversation the new process replayed from its session file, oldest first. It is
	// empty when the agent was already running (there was nothing to replay) and when there was no file.
	History []HistoryEntry
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
// the same as connecting once: a canvas has one agent. A page that comes back after its socket went
// away does not find the same process — it finds the same conversation file, replayed into a new one.
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
	state, history, err := readStartup(proc)
	if err != nil {
		proc.Release()
		return Connected{}, err
	}
	session := &Session{ID: id, proc: proc, state: state, pending: map[string]chan toolAnswer{}}
	session.gone = func(reason string) { s.Close(id, reason) }
	s.list[id] = session
	go session.pump()

	if s.Log != nil {
		s.Log.Info("agent: connected session=%s pid=%d models=%d active=%q", id, proc.Cmd.Process.Pid, len(state.models), activeName(state))
	}
	return Connected{Session: session, Startup: state.event(), History: history, Spawned: true}, nil
}

// List is every connected session. The MCP route uses it to find the session that owns a workspace,
// which is a name derived from the session rather than a registry kept beside it.
func (s *Sessions) List() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := make([]*Session, 0, len(s.list))
	for _, session := range s.list {
		all = append(all, session)
	}
	return all
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

// Unwatch detaches the connection. The pump keeps reading until the process is released — a reader that
// stopped with its socket could not be restarted, and whatever the agent says while it winds down still
// belongs in its log.
//
// Any tool call still waiting is answered here rather than left to time out: the page that would have
// run it is gone, and that is the answer.
func (s *Session) Unwatch() {
	s.mu.Lock()
	s.sink = nil
	pending := s.pending
	s.pending = map[string]chan toolAnswer{}
	s.mu.Unlock()
	for _, waiting := range pending {
		deliver(waiting, toolAnswer{content: TextContent("the canvas went away"), isError: true})
	}
}

// toolAnswer is what the canvas says about a tool call: the content blocks to hand the agent, and whether
// the tool failed rather than did something.
type toolAnswer struct {
	content json.RawMessage
	isError bool
}

// TextContent is a tool result that is only words, which is what the gateway says when it cannot get an
// answer at all.
func TextContent(text string) json.RawMessage {
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	if err != nil {
		return json.RawMessage(`[{"type":"text","text":"the canvas did not answer"}]`)
	}
	return content
}

// toolTimeout is how long a tool call waits for the canvas. Short of forever is the point: a call that
// never returns holds the agent's whole turn open, and a page that is not answering is not going to.
const toolTimeout = 30 * time.Second

// CallTool runs one of the gateway's tools by asking the canvas. The gateway knows the vocabulary, the
// page knows the nodes, so the call goes down the session's socket and this waits for the answer.
//
// The answer is whatever content blocks the page produced, carried through untouched: the protocol says
// what they are, so nothing here has to know. Every way of not getting an answer — nobody watching, a
// canvas that left, a page that never replied — is a text block with `isError` rather than an error: an
// agent told "the canvas did not answer" can say so to the user, while an aborted turn just disappears.
func (s *Session) CallTool(name string, arguments json.RawMessage) (json.RawMessage, bool) {
	s.mu.Lock()
	if s.sink == nil {
		s.mu.Unlock()
		return TextContent("no canvas is connected to this session"), true
	}
	s.calls++
	callID := "tool-" + strconv.Itoa(s.calls)
	waiting := make(chan toolAnswer, 1)
	s.pending[callID] = waiting
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, callID)
		s.mu.Unlock()
	}()

	s.publish(Event{Type: "tool", CallID: callID, Name: name, Arguments: arguments})
	select {
	case answer := <-waiting:
		return answer.content, answer.isError
	case <-time.After(toolTimeout):
		return TextContent("the canvas did not answer"), true
	}
}

// AnswerTool completes a call the canvas ran, and reports whether anyone was still waiting for it: an
// answer that arrives after the timeout has nobody left to tell.
func (s *Session) AnswerTool(callID string, content json.RawMessage, isError bool) bool {
	s.mu.Lock()
	waiting, ok := s.pending[callID]
	delete(s.pending, callID)
	s.mu.Unlock()
	if ok {
		deliver(waiting, toolAnswer{content: content, isError: isError})
	}
	return ok
}

// deliver hands over an answer without ever blocking: the channel holds one, and an answer that arrives
// twice (a canvas and a timeout both finishing) is not worth waiting on.
func deliver(waiting chan toolAnswer, answer toolAnswer) {
	select {
	case waiting <- answer:
	default:
	}
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
	for {
		frame, err := s.proc.Read()
		if err != nil || frame == nil {
			s.publish(Event{Type: "closed"})
			s.gone("exited")
			return
		}
		// The end of a turn is a marker, not a second copy of the answer: the deltas already carried it,
		// and two strings that agree until they do not are worse than one.
		if turnStep(&s.state, frame, true, s.publish) {
			s.publish(Event{Type: "done"})
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
// request rather than a kill; what it already said is in the session file either way.
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

// readStartup reads the frames an agent emits before it accepts input: its own startup broadcast, and
// the conversation it replayed from its session file.
func readStartup(proc *Process) (startup, []HistoryEntry, error) {
	state := startup{}
	history := []HistoryEntry{}
	err := waitReady(proc, &state, &history, func(Event) {}, func() error { return nil })
	return state, history, err
}
