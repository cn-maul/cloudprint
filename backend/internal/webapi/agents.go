package webapi

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"cloudprint/internal/auth"
	"cloudprint/internal/printer/discovery"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.Store.ListAgents(r.Context())
	if err != nil {
		s.jsonError(w, err)
		return
	}
	printers, _ := s.Store.ListPrinters(r.Context(), true)
	active := make([]store.Agent, 0, len(agents))
	for i := range agents {
		for _, p := range printers {
			for _, ep := range p.Endpoints {
				if ep.AgentID == agents[i].ID && ep.Kind == "agent" && ep.IsEnabled {
					agents[i].EndpointIDs = append(agents[i].EndpointIDs, ep.ID)
					agents[i].PrinterIDs = appendUnique(agents[i].PrinterIDs, p.ID)
				}
			}
		}
		// A retired helper (after "重新接入") is inactive and owns no enabled
		// endpoint. Showing it would make the page list the same Windows PC
		// twice, once offline forever.
		if agents[i].IsActive && len(agents[i].PrinterIDs) > 0 {
			active = append(active, agents[i])
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": active})
}

func appendUnique(list []string, value string) []string {
	for _, v := range list {
		if v == value {
			return list
		}
	}
	return append(list, value)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name         string `json:"name"`
		PrintCommand string `json:"print_command"`
		PrinterID    string `json:"printer_id"`
		QueueName    string `json:"queue_name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "NAME_REQUIRED", "代理名称不能为空")
		return
	}
	printerID := strings.TrimSpace(in.PrinterID)
	if printerID == "" {
		writeErr(w, http.StatusBadRequest, "PRINTER_REQUIRED",
			"请在“打印机管理”里添加打印机，系统会自动创建所需的 Windows 助手")
		return
	}
	p, err := s.Store.GetPrinter(r.Context(), printerID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	command := strings.TrimSpace(in.PrintCommand)
	if command == "" {
		if q := strings.TrimSpace(in.QueueName); q != "" {
			command = fmt.Sprintf(`powershell -NoProfile -Command Start-Process -FilePath %%s -Verb PrintTo -ArgumentList '%s'`,
				strings.ReplaceAll(q, "'", "''"))
		} else {
			command = `powershell -NoProfile -Command Start-Process -FilePath %s -Verb Print`
		}
	}
	token := auth.RandomToken(32)
	a, err := s.Store.CreateAgent(r.Context(), in.Name, auth.HashToken(token), command)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeErr(w, http.StatusConflict, "AGENT_EXISTS", "代理名称已存在")
			return
		}
		s.jsonError(w, err)
		return
	}
	ep, err := s.Store.CreateEndpoint(r.Context(), p.ID, store.NewEndpoint{
		Kind: "agent", URI: "agent://cloudprint/" + p.ID, Priority: 100, AgentID: a.ID,
	})
	if err != nil {
		_ = s.Store.DeleteAgent(r.Context(), a.ID)
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "create_agent", "agent", a.ID, map[string]string{"name": a.Name})
	writeJSON(w, http.StatusCreated, map[string]any{
		"agent":       a,
		"token":       token,
		"endpoint_id": ep.ID,
		"warning":     "安装命令只显示一次，请立即使用。",
	})
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeleteAgent(r.Context(), id); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "delete_agent", "agent", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleRotateAgentToken re-issues the install command. The plaintext token is
// never stored, so this is the only honest way to recover a lost one.
func (s *Server) handleRotateAgentToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, err := s.Store.GetAgent(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "AGENT_NOT_FOUND", "打印助手不存在")
		return
	}
	var endpointID string
	printers, _ := s.Store.ListPrinters(r.Context(), true)
	for _, p := range printers {
		for _, ep := range p.Endpoints {
			if ep.AgentID == id {
				endpointID = ep.ID
			}
		}
	}
	if endpointID == "" {
		writeErr(w, http.StatusConflict, "AGENT_UNBOUND",
			"这个助手还没有绑定打印机。请到“打印机管理”重新接入。")
		return
	}
	token := auth.RandomToken(32)
	if err := s.Store.UpdateAgentToken(r.Context(), id, auth.HashToken(token)); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "rotate_agent_token", "agent", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":        a,
		"token":        token,
		"endpoint_id":  endpointID,
		"setup_script": agentSetupScript(s.baseURL(r), token, endpointID),
		"message":      "旧密钥已作废，请用新命令重新安装或重启助手。",
	})
}

func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	var in struct {
		Version string `json:"version"`
		Queues  any    `json:"queues"`
		Status  any    `json:"status"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.Store.HeartbeatAgent(r.Context(), a.ID, in.Version, in.Queues, in.Status); err != nil {
		s.jsonError(w, err)
		return
	}
	// Make the printer usable immediately after the helper starts, instead of
	// waiting for the next full probe interval.
	go s.Printer.RefreshAgentEndpoints(context.Background())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server_time": storeNow()})
}

func (s *Server) handleAgentClaim(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	var in struct {
		EndpointID   string `json:"endpoint_id"`
		LeaseSeconds int    `json:"lease_seconds"`
	}
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &in) {
			return
		}
	}
	ep, err := s.Store.GetEndpoint(r.Context(), in.EndpointID)
	if err != nil || ep.AgentID != a.ID {
		writeErr(w, http.StatusForbidden, "ENDPOINT_FORBIDDEN", "该端点不属于当前代理")
		return
	}
	lease := time.Duration(in.LeaseSeconds) * time.Second
	if lease < 30*time.Second || lease > 30*time.Minute {
		lease = 15 * time.Minute
	}
	job, err := s.Store.ClaimJob(r.Context(), ep.PrinterID, ep.ID, a.ID, lease)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	if job == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.Store.RecordEndpointEvent(r.Context(), ep.ID, "agent_claim", map[string]string{"job": job.ID})
	s.Hub.Publish(job.UserID, "job.claimed", job)
	base := s.baseURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"job":           job,
		"download_url":  base + "/api/agent/files/" + job.PDFFile,
		"lease_seconds": int(lease.Seconds()),
		"print_command": a.PrintCommand,
	})
}

func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	id := chi.URLParam(r, "id")
	job, err := s.Store.GetJob(r.Context(), id)
	if err != nil || job.AgentID != a.ID {
		writeErr(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	if err := s.Store.MarkPrinting(r.Context(), id, job.EndpointID); err != nil {
		writeErr(w, http.StatusConflict, "STATE_CONFLICT", err.Error())
		return
	}
	s.Hub.Publish(job.UserID, "job.printing", job)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentResult(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	id := chi.URLParam(r, "id")
	var in struct {
		Success      bool   `json:"success"`
		ErrorCode    string `json:"error_code"`
		ErrorMessage string `json:"error_message"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	job, err := s.Store.GetJob(r.Context(), id)
	if err != nil || job.AgentID != a.ID {
		writeErr(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	if err := s.Store.CompleteJob(r.Context(), id, job.EndpointID, in.Success, in.ErrorCode, in.ErrorMessage); err != nil {
		writeErr(w, http.StatusConflict, "STATE_CONFLICT", err.Error())
		return
	}
	updated, _ := s.Store.GetJob(r.Context(), id)
	if in.Success {
		s.Hub.Publish(job.UserID, "job.completed", updated)
	} else {
		s.Hub.Publish(job.UserID, "job.failed", updated)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentFile(w http.ResponseWriter, r *http.Request) {
	a := currentAgent(r)
	id := chi.URLParam(r, "id")
	f, err := s.Store.GetFile(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在")
		return
	}
	if !s.agentMayAccessFile(r.Context(), a.ID, id) {
		writeErr(w, http.StatusForbidden, "FILE_FORBIDDEN", "该文件未被分配给当前代理任务")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=\"print.pdf\"")
	http.ServeFile(w, r, f.StoragePath)
}

func (s *Server) agentMayAccessFile(ctx context.Context, agentID, fileID string) bool {
	var n int
	err := s.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM print_jobs
		WHERE agent_id = ? AND pdf_file_id = ? AND status IN ('claimed','printing')`,
		agentID, fileID).Scan(&n)
	return err == nil && n > 0
}

func (s *Server) baseURL(r *http.Request) string {
	if s.Cfg.PublicURL != "" {
		return s.Cfg.PublicURL
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if s.Cfg.TrustedProxyHeaders {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
	}
	host := r.Host
	// Browser-originated install scripts must not point at 127.0.0.1 or
	// localhost: the helper runs on another computer.
	if h, port, err := net.SplitHostPort(r.Host); err == nil {
		if h == "127.0.0.1" || h == "::1" || strings.EqualFold(h, "localhost") {
			if lan := discovery.LocalAddress(); lan != "" {
				host = net.JoinHostPort(lan, port)
			}
		}
	}
	return scheme + "://" + host
}

func storeNow() string { return time.Now().UTC().Format(time.RFC3339) }
