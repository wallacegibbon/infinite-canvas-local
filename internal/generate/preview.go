package generate

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"sync"
	"time"

	"comfyui-gateway/internal/comfy"
	"comfyui-gateway/internal/logx"
)

// Events emitted by ComfyUI's /ws that carry information we cannot get from
// /history. Both come from third-party nodes, hence the odd names.
const (
	eventPreviewOverride = "kj_preview_override"
	eventProgressState   = "progress_state"

	defaultPreviewTTL = 10 * time.Minute
	idleSocketTTL     = 5 * time.Minute
)

// Preview is the latest in-progress frame for one prompt.
type Preview struct {
	Data      string
	Mime      string
	Step      *int
	Total     *int
	NodeID    string
	UpdatedAt time.Time
}

// Progress is the reconstructed step progress for one prompt.
type Progress struct {
	PromptID      string
	RequestID     string
	Total         int
	PlanNodes     map[string]int
	AbsoluteNodes map[string]bool
	NodeRatios    map[string]float64
	NodeValues    map[string]float64
	FallbackStep  float64
	FallbackTotal int
	Step          int
	State         string
	UpdatedAt     time.Time
}

// Hub relays ComfyUI preview frames and progress events to the canvas.
//
// ComfyUI pushes these over a WebSocket keyed by client_id, while the canvas polls
// plain HTTP endpoints. The hub is the bridge, and it is entirely best-effort:
// losing the socket must never fail a generation.
type Hub struct {
	comfyURL string
	logger   *logx.Logger
	ttl      time.Duration

	mu               sync.RWMutex
	previews         map[string]*Preview
	progressByPrompt map[string]*Progress
	promptByRequest  map[string]string
	sockets          map[string]*socketEntry
}

type socketEntry struct {
	conn      *comfy.WSConn
	promptID  string
	requestID string
	plan      StepPlan
	lastSeen  time.Time
	cancel    context.CancelFunc
}

// NewHub builds a preview hub for one ComfyUI instance.
func NewHub(comfyURL string, logger *logx.Logger) *Hub {
	hub := &Hub{
		comfyURL:         comfyURL,
		logger:           logger,
		ttl:              defaultPreviewTTL,
		previews:         map[string]*Preview{},
		progressByPrompt: map[string]*Progress{},
		promptByRequest:  map[string]string{},
		sockets:          map[string]*socketEntry{},
	}
	go hub.reapLoop()
	return hub
}

// Ensure opens (or reuses) the ComfyUI WebSocket for a client id.
//
// Failure is not an error: previews are a nicety, and generation proceeds without
// them. That is why this returns a bool instead of an error.
func (h *Hub) Ensure(ctx context.Context, clientID string) bool {
	if clientID == "" {
		return false
	}
	h.mu.RLock()
	_, connected := h.sockets[clientID]
	h.mu.RUnlock()
	if connected {
		return true
	}

	conn, err := comfy.DialWS(ctx, h.wsEndpoint(clientID))
	if err != nil {
		h.logger.Warn("preview WebSocket connection failed: %s", err)
		return false
	}

	socketCtx, cancel := context.WithCancel(context.Background())
	entry := &socketEntry{conn: conn, lastSeen: time.Now(), cancel: cancel}

	h.mu.Lock()
	h.sockets[clientID] = entry
	h.mu.Unlock()

	go h.readLoop(socketCtx, clientID, entry)
	return true
}

// wsEndpoint builds the ComfyUI /ws URL for a client id.
func (h *Hub) wsEndpoint(clientID string) string {
	parsed, err := url.Parse(h.comfyURL)
	if err != nil {
		return h.comfyURL + "/ws"
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = "/ws"
	parsed.RawQuery = url.Values{"clientId": []string{clientID}}.Encode()
	return parsed.String()
}

// readLoop consumes events until the socket dies.
func (h *Hub) readLoop(ctx context.Context, clientID string, entry *socketEntry) {
	defer func() {
		h.mu.Lock()
		if current, ok := h.sockets[clientID]; ok && current == entry {
			delete(h.sockets, clientID)
		}
		h.mu.Unlock()
		_ = entry.conn.Close()
	}()

	// Ask ComfyUI to include step metadata in preview frames.
	_ = entry.conn.WriteText([]byte(`{"type":"feature_flags","data":{"supports_preview_metadata":true}}`))

	for {
		if ctx.Err() != nil {
			return
		}
		message, err := entry.conn.ReadMessage(ctx)
		if err != nil {
			return
		}
		h.handleMessage(clientID, entry, message)
	}
}

// handleMessage dispatches one decoded ComfyUI event.
func (h *Hub) handleMessage(clientID string, entry *socketEntry, raw []byte) {
	var message struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &message); err != nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	switch message.Type {
	case eventPreviewOverride:
		var data struct {
			Image  string   `json:"image"`
			Mime   string   `json:"mime"`
			Step   *float64 `json:"step"`
			Total  *float64 `json:"total"`
			NodeID string   `json:"node_id"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			return
		}
		h.handlePreviewLocked(entry, data.Image, data.Mime, data.Step, data.Total, data.NodeID)

	case eventProgressState:
		var data struct {
			PromptID string `json:"prompt_id"`
			Nodes    map[string]struct {
				NodeID  any     `json:"node_id"`
				Value   float64 `json:"value"`
				Max     float64 `json:"max"`
				Display any     `json:"display_node_id"`
			} `json:"nodes"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil || data.PromptID == "" {
			return
		}
		record := h.ensureProgressLocked(entry, data.PromptID)
		for key, node := range data.Nodes {
			nodeID := coerceNodeID(node.NodeID)
			if nodeID == "" {
				nodeID = coerceNodeID(node.Display)
			}
			if nodeID == "" {
				nodeID = key
			}
			planned, tracked := record.PlanNodes[nodeID]
			if !tracked {
				continue
			}
			if node.Max <= 0 {
				continue
			}
			if record.AbsoluteNodes[nodeID] {
				if node.Value > record.NodeValues[nodeID] {
					record.NodeValues[nodeID] = node.Value
				}
				continue
			}
			ratio := clamp01(node.Value / node.Max)
			if ratio > record.NodeRatios[nodeID] {
				record.NodeRatios[nodeID] = ratio
			}
			_ = planned
		}
		record.State = "running"
		record.UpdatedAt = time.Now()
		computeStep(record)

	case "execution_success":
		h.finishLocked(entry, message.Data, "done")
	case "execution_error":
		h.finishLocked(entry, message.Data, "error")
	case "execution_interrupted":
		h.finishLocked(entry, message.Data, "cancelled")
	default:
		return
	}
	entry.lastSeen = time.Now()
}

// handlePreviewLocked stores a preview frame and refreshes step progress.
func (h *Hub) handlePreviewLocked(entry *socketEntry, image, mime string, step, total *float64, nodeID string) {
	if entry.promptID == "" || image == "" {
		return
	}
	if mime == "" {
		mime = "image/jpeg"
	}
	preview := &Preview{
		Data:      image,
		Mime:      mime,
		NodeID:    nodeID,
		UpdatedAt: time.Now(),
	}
	if step != nil {
		value := int(*step)
		preview.Step = &value
	}
	if total != nil {
		value := int(*total)
		preview.Total = &value
	}
	h.previews[entry.promptID] = preview

	// Nodes that report (step,total) but have no plan entry still deserve a bar.
	if step != nil && total != nil && *total > 0 {
		if record, ok := h.progressByPrompt[entry.promptID]; ok && record.Total == 0 {
			record.FallbackStep = *step
			record.FallbackTotal = int(*total)
			record.Total = int(*total)
			record.State = "running"
			record.UpdatedAt = time.Now()
			computeStep(record)
		}
	}
}

func (h *Hub) ensureProgressLocked(entry *socketEntry, promptID string) *Progress {
	if record, ok := h.progressByPrompt[promptID]; ok {
		return record
	}
	return h.createProgressLocked(entry, promptID)
}

func (h *Hub) createProgressLocked(entry *socketEntry, promptID string) *Progress {
	record := &Progress{
		PromptID:      promptID,
		RequestID:     entry.requestID,
		Total:         entry.plan.Total,
		PlanNodes:     entry.plan.Steps,
		AbsoluteNodes: entry.plan.Absolute,
		NodeRatios:    map[string]float64{},
		NodeValues:    map[string]float64{},
		State:         "queued",
		UpdatedAt:     time.Now(),
	}
	if record.PlanNodes == nil {
		record.PlanNodes = map[string]int{}
	}
	if record.AbsoluteNodes == nil {
		record.AbsoluteNodes = map[string]bool{}
	}
	h.progressByPrompt[promptID] = record
	if record.RequestID != "" {
		h.promptByRequest[record.RequestID] = promptID
	}
	return record
}

func (h *Hub) finishLocked(entry *socketEntry, raw json.RawMessage, state string) {
	var data struct {
		PromptID string `json:"prompt_id"`
	}
	_ = json.Unmarshal(raw, &data)
	promptID := data.PromptID
	if promptID == "" {
		promptID = entry.promptID
	}
	record, ok := h.progressByPrompt[promptID]
	if !ok {
		return
	}
	record.State = state
	if record.Total > 0 {
		record.Step = record.Total
	}
	record.UpdatedAt = time.Now()
}

// computeStep folds per-node ratios into one global step number.
func computeStep(record *Progress) {
	if len(record.PlanNodes) > 0 {
		steps := 0.0
		for nodeID, planned := range record.PlanNodes {
			if record.AbsoluteNodes[nodeID] {
				value := record.NodeValues[nodeID]
				if value > float64(planned) {
					value = float64(planned)
				}
				if value > 0 {
					steps += value
				}
				continue
			}
			steps += clamp01(record.NodeRatios[nodeID]) * float64(planned)
		}
		step := int(jsRound(steps))
		if step > record.Total {
			step = record.Total
		}
		record.Step = step
		return
	}
	if record.FallbackTotal > 0 {
		step := int(record.FallbackStep)
		if step < 0 {
			step = 0
		}
		if step > record.FallbackTotal {
			step = record.FallbackTotal
		}
		record.Step = step
	}
}

// BindPrompt associates a submitted prompt with the socket that will report on it.
func (h *Hub) BindPrompt(clientID, promptID, requestID string, plan StepPlan) {
	if promptID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.sockets[clientID]
	if !ok {
		return
	}
	entry.promptID = promptID
	entry.requestID = requestID
	entry.plan = plan
	h.createProgressLocked(entry, promptID)
}

// Get returns the newest preview for a prompt, honoring the TTL.
func (h *Hub) Get(promptID string) *Preview {
	h.mu.Lock()
	defer h.mu.Unlock()
	preview, ok := h.previews[promptID]
	if !ok {
		return nil
	}
	if time.Since(preview.UpdatedAt) > h.ttl {
		delete(h.previews, promptID)
		return nil
	}
	return preview
}

// GetProgress returns the progress record for a prompt, honoring the TTL.
func (h *Hub) GetProgress(promptID string) *Progress {
	h.mu.Lock()
	defer h.mu.Unlock()
	record, ok := h.progressByPrompt[promptID]
	if !ok {
		return nil
	}
	if time.Since(record.UpdatedAt) > h.ttl {
		delete(h.progressByPrompt, promptID)
		return nil
	}
	return record
}

// GetByRequest returns the progress record for a gateway request id.
func (h *Hub) GetByRequest(requestID string) *Progress {
	h.mu.RLock()
	promptID, ok := h.promptByRequest[requestID]
	h.mu.RUnlock()
	if !ok {
		return nil
	}
	return h.GetProgress(promptID)
}

// reapLoop closes idle sockets and drops expired records.
func (h *Hub) reapLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.closeIdle()
	}
}

func (h *Hub) closeIdle() {
	now := time.Now()
	h.mu.Lock()
	defer h.mu.Unlock()

	for clientID, entry := range h.sockets {
		if now.Sub(entry.lastSeen) < idleSocketTTL {
			continue
		}
		if entry.cancel != nil {
			entry.cancel()
		}
		delete(h.sockets, clientID)
	}
	for promptID, preview := range h.previews {
		if now.Sub(preview.UpdatedAt) > h.ttl {
			delete(h.previews, promptID)
		}
	}
	for promptID, record := range h.progressByPrompt {
		if now.Sub(record.UpdatedAt) <= h.ttl {
			continue
		}
		delete(h.progressByPrompt, promptID)
		if record.RequestID != "" {
			delete(h.promptByRequest, record.RequestID)
		}
	}
}

// coerceNodeID normalizes the loosely typed node ids ComfyUI sends.
func coerceNodeID(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	}
	return ""
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
