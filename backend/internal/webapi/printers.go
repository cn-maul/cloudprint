package webapi

import (
	"context"
	"net/http"
	"strings"

	"cloudprint/internal/store"
	"cloudprint/internal/support"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handlePrintersPublic(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	printers, err := s.Store.ListPrinters(r.Context(), false)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	out := make([]store.Printer, 0, len(printers))
	for _, p := range printers {
		allowed, err := s.Store.UserCanUsePrinter(r.Context(), u.ID, u.Role, p.ID)
		if err != nil {
			s.jsonError(w, err)
			return
		}
		if !allowed {
			continue
		}
		visible := make([]store.Endpoint, 0, len(p.Endpoints))
		for _, ep := range p.Endpoints {
			// Keep temporarily unreachable printers visible. Hiding them makes a
			// user think they lost permission, and gives no way to report the
			// real problem ("printer is off" / "helper not running").
			// Disabled endpoints are historic rows from a re-connect; they must
			// stay hidden or the user sees two conflicting states for the same
			// printer.
			if ep.IsEnabled && ep.Kind != "socket" {
				visible = append(visible, ep)
			}
		}
		if len(visible) == 0 {
			continue
		}
		p.Endpoints = visible
		// Records created before the whitelist kept the device's spelling
		// ("MS6000NW series"); show the one canonical name everywhere.
		p.Model = support.Canonical(p.Model)
		out = append(out, p)
	}
	// Per-printer active job counts, so the list can show "2 份在等" without
	// opening each detail page. A failure here must not hide the printers.
	counts, err := s.Store.PrinterQueueCounts(r.Context())
	if err != nil {
		counts = map[string]struct{ Queued, Printing int }{}
	}
	queues := make(map[string]map[string]int, len(counts))
	for id, c := range counts {
		queues[id] = map[string]int{"queued": c.Queued, "printing": c.Printing}
	}
	writeJSON(w, http.StatusOK, map[string]any{"printers": out, "queues": queues})
}

func (s *Server) handleAdminPrinters(w http.ResponseWriter, r *http.Request) {
	printers, err := s.Store.ListPrinters(r.Context(), true)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	for i := range printers {
		printers[i].Model = support.Canonical(printers[i].Model)
	}
	writeJSON(w, http.StatusOK, map[string]any{"printers": printers})
}

func (s *Server) handleAdminPrinter(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetPrinter(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreatePrinter(w http.ResponseWriter, r *http.Request) {
	var in store.NewPrinter
	if !decodeJSON(w, r, &in) {
		return
	}
	if !support.Supported(in.Model) {
		writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED", support.RejectMessage(in.Model))
		return
	}
	if m, ok := support.Match(in.Model); ok {
		in.Model = m.Name
	}
	p, err := s.Store.CreatePrinter(r.Context(), in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "PRINTER_INVALID", err.Error())
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "create_printer", "printer", p.ID, in)
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePrinter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Location    *string `json:"location"`
		Vendor      *string `json:"vendor"`
		Model       *string `json:"model"`
		ProfileID   *string `json:"profile_id"`
		IsEnabled   *bool   `json:"is_enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	// Editing the model is how a mis-detected device gets corrected, but it is
	// also a way to launder an unsupported printer into the printable set:
	// jobs only look at the stored model. Accept a value only if it names a
	// model on the list, and store the canonical name.
	if in.Model != nil {
		m, ok := support.Match(*in.Model)
		if !ok {
			writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED", support.RejectMessage(*in.Model))
			return
		}
		name := m.Name
		in.Model = &name
	}
	if err := s.Store.UpdatePrinter(r.Context(), id, in.DisplayName, in.Location,
		in.Vendor, in.Model, in.ProfileID, in.IsEnabled); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "update_printer", "printer", id, in)
	p, _ := s.Store.GetPrinter(r.Context(), id)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePrinter(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.DeletePrinter(r.Context(), id); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "delete_printer", "printer", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCreateEndpoint(w http.ResponseWriter, r *http.Request) {
	var in store.NewEndpoint
	if !decodeJSON(w, r, &in) {
		return
	}
	printerID := chi.URLParam(r, "id")
	p, err := s.Store.GetPrinter(r.Context(), printerID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	// Endpoints are what makes a printer printable. Adding one to a model that
	// never passed the whitelist would revive the very device the add path
	// refuses.
	if !support.Supported(firstNonEmpty(p.Model, p.DisplayName)) {
		writeErr(w, http.StatusConflict, "MODEL_UNSUPPORTED",
			support.RejectMessage(firstNonEmpty(p.Model, p.DisplayName)))
		return
	}
	in.URI = strings.TrimSpace(in.URI)
	if in.Kind == "ipp" && !strings.HasPrefix(strings.ToLower(in.URI), "ipp://") &&
		!strings.HasPrefix(strings.ToLower(in.URI), "ipps://") {
		writeErr(w, http.StatusBadRequest, "URI_INVALID", "IPP 端点地址必须以 ipp:// 或 ipps:// 开头")
		return
	}
	if in.Kind == "socket" && !strings.HasPrefix(strings.ToLower(in.URI), "socket://") {
		writeErr(w, http.StatusBadRequest, "URI_INVALID", "Socket 端点地址必须以 socket:// 开头")
		return
	}
	ep, err := s.Store.CreateEndpoint(r.Context(), printerID, in)
	if err != nil {
		if err == store.ErrConflict {
			writeErr(w, http.StatusConflict, "ENDPOINT_EXISTS", "该端点已存在")
			return
		}
		writeErr(w, http.StatusBadRequest, "ENDPOINT_INVALID", err.Error())
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "create_endpoint", "endpoint", ep.ID, in)
	go func() {
		probe := s.Printer.Probe(context.Background(), ep)
		s.Log.Info("endpoint probed", "endpoint", ep.ID, "health", probe.Health, "error", probe.Error)
	}()
	writeJSON(w, http.StatusCreated, ep)
}

func (s *Server) handleUpdateEndpoint(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Priority  *int    `json:"priority"`
		IsEnabled *bool   `json:"is_enabled"`
		URI       *string `json:"uri"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	if err := s.Store.UpdateEndpoint(r.Context(), id, in.Priority, in.IsEnabled, in.URI); err != nil {
		s.jsonError(w, err)
		return
	}
	ep, _ := s.Store.GetEndpoint(r.Context(), id)
	writeJSON(w, http.StatusOK, ep)
}

func (s *Server) handleProbeEndpoint(w http.ResponseWriter, r *http.Request) {
	ep, err := s.Store.GetEndpoint(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "ENDPOINT_NOT_FOUND", "端点不存在")
		return
	}
	res := s.Printer.Probe(r.Context(), ep)
	updated, _ := s.Store.GetEndpoint(r.Context(), ep.ID)
	writeJSON(w, http.StatusOK, map[string]any{"result": res, "endpoint": updated})
}

func (s *Server) handleProbePrinter(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetPrinter(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
		return
	}
	results := []map[string]any{}
	for i := range p.Endpoints {
		ep := p.Endpoints[i]
		// Disabled endpoints are leftovers from a previous connection; probing
		// them reports an error for a path nobody is trying to use.
		if !ep.IsEnabled {
			continue
		}
		res := s.Printer.Probe(r.Context(), &ep)
		results = append(results, map[string]any{"endpoint_id": ep.ID, "result": res})
	}
	updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
	writeJSON(w, http.StatusOK, map[string]any{"results": results, "printer": updated})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Store.ListProfiles(r.Context())
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}
