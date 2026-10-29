// Package workflow owns the declarative workflow registry: loading ComfyUI API
// graphs from disk, the binding model that maps request fields onto graph inputs,
// and base-model variant resolution.
//
// Graphs are kept as generic JSON trees on purpose. There are nineteen different
// ComfyUI graphs today and they carry node classes this codebase will never know
// about, so a closed Go struct would be a lie. The graph layer therefore offers a
// few well-named accessors, and everything above it is typed.
package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Graph is a ComfyUI API-format graph: node id -> node object.
type Graph map[string]any

// Node is one entry of a Graph.
type Node map[string]any

// ClassType returns the node's class_type, or "" when absent.
func (n Node) ClassType() string {
	value, _ := n["class_type"].(string)
	return value
}

// Inputs returns the node's inputs map. A missing map yields an empty, writable map.
func (n Node) Inputs() map[string]any {
	if inputs, ok := n["inputs"].(map[string]any); ok {
		return inputs
	}
	inputs := map[string]any{}
	n["inputs"] = inputs
	return inputs
}

// Meta returns the node's _meta map, creating it when absent.
func (n Node) Meta() map[string]any {
	if meta, ok := n["_meta"].(map[string]any); ok {
		return meta
	}
	meta := map[string]any{}
	n["_meta"] = meta
	return meta
}

// Title returns the node's display title, used when inferring bindings.
func (n Node) Title() string {
	value, _ := n.Meta()["title"].(string)
	return value
}

// NodeAt returns the node with the given id.
func (g Graph) NodeAt(id string) (Node, bool) {
	raw, ok := g[id]
	if !ok {
		return nil, false
	}
	node, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	return Node(node), true
}

// Clone returns a deep copy, mirroring structuredClone in the old implementation.
// Request handling mutates graphs, so every request must work on its own copy.
func (g Graph) Clone() Graph {
	cloned, ok := deepClone(g).(map[string]any)
	if !ok {
		return Graph{}
	}
	return Graph(cloned)
}

// IDs returns node ids in sorted order, which keeps behaviour deterministic.
func (g Graph) IDs() []string {
	ids := make([]string, 0, len(g))
	for id := range g {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// SortedNodes walks the graph in a stable order.
func (g Graph) SortedNodes() []struct {
	ID   string
	Node Node
} {
	ids := g.IDs()
	out := make([]struct {
		ID   string
		Node Node
	}, 0, len(ids))
	for _, id := range ids {
		if node, ok := g.NodeAt(id); ok {
			out = append(out, struct {
				ID   string
				Node Node
			}{ID: id, Node: node})
		}
	}
	return out
}

// NextNodeID returns an unused node id at or above minimum.
//
// ComfyUI only cares that ids are unique strings; using a high base keeps
// injected loader nodes visibly separate from authored ones.
func (g Graph) NextNodeID(minimum int) string {
	next := minimum
	for {
		id := fmt.Sprintf("%d", next)
		if _, exists := g[id]; !exists {
			return id
		}
		next++
	}
}

// deepClone recursively copies JSON-like values.
func deepClone(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = deepClone(item)
		}
		return out
	case Graph:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = deepClone(item)
		}
		return out
	case Node:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = deepClone(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = deepClone(item)
		}
		return out
	default:
		return value
	}
}

// IsAPIWorkflow reports whether the value is a ComfyUI API-format graph: a
// non-empty object whose every value has a string class_type and an inputs object.
func IsAPIWorkflow(value any) bool {
	object, ok := value.(map[string]any)
	if !ok || len(object) == 0 {
		return false
	}
	for _, raw := range object {
		node, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := node["class_type"].(string); !ok {
			return false
		}
		if _, ok := node["inputs"].(map[string]any); !ok {
			return false
		}
	}
	return true
}

// IsUIWorkflow reports whether the value is a ComfyUI UI-format workflow, which
// cannot be submitted to /prompt and must be re-exported in API format.
func IsUIWorkflow(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	_, hasNodes := object["nodes"].([]any)
	_, hasLinks := object["links"].([]any)
	return hasNodes && hasLinks
}

// DecodeGraph parses raw JSON bytes into a Graph.
func DecodeGraph(data []byte) (Graph, error) {
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return Graph(decoded), nil
}

// linkNodeID extracts the source node id from a ComfyUI link value like ["12", 0].
func linkNodeID(value any) (string, bool) {
	list, ok := value.([]any)
	if !ok || len(list) < 1 {
		return "", false
	}
	switch typed := list[0].(type) {
	case string:
		return typed, true
	case float64:
		return fmt.Sprintf("%d", int64(typed)), true
	}
	return "", false
}

// linkSlot extracts the source output slot from a link value.
func linkSlot(value any) (int, bool) {
	list, ok := value.([]any)
	if !ok || len(list) < 2 {
		return 0, false
	}
	switch typed := list[1].(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	}
	return 0, false
}

// asLink builds a ComfyUI link value: [nodeID, slot].
func asLink(nodeID string, slot int) []any {
	return []any{nodeID, slot}
}

// normalizeSlashes is the shared model-path comparison form.
func normalizeSlashes(value string) string {
	return strings.ReplaceAll(value, `\`, "/")
}
