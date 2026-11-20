package webapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"cloudprint/internal/store"
)

type Worker struct {
	Server   *Server
	Interval time.Duration
	Log      *slog.Logger
}

func (w *Worker) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 3 * time.Second
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	cleanup := time.NewTicker(30 * time.Minute)
	defer cleanup.Stop()
	spool := time.NewTicker(5 * time.Second)
	defer spool.Stop()
	probe := time.NewTicker(5 * time.Minute)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.dispatchQueued(ctx)
		case <-spool.C:
			w.pollCUPS(ctx)
		case <-cleanup.C:
			n, err := w.Server.Store.RequeueExpiredClaims(ctx)
			if err != nil {
				w.Log.Warn("requeue expired claims failed", "error", err)
			} else if n > 0 {
				w.Log.Info("requeued expired claims", "count", n)
			}
			_ = w.Server.Store.CleanupSessions(ctx)
		case <-probe.C:
			go w.probeAll(ctx)
		}
	}
}

// pollCUPS asks the device what happened to each in-flight job.
//
// Documents no longer go through cupsd (its filters cannot read the spool
// file), so the device's own job record is the only witness. The stored
// "CUPS job id" is the device's job-uri for those jobs.
func (w *Worker) pollCUPS(ctx context.Context) {
	jobs, err := w.Server.Store.PrintingCUPSJobs(ctx, 20)
	if err != nil {
		return
	}
	for i := range jobs {
		job := jobs[i]
		queue, jobRef := job.QueueName, job.CUPSJobID
		if queue == "" || w.Server.CUPS == nil {
			continue
		}
		if jobRef == "" {
			// The device accepted the document but answered without job-uri,
			// so look the job up by name. Without this the job stays in
			// "printing" forever and keeps the printer's only slot busy.
			if uri, _, ok := w.Server.CUPS.ResolveDeviceJob(ctx, queue, job.ID); ok {
				jobRef = uri
				_ = w.Server.Store.SetJobCUPSID(ctx, job.ID, uri)
			} else if jobOlderThan(job.StartedAt, 10*time.Minute) {
				_ = w.Server.Store.FailJob(ctx, job.ID, "DEVICE_JOB_LOST",
					"打印机没有确认收到这个任务。请检查打印机面板是否报错后重新提交。")
				if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
					w.Server.Hub.Publish(job.UserID, "job.failed", updated)
				}
				continue
			} else {
				continue
			}
		}
		pctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		state, reasons, err := w.Server.CUPS.DeviceJobState(pctx, queue, jobRef, job.ID)
		// A job's own state-reasons only say "job-queued" while the printer is
		// out of paper. The blocking condition (media-empty, media-jam, ...)
		// lives on the printer object, so merge both before explaining a stall.
		printerReasons := append([]string(nil), reasons...)
		if pstate, preasons, perr := w.Server.CUPS.DevicePrinterState(pctx, queue); perr == nil {
			if pstate == "stopped" {
				printerReasons = append(printerReasons, "printer-stopped")
			}
			printerReasons = append(printerReasons, preasons...)
		}
		cancel()
		switch {
		case err != nil:
			// The device can be briefly unreachable; leave the job alone and
			// let the claim lease fail it if this persists.
			if jobOlderThan(job.StartedAt, 30*time.Minute) {
				_ = w.Server.Store.FailJob(ctx, job.ID, "DEVICE_UNREACHABLE",
					"连不上打印机，无法确认是否出纸。请检查打印机电源和网络后重新提交。")
				if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
					w.Server.Hub.Publish(job.UserID, "job.failed", updated)
				}
			}
			continue
		case state == "pending":
			// Still queued behind another job; show why nothing moves yet.
			if w.failIfStalled(ctx, &job, printerReasons) {
				continue
			}
			w.syncDeviceNote(ctx, &job, printerReasons)
		case state == "processing":
			if w.failIfStalled(ctx, &job, printerReasons) {
				continue
			}
			w.syncDeviceNote(ctx, &job, printerReasons)
		case state == "completed":
			_ = w.Server.Store.CompleteJob(ctx, job.ID, job.EndpointID, true, "OK", "已出纸")
			if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
				w.Server.Hub.Publish(job.UserID, "job.completed", updated)
			}
		case state == "failed":
			msg := "打印机没有完成这个任务（可能缺纸、卡纸或被拒绝）。请检查打印机后重新提交。"
			if note := deviceNote("", printerReasons); note != "" {
				msg = note + "。处理后请重新提交。"
			}
			_ = w.Server.Store.FailJob(ctx, job.ID, "DEVICE_JOB_ABORTED", msg)
			if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
				w.Server.Hub.Publish(job.UserID, "job.failed", updated)
			}
		case state == "canceled":
			_ = w.Server.Store.CancelSubmittedJob(ctx, job.ID, "CANCELED", "打印任务已在队列中被取消")
			if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
				w.Server.Hub.Publish(job.UserID, "job.canceled", updated)
			}
		case state == "partial":
			// The device stopped partway through: some sheets came out and
			// the rest did not. Say exactly that, because "已出纸" would hide
			// a half-printed document and "取消" would deny the sheets the
			// user is holding.
			msg := "只打印了部分页面。请检查打印机（缺纸、卡纸）后重新提交剩余页。"
			_ = w.Server.Store.FailJob(ctx, job.ID, "DEVICE_JOB_PARTIAL", msg)
			if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
				w.Server.Hub.Publish(job.UserID, "job.failed", updated)
			}
		case state == "unknown":
			// This Pantum restarts job-id at 1 after a power cycle, so the
			// stored id can point at an unrelated old job (DeviceJobState
			// reports that as unknown). Re-resolve the id from the name before
			// concluding anything; the job itself may have printed fine.
			if uri, _, ok := w.Server.CUPS.ResolveDeviceJob(ctx, queue, job.ID); ok && uri != jobRef {
				_ = w.Server.Store.SetJobCUPSID(ctx, job.ID, uri)
				continue
			}
			// The device keeps a short history; a missing id right after
			// submission means it has not registered yet.
			if !jobOlderThan(job.StartedAt, 3*time.Minute) {
				continue
			}
			_ = w.Server.Store.FailJob(ctx, job.ID, "DEVICE_JOB_LOST",
				"打印机里找不到这个任务。请确认是否已出纸，未出纸请重新提交。")
			if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
				w.Server.Hub.Publish(job.UserID, "job.failed", updated)
			}
		}
	}
}

// stallTimeout is how long a job may sit queued or processing without the
// device reporting any completed impression before we stop waiting.
//
// A stalled device (paper out, or a zombie job the hardware never clears)
// otherwise holds the printer's single active-job slot forever: every later
// upload is rejected as busy even though nothing will ever come out.
const stallTimeout = 15 * time.Minute

// failIfStalled fails a job that has made no progress for stallTimeout and
// reports whether it did so.
func (w *Worker) failIfStalled(ctx context.Context, job *store.Job, reasons []string) bool {
	if !jobOlderThan(job.StartedAt, stallTimeout) {
		return false
	}
	msg := "打印机长时间没有动作，任务已停止。请检查打印机面板（缺纸、卡纸、暂停）后再重新提交。"
	if note := deviceNote("", reasons); note != "" {
		msg = note + "。长时间没有恢复，任务已停止，请处理后重新提交。"
	}
	_ = w.Server.Store.FailJob(ctx, job.ID, "DEVICE_STALLED", msg)
	if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
		w.Server.Hub.Publish(job.UserID, "job.failed", updated)
	}
	return true
}

// syncDeviceNote keeps the job's explanation in step with the printer. A
// blocking reason becomes a note, and a cleared reason removes it: a job that
// resumed must not keep claiming the printer is out of paper.
func (w *Worker) syncDeviceNote(ctx context.Context, job *store.Job, reasons []string) {
	note := deviceNote("", reasons)
	if note == job.ErrorMessage {
		return
	}
	if note == "" {
		if job.ErrorCode != "DEVICE_ATTENTION" {
			return
		}
		_ = w.Server.Store.ClearJobNote(ctx, job.ID)
	} else {
		_ = w.Server.Store.SetJobNote(ctx, job.ID, "DEVICE_ATTENTION", note)
	}
	if updated, gerr := w.Server.Store.GetJob(ctx, job.ID); gerr == nil {
		w.Server.Hub.Publish(job.UserID, "job.printing", updated)
	}
}

// deviceNote turns IPP printer-state-reasons into one short human sentence.
func deviceNote(state string, reasons []string) string {
	for _, r := range reasons {
		// IPP defines "-report" variants as informational: the condition
		// exists but does not block printing. Only act on blocking reasons.
		if strings.HasSuffix(r, "-report") {
			continue
		}
		switch {
		case strings.HasPrefix(r, "media-jam"):
			return "打印机卡纸了，请取出卡住的纸张后再等它继续"
		case strings.HasPrefix(r, "media-empty"), strings.HasPrefix(r, "media-needed"):
			return "打印机缺纸了，请放入纸张后再等它继续"
		case strings.HasPrefix(r, "toner-empty"), strings.HasPrefix(r, "marker-supply-empty"):
			return "打印机墨粉用完了，请更换粉盒"
		case strings.HasPrefix(r, "cover-open"), strings.HasPrefix(r, "door-open"):
			return "打印机盖板未关好，请关好后继续"
		case strings.HasPrefix(r, "input-tray-missing"):
			return "打印机纸盒没有装好"
		}
	}
	if state == "stopped" {
		return "打印机已暂停，请在打印机面板上恢复"
	}
	return ""
}

func jobOlderThan(stamp string, d time.Duration) bool {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(stamp))
	if err != nil {
		return false
	}
	return time.Since(t) > d
}

func (w *Worker) dispatchQueued(ctx context.Context) {
	jobs, err := w.Server.Store.QueuedJobs(ctx, 10)
	if err != nil {
		w.Log.Warn("list queued jobs failed", "error", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	seenPrinter := map[string]bool{}
	for i := range jobs {
		job := jobs[i]
		if seenPrinter[job.PrinterID] {
			continue
		}
		seenPrinter[job.PrinterID] = true
		busy, err := w.Server.Store.HasActiveJob(ctx, job.PrinterID)
		if err != nil || busy {
			continue
		}
		p, err := w.Server.Store.GetPrinter(ctx, job.PrinterID)
		if err != nil {
			continue
		}
		if !shouldServerDispatch(p, job.Duplex) {
			continue
		}
		go w.dispatchOne(ctx, job)
	}
}

func (w *Worker) dispatchOne(ctx context.Context, job store.Job) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res := w.Server.Exec.Dispatch(cctx, &job)
	updated, err := w.Server.Store.GetJob(ctx, job.ID)
	if err != nil {
		return
	}
	switch {
	case res.OK && updated.Status == "printing":
		// Accepted by cupsd; pollCUPS publishes completion once it drains.
	case res.OK:
		w.Server.Hub.Publish(job.UserID, "job.completed", updated)
	case res.Code == "PRINTER_BUSY":
	default:
		w.Server.Hub.Publish(job.UserID, "job.failed", updated)
	}
}

func (w *Worker) probeAll(ctx context.Context) {
	pctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	w.Server.Printer.ProbeAll(pctx)
}
