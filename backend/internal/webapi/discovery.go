package webapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudprint/internal/cups"
	"cloudprint/internal/ipp"
	"cloudprint/internal/printer/discovery"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	list, err := s.Store.ListCandidates(r.Context(), status)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	// A Windows-helper endpoint has no IP in its URI, so comparing discovered
	// URIs alone makes an already-added printer show up as "new" on every scan.
	// Match by IP / MAC / serial against the printers that actually exist.
	printers, _ := s.Store.ListPrinters(r.Context(), true)
	byHost := map[string]string{}
	byMAC := map[string]string{}
	bySerial := map[string]string{}
	names := map[string]string{}
	for _, p := range printers {
		names[p.ID] = p.DisplayName
		if p.NetworkAddress != "" {
			byHost[strings.ToLower(strings.TrimSpace(p.NetworkAddress))] = p.DisplayName
		}
		for _, ep := range p.Endpoints {
			if host := uriHost(ep.URI); host != "" {
				byHost[host] = p.DisplayName
			}
			if ep.MAC != "" {
				byMAC[strings.ToLower(strings.TrimSpace(ep.MAC))] = p.DisplayName
			}
			if ep.SerialNumber != "" {
				bySerial[strings.ToLower(strings.TrimSpace(ep.SerialNumber))] = p.DisplayName
			}
		}
	}
	for i := range list {
		c := &list[i]
		if c.MergedPrinterID != "" {
			c.AlreadyAdded = true
			c.PrinterName = names[c.MergedPrinterID]
		}
		if name, ok := byHost[strings.ToLower(strings.TrimSpace(c.Host))]; ok {
			c.AlreadyAdded = true
			c.PrinterName = firstNonEmpty(c.PrinterName, name)
		}
		if name, ok := byMAC[strings.ToLower(strings.TrimSpace(c.MAC))]; ok && c.MAC != "" {
			c.AlreadyAdded = true
			c.PrinterName = firstNonEmpty(c.PrinterName, name)
		}
		if name, ok := bySerial[strings.ToLower(strings.TrimSpace(c.SerialNumber))]; ok && c.SerialNumber != "" {
			c.AlreadyAdded = true
			c.PrinterName = firstNonEmpty(c.PrinterName, name)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": list})
}

func uriHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// candidateHost extracts the printer's LAN address from the address the admin
// typed or selected. A bare IP is accepted too, because the UI normalises it
// before calling the API but older clients may not.
func candidateHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host := uriHost(raw); host != "" {
		return host
	}
	if i := strings.Index(raw, ":"); i > 0 && !strings.Contains(raw, "/") {
		raw = raw[:i]
	}
	raw = strings.Trim(raw, "[]")
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String()
	}
	return ""
}

func (s *Server) handleAddCandidate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URI   string `json:"uri"`
		Kind  string `json:"kind"`
		Notes string `json:"notes"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	f, err := parseCandidateURI(in.URI, in.Kind)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "URI_INVALID", err.Error())
		return
	}
	f.Source = "manual"
	f.Raw = map[string]any{"notes": in.Notes, "added_by": currentUser(r).Username}
	id, created, err := s.Store.UpsertCandidate(r.Context(), store.CandidateInput{
		Source: f.Source, DiscoveredURI: f.URI, Kind: f.Kind, Host: f.Host,
		Port: f.Port, Path: f.Path, MAC: f.MAC, Vendor: f.Vendor,
		Model: f.Model, SerialNumber: f.Serial, Raw: f.Raw,
	})
	if err != nil {
		s.jsonError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"id": id, "created": created})
}

func parseCandidateURI(raw, kind string) (discovery.Found, error) {
	raw = strings.TrimSpace(raw)
	if kind == "" {
		kind = "ipp"
	}
	if kind == "socket" {
		return discovery.Found{}, errString("RAW 9100 无法解释 PDF，请改用 CUPS 队列接入")
	}
	if kind != "ipp" {
		return discovery.Found{}, errString("端点类型必须是 ipp")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return discovery.Found{}, errString("地址格式不正确")
	}
	if kind == "ipp" && u.Scheme != "ipp" && u.Scheme != "ipps" {
		return discovery.Found{}, errString("IPP 地址必须以 ipp:// 或 ipps:// 开头")
	}
	host := u.Hostname()
	if host == "" {
		return discovery.Found{}, errString("地址缺少主机名或 IP")
	}
	port := u.Port()
	if port == "" {
		port = "631"
	}
	pn, err := strconv.Atoi(port)
	if err != nil || pn < 1 || pn > 65535 {
		return discovery.Found{}, errString("端口无效")
	}
	path := u.Path
	if path == "" && kind == "ipp" {
		path = "/ipp/print"
	}
	uri := kind + "://" + net.JoinHostPort(host, port) + path
	return discovery.Found{
		URI: uri, Kind: kind, Host: host, Port: pn, Path: path,
	}, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func (s *Server) handleApproveCandidate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PrinterID   string `json:"printer_id"`
		DisplayName string `json:"display_name"`
		Location    string `json:"location"`
		Priority    int    `json:"priority"`
		Probe       *bool  `json:"probe"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	c, err := s.Store.GetCandidate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "CANDIDATE_NOT_FOUND", "候选不存在")
		return
	}
	if c.Status != "new" {
		writeErr(w, http.StatusConflict, "CANDIDATE_CLOSED", "该候选已处理")
		return
	}
	var printerID string
	if in.PrinterID != "" {
		if _, err := s.Store.GetPrinter(r.Context(), in.PrinterID); err != nil {
			writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "目标打印机不存在")
			return
		}
		printerID = in.PrinterID
	} else {
		name := strings.TrimSpace(in.DisplayName)
		if name == "" {
			name = firstNonEmpty(c.Model, c.Host, "新打印机")
		}
		p, err := s.Store.CreatePrinter(r.Context(), store.NewPrinter{
			DisplayName:  name,
			Location:     strings.TrimSpace(in.Location),
			Vendor:       c.Vendor,
			Model:        c.Model,
			SerialNumber: c.SerialNumber,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "PRINTER_INVALID", err.Error())
			return
		}
		printerID = p.ID
	}
	ep, err := s.Store.CreateEndpoint(r.Context(), printerID, store.NewEndpoint{
		Kind: c.Kind, URI: c.DiscoveredURI, Priority: in.Priority,
		MAC: c.MAC, SerialNumber: c.SerialNumber,
	})
	if errors.Is(err, store.ErrConflict) {
		existing, getErr := s.Store.GetEndpointByDedupe(r.Context(), store.DedupeKey(c.Kind, c.DiscoveredURI))
		if getErr != nil {
			writeErr(w, http.StatusConflict, "ENDPOINT_EXISTS", "这个地址已经接入过，请刷新页面查看")
			return
		}
		ep = existing
		err = nil
	}
	if err != nil {
		s.jsonError(w, err)
		return
	}
	_ = s.Store.SetCandidateStatus(r.Context(), c.ID, "approved", printerID)
	s.Store.Audit(r.Context(), currentUser(r).ID, "approve_candidate", "candidate", c.ID, map[string]any{
		"printer_id": printerID, "endpoint_id": ep.ID,
	})
	if in.Probe == nil || *in.Probe {
		go func() {
			probe := s.Printer.Probe(context.Background(), ep)
			s.Log.Info("approved endpoint probed", "endpoint", ep.ID, "health", probe.Health)
		}()
	}
	p, _ := s.Store.GetPrinter(r.Context(), printerID)
	writeJSON(w, http.StatusOK, map[string]any{"printer": p, "candidate": c})
}

func (s *Server) handleIgnoreCandidate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.Store.SetCandidateStatus(r.Context(), id, "ignored", ""); err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "ignore_candidate", "candidate", id, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode  string   `json:"mode"`
		CIDR  string   `json:"cidr"`
		CIDRs []string `json:"cidrs"`
	}
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &in) {
			return
		}
	}
	if in.Mode == "" {
		in.Mode = "mdns"
	}
	if in.Mode != "mdns" && in.Mode != "scan" && in.Mode != "all" {
		writeErr(w, http.StatusBadRequest, "MODE_INVALID", "搜索方式不正确")
		return
	}
	cidrs := append([]string{}, in.CIDRs...)
	if strings.TrimSpace(in.CIDR) != "" {
		cidrs = append(cidrs, in.CIDR)
	}
	if len(cidrs) == 0 && len(s.Cfg.ScanCIDRs) > 0 {
		cidrs = append(cidrs, s.Cfg.ScanCIDRs...)
	}
	if in.Mode == "scan" || in.Mode == "all" {
		if !s.Cfg.AllowPrivateScan {
			writeErr(w, http.StatusForbidden, "SCAN_DISABLED", "自动搜索已关闭，请手动添加打印机")
			return
		}
		if len(cidrs) == 0 {
			cidrs = discovery.LocalCIDRs()
		}
		if len(cidrs) == 0 {
			writeErr(w, http.StatusBadRequest, "SCAN_NO_NETWORK", "没有找到可搜索的公司网络。请确认这台电脑连着公司网络，或手动填写打印机 IP。")
			return
		}
		normalized := []string{}
		for _, raw := range cidrs {
			c, err := discovery.NormalizeCIDR(raw)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "CIDR_INVALID", err.Error())
				return
			}
			skip := false
			for _, existing := range normalized {
				if existing == c {
					skip = true
					break
				}
			}
			if !skip {
				normalized = append(normalized, c)
			}
		}
		cidrs = normalized
	}
	run, err := s.Store.CreateDiscoveryRun(r.Context(), in.Mode, strings.Join(cidrs, ","))
	if err != nil {
		s.jsonError(w, err)
		return
	}
	go s.runDiscovery(run.ID, in.Mode, cidrs)
	writeJSON(w, http.StatusAccepted, run)
}

type discoveryResult struct {
	items []discovery.Found
	errs  []string
}

func (s *Server) runDiscovery(runID, mode string, cidrs []string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	ch := make(chan discoveryResult, 2)
	jobs := 0
	if mode == "mdns" || mode == "all" {
		if s.Cfg.EnableMDNS {
			jobs++
			go func() {
				res := discovery.BrowseMDNS(ctx, 5*time.Second)
				ch <- discoveryResult{items: res.Services, errs: res.Errors}
			}()
		}
	}
	if mode == "scan" || mode == "all" {
		jobs++
		go func() {
			var out discoveryResult
			for _, cidr := range cidrs {
				res, err := (discovery.Scanner{}).ScanCIDR(ctx, cidr)
				if err != nil {
					out.errs = append(out.errs, err.Error())
				}
				if len(res) > 0 {
					// Scan results are candidates; do not print or auto-enable.
					out.items = append(out.items, res...)
				}
			}
			ch <- out
		}()
	}

	var results []discoveryResult
	errs := []string{}
	for i := 0; i < jobs; i++ {
		// Defensive bound: the discovery worker must always finish so the UI
		// stops saying "搜索中". All scanners already honor ctx.
		select {
		case r := <-ch:
			results = append(results, r)
		case <-ctx.Done():
			errs = append(errs, "搜索超时，请稍后再试")
			_ = s.Store.FinishDiscoveryRun(ctx, runID, 0, strings.Join(errs, "; "))
			return
		}
	}
	found := mergeFound(results)
	for _, r := range results {
		errs = append(errs, r.errs...)
	}

	// A dual-advertised printer (IPP + LPR, or the same device on two IPs)
	// should be one row. Prefer the IPP record, then the one with a model name.
	found = collapseFound(found)

	// Probing every IPP device sequentially can make a /24 scan feel stuck.
	// The probes are read-only, so a small worker pool is safe.
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for _, item := range found {
		item := item
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			raw := item.Raw
			if raw == nil {
				raw = map[string]any{}
			}
			if item.Kind == "ipp" && item.Port == 631 {
				// mDNS only tells us the device advertises IPP. The authoritative
				// answer to "can it print our PDF directly" is a read-only query.
				probeCtx, probeCancel := context.WithTimeout(ctx, 5*time.Second)
				direct, model, vendor, ok := discovery.ProbeDirect(probeCtx, item.URI)
				probeCancel()
				raw["probed"] = ok
				raw["direct"] = ok && direct
				raw["needs_driver"] = ok && !direct
				if model != "" {
					if clean, ok := cleanModelName(model); ok {
						item.Model = clean
					}
				}
				if vendor != "" {
					item.Vendor = vendor
				}
			}
			if item.Kind == "socket" {
				raw["direct"] = false
				raw["needs_driver"] = true
			}
			_, _, err := s.Store.UpsertCandidate(ctx, store.CandidateInput{
				Source: item.Source, DiscoveredURI: item.URI, Kind: item.Kind, Host: item.Host,
				Port: item.Port, Path: item.Path, MAC: item.MAC, Vendor: item.Vendor,
				Model: item.Model, SerialNumber: item.Serial, Raw: raw,
			})
			if err == nil {
				// Do not resurrect ignored candidates: an admin who clicked
				// "忽略" expects that device to stay out of the list on the
				// next scan. Already-added devices are filtered by IP.
				mu.Lock()
				count++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	errMsg := strings.Join(errs, "; ")
	_ = s.Store.FinishDiscoveryRun(ctx, runID, count, errMsg)
}

// mergeFound removes duplicates that arrive from mDNS and a subnet scan at the
// same time, and keeps the richer record when one source knows the model name.
func mergeFound(results []discoveryResult) []discovery.Found {
	index := map[string]int{}
	out := make([]discovery.Found, 0)
	for _, r := range results {
		for _, f := range r.items {
			key := strings.ToLower(f.Kind + "|" + f.URI)
			if i, ok := index[key]; ok {
				if out[i].Model == "" {
					out[i].Model = f.Model
				}
				if out[i].Vendor == "" {
					out[i].Vendor = f.Vendor
				}
				if out[i].Serial == "" {
					out[i].Serial = f.Serial
				}
				continue
			}
			index[key] = len(out)
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// collapseFound merges the same physical device advertised by several
// protocols. The IP is the stable key here: the UI's "add" action uses it, and
// both IPP and LPR records point at the same printer.
func collapseFound(items []discovery.Found) []discovery.Found {
	byHost := map[string]int{}
	out := make([]discovery.Found, 0, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item.Host))
		if key == "" {
			continue
		}
		if i, ok := byHost[key]; ok {
			keep := out[i]
			// Prefer a real IPP endpoint, then the entry that knows the model.
			if (item.Kind == "ipp" && keep.Kind != "ipp") ||
				(item.Kind == keep.Kind && keep.Model == "" && item.Model != "") {
				item.Model = firstNonEmpty(item.Model, keep.Model)
				item.Vendor = firstNonEmpty(item.Vendor, keep.Vendor)
				item.Serial = firstNonEmpty(item.Serial, keep.Serial)
				out[i] = item
			} else {
				if out[i].Model == "" && item.Model != "" {
					out[i].Model = item.Model
				}
				if out[i].Vendor == "" && item.Vendor != "" {
					out[i].Vendor = item.Vendor
				}
				if out[i].Serial == "" && item.Serial != "" {
					out[i].Serial = item.Serial
				}
			}
			continue
		}
		byHost[key] = len(out)
		out = append(out, item)
	}
	return out
}

func (s *Server) handleDiscoveryRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Store.ListDiscoveryRuns(r.Context(), 20)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// handleScanTargets suggests which subnets to scan, in the order most likely
// to be right. A container only sees the Docker bridge, so the office LAN
// usually has to come from an existing printer or the admin's own address.
func (s *Server) handleScanTargets(w http.ResponseWriter, r *http.Request) {
	printers, _ := s.Store.ListPrinters(r.Context(), true)
	hosts := make([]string, 0, len(printers))
	for _, p := range printers {
		if p.NetworkAddress != "" {
			hosts = append(hosts, p.NetworkAddress)
		}
	}
	client := discovery.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"))
	list := discovery.SuggestCIDRs(s.Cfg.ScanCIDRs, hosts, client)
	writeJSON(w, http.StatusOK, map[string]any{
		"targets":          discovery.FilterCandidateCIDRs(list),
		"client_ip":        client,
		"can_scan":         s.Cfg.AllowPrivateScan,
		"configured_cidrs": s.Cfg.ScanCIDRs,
	})
}

// handleQuickAddPrinter is the one-click path used by the UI. It probes the
// device, then picks one of three Linux-native routes:
//
//   - the device accepts PDF over IPP  -> schedule it directly
//   - it speaks PWG-Raster/URF         -> driverless CUPS queue
//   - neither                          -> CUPS queue with a vendor PPD
//
// There is no Windows relay any more; CUPS plus the vendor driver is the
// equivalent, and it runs on the same Linux host as CloudPrint.
func (s *Server) handleQuickAddPrinter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName string `json:"display_name"`
		Location    string `json:"location"`
		URI         string `json:"uri"`
		Host        string `json:"host"`
		Vendor      string `json:"vendor"`
		Model       string `json:"model"`
		PPD         string `json:"ppd"`
		DriverMode  string `json:"driver_mode"`
		CandidateID string `json:"candidate_id"`
		PrinterID   string `json:"printer_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "NAME_REQUIRED", "请填写打印机名称，方便同事辨认")
		return
	}
	rawURI := strings.TrimSpace(in.URI)
	if rawURI == "" {
		rawURI = strings.TrimSpace(in.Host)
	}
	if rawURI == "" {
		writeErr(w, http.StatusBadRequest, "URI_REQUIRED", "请填写打印机 IP 地址")
		return
	}
	if !strings.Contains(rawURI, "://") {
		rawURI = "ipp://" + strings.TrimSuffix(rawURI, "/") + "/ipp/print"
	}
	f, err := parseCandidateURI(rawURI, "ipp")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "URI_INVALID", err.Error())
		return
	}

	var p *store.Printer
	created := false
	if in.PrinterID != "" {
		existing, err := s.Store.GetPrinter(r.Context(), in.PrinterID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
			return
		}
		p = existing
		_ = s.Store.UpdatePrinter(r.Context(), p.ID, &name, &in.Location, nil, nil, nil, nil)
	} else {
		made, err := s.Store.CreatePrinter(r.Context(), store.NewPrinter{
			DisplayName: name,
			Location:    strings.TrimSpace(in.Location),
			Vendor:      strings.TrimSpace(in.Vendor),
			Model:       strings.TrimSpace(in.Model),
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, "PRINTER_INVALID", err.Error())
			return
		}
		p = made
		created = true
	}
	cleanup := func() {
		if created {
			_ = s.Store.DeletePrinter(r.Context(), p.ID)
		}
	}
	if host := candidateHost(f.URI); host != "" {
		_ = s.Store.SetPrinterNetworkAddress(r.Context(), p.ID, host)
	}

	// Everything below is decided by what the device actually reports. A model
	// name taken from discovery can be stale or wrong, and picking a driver
	// from the wrong device is how jobs silently vanish.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	vendor := firstNonEmpty(in.Vendor, f.Vendor)
	model := firstNonEmpty(in.Model, f.Model)
	directPDF := false
	driverless := false
	if c, err := ipp.NewClient(f.URI, 15*time.Second); err == nil {
		if msg, err := c.GetPrinterAttributes(ctx, []string{
			"printer-make-and-model", "document-format-supported",
		}); err == nil {
			if mm := strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-make-and-model")); mm != "" {
				if clean, ok := cleanModelName(mm); ok {
					model = firstNonEmpty(model, clean)
				}
			}
			for _, format := range msg.Strings(ipp.TagPrinterAttributes, "document-format-supported") {
				if format == "application/pdf" || format == "*/*" {
					directPDF = true
				}
				// IPP Everywhere / AirPrint devices accept PWG-Raster or URF.
				// CUPS can drive them with the "everywhere" PPD, which is
				// generic and survives driver updates.
				if format == "image/pwg-raster" || format == "image/urf" {
					driverless = true
				}
			}
		}
	}
	_ = s.Store.UpdatePrinter(r.Context(), p.ID, nil, nil, &vendor, &model, nil, nil)

	if directPDF && strings.TrimSpace(in.PPD) == "" {
		_ = s.Store.DisablePrinterEndpoints(r.Context(), p.ID)
		ep, err := s.Store.EnablePrinterEndpoint(r.Context(), p.ID, "ipp", f.URI)
		if err != nil {
			cleanup()
			writeErr(w, http.StatusBadRequest, "ENDPOINT_INVALID", err.Error())
			return
		}
		res := s.Printer.Probe(ctx, ep)
		s.Store.Audit(r.Context(), currentUser(r).ID, "quick_add_printer", "printer", p.ID, map[string]any{
			"uri": f.URI, "mode": "ipp", "health": res.Health,
		})
		updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
		writeJSON(w, http.StatusCreated, map[string]any{
			"printer": updated, "probe": res, "mode": "ipp", "direct": true,
			"message": "已接入，可以直接打印",
		})
		if in.CandidateID != "" {
			_ = s.Store.SetCandidateStatus(r.Context(), in.CandidateID, "approved", p.ID)
		}
		return
	}

	// The device needs a driver. Match one by the model it reported unless the
	// admin picked a PPD explicitly.
	ok, reason := s.CUPS.Available()
	if !ok {
		cleanup()
		writeErr(w, http.StatusConflict, "CUPS_UNAVAILABLE", reason)
		return
	}
	ppdPath := strings.TrimSpace(in.PPD)
	driverName := ""
	if ppdPath != "" {
		driverName = ppdPath
	}
	// A vendor PPD wins over driverless even when the device advertises
	// PWG-Raster or URF. Those formats are only a claim: several network
	// printers here answer with "document-format-error" for anything but their
	// own dialect, and the DRIVERLESS queue then swallows every job. A matched
	// PPD names the one filter that actually works with this hardware.
	if ppdPath == "" {
		if best := s.CUPS.MatchPPD(model); best != nil {
			ppdPath = best.Path
			driverName = best.Name
		}
	}
	useDriverless := ppdPath == "" && driverless && !strings.EqualFold(in.DriverMode, "ppd")
	if ppdPath == "" {
		if !useDriverless {
			cleanup()
			writeErr(w, http.StatusConflict, "DRIVER_REQUIRED",
				"这台打印机不接收 PDF，也不支持无驱（PWG-Raster/URF）协议，系统里没有匹配的驱动。请先安装厂商 Linux 驱动（PPD），再重新添加。")
			return
		}
		driverName = "IPP Everywhere（无驱）"
	}
	queue, err := s.CUPS.UniqueQueueName(ctx, p.DisplayName, p.ID, f.URI)
	if err != nil {
		cleanup()
		writeErr(w, http.StatusBadGateway, "CUPS_LIST_FAILED",
			"无法读取 CUPS 队列列表："+err.Error())
		return
	}
	if err := s.CUPS.Create(ctx, cups.CreateOptions{
		Name:      queue,
		DeviceURI: f.URI,
		PPD:       ppdPath,
		Location:  p.Location,
		Info:      p.DisplayName,
	}); err != nil {
		cleanup()
		writeErr(w, http.StatusBadGateway, "CUPS_CREATE_FAILED", "创建 CUPS 队列失败："+err.Error())
		return
	}
	_ = s.Store.DisablePrinterEndpoints(r.Context(), p.ID)
	ep, err := s.Store.EnablePrinterEndpoint(r.Context(), p.ID, "cups", queue)
	if err != nil {
		cleanup()
		writeErr(w, http.StatusBadRequest, "ENDPOINT_INVALID", err.Error())
		return
	}
	_ = s.Store.SetEndpointQueue(r.Context(), ep.ID, queue, queue, driverName)
	// SetEndpointQueue fills in queue_name, which probeCUPS needs. Reload so the
	// probe does not use the pre-update copy with an empty queue name.
	if fresh, getErr := s.Store.GetEndpoint(r.Context(), ep.ID); getErr == nil {
		ep = fresh
	}
	res := s.Printer.Probe(ctx, ep)
	s.Store.Audit(r.Context(), currentUser(r).ID, "quick_add_printer", "printer", p.ID, map[string]any{
		"uri": f.URI, "mode": "cups", "queue": queue, "ppd": driverName,
	})
	updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
	message := "已用 CUPS 驱动（" + driverName + "）接入，可以直接打印"
	if res.Health != "healthy" {
		message = "CUPS 队列已创建，但打印机现在没响应。请检查它是否开机、IP 是否变化。"
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"printer": updated, "probe": res, "mode": "cups", "direct": false,
		"queue": queue, "driver": driverName, "message": message,
	})
	if in.CandidateID != "" {
		_ = s.Store.SetCandidateStatus(r.Context(), in.CandidateID, "approved", p.ID)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// cleanModelName drops vendor boilerplate and control bytes that some
// printers put in printer-make-and-model. A name like "Pantum MS6000NW
// series" is useful; a raw UTF-16 blob is not.
func cleanModelName(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > 120 {
		return "", false
	}
	printable := 0
	for _, r := range s {
		if r < 0x20 && r != '\t' || r == 0x7f || r == 0xfffd {
			return "", false
		}
		if r != ' ' {
			printable++
		}
	}
	if printable < 3 {
		return "", false
	}
	return s, true
}
