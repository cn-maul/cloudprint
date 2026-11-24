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
	"cloudprint/internal/ipp"
	"cloudprint/internal/printer"
	"cloudprint/internal/store"
	"cloudprint/internal/support"

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
	allowed, err := s.Store.UserCanUsePrinter(r.Context(), u, in.PrinterID)
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
	// Adding a printer is already restricted to the supported-model list, but
	// a device registered before that list existed (or via an older build)
	// would otherwise still accept jobs and fail on paper. Refuse up front.
	if !support.Supported(firstNonEmpty(p.Model, p.DisplayName)) {
		writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED",
			support.RejectMessage(firstNonEmpty(p.Model, p.DisplayName)))
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
	// The job history must point at what the user uploaded, not at the
	// throwaway PDF carved out for a page range.
	in.OriginalFileID = file.ID
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
	lookupCtx, lookupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	job, err := s.Store.GetJob(lookupCtx, jobID)
	lookupCancel()
	if err != nil || job.Status != "queued" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), printer.DispatchBudget(printer.JobPages(job)))
	defer cancel()
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
		// The device accepted the file; pollCUPS publishes completion once it
		// reports the job drained. Dispatch deliberately leaves the row in
		// "printing" for this case, so publishing completion here would tell
		// the user the job is done while the printer is still working.
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
	// Purging history wipes every user's finished rows for that printer, so it
	// is a management action even though the row's owner check would not catch
	// it. Guests keep the queue clean of their own work via cancel instead.
	if u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "只有管理员可以清空任务记录")
		return
	}
	printerID := chi.URLParam(r, "id")
	if _, err := s.Store.GetPrinter(r.Context(), printerID); err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	allowed, err := s.Store.UserCanUsePrinter(r.Context(), u, printerID)
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
	// A single-job view is polled while the document prints, so it is the right
	// place to ask the device how many sheets are done. List views skip this:
	// one IPP round trip per row would make the queue slow.
	s.fillJobProgress(r.Context(), job)
	writeJSON(w, http.StatusOK, job)
}

// fillJobProgress adds the device's impression counter to an in-flight job.
// It is best-effort: an unreachable device leaves progress empty rather than
// failing the request.
func (s *Server) fillJobProgress(ctx context.Context, job *store.Job) {
	if job == nil || job.Status != "printing" || job.CUPSJobID == "" || job.QueueName == "" || s.CUPS == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done, total, ok := s.CUPS.DeviceJobProgress(ctx, job.QueueName, job.CUPSJobID)
	if !ok {
		return
	}
	if total <= 0 {
		total = job.TotalPages
	}
	if total < done {
		total = done
	}
	job.Progress = &store.JobProgress{Printed: done, Total: total}
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
	if job.Status == "printing" && job.CUPSJobID != "" {
		// The document went straight to the device, so the job-id only exists
		// on the printer. Asking cupsd to cancel it would always fail with
		// "no such job"; send Cancel-Job to the device instead so the database
		// and the paper stay in step.
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		err := s.cancelDeviceJob(ctx, job)
		cancel()
		if err != nil {
			writeErr(w, http.StatusBadGateway, "DEVICE_CANCEL_FAILED",
				"无法取消打印机上的任务："+err.Error())
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
	if job.Status == "printing" && job.CUPSJobID == "" {
		// The document is in the device's hands but it has not answered with a
		// job reference yet, so there is nothing to name in Cancel-Job. Saying
		// "not in a cancelable state" would be wrong; the job is printing.
		writeErr(w, http.StatusConflict, "CANCEL_NOT_READY",
			"打印机还在接收这份文件，暂时无法取消。请等几秒再试，或直接在打印机上停止。")
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

// cancelDeviceJob reaches the printer that actually holds the document. A
// CUPS-backed printer is resolved through its queue; a direct IPP endpoint has
// its address in the job snapshot.
func (s *Server) cancelDeviceJob(ctx context.Context, job *store.Job) error {
	jobID, ok := ipp.JobIDFromURI(job.CUPSJobID)
	if !ok {
		return fmt.Errorf("无法从任务地址 %q 解析任务编号", job.CUPSJobID)
	}
	if job.QueueName != "" && s.CUPS != nil {
		return s.CUPS.CancelDeviceJob(ctx, job.QueueName, jobID, job.Username)
	}
	uri := strings.TrimSpace(job.EndpointURI)
	if !strings.HasPrefix(uri, "ipp://") && !strings.HasPrefix(uri, "ipps://") {
		return fmt.Errorf("任务没有记录可用的打印机地址")
	}
	client, err := ipp.NewClient(uri, 15*time.Second)
	if err != nil {
		return err
	}
	return client.CancelJob(ctx, jobID, job.Username)
}

// handleRetryJob requeues a failed or canceled job without making the user
// upload the same document again. The new row keeps retry_of so the queue can
// show that it is a second attempt at the same file.
//
// The already-extracted PDF is reused verbatim: page ranges were applied when
// the original job was created, so re-applying the range here would print the
// wrong pages of the subset.
func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	src, err := s.Store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	// Only the owner may retry: the new job must land in the same personal
	// queue and spend the same daily quota as the failed attempt.
	if src.UserID != u.ID {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "无权重试该任务")
		return
	}
	if src.Status != "failed" && src.Status != "canceled" {
		writeErr(w, http.StatusConflict, "RETRY_NOT_ALLOWED",
			"只有失败或已取消的任务可以重试；正在打印的任务请先取消")
		return
	}
	if src.PrinterID == "" {
		writeErr(w, http.StatusConflict, "PRINTER_GONE", "这台打印机已被删除，请重新选择打印机")
		return
	}
	p, err := s.Store.GetPrinter(r.Context(), src.PrinterID)
	if err != nil {
		writeErr(w, http.StatusConflict, "PRINTER_GONE", "这台打印机已被删除，请重新选择打印机")
		return
	}
	if !p.IsEnabled {
		writeErr(w, http.StatusConflict, "PRINTER_DISABLED", "这台打印机已停用")
		return
	}
	if !support.Supported(firstNonEmpty(p.Model, p.DisplayName)) {
		writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED",
			support.RejectMessage(firstNonEmpty(p.Model, p.DisplayName)))
		return
	}
	allowed, err := s.Store.UserCanUsePrinter(r.Context(), u, p.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "PRINTER_FORBIDDEN", "你所在的用户组没有这台打印机的使用权限")
		return
	}
	file, err := s.Store.GetFile(r.Context(), src.PDFFile)
	if err != nil || file.StoragePath == "" {
		writeErr(w, http.StatusGone, "FILE_EXPIRED",
			"原始文件已超过保留期被清理，请重新上传后再打印")
		return
	}
	pages := src.PageCount
	if pages <= 0 {
		pages = file.PageCount
	}
	used, err := s.Store.PagesToday(r.Context(), u.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	required := pages * src.Copies
	if u.DailyPageQuota > 0 && used+required > u.DailyPageQuota {
		writeErr(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED",
			fmt.Sprintf("今日剩余 %d 页，本次需要 %d 页。", maxInt(u.DailyPageQuota-used, 0), required))
		return
	}
	if printer.SelectEndpoint(p, src.Duplex) == nil {
		writeErr(w, http.StatusConflict, "PRINTER_OFFLINE", "这台打印机现在连不上，请稍后再试")
		return
	}
	job, err := s.Store.CreateJob(r.Context(), store.NewJob{
		UserID:         u.ID,
		PrinterID:      p.ID,
		FileID:         src.PDFFile,
		Copies:         src.Copies,
		Duplex:         src.Duplex,
		ColorMode:      src.ColorMode,
		PaperSize:      src.PaperSize,
		PageRange:      src.PageRange,
		Pages:          pages,
		RetryOf:        src.ID,
		OriginalFileID: src.OriginalFile,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "JOB_REJECTED", err.Error())
		return
	}
	s.Store.Audit(r.Context(), u.ID, "retry", "job", job.ID,
		map[string]any{"retry_of": src.ID, "printer_id": job.PrinterID})
	s.Log.Info("job retried", "job", job.ID, "retry_of", src.ID, "user", u.Username)
	s.Hub.Publish(job.UserID, "job.created", job)
	go s.tryDispatch(job.ID, u.ID)
	writeJSON(w, http.StatusCreated, job)
}

// handleReprintJob submits another copy of a finished job.
//
// "再打一份" is the most common office request after a document comes out, and
// making the user find the file and set the page range again is pure friction.
// The stored (already page-ranged) PDF is reused verbatim, so what gets printed
// is exactly what came out the first time.
func (s *Server) handleReprintJob(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	src, err := s.Store.GetJob(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "JOB_NOT_FOUND", "任务不存在")
		return
	}
	if src.UserID != u.ID {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "无权重打该任务")
		return
	}
	if src.Status != "completed" {
		writeErr(w, http.StatusConflict, "REPRINT_NOT_ALLOWED",
			"只有已出纸的任务可以再打一份")
		return
	}
	if src.PrinterID == "" {
		writeErr(w, http.StatusConflict, "PRINTER_GONE", "这台打印机已被删除")
		return
	}
	p, err := s.Store.GetPrinter(r.Context(), src.PrinterID)
	if err != nil || !p.IsEnabled {
		writeErr(w, http.StatusConflict, "PRINTER_GONE", "这台打印机已删除或停用")
		return
	}
	// Reprint and retry both create a fresh job; the same whitelist applies as
	// for an upload, or an old job becomes a way to print from a model that was
	// never verified.
	if !support.Supported(firstNonEmpty(p.Model, p.DisplayName)) {
		writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED",
			support.RejectMessage(firstNonEmpty(p.Model, p.DisplayName)))
		return
	}
	file, err := s.Store.GetFile(r.Context(), src.PDFFile)
	if err != nil || file.StoragePath == "" {
		writeErr(w, http.StatusGone, "FILE_EXPIRED",
			"原始文件已超过保留期被清理，请重新上传后再打印")
		return
	}
	// An empty body means "一样的再来一份".
	var in struct {
		Copies int `json:"copies"`
	}
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &in) {
			return
		}
	}
	copies := in.Copies
	if copies <= 0 {
		copies = src.Copies
	}
	if copies > 99 {
		copies = 99
	}
	pages := src.PageCount
	if pages <= 0 {
		pages = file.PageCount
	}
	used, err := s.Store.PagesToday(r.Context(), u.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	required := pages * copies
	if u.DailyPageQuota > 0 && used+required > u.DailyPageQuota {
		writeErr(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED",
			fmt.Sprintf("今日剩余 %d 页，本次需要 %d 页。", maxInt(u.DailyPageQuota-used, 0), required))
		return
	}
	if printer.SelectEndpoint(p, src.Duplex) == nil {
		writeErr(w, http.StatusConflict, "PRINTER_OFFLINE", "这台打印机现在连不上，请稍后再试")
		return
	}
	job, err := s.Store.CreateJob(r.Context(), store.NewJob{
		UserID:         u.ID,
		PrinterID:      p.ID,
		FileID:         src.PDFFile,
		Copies:         copies,
		Duplex:         src.Duplex,
		ColorMode:      src.ColorMode,
		PaperSize:      src.PaperSize,
		PageRange:      src.PageRange,
		Pages:          pages,
		OriginalFileID: src.OriginalFile,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, "JOB_REJECTED", err.Error())
		return
	}
	s.Store.Audit(r.Context(), u.ID, "reprint", "job", job.ID,
		map[string]any{"reprint_of": src.ID, "printer_id": job.PrinterID, "copies": copies})
	s.Log.Info("job reprinted", "job", job.ID, "of", src.ID, "user", u.Username)
	s.Hub.Publish(job.UserID, "job.created", job)
	go s.tryDispatch(job.ID, u.ID)
	writeJSON(w, http.StatusCreated, job)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
