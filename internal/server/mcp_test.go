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

	"comfyui-gateway/internal/config"
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

// postMCP sends one JSON-RPC call and returns the status and the decoded answer. It is the raw half, so
// a test can post from a goroutine while the main one reads the canvas's socket.
func postMCP(gateway *httptest.Server, workspace string, call map[string]any) (int, map[string]any, error) {
	body, err := json.Marshal(call)
	if err != nil {
		return 0, nil, err
	}
	response, err := http.Post(gateway.URL+"/mcp/"+workspace, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	var answer map[string]any
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
			return response.StatusCode, nil, err
		}
	}
	return response.StatusCode, answer, nil
}

// mcpCall is postMCP for a call that is supposed to happen on this goroutine.
func mcpCall(t *testing.T, gateway *httptest.Server, workspace string, call map[string]any) (int, map[string]any) {
	t.Helper()
	status, answer, err := postMCP(gateway, workspace, call)
	if err != nil {
		t.Fatalf("mcp call: %v", err)
	}
	return status, answer
}

// toolCall is one `tools/call`, which is what an agent does once it has decided to use a tool.
func toolCall(id int, name string, arguments map[string]any) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": arguments},
	}
}

// toolAnswerOf wraps content blocks the way the canvas sends them back.
func toolAnswerOf(callID string, content ...any) map[string]any {
	return map[string]any{"toolResult": map[string]any{"callId": callID, "content": content}}
}

// textBlock is one text content block, which is what most tools answer with.
func textBlock(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

// textOfResult is the one text block a tool result carries.
func textOfResult(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("result content = %v, want one block", result["content"])
	}
	return content[0].(map[string]any)["text"].(string)
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
	// The vocabulary, in the order it is offered: a name that disappears from here is a tool the canvas
	// can no longer be asked for.
	var names []string
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "ping,list_nodes,create_node,connect,set_prompt,generate,read_node" {
		t.Fatalf("tools/list = %v, want ping, list_nodes, create_node", names)
	}
}

// TestMCPToolIsRunByTheCanvas is the whole point of the pipe: a call carries no identity, the URL has to
// be enough to find the session, and only the page can actually run the tool.
func TestMCPToolIsRunByTheCanvas(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	answered := make(chan map[string]any, 1)
	go func() {
		_, answer, err := postMCP(gateway, agentWorkspaceKey("canvas-1"), toolCall(3, "ping", map[string]any{"where": "here"}))
		if err != nil {
			answered <- map[string]any{"post": err.Error()}
			return
		}
		answered <- answer
	}()

	// The page is asked, with an id to answer with and the arguments as they were sent.
	asked := nextEvent(t, conn)
	if asked["type"] != "tool" || asked["name"] != "ping" {
		t.Fatalf("canvas was asked %v, want a ping tool call", asked)
	}
	callID, ok := asked["callId"].(string)
	if !ok || callID == "" {
		t.Fatalf("tool call has no id: %v", asked)
	}
	if arguments, ok := asked["arguments"].(map[string]any); !ok || arguments["where"] != "here" {
		t.Fatalf("arguments = %v, want the call's own", asked["arguments"])
	}

	send(t, conn, toolAnswerOf(callID, textBlock("pong from the page")))

	answer := <-answered
	if failure, ok := answer["post"]; ok {
		t.Fatalf("post: %v", failure)
	}
	result := resultOf(t, answer)
	if text := textOfResult(t, result); text != "pong from the page" {
		t.Fatalf("tool result = %q, want what the canvas said", text)
	}
	if result["isError"] != nil {
		t.Fatalf("the call is an error: %v", result)
	}
}

// TestMCPCarriesTheBlocksThePageProduced is what a read tool needs: the content the canvas sends reaches
// AlayaCore unchanged. Nothing here inspects it, so a media block — and any field a later revision adds to
// one — has to survive the trip exactly as written.
func TestMCPCarriesTheBlocksThePageProduced(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	answered := make(chan map[string]any, 1)
	go func() {
		_, answer, err := postMCP(gateway, agentWorkspaceKey("canvas-1"), toolCall(8, "read_node", map[string]any{"nodeId": "image-1"}))
		if err != nil {
			answered <- map[string]any{"post": err.Error()}
			return
		}
		answered <- answer
	}()

	asked := nextEvent(t, conn)
	// A picture and a sentence, as the page would send a node's picture with its prompt.
	blocks := []any{
		textBlock("a red ceramic teapot"),
		map[string]any{"type": "image", "data": "iVBORw0KGgoAAAANSUhEUg==", "mimeType": "image/png"},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "canvas://video-1", "mimeType": "video/mp4", "blob": "AAAAIGZ0eXBpc29t"}},
	}
	send(t, conn, toolAnswerOf(asked["callId"].(string), blocks...))

	result := resultOf(t, <-answered)
	got, err := json.Marshal(result["content"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content = %s, want it carried through as %s", got, want)
	}
}

// TestMCPToolReportsWhatTheCanvasCalledAFailure: a tool that failed is a result the agent can read, not a
// broken response.
func TestMCPToolReportsWhatTheCanvasCalledAFailure(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)
	conn := openSocket(t, gateway, "canvas-1")

	answered := make(chan map[string]any, 1)
	go func() {
		_, answer, err := postMCP(gateway, agentWorkspaceKey("canvas-1"), toolCall(4, "ping", map[string]any{}))
		if err != nil {
			answered <- map[string]any{"post": err.Error()}
			return
		}
		answered <- answer
	}()

	asked := nextEvent(t, conn)
	send(t, conn, map[string]any{"toolResult": map[string]any{
		"callId": asked["callId"], "content": []any{textBlock("there is no such node")}, "isError": true,
	}})

	result := resultOf(t, <-answered)
	if result["isError"] != true {
		t.Fatalf("a tool the canvas failed is not reported as one: %v", result)
	}
	if text := textOfResult(t, result); text != "there is no such node" {
		t.Fatalf("tool result = %q, want the canvas's reason", text)
	}
}

// TestMCPToolSaysWhenNoCanvasIsWatching: the session outlives its socket (that is what makes coming back
// a continuation), so a call can arrive with nobody to run it — and has to be answered anyway.
func TestMCPToolSaysWhenNoCanvasIsWatching(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)
	conn := openSocket(t, gateway, "canvas-1")
	_ = conn.Close()
	time.Sleep(300 * time.Millisecond) // long enough for the gateway to notice the socket is gone

	_, answer := mcpCall(t, gateway, agentWorkspaceKey("canvas-1"), toolCall(5, "ping", map[string]any{}))
	result := resultOf(t, answer)
	if result["isError"] != true {
		t.Fatalf("a call with no canvas behind it is not an error: %v", result)
	}
	if text := textOfResult(t, result); !strings.Contains(text, "no canvas") {
		t.Fatalf("answer = %q, want it to say no canvas is connected", text)
	}
}

// TestMCPUnknownToolIsRefused: the vocabulary is the gateway's, so a name it never advertised does not
// reach the canvas at all.
func TestMCPUnknownToolIsRefused(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)
	openSocket(t, gateway, "canvas-1")

	_, answer := mcpCall(t, gateway, agentWorkspaceKey("canvas-1"), toolCall(6, "empty_the_canvas", map[string]any{}))
	result := resultOf(t, answer)
	if text := textOfResult(t, result); !strings.Contains(text, "no such tool") {
		t.Fatalf("answer = %q, want it to refuse the name", text)
	}
}

// TestMCPToolSaysWhenNoAgentOwnsTheWorkspace: a call that arrives after the session was released is
// answered, not dropped — the agent is waiting for this write.
func TestMCPToolSaysWhenNoAgentOwnsTheWorkspace(t *testing.T) {
	gateway, _, _ := startMCPGateway(t)

	_, answer := mcpCall(t, gateway, agentWorkspaceKey("canvas-1"), toolCall(7, "ping", map[string]any{}))
	result := resultOf(t, answer)
	if result["isError"] != true {
		t.Fatalf("a call with no agent behind it is not an error: %v", result)
	}
	if text := textOfResult(t, result); !strings.Contains(text, "no agent") {
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

// TestMCPURLIsAnAddressTheAgentCanDial: the agent runs on this machine, so the only address that is
// certainly up is the one the gateway listens on. Handing it `127.0.0.1` while the gateway is bound to a
// LAN address is a server the agent can never reach — it loads no tools, and nothing here would notice.
func TestMCPURLIsAnAddressTheAgentCanDial(t *testing.T) {
	cases := []struct{ host, want string }{
		{"127.0.0.1", "http://127.0.0.1:8190/mcp/w"},
		{"192.168.3.232", "http://192.168.3.232:8190/mcp/w"},
		{"0.0.0.0", "http://127.0.0.1:8190/mcp/w"}, // a wildcard has no address of its own
		{"", "http://127.0.0.1:8190/mcp/w"},
		{"::1", "http://[::1]:8190/mcp/w"},
	}
	for _, one := range cases {
		server := &Server{cfg: &config.Config{GatewayHost: one.host, GatewayPort: 8190}}
		if got := server.mcpURL("w"); got != one.want {
			t.Fatalf("mcpURL with host %q = %q, want %q", one.host, got, one.want)
		}
	}
}
