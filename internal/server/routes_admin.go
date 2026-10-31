package server

import (
	"net/http"
	"strings"
	"time"

	"comfyui-gateway/internal/config"
	"comfyui-gateway/internal/httpx"
	"comfyui-gateway/internal/store"
	"comfyui-gateway/internal/sysinfo"
	"comfyui-gateway/internal/task"
	"comfyui-gateway/internal/workflow"
)

// routes registers the full HTTP surface.
func (s *Server) routes(mux *http.ServeMux) {
	s.routesGeneration(mux)
	s.routesRuns(mux)
	s.routesAdmin(mux)
}

// routesAdmin registers health, configuration and management endpoints.
func (s *Server) routesAdmin(mux *http.ServeMux) {
	s.route(mux, "GET /health", s.handleHealth)

	s.route(mux, "GET /api/setup/status", s.handleSetupStatus)
	s.route(mux, "POST /api/setup/model-alias", s.handleSetupModelAlias)

	s.route(mux, "GET /api/resources/status", s.handleResourcesStatus)
	s.route(mux, "POST /api/resources/refresh", s.handleResourcesRefresh)
	s.route(mux, "POST /api/resources/install", s.handleResourcesInstall)
	s.route(mux, "GET /api/resources/jobs/{id}", s.handleResourceJob)
	s.route(mux, "POST /api/resources/jobs/{id}/cancel", s.handleResourceJobCancel)
	s.route(mux, "POST /api/resources/pick-replacement", s.handleResourcePickReplacement)
	s.route(mux, "POST /api/resources/replacement", s.handleResourceReplacement)
	s.route(mux, "POST /api/resources/packs/{id}/main-model", s.handlePackMainModel)

	s.route(mux, "GET /api/workflows", s.handleWorkflows)
	s.route(mux, "GET /api/workflows/{id}", s.handleWorkflowGet)
	s.route(mux, "GET /api/workflows/{id}/schema", s.handleWorkflowSchema)
	s.route(mux, "POST /api/workflows/{id}/visibility", s.handleWorkflowVisibility)
	s.route(mux, "DELETE /api/workflows/{id}", s.handleWorkflowDelete)

	s.route(mux, "GET /v1/models", s.handleModels)
	s.route(mux, "GET /api/loras", s.handleLoras)
	s.route(mux, "GET /api/skin-unlocks", s.handleSkinUnlocksGet)
	s.route(mux, "POST /api/skin-unlocks", s.handleSkinUnlocksPost)

	s.route(mux, "GET /api/system/stats", s.handleSystemStats)
	s.route(mux, "POST /api/system/free-memory", s.handleFreeMemory)

	s.route(mux, "POST /api/session/open", s.handleSessionTouch)
	s.route(mux, "POST /api/session/heartbeat", s.handleSessionTouch)
	s.route(mux, "POST /api/session/close", s.handleSessionClose)
	s.route(mux, "GET /api/session/status", s.handleSessionStatus)

	s.route(mux, "POST /api/requests/{id}/cancel", s.handleRequestCancel)
	s.route(mux, "GET /api/tasks", s.handleTasks)
	s.route(mux, "GET /api/tasks/{id}", s.handleTaskGet)
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

// handleSetupStatus implements GET /api/setup/status.
func (s *Server) handleSetupStatus(ctx *httpx.Ctx) error {
	okJSON(ctx.W, map[string]any{
		"status":   s.setup.Status(),
		"comfyUrl": s.cfg.ComfyURL,
	})
	return nil
}

// handleSetupModelAlias implements POST /api/setup/model-alias.
func (s *Server) handleSetupModelAlias(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	status, err := s.setup.SetModelAlias(body)
	if err != nil {
		return err
	}
	okJSON(ctx.W, map[string]any{"status": status})
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

// handlePackMainModel implements POST /api/resources/packs/{id}/main-model.
func (s *Server) handlePackMainModel(ctx *httpx.Ctx) error {
	body, err := bodyOrEmpty(ctx)
	if err != nil {
		return err
	}
	status, err := s.resources.SetMainModel(ctx.R.PathValue("id"), httpx.String(body, "file"))
	if err != nil {
		return err
	}
	okJSON(ctx.W, map[string]any{"status": status})
	return nil
}

// handleWorkflows implements GET /api/workflows.
func (s *Server) handleWorkflows(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	projected := s.registry.List()

	type row struct {
		workflow.PublicEntry
		Enabled      bool        `json:"enabled"`
		Availability interface{} `json:"availability"`
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
		})
	}
	httpx.OK(ctx.W, map[string]any{"workflows": rows})
	return nil
}

// handleWorkflowGet implements GET /api/workflows/{id}.
func (s *Server) handleWorkflowGet(ctx *httpx.Ctx) error {
	entry := s.registry.Resolve(ctx.R.PathValue("id"))
	if entry == nil {
		return httpx.NotFound("workflow_not_found", "workflow not found")
	}
	httpx.OK(ctx.W, map[string]any{
		"workflow": workflow.PublicProjection(entry),
		"schema":   s.registry.Schema(entry),
	})
	return nil
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

// handleWorkflowDelete implements DELETE /api/workflows/{id}.
func (s *Server) handleWorkflowDelete(ctx *httpx.Ctx) error {
	removed, err := s.registry.Remove(ctx.R.PathValue("id"))
	if err != nil {
		return err
	}
	if !removed {
		return httpx.NotFound("workflow_not_found", "workflow not found")
	}
	okJSON(ctx.W, nil)
	return nil
}

// handleModels implements GET /v1/models.
func (s *Server) handleModels(ctx *httpx.Ctx) error {
	s.registry.ReloadIfChanged()
	loaderOptions := s.setup.LoaderOptions()

	data := make([]map[string]any, 0)
	for _, descriptor := range s.registry.ListModels(loaderOptions) {
		entry := descriptor.Entry
		if !s.setup.IsAvailable(descriptor.BaseID) {
			continue
		}
		if s.visibility.IsHidden(entry.ID) {
			continue
		}

		label := defaultStringOf(entry.Label, entry.Name)
		if entry.BaseModel != "" {
			label = defaultStringOf(entry.Label, entry.Name) + " · " + defaultStringOf(entry.BaseModelLabel, "variant")
		}

		metadata := map[string]any{
			"type":          entry.Type,
			"workflowId":    entry.ID,
			"label":         label,
			"imageInputs":   workflow.ImageInputCapacity(entry),
			"description":   entry.Description,
			"variantModes":  entry.VariantModes(),
			"variantLabels": entry.VariantLabelsMap(),
			"parameters":    s.enrichParameters(entry.Parameters),
			"availability":  s.setup.WorkflowStatus(descriptor.BaseID),
		}
		// Present but null when unset: the canvas distinguishes "no base model
		// concept" from "field missing" when deciding whether to show the picker.
		metadata["baseModel"] = nil
		metadata["baseModelLabel"] = nil
		if entry.BaseModel != "" {
			metadata["baseModel"] = entry.BaseModel
		}
		if entry.BaseModelLabel != "" {
			metadata["baseModelLabel"] = entry.BaseModelLabel
		}

		data = append(data, map[string]any{
			"id":       descriptor.ModelID,
			"object":   "model",
			"created":  0,
			"owned_by": "local-comfyui",
			"metadata": metadata,
		})
	}
	httpx.OK(ctx.W, map[string]any{"object": "list", "data": data})
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
func (s *Server) handleSystemStats(ctx *httpx.Ctx) error {
	sample := s.stats.Read(1500)

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
		"ok":       true,
		"at":       sample.At,
		"gpu":      sample.GPU,
		"gpuError": sample.GPUError,
		"ram":      sample.Ram,
		"comfy":    map[string]any{"url": s.cfg.ComfyURL, "reachable": s.setup.Status().ComfyReachable},
		"lastOOM":  lastOOM,
	}
	httpx.OK(ctx.W, payload)
	return nil
}

// handleFreeMemory implements POST /api/system/free-memory.
func (s *Server) handleFreeMemory(ctx *httpx.Ctx) error {
	if !s.setup.Status().ComfyReachable {
		return httpx.NewError(http.StatusServiceUnavailable, "comfy_unreachable", "ComfyUI is unreachable; cannot free VRAM")
	}

	before := s.stats.Read(0)
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
		after = s.stats.Read(0)
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

// handleSessionStatus implements GET /api/session/status.
func (s *Server) handleSessionStatus(ctx *httpx.Ctx) error {
	active := 0
	for _, stored := range s.tasks.List() {
		if stored.Status == "queued" || stored.Status == "running" {
			active++
		}
	}
	payload := sessionPayload(s.sessions.Status())
	payload["activeTasks"] = active
	okJSON(ctx.W, payload)
	return nil
}

// handleRequestCancel implements POST /api/requests/{id}/cancel.
func (s *Server) handleRequestCancel(ctx *httpx.Ctx) error {
	okJSON(ctx.W, map[string]any{"cancelled": s.cancelRequest(ctx.R.PathValue("id"))})
	return nil
}

// handleTasks implements GET /api/tasks.
func (s *Server) handleTasks(ctx *httpx.Ctx) error {
	list := s.tasks.List()
	if len(list) > 50 {
		list = list[:50]
	}
	rows := make([]map[string]any, 0, len(list))
	for _, stored := range list {
		rows = append(rows, task.PublicTask(stored))
	}
	httpx.OK(ctx.W, map[string]any{"tasks": rows})
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
