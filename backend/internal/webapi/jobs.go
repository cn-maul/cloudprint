package webapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloudprint/internal/printer"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var in store.NewJob
	if !decodeJSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	in.UserID = u.ID
	_, err := s.Store.GetPrinter(r.Context(), in.PrinterID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	allowed, err := s.Store.UserCanUsePrinter(r.Context(), u.ID, u.Role, in.PrinterID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "PRINTER_FORBIDDEN", "你所在的用户组没有这台打印机的使用权限")
		return
	}
	p, err := s.Store.GetPrinter(r.Context(), in.PrinterID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	file, err := s.Store.GetFile(r.Context(), in.FileID)
	if err != nil || file.OwnerID != u.ID {
		writeErr(w, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在或已过期")
		return
	}
	if in.Copies <= 0 {
		in.Copies = 1
	}
	required := file.PageCount * in.Copies
	used, err := s.Store.PagesToday(r.Context(), u.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	if u.DailyPageQuota > 0 && used+required > u.DailyPageQuota {
		writeErr(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED",
			fmt.Sprintf("今日剩余 %d 页，本次需要 %d 页。", maxInt(u.DailyPageQuota-used, 0), required))
		return
	}
	if ep := printer.SelectEndpoint(p, in.Duplex, false); ep == nil {
		// Queueing a job on a printer nobody can drive leaves the user staring
		// at "waiting to print" forever. Say exactly which part is offline.
		msg := "这台打印机现在连不上。请稍后再试，或联系管理员检查设备电源和网络。"
		code := "PRINTER_OFFLINE"
		for _, candidate := range p.Endpoints {
			if candidate.IsEnabled && candidate.Kind == "agent" && candidate.Health != "healthy" {
				msg = "转发用的 Windows 电脑上没有运行打印助手。请让管理员在那台电脑上启动助手后再打印。"
				code = "HELPER_OFFLINE"
				break
			}
		}
		writeErr(w, http.StatusConflict, code, msg)
		return
	}
	job, err := s.Store.CreateJob(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "JOB_REJECTED", err.Error())
		return
	}
	s.Store.Audit(r.Context(), u.ID, "submit", "job", job.ID, map[string]any{
		"printer_id": job.PrinterID, "file_id": job.PDFFile, "copies": job.Copies,
		"duplex": job.Duplex, "pages": job.TotalPages,
	})
	s.Log.Info("job queued", "job", job.ID, "printer", job.PrinterID, "user", u.Username)
	s.Hub.Publish(job.UserID, "job.created", job)
	go s.tryDispatch(job.ID, u.ID)
	writeJSON(w, http.StatusCreated, job)
}

func (s *Server) tryDispatch(jobID, userID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	job, err := s.Store.GetJob(ctx, jobID)
	if err != nil || job.Status != "queued" {
		return
	}
	busy, err := s.Store.HasActiveJob(ctx, job.PrinterID)
	if err != nil || busy {
		return
	}
	p, err := s.Store.GetPrinter(ctx, job.PrinterID)
	if err != nil {
		return
	}
	ep := printer.SelectEndpoint(p, job.Duplex, false)
	if ep == nil {
		if hasAgentEndpoint(p) {
			s.Hub.Publish(userID, "job.waiting_agent", job)
		} else {
			s.Hub.Publish(userID, "job.waiting", job)
		}
		return
	}
	if ep.Kind != "ipp" {
		// Agent endpoints are claimed by the Windows agent; leave the job queued.
		s.Hub.Publish(userID, "job.waiting_agent", job)
		return
	}
	res := s.Exec.Dispatch(ctx, job)
	updated, err := s.Store.GetJob(ctx, jobID)
	if err != nil {
		return
	}
	if res.OK {
		s.Hub.Publish(userID, "job.completed", updated)
	} else if res.Code != "PRINTER_BUSY" {
		s.Hub.Publish(userID, "job.failed", updated)
	}
}

// shouldServerDispatch is the worker-side gate. Agent jobs stay queued for the
// Windows agent to claim, instead of being failed by the server.
func shouldServerDispatch(p *store.Printer, duplex string) bool {
	ep := printer.SelectEndpoint(p, duplex, false)
	return ep != nil && ep.Kind == "ipp"
}

func hasAgentEndpoint(p *store.Printer) bool {
	for _, ep := range p.Endpoints {
		if ep.IsEnabled && ep.Kind == "agent" {
			return true
		}
	}
	return false
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	isAdminView := q.Get("scope") == "all" && u.Role == "admin"
	filter := store.JobFilter{Limit: limit, Offset: offset}
	if isAdminView {
		filter.UserID = q.Get("user_id")
	} else {
		filter.UserID = u.ID
	}
	if st := q.Get("status"); st != "" {
		filter.Statuses = strings.Split(st, ",")
	}
	jobs, total, err := s.Store.ListJobs(r.Context(), filter)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "total": total, "limit": filter.Limit, "offset": filter.Offset})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	u := currentUser(r)
	if job.UserID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "无权查看该任务")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	id := chi.URLParam(r, "id")
	job, err := s.Store.GetJob(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	if job.UserID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "无权取消该任务")
		return
	}
	if err := s.Store.CancelJob(r.Context(), id, u.ID, u.Role == "admin"); err != nil {
		writeErr(w, http.StatusConflict, "CANCEL_REJECTED", err.Error())
		return
	}
	s.Store.Audit(r.Context(), u.ID, "cancel", "job", id, nil)
	updated, _ := s.Store.GetJob(r.Context(), id)
	s.Hub.Publish(job.UserID, "job.canceled", updated)
	writeJSON(w, http.StatusOK, updated)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
