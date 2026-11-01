package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/store"
	"comfyui-gateway/internal/sysinfo"
	"comfyui-gateway/internal/util"
	"comfyui-gateway/internal/workflow"
)

// routes registers the full HTTP surface.
func (s *Server) routes(mux *http.ServeMux) {
	s.routesGeneration(mux)
	s.routesRuns(mux)
	s.routesAdmin(mux)
	// The agent's own protocol, outside `/api`: it is what AlayaCore talks to when a tool is called.
	s.routesMCP(mux)
}

// routesAdmin registers health, configuration and management endpoints.
func (s *Server) routesAdmin(mux *http.ServeMux) {
	s.route(mux, "GET /health", s.handleHealth)

	s.route(mux, "GET /api/resources/status", s.handleResourcesStatus)
	s.route(mux, "POST /api/resources/refresh", s.handleResourcesRefresh)
	s.route(mux, "POST /api/resources/install", s.handleResourcesInstall)
	s.route(mux, "GET /api/resources/jobs/{id}", s.handleResourceJob)
	s.route(mux, "POST /api/resources/jobs/{id}/cancel", s.handleResourceJobCancel)
	s.route(mux, "POST /api/resources/pick-replacement", s.handleResourcePickReplacement)
	s.route(mux, "POST /api/resources/replacement", s.handleResourceReplacement)

	s.route(mux, "GET /api/workflows", s.handleWorkflows)
	s.route(mux, "GET /api/workflows/{id}/schema", s.handleWorkflowSchema)
	s.route(mux, "POST /api/workflows/{id}/visibility", s.handleWorkflowVisibility)

	s.route(mux, "GET /api/loras", s.handleLoras)
	s.route(mux, "GET /api/skin-unlocks", s.handleSkinUnlocksGet)
	s.route(mux, "POST /api/skin-unlocks", s.handleSkinUnlocksPost)

	s.route(mux, "GET /api/system/stats", s.handleSystemStats)
	s.route(mux, "POST /api/system/free-memory", s.handleFreeMemory)

	s.route(mux, "POST /api/session/open", s.handleSessionTouch)
	s.route(mux, "POST /api/session/heartbeat", s.handleSessionTouch)
	s.route(mux, "POST /api/session/close", s.handleSessionClose)
}

// handleHealth implements GET /health, the canvas's liveness probe.
func (s *Server) handleHealth(ctx *httpx.Ctx) error {
	probeCtx, cancel := contextWithTimeout(1500)
	defer cancel()

	reachable := make(chan bool, 1)
	go func() { reachable <- s.comfy.IsReachable(probeCtx) }()

	comfyReachable := false
	select {
	case value := <-reachable:
		comfyReachable = value
	case <-time.After(1500 * time.Millisecond):
	}

	status := s.setup.Status()
	count := status.Counts.Total
	if count == 0 {
		count = s.registry.Len()
	}

	httpx.OK(ctx.W, map[string]any{
		"status":  "ok",
		"version": config.Version,
		"comfy": map[string]any{
			"url":       s.cfg.ComfyURL,
			"reachable": comfyReachable,
		},
		"workflows": map[string]any{"count": count},
		"setup":     map[string]any{"phase": status.Phase, "counts": status.Counts},
		"tasks": map[string]any{
			"count":          len(s.tasks.List()),
			"retentionDays":  s.cfg.TaskRetentionDays,
			"maxStoredTasks": s.cfg.MaxStoredTasks,
		},
		"uptimeSeconds": int64(time.Since(s.startedAt).Seconds()),
	})
	return nil
}

// handleResourcesStatus implements GET /api/resources/status.
func (s *Server) handleResourcesStatus(ctx *httpx.Ctx) error {
	httpx.OK(ctx.W, s.resources.Status())
	return nil
}

// handleResourcesRefresh implements POST /api/resources/refresh — the gateway's only rescan.
//
// One entry point on purpose. It reloads the resource index, re-scans ComfyUI (nodes, models,
// availability, and the graphs sitting in workflows/) and answers with the resource status the
// panel renders. A second "scan" route used to exist and behaved differently in two ways that
// mattered: it ran asynchronously, so the caller got the pre-scan status, and it skipped the
// resource index. Fewer paths, one behaviour.
func (s *Server) handleResourcesRefresh(ctx *httpx.Ctx) error {
	status, err := s.resources.Refresh()
	if err != nil {
		return err
	}
	httpx.OK(ctx.W, status)
	return nil
}

// handleResourcesInstall implements POST /api/resources/install.
func (s *Server) handleResourcesInstall(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	job, err := s.resources.Install(httpx.StringSlice(body, "ids"), httpx.StringSlice(body, "packs"))
	if err != nil {
		return err
	}
	okJSON(ctx.W, map[string]any{"job": job})
	return nil
}

// handleResourceJob implements GET /api/resources/jobs/{id}.
func (s *Server) handleResourceJob(ctx *httpx.Ctx) error {
	job := s.resources.GetJob(ctx.R.PathValue("id"))
	if job == nil {
		return httpx.NotFound("job_not_found", "task not found")
	}
	okJSON(ctx.W, map[string]any{"job": job})
	return nil
}

// handleResourceJobCancel implements POST /api/resources/jobs/{id}/cancel.
func (s *Server) handleResourceJobCancel(ctx *httpx.Ctx) error {
	job := s.resources.CancelJob(ctx.R.PathValue("id"))
	if job == nil {
		return httpx.NotFound("job_not_found", "task not found")
	}
	okJSON(ctx.W, map[string]any{"job": job})
	return nil
}

// handleResourcePickReplacement implements POST /api/resources/pick-replacement.
func (s *Server) handleResourcePickReplacement(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	file, err := s.resources.PickResourceReplacement(httpx.String(body, "resourceId"))
	if err != nil {
		return err
	}
	okJSON(ctx.W, map[string]any{"file": file})
	return nil
}

// handleResourceReplacement implements POST /api/resources/replacement.
func (s *Server) handleResourceReplacement(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	status, err := s.resources.SetResourceReplacement(httpx.String(body, "resourceId"), httpx.String(body, "file"))
	if err != nil {
		return err
	}
	okJSON(ctx.W, map[string]any{"status": status})
	return nil
}

// handleWorkflows implements GET /api/workflows.
//
// One row per workflow, carrying everything a client needs to offer it: the media kind, the typed
// inputs, whether ComfyUI can run it, and the model ids it answers to. Those ids are not a second
// concept — a workflow is the thing that runs, and a base model is one of its inputs — but the id
// *spelling* (`img-zit-int8`) is built by the gateway and resolved by the gateway, so the gateway has
// to report it. `/v1/models` used to be the only place that happened, wrapped in an OpenAI envelope.
func (s *Server) handleWorkflows(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	projected := s.registry.List()
	models := s.workflowModels()

	type row struct {
		workflow.PublicEntry
		Enabled      bool             `json:"enabled"`
		Availability interface{}      `json:"availability"`
		Models       []map[string]any `json:"models"`
	}
	rows := make([]row, 0, len(projected))
	for _, entry := range projected {
		// The parameters carry the loader's option lists here, so one request describes a
		// workflow completely: a picker of upscale models or checkpoint names is data the list
		// already has, and asking per workflow would be a second round trip for it. Clients
		// that only want names ignore the field.
		entry.Parameters = s.enrichParameters(entry.Parameters)
		rows = append(rows, row{
			PublicEntry:  entry,
			Enabled:      !s.visibility.IsHidden(entry.ID),
			Availability: s.setup.WorkflowStatus(entry.ID),
			Models:       models[entry.ID],
		})
	}
	httpx.OK(ctx.W, map[string]any{"workflows": rows})
	return nil
}

// workflowModels lists the model ids each workflow answers to, keyed by workflow id.
//
// The set is the workflow itself plus one id per base model installed here, produced by the same
// expansion `ResolveModel` accepts, so a client can send back exactly what it was handed. A workflow
// whose default base model is not installed contributes nothing: it cannot run, and offering it would
// only fail later.
func (s *Server) workflowModels() map[string][]map[string]any {
	rows := make(map[string][]map[string]any)
	for _, descriptor := range s.registry.ListModels(s.setup.LoaderOptions()) {
		entry := descriptor.Entry
		model := map[string]any{
			"id":             descriptor.ModelID,
			"label":          defaultStringOf(entry.Label, entry.Name),
			"imageInputs":    workflow.ImageInputCapacity(entry),
			"baseModel":      nil,
			"baseModelLabel": nil,
		}
		if entry.BaseModel != "" {
			model["baseModel"] = entry.BaseModel
			model["baseModelLabel"] = defaultStringOf(entry.BaseModelLabel, "variant")
			model["label"] = defaultStringOf(entry.Label, entry.Name) + " · " + defaultStringOf(entry.BaseModelLabel, "variant")
		}
		rows[descriptor.BaseID] = append(rows[descriptor.BaseID], model)
	}
	return rows
}

// handleWorkflowSchema implements GET /api/workflows/{id}/schema.
func (s *Server) handleWorkflowSchema(ctx *httpx.Ctx) error {
	found := s.registry.ResolveModel(ctx.R.PathValue("id"), s.setup.LoaderOptions())
	if found == nil {
		return httpx.NotFound("workflow_not_found", "workflow not found")
	}
	schema := s.registry.Schema(found.Entry)
	schema.Parameters = s.enrichParameters(schema.Parameters)
	httpx.OK(ctx.W, map[string]any{"schema": schema})
	return nil
}

// handleWorkflowVisibility implements POST /api/workflows/{id}/visibility.
func (s *Server) handleWorkflowVisibility(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	entry := s.registry.Resolve(ctx.R.PathValue("id"))
	if entry == nil {
		return httpx.NotFound("workflow_not_found", "workflow not found")
	}
	enabled := true
	if value, ok := body["enabled"].(bool); ok && !value {
		enabled = false
	}
	if err := s.visibility.SetHidden(entry.ID, !enabled); err != nil {
		return err
	}
	s.logger.Info("workflow visibility %s -> %s", entry.ID, map[bool]string{true: "enabled", false: "hidden"}[enabled])
	okJSON(ctx.W, map[string]any{"id": entry.ID, "enabled": enabled})
	return nil
}

// handleLoras implements GET /api/loras.
func (s *Server) handleLoras(ctx *httpx.Ctx) error {
	query := strings.ToLower(strings.TrimSpace(ctx.R.URL.Query().Get("q")))
	names := make([]string, 0)
	for _, name := range s.setup.LoraNames() {
		if query == "" || strings.Contains(strings.ToLower(name), query) {
			names = append(names, name)
		}
	}
	okJSON(ctx.W, map[string]any{"loras": names})
	return nil
}

// handleSkinUnlocksGet implements GET /api/skin-unlocks.
func (s *Server) handleSkinUnlocksGet(ctx *httpx.Ctx) error {
	okJSON(ctx.W, map[string]any{
		"unlocked": s.skins.List(),
		"entries":  s.skins.Entries(),
	})
	return nil
}

// handleSkinUnlocksPost implements POST /api/skin-unlocks.
func (s *Server) handleSkinUnlocksPost(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	entry, err := s.skins.Unlock(httpx.String(body, "skinId"))
	if err != nil {
		return err
	}
	okJSON(ctx.W, map[string]any{
		"unlocked": s.skins.List(),
		"entry":    entry,
	})
	return nil
}

// handleSystemStats implements GET /api/system/stats.
//
// Every number here comes from ComfyUI's own /system_stats: the panel is asked about the machine
// that does the generating, and the gateway's host is only interesting when it is that machine —
// which is not a question this handler can answer, so it does not try. When ComfyUI does not say,
// `available` is false and the panel shows nothing rather than its own host's figures.
func (s *Server) handleSystemStats(ctx *httpx.Ctx) error {
	requestCtx, cancel := contextWithTimeout(2000)
	defer cancel()
	sample, _ := s.comfyHostStats(requestCtx)

	s.lastOOM.Lock()
	lastOOM := map[string]any(nil)
	if s.lastOOMAt != "" {
		lastOOM = map[string]any{"at": s.lastOOMAt, "message": s.lastOOMText}
	}
	s.lastOOM.Unlock()

	if lastOOM == nil {
		for _, stored := range s.tasks.List() {
			if stored.Error == nil || !isOOM(*stored.Error) {
				continue
			}
			at := ""
			if stored.CompletedAt != nil {
				at = *stored.CompletedAt
			}
			lastOOM = map[string]any{"at": at, "message": *stored.Error}
			break
		}
	}

	payload := map[string]any{
		"ok":        true,
		"at":        util.NowIso(),
		"available": sample != nil,
		"gpu":       []sysinfo.GPU{},
		"ram":       sysinfo.Ram{},
		"comfy":     map[string]any{"url": s.cfg.ComfyURL, "reachable": s.setup.Status().ComfyReachable},
		"lastOOM":   lastOOM,
	}
	if sample != nil {
		payload["at"] = sample.At
		payload["gpu"] = sample.GPU
		payload["ram"] = sample.Ram
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handleFreeMemory implements POST /api/system/free-memory.
func (s *Server) handleFreeMemory(ctx *httpx.Ctx) error {
	if !s.setup.Status().ComfyReachable {
		return httpx.NewError(http.StatusServiceUnavailable, "comfy_unreachable", "ComfyUI is unreachable; cannot free VRAM")
	}

	sampleCtx, cancelSample := contextWithTimeout(2000)
	before, err := s.comfyHostStats(sampleCtx)
	cancelSample()
	if err != nil {
		return httpx.NewError(http.StatusServiceUnavailable, "comfy_stats_unavailable", "ComfyUI did not report its memory")
	}
	started := time.Now()

	requestCtx, cancel := contextWithTimeout(15000)
	defer cancel()
	if err := s.comfy.FreeMemory(requestCtx); err != nil {
		return err
	}

	beforeVRAM := gpuUsedMB(before)
	after := before
	lastVRAM := beforeVRAM
	stable := 0
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		pollCtx, cancelPoll := contextWithTimeout(2000)
		after, err = s.comfyHostStats(pollCtx)
		cancelPoll()
		if err != nil || after == nil {
			after = before
			break
		}
		used := gpuUsedMB(after)
		if beforeVRAM == 0 || used <= maxIntInt(beforeVRAM-256, 0) {
			break
		}
		if absInt(used-lastVRAM) <= 8 {
			stable++
		} else {
			stable = 0
		}
		lastVRAM = used
		if stable >= 2 && time.Since(started) >= 900*time.Millisecond {
			break
		}
	}

	afterVRAM := gpuUsedMB(after)
	freedRAM := before.Ram.UsedBytes - after.Ram.UsedBytes
	if freedRAM < 0 {
		freedRAM = 0
	}

	okJSON(ctx.W, map[string]any{
		"before":        before,
		"after":         after,
		"freedVramMb":   maxIntInt(beforeVRAM-afterVRAM, 0),
		"freedRamBytes": freedRAM,
		"durationMs":    time.Since(started).Milliseconds(),
	})
	return nil
}

// handleSessionTouch implements open/heartbeat.
func (s *Server) handleSessionTouch(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	status := s.sessions.Touch(httpx.String(body, "id"))
	okJSON(ctx.W, sessionPayload(status))
	return nil
}

// handleSessionClose implements POST /api/session/close.
func (s *Server) handleSessionClose(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	status := s.sessions.Close(httpx.String(body, "id"))
	okJSON(ctx.W, sessionPayload(status))
	return nil
}

// sessionPayload renders a session snapshot without relying on struct tags.
func sessionPayload(status store.SessionStatus) map[string]any {
	payload := map[string]any{
		"count":     status.Count,
		"seen":      status.Seen,
		"idleMs":    status.IdleMs,
		"staleMs":   status.StaleMs,
		"startedAt": status.StartedAt,
		"closedAt":  status.ClosedAt,
	}
	if status.LastSeenAt != nil {
		payload["lastSeenAt"] = *status.LastSeenAt
	}
	return payload
}

// comfyHostStats asks ComfyUI about its own machine. Nil means it did not answer usefully.
func (s *Server) comfyHostStats(ctx context.Context) (*sysinfo.Stats, error) {
	payload, err := s.comfy.SystemStats(ctx)
	if err != nil {
		return nil, err
	}
	stats := sysinfo.FromComfy(payload)
	if stats == nil {
		return nil, errors.New("ComfyUI reported no device memory")
	}
	return stats, nil
}

func gpuUsedMB(sample *sysinfo.Stats) int {
	if sample == nil || len(sample.GPU) == 0 {
		return 0
	}
	return sample.GPU[0].MemoryUsedMb
}

func isOOM(message string) bool {
	lowered := strings.ToLower(message)
	return strings.Contains(lowered, "out of memory") ||
		strings.Contains(lowered, "cuda oom") ||
		strings.Contains(lowered, "allocation on device")
}

func maxIntInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
