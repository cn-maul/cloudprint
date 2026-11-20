package webapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handlePrinterEvents returns the driver/probe history of a printer's
// connections. Jobs explain what the user asked for; this explains why the
// device stopped accepting it (a filter that failed, a device that went
// unreachable, a driver that worked earlier).
func (s *Server) handlePrinterEvents(w http.ResponseWriter, r *http.Request) {
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
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	events, err := s.Store.ListEndpointEvents(r.Context(), printerID, limit,
		strings.TrimSpace(r.URL.Query().Get("event")))
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}
