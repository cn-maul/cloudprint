package webapi

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"cloudprint/internal/ipp"
	"cloudprint/internal/render"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

type diagnosticsCUPS struct {
	Available bool     `json:"available"`
	Reason    string   `json:"reason,omitempty"`
	Queue     string   `json:"queue,omitempty"`
	State     string   `json:"state,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
	Jobs      int      `json:"jobs"`
	MakeModel string   `json:"make_model,omitempty"`
	PPD       string   `json:"ppd,omitempty"`
}

type diagnosticsFilter struct {
	Name      string `json:"name,omitempty"`
	Source    string `json:"source,omitempty"`
	Dest      string `json:"dest,omitempty"`
	Path      string `json:"path,omitempty"`
	Installed bool   `json:"installed"`
	Error     string `json:"error,omitempty"`
}

type diagnosticsDevice struct {
	Kind       string   `json:"kind"`
	URI        string   `json:"uri"`
	Health     string   `json:"health"`
	Error      string   `json:"error,omitempty"`
	State      string   `json:"state,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
	Formats    []string `json:"document_formats,omitempty"`
	Driverless bool     `json:"accepts_pdf"`
}

// handlePrinterDiagnostics answers "why did nothing come out" in one request.
//
// A failed job only says what the last step reported. The usual causes live
// one layer earlier: cupsd is down, the queue lost its PPD, the vendor filter
// was never installed, or the device is unreachable. Collecting those facts
// here keeps an operator from having to run five shell commands on the NAS.
//
// It is read-only and never fails because one probe failed; each block reports
// its own status.
func (s *Server) handlePrinterDiagnostics(w http.ResponseWriter, r *http.Request) {
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
	if !allowed && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "PRINTER_FORBIDDEN", "没有这台打印机的使用权限")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	out := map[string]any{
		"printer": p.ID,
		"name":    p.DisplayName,
		"enabled": p.IsEnabled,
	}

	eps := make([]store.Endpoint, 0, len(p.Endpoints))
	for _, ep := range p.Endpoints {
		if ep.IsEnabled && ep.Kind != "socket" {
			eps = append(eps, ep)
		}
	}
	var primary *store.Endpoint
	for i := range eps {
		if eps[i].Kind == "cups" {
			primary = &eps[i]
			break
		}
	}
	if primary == nil && len(eps) > 0 {
		primary = &eps[0]
	}

	cupsInfo := diagnosticsCUPS{}
	filter := diagnosticsFilter{}
	// Report the spooler even when the printer has no queue yet: "CUPS is
	// missing" is the first thing to rule out, and hiding it behind a missing
	// endpoint makes an empty printer look healthy.
	if s.CUPS != nil {
		cupsInfo.Available, cupsInfo.Reason = s.CUPS.Available()
	}
	if primary != nil && primary.Kind == "cups" {
		cupsInfo.Queue = firstNonEmpty(primary.QueueName, primary.URI)
		if s.CUPS != nil && cupsInfo.Available {
			if state, reasons, err := s.CUPS.PrinterState(ctx, cupsInfo.Queue); err == nil {
				cupsInfo.State = state
				cupsInfo.Reasons = reasons
			}
			if q, err := s.CUPS.Queue(ctx, cupsInfo.Queue); err == nil {
				cupsInfo.Jobs = q.Jobs
				cupsInfo.MakeModel = q.MakeModel
				if cupsInfo.State == "" {
					cupsInfo.State = q.State
				}
			}
			if ppdPath, makeModel, err := s.CUPS.QueuePPD(ctx, cupsInfo.Queue); err == nil {
				cupsInfo.PPD = ppdPath
				if cupsInfo.MakeModel == "" {
					cupsInfo.MakeModel = makeModel
				}
				filter = inspectPPD(ppdPath)
			} else {
				filter.Error = "读不到队列的 PPD：" + err.Error()
			}
		}
	}
	ghostscript, gsErr := exec.LookPath("gs")
	out["cups"] = cupsInfo
	out["filter"] = filter
	out["ghostscript"] = map[string]any{
		"path": ghostscript, "installed": gsErr == nil,
	}
	out["data_dir"] = map[string]any{"path": s.Cfg.DataDir, "writable": dirWritable(s.Cfg.DataDir)}

	devices := make([]diagnosticsDevice, 0, len(eps))
	for i := range eps {
		ep := &eps[i]
		db := diagnosticsDevice{Kind: ep.Kind, URI: ep.URI, Health: ep.Health,
			Driverless: ep.Capabilities.SupportsPDF()}
		db.Formats = ep.Capabilities.DocumentFormats
		if ep.Kind == "cups" && s.CUPS != nil {
			if uri, err := s.CUPS.DeviceURI(ctx, firstNonEmpty(ep.QueueName, ep.URI)); err == nil {
				db.URI = uri
			} else {
				db.Error = err.Error()
			}
			if db.URI != "" && strings.HasPrefix(db.URI, "ipp") {
				probeDeviceState(ctx, db.URI, &db)
			}
		} else if ep.Kind == "ipp" {
			probeDeviceState(ctx, ep.URI, &db)
		}
		devices = append(devices, db)
	}
	out["endpoints"] = devices
	if events, err := s.Store.ListEndpointEvents(r.Context(), p.ID, 5, ""); err == nil {
		out["recent_events"] = events
	}
	writeJSON(w, http.StatusOK, out)
}

// inspectPPD reports the filter CUPS would run for raster input, and whether
// that binary is actually present in the image.
func inspectPPD(path string) diagnosticsFilter {
	var out diagnosticsFilter
	body, err := os.ReadFile(path)
	if err != nil {
		out.Error = "读取 PPD 失败：" + err.Error()
		return out
	}
	spec, ok := render.PickFilter(render.ParseFilters(string(body)))
	if !ok {
		out.Error = "这个 PPD 没有可用的光栅过滤器（需要 *cupsFilter 处理 application/vnd.cups-raster）"
		return out
	}
	out.Name, out.Source, out.Dest = spec.Program, spec.Source, spec.Dest
	bin, err := render.FilterPath(spec.Program)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Path, out.Installed = bin, true
	return out
}

// probeDeviceState asks the printer itself for its state. This is read-only on
// purpose: running the normal probe here would rewrite endpoint health and
// endpoint_events every time someone opened a diagnostic page.
func probeDeviceState(ctx context.Context, uri string, db *diagnosticsDevice) {
	client, err := ipp.NewClient(uri, 8*time.Second)
	if err != nil {
		db.Error = err.Error()
		return
	}
	msg, err := client.GetPrinterAttributes(ctx, []string{
		"printer-state", "printer-state-reasons", "printer-is-accepting-jobs",
		"document-format-supported", "printer-make-and-model",
	})
	if err != nil {
		db.Error = err.Error()
		return
	}
	db.Health = "healthy"
	switch v, _ := msg.Int(ipp.TagPrinterAttributes, "printer-state"); v {
	case 3:
		db.State = "idle"
	case 4:
		db.State = "processing"
	case 5:
		db.State = "stopped"
	}
	db.Reasons = msg.Strings(ipp.TagPrinterAttributes, "printer-state-reasons")
	if formats := msg.Strings(ipp.TagPrinterAttributes, "document-format-supported"); len(formats) > 0 {
		db.Formats = formats
		db.Driverless = store.Capabilities{DocumentFormats: formats}.SupportsPDF()
	}
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name) == nil
}
