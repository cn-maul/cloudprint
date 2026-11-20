package webapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cloudprint/internal/cups"
	"cloudprint/internal/ipp"
	"cloudprint/internal/store"
	"cloudprint/internal/support"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleCUPSQueues(w http.ResponseWriter, r *http.Request) {
	ok, reason := s.CUPS.Available()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false, "reason": reason, "queues": []cups.Queue{},
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	queues, err := s.CUPS.Queues(ctx)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	used := map[string]string{}
	if eps, err := s.Store.ListAllEndpoints(r.Context()); err == nil {
		for _, ep := range eps {
			if ep.Kind == "cups" {
				used[ep.QueueName] = ep.PrinterID
			}
		}
	}
	type item struct {
		cups.Queue
		LinkedPrinterID string `json:"linked_printer_id,omitempty"`
	}
	out := make([]any, 0, len(queues))
	for _, q := range queues {
		out = append(out, item{Queue: q, LinkedPrinterID: used[q.Name]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "queues": out})
}

func (s *Server) handleCUPSDrivers(w http.ResponseWriter, r *http.Request) {
	ok, reason := s.CUPS.Available()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"available": false, "reason": reason, "drivers": []cups.PPD{},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "drivers": s.CUPS.PPDs()})
}

// handleCUPSConnect is the one-click path: create a CUPS queue for an existing
// printer (or a new one), with either an explicit PPD or the best installed
// driver for the model the device reports.
func (s *Server) handleCUPSConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PrinterID   string `json:"printer_id"`
		DisplayName string `json:"display_name"`
		Location    string `json:"location"`
		Host        string `json:"host"`
		URI         string `json:"uri"`
		Model       string `json:"model"`
		Vendor      string `json:"vendor"`
		PPD         string `json:"ppd"`
		Queue       string `json:"queue"`
		CandidateID string `json:"candidate_id"`
		CreateQueue bool   `json:"create_queue"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	ok, reason := s.CUPS.Available()
	if !ok {
		writeErr(w, http.StatusConflict, "CUPS_UNAVAILABLE", reason)
		return
	}

	deviceURI := strings.TrimSpace(in.URI)
	if deviceURI == "" {
		host := strings.TrimSpace(in.Host)
		if host == "" {
			writeErr(w, http.StatusBadRequest, "HOST_REQUIRED", "请填写打印机 IP 地址")
			return
		}
		if !strings.Contains(host, "://") {
			deviceURI = "ipp://" + host + "/ipp/print"
		} else {
			deviceURI = host
		}
	}
	if !strings.Contains(deviceURI, "://") {
		deviceURI = "ipp://" + deviceURI + "/ipp/print"
	}

	var p *store.Printer
	createdPrinter := false
	if in.PrinterID != "" {
		existing, err := s.Store.GetPrinter(r.Context(), in.PrinterID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
			return
		}
		p = existing
		if n := strings.TrimSpace(in.DisplayName); n != "" {
			_ = s.Store.UpdatePrinter(r.Context(), p.ID, &n, nil, nil, nil, nil, nil)
		}
	} else {
		name := strings.TrimSpace(in.DisplayName)
		if name == "" {
			name = firstNonEmpty(in.Model, in.Host, "新打印机")
		}
		created, err := s.Store.CreatePrinter(r.Context(), store.NewPrinter{
			DisplayName: name,
			Location:    strings.TrimSpace(in.Location),
			Vendor:      strings.TrimSpace(in.Vendor),
			Model:       strings.TrimSpace(in.Model),
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "PRINTER_INVALID", err.Error())
			return
		}
		p = created
		createdPrinter = true
	}
	cleanup := func() {
		if createdPrinter {
			_ = s.Store.DeletePrinter(r.Context(), p.ID)
		}
	}
	if host := candidateHost(deviceURI); host != "" {
		_ = s.Store.SetPrinterNetworkAddress(r.Context(), p.ID, host)
	}

	// Ask the device for its model so driver matching is based on what the
	// hardware reports, not on what the admin guessed.
	model := strings.TrimSpace(in.Model)
	vendor := strings.TrimSpace(in.Vendor)
	directPDF := false
	probeCtx, probeCancel := context.WithTimeout(r.Context(), 8*time.Second)
	if c, err := ipp.NewClient(deviceURI, 8*time.Second); err == nil {
		if msg, err := c.GetPrinterAttributes(probeCtx, []string{
			"printer-make-and-model", "document-format-supported",
		}); err == nil {
			// The device is authoritative: a form field can say anything, and
			// the whitelist must judge the hardware that receives the jobs.
			if mm := strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-make-and-model")); mm != "" {
				if clean, ok := cleanModelName(mm); ok {
					model = clean
				}
			}
			for _, f := range msg.Strings(ipp.TagPrinterAttributes, "document-format-supported") {
				if f == "application/pdf" || f == "*/*" {
					directPDF = true
				}
			}
		}
	}
	probeCancel()
	if model != "" || vendor != "" {
		_ = s.Store.UpdatePrinter(r.Context(), p.ID, nil, nil, &vendor, &model, nil, nil)
	}
	// Same whitelist as the one-click path: this route can create a printer
	// too, so it must not become a way around the supported-model list.
	listed, ok := support.Match(model)
	if !ok {
		cleanup()
		writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED", support.RejectMessage(model))
		return
	}
	model = listed.Name

	ppdPath := strings.TrimSpace(in.PPD)
	var driver cups.PPD
	if ppdPath == "" {
		// Prefer the driver the whitelist says was tested with this model;
		// the fuzzy match can land on a sibling model's PPD.
		if best := s.CUPS.PreferredPPD(listed.DriverHint()); best != nil {
			driver = *best
			ppdPath = best.Path
		} else if best := s.CUPS.MatchPPD(model); best != nil {
			driver = *best
			ppdPath = best.Path
		}
	} else {
		driver = cups.PPD{Path: ppdPath, Name: ppdPath}
	}
	createQueue := in.CreateQueue || (ppdPath != "" && !directPDF)
	queueName := strings.TrimSpace(in.Queue)
	if queueName == "" {
		var qerr error
		queueName, qerr = s.CUPS.UniqueQueueName(r.Context(), p.DisplayName, p.ID, deviceURI)
		if qerr != nil {
			cleanup()
			writeErr(w, http.StatusBadGateway, "CUPS_LIST_FAILED",
				"无法读取 CUPS 队列列表："+qerr.Error())
			return
		}
	}

	if createQueue {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		err := s.CUPS.Create(ctx, cups.CreateOptions{
			Name:      queueName,
			DeviceURI: deviceURI,
			PPD:       ppdPath,
			Location:  p.Location,
			Info:      p.DisplayName,
		})
		cancel()
		if err != nil {
			if createdPrinter {
				cleanup()
			}
			writeErr(w, http.StatusBadGateway, "CUPS_CREATE_FAILED",
				"创建 CUPS 队列失败："+err.Error())
			return
		}
		_ = s.Store.DisablePrinterEndpoints(r.Context(), p.ID)
		ep, err := s.Store.EnablePrinterEndpoint(r.Context(), p.ID, "cups", queueName)
		if err != nil {
			cleanup()
			s.jsonError(w, err)
			return
		}
		_ = s.Store.SetEndpointQueue(r.Context(), ep.ID, queueName, queueName, driver.Name)
		// probeCUPS reads queue_name from the endpoint, so reload the row
		// instead of probing the pre-update copy.
		if fresh, getErr := s.Store.GetEndpoint(r.Context(), ep.ID); getErr == nil {
			ep = fresh
		}
		ctx2, cancel2 := context.WithTimeout(r.Context(), 20*time.Second)
		res := s.Printer.Probe(ctx2, ep)
		cancel2()
		s.Store.Audit(r.Context(), currentUser(r).ID, "cups_connect", "printer", p.ID, map[string]any{
			"queue": queueName, "uri": deviceURI, "ppd": driver.Name,
		})
		updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
		if in.CandidateID != "" {
			_ = s.Store.SetCandidateStatus(r.Context(), in.CandidateID, "approved", p.ID)
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"printer": updated, "queue": queueName, "driver": driver,
			"probe": res, "mode": "cups",
			"message": cupsMessage(res.Health, driver.Name),
		})
		return
	}

	if !directPDF {
		cleanup()
		writeErr(w, http.StatusConflict, "DRIVER_REQUIRED",
			"没有找到这台打印机的驱动。请先安装厂商驱动（PPD）到 /usr/share/cups/model，或手动选择一个 PPD。")
		return
	}
	_ = s.Store.DisablePrinterEndpoints(r.Context(), p.ID)
	ep, err := s.Store.EnablePrinterEndpoint(r.Context(), p.ID, "ipp", deviceURI)
	if err != nil {
		cleanup()
		s.jsonError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	res := s.Printer.Probe(ctx, ep)
	cancel()
	s.Store.Audit(r.Context(), currentUser(r).ID, "cups_connect", "printer", p.ID, map[string]any{
		"uri": deviceURI, "mode": "ipp",
	})
	updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
	if in.CandidateID != "" {
		_ = s.Store.SetCandidateStatus(r.Context(), in.CandidateID, "approved", p.ID)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"printer": updated, "probe": res, "mode": "ipp",
		"message": "这台打印机可以直接接收 PDF，已按直连方式接入。",
	})
}

func cupsMessage(health, driver string) string {
	switch health {
	case "healthy":
		if driver != "" {
			return "已用 CUPS 驱动接入，可以直接打印"
		}
		return "已接入 CUPS，可以直接打印"
	default:
		return "已创建 CUPS 队列，但打印机现在没响应。请检查它是否开机、IP 是否变化。"
	}
}

func (s *Server) handleCUPSDeleteQueue(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if strings.TrimSpace(name) == "" {
		writeErr(w, http.StatusBadRequest, "QUEUE_REQUIRED", "缺少队列名")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := s.CUPS.Delete(ctx, name); err != nil {
		writeErr(w, http.StatusBadGateway, "CUPS_DELETE_FAILED", err.Error())
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "cups_queue_deleted", "queue", name, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
