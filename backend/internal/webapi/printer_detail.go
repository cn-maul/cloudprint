package webapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

// handlePrinterDetail powers the printer detail page: the device's live
// status plus its queue. It is one request so the page does not coordinate two
// loading states, and it never fails just because the device is unreachable —
// the queue must still render.
func (s *Server) handlePrinterDetail(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	p, err := s.Store.GetPrinter(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	allowed, err := s.Store.UserCanUsePrinter(r.Context(), u.ID, u.Role, p.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	if !allowed {
		writeErr(w, http.StatusForbidden, "PRINTER_FORBIDDEN", "没有这台打印机的使用权限")
		return
	}
	// Only show the endpoints the platform would actually schedule on, so the
	// status here matches the one a submitted job will hit.
	visible := make([]store.Endpoint, 0, len(p.Endpoints))
	for _, ep := range p.Endpoints {
		if ep.IsEnabled && ep.Kind != "socket" {
			visible = append(visible, ep)
		}
	}
	p.Endpoints = visible

	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	status := s.printerStatus(ctx, p)

	limit := 30
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 100 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	filter := store.JobFilter{PrinterID: p.ID, Limit: limit, Offset: offset}
	if st := strings.TrimSpace(r.URL.Query().Get("status")); st != "" {
		filter.Statuses = strings.Split(st, ",")
	}
	jobs, total, err := s.Store.ListJobs(r.Context(), filter)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"printer": p,
		"status":  status,
		"jobs":    jobs,
		"total":   total,
		"limit":   filter.Limit,
		"offset":  filter.Offset,
	})
}

type printerStatus struct {
	State      string   `json:"state"`
	Reasons    []string `json:"reasons,omitempty"`
	Message    string   `json:"message,omitempty"`
	Queue      string   `json:"queue,omitempty"`
	QueueState string   `json:"queue_state,omitempty"`
	DirectPDF  bool     `json:"direct_pdf"`
	ActiveJobs int      `json:"active_jobs"`
	Model      string   `json:"model,omitempty"`
}

// printerStatus asks CUPS (which owns the vendor filter) and falls back to the
// stored probe for direct-IPP printers.
func (s *Server) printerStatus(ctx context.Context, p *store.Printer) printerStatus {
	out := printerStatus{State: "unknown"}
	var ep *store.Endpoint
	for i := range p.Endpoints {
		if p.Endpoints[i].Kind == "cups" {
			ep = &p.Endpoints[i]
			break
		}
	}
	if ep == nil && len(p.Endpoints) > 0 {
		ep = &p.Endpoints[0]
	}
	if ep == nil {
		out.State = "stopped"
		out.Message = "这台打印机还没有可用的连接方式"
		return out
	}
	out.Model = firstNonEmpty(ep.Capabilities.MakeModel, p.Model)
	out.DirectPDF = ep.Kind == "ipp" && ep.Capabilities.SupportsPDF()
	if ep.Kind == "cups" {
		out.Queue = firstNonEmpty(ep.QueueName, ep.URI)
		if s.CUPS != nil {
			if state, reasons, err := s.CUPS.PrinterState(ctx, out.Queue); err == nil {
				out.QueueState = state
				out.State = normalizeState(state)
				out.Reasons = reasons
			}
			if q, err := s.CUPS.Queue(ctx, out.Queue); err == nil {
				out.ActiveJobs = q.Jobs
				if out.State == "unknown" {
					out.State = normalizeState(q.State)
				}
			}
		}
	} else if s.Printer != nil {
		res := s.Printer.Probe(ctx, ep)
		out.State = res.Capabilities.State
		out.Reasons = res.Capabilities.StateReasons
		if out.State == "" {
			if res.Health == "healthy" {
				out.State = "idle"
			} else {
				out.State = "stopped"
			}
		}
		if res.Error != "" {
			out.Message = res.Error
		}
	}
	if out.Message == "" {
		out.Message = deviceStatusText(out.State, out.Reasons)
	}
	return out
}

// deviceStatusText explains printer-state-reasons in the same words the job
// worker uses, so the queue and the printer card never disagree.
func deviceStatusText(state string, reasons []string) string {
	for _, r := range reasons {
		if strings.HasSuffix(r, "-report") {
			continue
		}
		switch {
		case strings.HasPrefix(r, "media-jam"):
			return "卡纸了"
		case strings.HasPrefix(r, "media-empty"), strings.HasPrefix(r, "media-needed"):
			return "缺纸"
		case strings.HasPrefix(r, "toner-empty"), strings.HasPrefix(r, "marker-supply-empty"):
			return "墨粉用完"
		case strings.HasPrefix(r, "cover-open"), strings.HasPrefix(r, "door-open"):
			return "盖板未关"
		case strings.HasPrefix(r, "input-tray-missing"):
			return "纸盒未装好"
		case strings.HasPrefix(r, "offline"):
			return "离线"
		}
	}
	switch normalizeState(state) {
	case "idle":
		return "就绪"
	case "processing":
		return "正在打印"
	case "stopped":
		return "已暂停"
	}
	return ""
}

// normalizeState maps CUPS wording ("printing") onto the three states the UI
// and the device itself report (idle / processing / stopped).
func normalizeState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "idle":
		return "idle"
	case "printing", "processing":
		return "processing"
	case "stopped", "disabled", "down":
		return "stopped"
	}
	return "unknown"
}
