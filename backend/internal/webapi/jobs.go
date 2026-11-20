package webapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"cloudprint/internal/files"
	"cloudprint/internal/printer"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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
	sel, pages, err := files.PageSelection(in.PageRange, file.PageCount)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "PAGE_RANGE_INVALID", err.Error())
		return
	}
	fileForJob := file
	if in.PageRange != "" && pages != file.PageCount {
		sub, subErr := s.extractPagesForJob(r, file, sel, pages)
		if subErr != nil {
			writeErr(w, http.StatusUnprocessableEntity, "PAGE_EXTRACT_FAILED", subErr.Error())
			return
		}
		fileForJob = sub
	}
	in.FileID = fileForJob.ID
	in.PDFFileID = fileForJob.ID
	in.Pages = pages
	if in.PageRange != "" && pages == file.PageCount {
		// "1-全部页" still prints the whole document; keep the label but do
		// not create a duplicate PDF on disk.
		in.PageRange = ""
	}
	required := pages * in.Copies
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
	if ep := printer.SelectEndpoint(p, in.Duplex); ep == nil {
		// Queueing a job on a printer nobody can drive leaves the user staring
		// at "waiting to print" forever. Say exactly which part is offline.
		msg := "这台打印机现在连不上。请稍后再试，或联系管理员检查设备电源和网络。"
		for _, candidate := range p.Endpoints {
			if !candidate.IsEnabled {
				continue
			}
			if candidate.Kind == "cups" {
				msg = "服务器的打印队列现在不可用。请让管理员在“服务器打印驱动”里检查 CUPS 队列是否正常。"
				break
			}
			if candidate.Health != "healthy" && candidate.LastError != "" {
				msg = candidate.LastError
				break
			}
		}
		writeErr(w, http.StatusConflict, "PRINTER_OFFLINE", msg)
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

// extractPagesForJob writes a PDF containing only the selected pages and
// registers it as a file owned by the same user, so the normal job path can
// print it without special-casing page ranges downstream (CUPS, IPP and the
// device all just receive a complete PDF).
func (s *Server) extractPagesForJob(r *http.Request, src *store.StoredFile, sel []string, pages int) (*store.StoredFile, error) {
	u := currentUser(r)
	dir := filepath.Join(s.Cfg.DataDir, "pdf")
	name := "pages-" + uuid.NewString() + ".pdf"
	out := filepath.Join(dir, name)
	if err := s.Pipeline.ExtractPages(r.Context(), src.StoragePath, out, sel); err != nil {
		return nil, err
	}
	st, err := os.Stat(out)
	if err != nil {
		return nil, err
	}
	sha, err := files.HashFile(out)
	if err != nil {
		return nil, err
	}
	sub, err := s.Store.CreateFile(r.Context(), u.ID, "pdf", src.OriginalName,
		"application/pdf", sha, out, st.Size(), pages, s.Cfg.RetentionDays)
	if err != nil {
		return nil, err
	}
	return sub, nil
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
	ep := printer.SelectEndpoint(p, job.Duplex)
	if ep == nil {
		s.Hub.Publish(userID, "job.waiting", job)
		return
	}
	res := s.Exec.Dispatch(ctx, job)
	updated, err := s.Store.GetJob(ctx, jobID)
	if err != nil {
		return
	}
	if res.OK && updated.Status == "printing" {
		// CUPS accepted the file; the worker publishes completion when the
		// spooler reports the job drained.
		return
	}
	if res.OK {
		s.Hub.Publish(userID, "job.completed", updated)
	} else if res.Code != "PRINTER_BUSY" {
		s.Hub.Publish(userID, "job.failed", updated)
	}
}

// shouldServerDispatch is the worker-side gate. Both endpoints are driven from
// this process now: ipp goes straight to the device, cups goes through the
// local spooler and its vendor PPD.
func shouldServerDispatch(p *store.Printer, duplex string) bool {
	ep := printer.SelectEndpoint(p, duplex)
	return ep != nil
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
	if pid := q.Get("printer_id"); pid != "" {
		filter.PrinterID = pid
	}
	jobs, total, err := s.Store.ListJobs(r.Context(), filter)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "total": total, "limit": filter.Limit, "offset": filter.Offset})
}

// handleClearJobs drops a printer's finished history. It exists because the
// queue view is also the troubleshooting view: once a fault is understood, the
// old failed rows only get in the way of reading the current state.
//
// Running work is never touched. The response reports what was removed so the
// UI can say something concrete instead of just "done".
func (s *Server) handleClearJobs(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	printerID := chi.URLParam(r, "id")
	if _, err := s.Store.GetPrinter(r.Context(), printerID); err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	allowed, err := s.Store.UserCanUsePrinter(r.Context(), u.ID, u.Role, printerID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "PRINTER_FORBIDDEN", "没有这台打印机的使用权限")
		return
	}
	n, err := s.Store.ClearFinishedJobs(r.Context(), printerID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "clear_jobs", "printer", printerID,
		map[string]any{"removed": n})
	writeJSON(w, http.StatusOK, map[string]any{"removed": n})
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
	if job.Status == "printing" && job.CUPSJobID != "" && s.CUPS != nil {
		// The job is already in cupsd. Cancel it there first; otherwise the
		// database says "canceled" while the printer keeps printing.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err := s.CUPS.CancelJob(ctx, job.CUPSJobID)
		cancel()
		if err != nil {
			writeErr(w, http.StatusBadGateway, "CUPS_CANCEL_FAILED",
				"无法取消打印队列里的任务："+err.Error())
			return
		}
		if err := s.Store.CancelSubmittedJob(r.Context(), id, "CANCELED", "用户取消了打印"); err != nil {
			writeErr(w, http.StatusConflict, "CANCEL_REJECTED", err.Error())
			return
		}
		s.Store.Audit(r.Context(), u.ID, "cancel", "job", id, map[string]any{"cups_job": job.CUPSJobID})
		updated, _ := s.Store.GetJob(r.Context(), id)
		s.Hub.Publish(job.UserID, "job.canceled", updated)
		writeJSON(w, http.StatusOK, updated)
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
