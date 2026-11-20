package webapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cloudprint/internal/ipp"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

// handlePrinterOptions returns what a specific printer can actually do, so a
// client can render only valid choices instead of offering duplex on a
// one-sided device or A3 on a printer with an A4 tray.
//
// Values are derived from the endpoint's probed capabilities, then narrowed by
// a live IPP query when the device is reachable. The renderer only understands
// the four paper names below, so media is reported in that vocabulary rather
// than as raw IPP keywords the client cannot use.
func (s *Server) handlePrinterOptions(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	caps := store.Capabilities{}
	model := p.Model
	reachable := false
	for i := range p.Endpoints {
		ep := &p.Endpoints[i]
		if !ep.IsEnabled || ep.Kind == "socket" {
			continue
		}
		if ep.Capabilities.MakeModel != "" && model == "" {
			model = ep.Capabilities.MakeModel
		}
		// Keep the last-probed capabilities as the baseline: an unreachable
		// device must still report what it offered when it was last seen,
		// otherwise the form collapses to "A4, no duplex" for every fault.
		if len(caps.DocumentFormats) == 0 && len(ep.Capabilities.DocumentFormats) > 0 {
			caps = ep.Capabilities
		}
		uri := ep.URI
		if ep.Kind == "cups" && s.CUPS != nil {
			if deviceURI, derr := s.CUPS.DeviceURI(ctx, firstNonEmpty(ep.QueueName, ep.URI)); derr == nil {
				uri = deviceURI
			}
		}
		if !strings.HasPrefix(uri, "ipp://") && !strings.HasPrefix(uri, "ipps://") {
			continue
		}
		// Read-only query: asking for options must not mutate endpoint health
		// or append to the endpoint event log.
		client, cerr := ipp.NewClient(uri, 5*time.Second)
		if cerr != nil {
			continue
		}
		msg, cerr := client.GetPrinterAttributes(ctx, []string{
			"printer-make-and-model", "document-format-supported",
			"sides-supported", "media-supported", "media-default",
			"print-color-mode-supported", "copies-supported", "printer-state",
		})
		if cerr != nil {
			continue
		}
		reachable = true
		live := store.Capabilities{
			MakeModel:       msg.String(ipp.TagPrinterAttributes, "printer-make-and-model"),
			DocumentFormats: msg.Strings(ipp.TagPrinterAttributes, "document-format-supported"),
			Sides:           msg.Strings(ipp.TagPrinterAttributes, "sides-supported"),
			Media:           msg.Strings(ipp.TagPrinterAttributes, "media-supported"),
			ColorModes:      msg.Strings(ipp.TagPrinterAttributes, "print-color-mode-supported"),
		}
		if v, ok := msg.Int(ipp.TagPrinterAttributes, "copies-supported"); ok && v > 1 {
			live.MaxCopies = int(v)
		}
		// Only overwrite a dimension the device actually answered; many
		// devices omit attributes they do not support.
		if len(live.DocumentFormats) > 0 {
			caps.DocumentFormats = live.DocumentFormats
		}
		if len(live.Sides) > 0 {
			caps.Sides = live.Sides
		}
		if len(live.Media) > 0 {
			caps.Media = live.Media
		}
		if len(live.ColorModes) > 0 {
			caps.ColorModes = live.ColorModes
		}
		if live.MaxCopies > 0 {
			caps.MaxCopies = live.MaxCopies
		}
		if live.MakeModel != "" {
			model = live.MakeModel
		}
		break
	}

	if caps.Sides == nil {
		caps.Sides = []string{}
	}
	if caps.Media == nil {
		caps.Media = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"printer_id":  p.ID,
		"model":       model,
		"reachable":   reachable,
		"paper_sizes": availablePapers(caps.Media),
		"copies_max":  copiesMax(caps.MaxCopies),
		"can_duplex":  supportsSides(caps.Sides),
		"can_color":   supportsColor(caps.ColorModes),
		"accepts_pdf": caps.SupportsPDF(),
		"sides":       caps.Sides,
		"media":       caps.Media,
	})
}

// availablePapers maps the device's IPP media keywords onto the four names the
// renderer knows. Unknown sizes are dropped rather than passed through: a paper
// name the renderer cannot map fails the whole job.
func availablePapers(media []string) []string {
	if len(media) == 0 {
		return []string{"A4"}
	}
	seen := map[string]bool{}
	out := []string{}
	add := func(name string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, m := range media {
		lower := strings.ToLower(strings.TrimSpace(m))
		switch {
		case strings.HasPrefix(lower, "iso_a4"), lower == "a4":
			add("A4")
		case strings.HasPrefix(lower, "iso_a3"), lower == "a3":
			add("A3")
		case strings.HasPrefix(lower, "na_letter"), lower == "letter":
			add("Letter")
		case strings.HasPrefix(lower, "na_legal"), lower == "legal":
			add("Legal")
		}
	}
	if len(out) == 0 {
		return []string{"A4"}
	}
	return out
}

func copiesMax(n int) int {
	if n < 1 || n > 99 {
		return 99
	}
	return n
}

// supportsSides reports duplex only when the device says so. An empty list
// means the queue never reported sides; that is "unknown", and offering duplex
// blindly is what produces a one-sided print or a rejected job.
func supportsSides(sides []string) bool {
	for _, s := range sides {
		if strings.HasPrefix(strings.TrimSpace(s), "two-sided") {
			return true
		}
	}
	return false
}

func supportsColor(modes []string) bool {
	for _, m := range modes {
		if strings.EqualFold(strings.TrimSpace(m), "color") {
			return true
		}
	}
	return false
}
