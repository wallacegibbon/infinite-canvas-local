package server

// Every agent session gets its own config directory.
//
// AlayaCore is told where its configuration lives with `--config-path`, and that directory is where it
// reads `model.conf` and `mcp.conf` and where it *writes* `runtime.conf` when a model is chosen. A
// session needs its own copy for two reasons: the model a session picks must not become the default for
// the next one, and `mcp.conf` is how the agent is told where to send its tool calls — which has to name
// the session, because an MCP call carries no identity of its own.
//
// The directory is removed with the process: see `agent.SpawnOptions.WorkDir`.
//
// The conversation is a different thing and lives in a different place. It is a file AlayaCore writes
// for `--session`, it outlives the process on purpose, and it is what a reloaded page gets replayed
// into its panel: see `agent.SpawnOptions.Session`.

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"comfyui-gateway/internal/agent"
)

// agentWorkspaceKey is the directory (and URL) name for a session: readable, and unique by hash.
//
// It is derived from the session id rather than stored, so the MCP route can find the session that owns
// a workspace without a registry of its own — a name that cannot be mapped back is worse than a name
// that is ugly.
func agentWorkspaceKey(session string) string {
	var safe strings.Builder
	for _, r := range session {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			safe.WriteRune(r)
			if safe.Len() >= 40 {
				break
			}
		default:
			safe.WriteByte('-')
		}
	}
	sum := sha1.Sum([]byte(session))
	return fmt.Sprintf("%s-%x", safe.String(), sum[:4])
}

// workspacesRoot holds one config directory per session; sessionsRoot holds their conversations.
func workspacesRoot(dataDir string) string { return filepath.Join(dataDir, "agent", "workspaces") }
func sessionsRoot(dataDir string) string   { return filepath.Join(dataDir, "agent", "sessions") }

// prepareAgentWorkspace lays out one session's config directory and returns it.
func (s *Server) prepareAgentWorkspace(session string) (string, error) {
	dir := filepath.Join(workspacesRoot(s.cfg.DataDir), agentWorkspaceKey(session))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// The operator's models, copied: the agent reads them and must not be able to change them.
	for _, name := range []string{"model.conf", "runtime.conf"} {
		source := filepath.Join(s.cfg.AgentConfigPath, name)
		if data, err := os.ReadFile(source); err == nil {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return "", err
			}
		}
	}
	// Where this session's tool calls go. The URL names the workspace, which names the session.
	mcp := "server: canvas\n" +
		"url: " + s.mcpURL(agentWorkspaceKey(session)) + "\n" +
		"proto-version: " + mcpProtocolVersion + "\n"
	if err := os.WriteFile(filepath.Join(dir, "mcp.conf"), []byte(mcp), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// mcpURL is the address an agent on this machine dials to reach its workspace.
//
// It is built from the address the gateway actually listens on, because that is the only one that is
// certainly up: a gateway bound to a LAN address does not answer on 127.0.0.1, and an agent handed
// `127.0.0.1:<port>` in that setup cannot connect at all — it loads no tools and says so only in its own
// output. A wildcard bind cannot be dialled, so it becomes loopback, which is where a wildcard reaches.
func (s *Server) mcpURL(workspace string) string {
	host := s.cfg.GatewayHost
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // an IPv6 literal
	}
	return fmt.Sprintf("http://%s:%d/mcp/%s", host, s.cfg.GatewayPort, workspace)
}

// clearAgentWorkspaces removes the config copies a previous run left behind: they are rebuilt from the
// operator's configuration on the next spawn.
//
// Conversations are not removed. They are the user's words, they cost a few kilobytes, and the whole
// point of the file is that reopening a canvas finds what was said in it — a gateway restart is not a
// reason to forget.
func clearAgentWorkspaces(dataDir string) {
	_ = os.RemoveAll(workspacesRoot(dataDir))
}

// sessionFile is where this session's conversation is kept, created if this is the first time.
func (s *Server) sessionFile(session string) (string, error) {
	root := sessionsRoot(s.cfg.DataDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(root, agentWorkspaceKey(session)+".session"), nil
}

// agentOptionsFor is what one session's spawn needs: the shared options, a workspace of its own and — when
// the client says this name is a conversation rather than a question — a session file to keep it in.
//
// A node's agent is asked one thing at a time and writes the answer into the node; keeping that in a file
// would leave one per node on disk, and would carry an earlier question into the next answer.
func (s *Server) agentOptionsFor(session string, history bool) (agent.SpawnOptions, error) {
	opts := s.agentOptions()
	dir, err := s.prepareAgentWorkspace(session)
	if err != nil {
		return opts, err
	}
	opts.ConfigPath = dir
	opts.WorkDir = dir
	if !history {
		return opts, nil
	}
	file, err := s.sessionFile(session)
	if err != nil {
		return opts, err
	}
	opts.Session = file
	return opts, nil
}

// sessionForWorkspace finds the session that owns a workspace name, which is what an MCP call has to
// identify itself with. The name is derived from the session id, so this is a scan over the sessions
// that exist rather than a registry that could disagree with them.
func (s *Server) sessionForWorkspace(key string) (*agent.Session, bool) {
	for _, session := range s.agentSessions.List() {
		if agentWorkspaceKey(session.ID) == key {
			return session, true
		}
	}
	return nil, false
}
