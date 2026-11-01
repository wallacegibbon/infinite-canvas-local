package server

// The MCP endpoint, through the HTTP layer: what AlayaCore sees when it reads the `mcp.conf` this
// gateway wrote for it.
//
// These tests are about the two halves that have to agree: a call arrives with a workspace in the URL
// and nothing else, so the workspace has to be enough to find the session that sent it; and the tool
// names the model sees are not the names that arrive here.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startMCPGateway is a gateway whose agent is the fake binary and whose operator configuration has one
// file in it, so a session's copy can be recognised.
func startMCPGateway(t *testing.T) (*httptest.Server, *Server, string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "fake-agent")
	if err := os.WriteFile(binary, []byte(fakeAgentScript), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "model.conf"), []byte("the operator's models\n"), 0o644); err != nil {
		t.Fatalf("write model.conf: %v", err)
	}
	t.Setenv("FAKE_SPAWN_LOG", filepath.Join(dir, "spawned"))
	t.Setenv("ALAYACORE_BIN", binary)
	t.Setenv("AGENT_CONFIG_PATH", configDir)

	server := newTestGateway(t, newFakeComfy(t))
	gateway := httptest.NewServer(server.Handler())
	t.Cleanup(gateway.Close)
	return gateway, server, configDir
}

// mcpCall posts one JSON-RPC call to a workspace and returns the status and the decoded answer.
func mcpCall(t *testing.T, gateway *httptest.Server, workspace string, call map[string]any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(call)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	response, err := http.Post(gateway.URL+"/mcp/"+workspace, "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	var answer map[string]any
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
			t.Fatalf("answer is not JSON: %v", err)
		}
	}
	return response.StatusCode, answer
}

// resultOf is the `result` of a call that has to succeed.
func resultOf(t *testing.T, answer map[string]any) map[string]any {
	t.Helper()
	if failure, ok := answer["error"]; ok {
		t.Fatalf("call failed: %v", failure)
	}
	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("answer has no result: %v", answer)
	}
	return result
}

// TestMCPHandshakeIsEnoughToDiscoverTheTools: what AlayaCore does on connect — initialize, then
// tools/list — and what it has to get back for the server to show up at all.
func TestMCPHandshakeIsEnoughToDiscoverTheTools(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)

	_, answer := mcpCall(t, gateway, "whatever", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{},
	})
	handshake := resultOf(t, answer)
	if handshake["protocolVersion"] != mcpProtocolVersion {
		t.Fatalf("protocolVersion = %v, want %q", handshake["protocolVersion"], mcpProtocolVersion)
	}
	if _, ok := handshake["serverInfo"]; !ok {
		t.Fatalf("handshake has no serverInfo: %v", handshake)
	}

	_, answer = mcpCall(t, gateway, "whatever", map[string]any{
		"jsonrpc": "2.0", "id": 2, "method": "tools/list",
	})
	tools, ok := resultOf(t, answer)["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list = %v, want at least one tool", answer)
	}
	name := tools[0].(map[string]any)["name"]
	if name != "ping" {
		t.Fatalf("tool name = %v, want ping", name)
	}
}

// TestMCPToolAnswersWithTheSessionThatAsked is the whole point of the per-session workspace: a call
// carries no identity, and the URL has to be enough.
func TestMCPToolAnswersWithTheSessionThatAsked(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)
	openSocket(t, gateway, "canvas-1")

	_, answer := mcpCall(t, gateway, agentWorkspaceKey("canvas-1"), map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "ping", "arguments": map[string]any{}},
	})
	result := resultOf(t, answer)
	content := result["content"].([]any)
	if text := content[0].(map[string]any)["text"]; text != "pong from canvas-1" {
		t.Fatalf("ping answered %q, want the session's name", text)
	}
	if result["isError"] != nil {
		t.Fatalf("ping is an error: %v", result)
	}
}

// TestMCPToolSaysWhenNoAgentOwnsTheWorkspace: a call that arrives after the session was released is
// answered, not dropped — the agent is waiting for this write.
func TestMCPToolSaysWhenNoAgentOwnsTheWorkspace(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)

	_, answer := mcpCall(t, gateway, agentWorkspaceKey("canvas-1"), map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "ping", "arguments": map[string]any{}},
	})
	result := resultOf(t, answer)
	if result["isError"] != true {
		t.Fatalf("a call with no agent behind it is not an error: %v", result)
	}
	if text := result["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "no agent") {
		t.Fatalf("answer = %q, want it to say no agent is attached", text)
	}
}

// TestMCPNotificationIsAccepted: `notifications/initialized` has no id and no answer, and a server that
// replies to it leaves the client waiting for an answer to something it never asked.
func TestMCPNotificationIsAccepted(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)

	status, answer := mcpCall(t, gateway, "whatever", map[string]any{
		"jsonrpc": "2.0", "method": "notifications/initialized",
	})
	if status != http.StatusAccepted {
		t.Fatalf("notification answered %d, want %d", status, http.StatusAccepted)
	}
	if answer != nil {
		t.Fatalf("notification was answered with %v, want nothing", answer)
	}
}

// TestMCPWorkspaceIsTheSessionsOwnConfig: the directory the agent reads is written per session, points
// its tool calls at that session, and is removed with the session.
func TestMCPWorkspaceIsTheSessionsOwnConfig(t *testing.T) {
	gateway, server, configDir := startMCPGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	key := agentWorkspaceKey("canvas-1")
	dir := filepath.Join(server.cfg.DataDir, "agent", key)

	// The operator's models, copied rather than shared: choosing one must not change the default.
	copied, err := os.ReadFile(filepath.Join(dir, "model.conf"))
	if err != nil {
		t.Fatalf("read the session's model.conf: %v", err)
	}
	original, err := os.ReadFile(filepath.Join(configDir, "model.conf"))
	if err != nil {
		t.Fatalf("read the operator's model.conf: %v", err)
	}
	if string(copied) != string(original) {
		t.Fatalf("model.conf = %q, want the operator's %q", copied, original)
	}

	// And where its tool calls go, which is the only thing that says which session is asking.
	written, err := os.ReadFile(filepath.Join(dir, "mcp.conf"))
	if err != nil {
		t.Fatalf("read the session's mcp.conf: %v", err)
	}
	want := "/mcp/" + key
	if !strings.Contains(string(written), want) {
		t.Fatalf("mcp.conf = %q, want the URL to contain %q", written, want)
	}
	if !strings.Contains(string(written), "proto-version: "+mcpProtocolVersion) {
		t.Fatalf("mcp.conf = %q, want the protocol version it speaks", written)
	}

	send(t, conn, map[string]any{"close": true})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the workspace %s outlived the session", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestMCPWorkspaceIsNotReused is the other direction: a session is not a file, so the same name later
// gets a directory built for it rather than the last one's leftovers.
func TestMCPWorkspaceIsNotReused(t *testing.T) {
	gateway, server, _ := startMCPGateway(t)
	conn := openSocket(t, gateway, "canvas-1")
	key := agentWorkspaceKey("canvas-1")
	dir := filepath.Join(server.cfg.DataDir, "agent", key)

	// Something the agent itself would have written: a model chosen in that session.
	if err := os.WriteFile(filepath.Join(dir, "runtime.conf"), []byte("chosen in the last session\n"), 0o644); err != nil {
		t.Fatalf("write runtime.conf: %v", err)
	}
	send(t, conn, map[string]any{"close": true})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the workspace %s outlived the session", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A new session of the same name starts from the operator's configuration, not the last model.
	openSocket(t, gateway, "canvas-1")
	written, err := os.ReadFile(filepath.Join(dir, "runtime.conf"))
	if err != nil {
		return // the operator has no runtime.conf to copy, which is a fresh start too
	}
	if strings.Contains(string(written), "chosen in the last session") {
		t.Fatalf("the new session inherited %q", written)
	}
}
