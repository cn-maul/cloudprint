package webapi

import (
	"context"
	"log/slog"
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
	probe := time.NewTicker(5 * time.Minute)
	defer probe.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.dispatchQueued(ctx)
			w.Server.Printer.RefreshAgentEndpoints(ctx)
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
