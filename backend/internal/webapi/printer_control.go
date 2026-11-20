package webapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"cloudprint/internal/ipp"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

// handlePrinterControl pauses, resumes, or purges the printer itself.
//
// The queue view is a troubleshooting view, so the actions an operator needs
// when paper jams belong next to it: stop the device from claiming more work,
// throw away the stale jobs it is holding, then resume once it is fixed.
// cancelling one job at a time is not enough when a dozen are stuck behind it.
//
// Pausing a device that is out of paper also prevents every later upload from
// failing; those jobs stay queued here until the printer is back.
func (s *Server) handlePrinterControl(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	printerID := chi.URLParam(r, "id")
	var in struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action != "pause" && action != "resume" && action != "purge" {
		writeErr(w, http.StatusBadRequest, "ACTION_INVALID",
			"action 只能是 pause、resume 或 purge")
		return
	}
	p, err := s.Store.GetPrinter(r.Context(), printerID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	// Pausing or purging affects everyone sharing the device, so it is an
	// admin action. A normal user can still cancel their own job.
	if u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "需要管理员权限才能控制打印机")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	// Some devices (including this Pantum) do not implement the optional
	// Pause-Printer/Resume-Printer/Purge-Jobs operations. Report that plainly
	// instead of leaking a raw "0x0501" at the operator.
	if !s.deviceSupportsAction(ctx, p.Endpoints, action) {
		writeErr(w, http.StatusNotImplemented, "ACTION_UNSUPPORTED",
			"这台打印机不支持远程"+actionLabel(action)+"，请在打印机面板上操作")
		return
	}
	if err := s.printerDeviceAction(ctx, p.Endpoints, action, u.Username); err != nil {
		writeErr(w, http.StatusBadGateway, "DEVICE_CONTROL_FAILED", err.Error())
		return
	}
	switch action {
	case "purge":
		// The device dropped the documents, so the local rows must stop
		// claiming they are in flight; a live "printing" row would hold the
		// printer's single active slot forever.
		n, err := s.Store.CancelPrintingJobs(r.Context(), p.ID,
			"被管理员清空打印机队列")
		if err != nil {
			s.jsonError(w, err)
			return
		}
		s.Store.Audit(r.Context(), u.ID, "purge_printer", "printer", p.ID,
			map[string]any{"canceled_jobs": n})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action, "canceled_jobs": n})
		return
	}
	s.Store.Audit(r.Context(), u.ID, action+"_printer", "printer", p.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": action})
}

func actionLabel(action string) string {
	switch action {
	case "pause":
		return "暂停"
	case "resume":
		return "恢复"
	case "purge":
		return "清空队列"
	}
	return action
}

// deviceSupportsAction asks the device which operations it implements.
//
// An unanswered query is treated as "unknown, try it": a device that cannot
// list its operations is not proof that it cannot perform one. Only an explicit
// operations-supported list that omits the action blocks the call.
func (s *Server) deviceSupportsAction(ctx context.Context, eps []store.Endpoint, action string) bool {
	want := map[string]int64{"pause": ipp.OpPausePrinter,
		"resume": ipp.OpResumePrinter, "purge": ipp.OpPurgeJobs}[action]
	for i := range eps {
		ep := &eps[i]
		if !ep.IsEnabled || ep.Kind == "socket" {
			continue
		}
		uri := ep.URI
		if ep.Kind == "cups" && s.CUPS != nil {
			if deviceURI, err := s.CUPS.DeviceURI(ctx, firstNonEmpty(ep.QueueName, ep.URI)); err == nil {
				uri = deviceURI
			}
		}
		if !strings.HasPrefix(uri, "ipp://") && !strings.HasPrefix(uri, "ipps://") {
			continue
		}
		client, err := ipp.NewClient(uri, 8*time.Second)
		if err != nil {
			continue
		}
		msg, err := client.GetPrinterAttributes(ctx, []string{"operations-supported"})
		if err != nil {
			continue
		}
		ops := msg.Integers(ipp.TagPrinterAttributes, "operations-supported")
		if len(ops) == 0 {
			return true
		}
		for _, op := range ops {
			if op == want {
				return true
			}
		}
		return false
	}
	return true
}

// printerDeviceAction runs the operation on the first reachable connection.
// A printer with several endpoints (an old CUPS queue plus a direct IPP row)
// only needs one to succeed.
func (s *Server) printerDeviceAction(ctx context.Context, eps []store.Endpoint, action, userName string) error {
	var lastErr error
	tried := false
	for i := range eps {
		ep := &eps[i]
		if !ep.IsEnabled || ep.Kind == "socket" {
			continue
		}
		tried = true
		uri := ep.URI
		if ep.Kind == "cups" {
			if s.CUPS == nil {
				lastErr = fmt.Errorf("CUPS 未初始化")
				continue
			}
			queue := firstNonEmpty(ep.QueueName, ep.URI)
			if err := s.CUPS.DeviceControl(ctx, queue, action, userName); err == nil {
				return nil
			} else {
				lastErr = err
			}
			continue
		}
		if !strings.HasPrefix(uri, "ipp://") && !strings.HasPrefix(uri, "ipps://") {
			continue
		}
		client, err := ipp.NewClient(uri, 15*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		switch action {
		case "pause":
			err = client.PausePrinter(ctx, userName)
		case "resume":
			err = client.ResumePrinter(ctx, userName)
		case "purge":
			err = client.PurgeJobs(ctx, userName)
		}
		if err == nil {
			return nil
		}
		lastErr = err
	}
	if !tried {
		return fmt.Errorf("这台打印机没有可用的连接方式")
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("打印机没有接受这个操作")
	}
	return lastErr
}
