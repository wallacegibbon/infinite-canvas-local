package server

// The agent's hands.
//
// AlayaCore runs its own built-in tools, so the only way for it to do something this gateway knows
// about is MCP (`mcp.conf` in its config directory). The gateway is already the process holding the
// socket to the browser, so it is also the natural MCP server: a tool call arrives here, goes down the
// session's socket, and comes back with whatever the canvas did.
//
// What is here is the protocol only — the tool table and the JSON-RPC envelope. Where a call is routed,
// and what the tools mean, is the next layer.

import (
	"encoding/json"
	"net/http"

	"comfyui-gateway/internal/agent"
	"comfyui-gateway/internal/httpx"
)

// mcpRequest is one JSON-RPC call.
type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// mcpTool is one entry of `tools/list`.
type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// mcpResult is what `tools/call` answers: content blocks, and whether the tool failed.
//
// The blocks are carried as the page produced them — `json.RawMessage` and not a struct, because the
// protocol's content is self-describing and grows: text, image and audio carry their own `mimeType`,
// `resource` carries a blob, and a field this gateway has never heard of is still a field the client may
// need. Nothing here inspects them, and nothing converts one kind into another.
type mcpResult struct {
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"isError,omitempty"`
}

// mcpProtocolVersion is what this server speaks. The client announces its own; a mismatch is the
// client's to reconcile, and AlayaCore's `proto-version` field is where it says which it wants.
const mcpProtocolVersion = "2025-11-25"

// routesMCP registers the MCP endpoint.
//
// It is deliberately outside `/api`: it is a protocol for another process, not a route for a canvas.
// The workspace in the path is how a tool call says which agent made it — MCP itself carries no such
// identity, and the alternative (guessing the session that has a turn in flight) is wrong as soon as
// two panels are asking at once.
func (s *Server) routesMCP(mux *http.ServeMux) {
	s.route(mux, "POST /mcp/{workspace}", s.handleMCP)
}

// handleMCP speaks just enough of the protocol for a client to discover and call tools.
func (s *Server) handleMCP(ctx *httpx.Ctx) error {
	var request mcpRequest
	if err := json.Unmarshal(mustBody(ctx), &request); err != nil {
		return mcpError(ctx, nil, -32700, "parse error")
	}

	// A notification has no id and no answer. The only one that matters is `notifications/initialized`,
	// which says the client is done handshaking.
	if len(request.ID) == 0 || string(request.ID) == "null" {
		ctx.W.WriteHeader(http.StatusAccepted)
		return nil
	}

	switch request.Method {
	case "initialize":
		return mcpReply(ctx, request.ID, map[string]any{
			"protocolVersion": mcpProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "comfyui-gateway", "version": "0.1.0"},
		})
	case "tools/list":
		return mcpReply(ctx, request.ID, map[string]any{"tools": s.mcpTools()})
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			return mcpError(ctx, request.ID, -32602, "invalid params")
		}
		result, err := s.callMCPTool(ctx.R.PathValue("workspace"), params.Name, params.Arguments)
		if err != nil {
			return mcpError(ctx, request.ID, -32603, err.Error())
		}
		return mcpReply(ctx, request.ID, result)
	default:
		return mcpError(ctx, request.ID, -32601, "unknown method: "+request.Method)
	}
}

// mcpTools is the list the agent is offered. The names are the vocabulary; running one is the canvas's.
//
// A tool's name here is not the name the model sees: AlayaCore prefixes it with the server's name, so
// this `ping` is the model's `canvas_ping`, and calling it arrives here again as `ping`. Naming a tool
// `canvas_ping` would show the model `canvas_canvas_ping` (measured, not guessed).
func (s *Server) mcpTools() []mcpTool {
	noArguments := json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	return []mcpTool{{
		Name:        "ping",
		Description: "Check that the canvas is listening. Answers with how much is on it.",
		InputSchema: noArguments,
	}, {
		Name:        "list_nodes",
		Description: "Every node on the canvas — id, type, title, prompt, position — and every connection between them. The ids are what the other tools act on.",
		InputSchema: noArguments,
	}, {
		Name:        "create_node",
		Description: "Make one node on the canvas, the way the user's own button makes one: it appears where the view is looking, selected, with its panel open when that is what pressing the button does. Answers with the new node's id and position.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"type": {"type": "string", "enum": ["image", "video", "audio", "text", "config"], "description": "What kind of node."},
				"prompt": {"type": "string", "description": "What the node starts with: a text node's body, or the prompt the node's own panel shows."},
				"position": {
					"type": "object",
					"properties": {"x": {"type": "number"}, "y": {"type": "number"}},
					"required": ["x", "y"],
					"additionalProperties": false,
					"description": "Top-left corner in canvas coordinates. Omit it and the node lands in the middle of the view."
				}
			},
			"required": ["type"],
			"additionalProperties": false
		}`),
	}, {
		Name:        "read_node",
		Description: "Read one node's actual content: its words, or the image, video or audio it holds. Answers with the content itself — an image, a video, a sound or text — not a description of it.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"nodeId": {"type": "string", "description": "The id list_nodes reported."}
			},
			"required": ["nodeId"],
			"additionalProperties": false
		}`),
	}}
}

// callMCPTool hands one call to the session's canvas, which is the only thing that can run it: the
// gateway knows the vocabulary, the page knows the nodes.
//
// Everything that goes wrong is a tool result with `isError`, not a JSON-RPC error. The agent is holding
// a turn open waiting for this write; telling it "the canvas did not answer" lets it say so, while a
// broken response leaves it with nothing to report.
func (s *Server) callMCPTool(workspace, name string, args json.RawMessage) (mcpResult, error) {
	session, ok := s.sessionForWorkspace(workspace)
	if !ok {
		return mcpResult{Content: agent.TextContent("no agent is attached to this workspace"), IsError: true}, nil
	}
	if !s.hasMCPTool(name) {
		return mcpResult{Content: agent.TextContent("no such tool: " + name), IsError: true}, nil
	}
	content, failed := session.CallTool(name, args)
	return mcpResult{Content: content, IsError: failed}, nil
}

// hasMCPTool reports whether the advertised table contains this name: the gateway owns the vocabulary
// even though the canvas is what runs a tool, so an unknown name is refused before it leaves.
func (s *Server) hasMCPTool(name string) bool {
	for _, tool := range s.mcpTools() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func mcpReply(ctx *httpx.Ctx, id json.RawMessage, result any) error {
	httpx.JSON(ctx.W, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return nil
}

func mcpError(ctx *httpx.Ctx, id json.RawMessage, code int, message string) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	httpx.JSON(ctx.W, http.StatusOK, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
	return nil
}

// mustBody reads the request body, or an empty object when it cannot be read — a malformed call is
// answered as a parse error, not as a panic.
func mustBody(ctx *httpx.Ctx) []byte {
	body, err := ctx.Body()
	if err != nil || body == nil {
		return []byte("{}")
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}
