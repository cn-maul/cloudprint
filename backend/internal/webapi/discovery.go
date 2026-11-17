package webapi

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudprint/internal/auth"
	"cloudprint/internal/printer/discovery"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
	"net/http"
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
		return discovery.Found{}, errString("RAW 9100 无法直接解释 PDF，请改用 Windows 代理端点")
	}
	if kind != "ipp" && kind != "agent" {
		return discovery.Found{}, errString("端点类型必须是 ipp 或 agent")
	}
	if kind == "agent" {
		return discovery.Found{
			URI: raw, Kind: kind, Host: raw, Port: 0, Path: "",
		}, nil
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
	if err != nil && err != store.ErrConflict {
		s.jsonError(w, err)
		return
	}
	_ = s.Store.SetCandidateStatus(r.Context(), c.ID, "approved", printerID)
	s.Store.Audit(r.Context(), currentUser(r).ID, "approve_candidate", "candidate", c.ID, map[string]any{
		"printer_id": printerID, "endpoint_id": ep.ID,
	})
	if ep != nil && (in.Probe == nil || *in.Probe) {
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
				raw["needs_agent"] = !ok || !direct
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
				raw["needs_agent"] = true
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

// handleQuickAddPrinter is the one-click path used by the UI: create the
// printer, attach the discovered address, probe it and report in plain language
// whether it can print directly.
func (s *Server) handleQuickAddPrinter(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName string `json:"display_name"`
		Location    string `json:"location"`
		URI         string `json:"uri"`
		Vendor      string `json:"vendor"`
		Model       string `json:"model"`
		Mode        string `json:"mode"`
		HelperName  string `json:"helper_name"`
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
	var p *store.Printer
	if in.PrinterID != "" {
		existing, err := s.Store.GetPrinter(r.Context(), in.PrinterID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "PRINTER_NOT_FOUND", "打印机不存在")
			return
		}
		p = existing
		_ = s.Store.UpdatePrinter(r.Context(), p.ID, &name, &in.Location, nil, nil, nil, nil)
	} else {
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
	}
	if host := candidateHost(rawURI); host != "" {
		_ = s.Store.SetPrinterNetworkAddress(r.Context(), p.ID, host)
		p.NetworkAddress = host
	}
	forceAgent := in.Mode == "agent" || strings.HasPrefix(strings.ToLower(rawURI), "socket://")
	if !forceAgent && rawURI != "" {
		f, err := parseCandidateURI(rawURI, "ipp")
		if err != nil {
			if in.PrinterID == "" {
				_ = s.Store.DeletePrinter(r.Context(), p.ID)
			}
			writeErr(w, http.StatusBadRequest, "URI_INVALID", err.Error())
			return
		}
		probeCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		direct, model, vendor, probed := discovery.ProbeDirect(probeCtx, f.URI)
		cancel()
		if !probed {
			// No answer at all: save the address and let the page say the
			// printer is unreachable. Forcing a Windows helper here would ask
			// the admin to install a program on a computer for a printer that
			// may simply be switched off.
			rawURI = f.URI
		} else if !direct {
			// The device answered but cannot consume PDF. Attach the Windows
			// helper instead of leaving an endpoint that can never print.
			forceAgent = true
			if p.Model == "" {
				p.Model = model
			}
			if p.Vendor == "" {
				p.Vendor = vendor
			}
			_ = s.Store.UpdatePrinter(r.Context(), p.ID, nil, nil, &p.Vendor, &p.Model, nil, nil)
		} else {
			rawURI = f.URI
		}
	}
	if forceAgent {
		if in.PrinterID != "" {
			// Reconnecting an old printer must not leave its previous dead
			// endpoint enabled; otherwise the UI keeps showing "助手没运行".
			// Rows are disabled, not deleted: historic jobs reference them.
			if oldAgents, err := s.Store.DisablePrinterEndpoints(r.Context(), p.ID); err == nil {
				for _, id := range oldAgents {
					_ = s.Store.DeactivateAgent(r.Context(), id)
				}
			}
		}
		setup, err := s.createAgentForPrinter(r, p, strings.TrimSpace(in.HelperName))
		if err != nil {
			if in.PrinterID == "" {
				_ = s.Store.DeletePrinter(r.Context(), p.ID)
			}
			s.jsonError(w, err)
			return
		}
		updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
		s.Store.Audit(r.Context(), currentUser(r).ID, "quick_add_printer", "printer", p.ID, map[string]any{
			"uri": rawURI, "mode": "agent", "endpoint": setup["endpoint_id"],
		})
		writeJSON(w, http.StatusCreated, map[string]any{
			"printer": updated,
			"direct":  false,
			"mode":    "agent",
			"message": "打印机已保存。还差一步：在一台能正常打印它的 Windows 电脑上运行安装命令。",
			"agent":   setup,
		})
		if in.CandidateID != "" {
			_ = s.Store.SetCandidateStatus(r.Context(), in.CandidateID, "approved", p.ID)
		}
		return
	}
	f, err := parseCandidateURI(rawURI, "ipp")
	if err != nil {
		if in.PrinterID == "" {
			_ = s.Store.DeletePrinter(r.Context(), p.ID)
		}
		writeErr(w, http.StatusBadRequest, "URI_INVALID", err.Error())
		return
	}
	if p.Vendor == "" {
		p.Vendor = f.Vendor
	}
	if p.Model == "" {
		p.Model = f.Model
	}
	_ = s.Store.UpdatePrinter(r.Context(), p.ID, nil, nil, &p.Vendor, &p.Model, nil, nil)
	ep, err := s.Store.EnablePrinterEndpoint(r.Context(), p.ID, "ipp", f.URI)
	if err != nil {
		if in.PrinterID == "" {
			_ = s.Store.DeletePrinter(r.Context(), p.ID)
		}
		if err == store.ErrConflict {
			writeErr(w, http.StatusConflict, "ENDPOINT_EXISTS", "这个地址已经被另一台打印机使用，请检查是否重复添加")
			return
		}
		writeErr(w, http.StatusBadRequest, "ENDPOINT_INVALID", err.Error())
		return
	}
	if in.PrinterID != "" {
		// Switching a printer from a Windows helper back to a network address
		// must retire the other endpoints, otherwise two paths stay enabled
		// and the page may report the wrong reason when printing fails. Do it
		// after the new endpoint resolved, so a conflict cannot leave the
		// printer with nothing enabled.
		if oldAgents, err := s.Store.DisablePrinterEndpoints(r.Context(), p.ID); err == nil {
			for _, id := range oldAgents {
				_ = s.Store.DeactivateAgent(r.Context(), id)
			}
		}
		ep, err = s.Store.EnablePrinterEndpoint(r.Context(), p.ID, "ipp", f.URI)
		if err != nil {
			s.jsonError(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	res := s.Printer.Probe(ctx, ep)
	s.Store.Audit(r.Context(), currentUser(r).ID, "quick_add_printer", "printer", p.ID, map[string]any{
		"uri": f.URI, "health": res.Health,
	})
	updated, _ := s.Store.GetPrinter(r.Context(), p.ID)
	direct := res.Health == "healthy" && res.Capabilities.SupportsPDF()
	message := "已添加，可以直接发起打印"
	if !direct {
		switch {
		case res.Health == "down":
			message = "已保存，但现在连不上这台打印机。请检查它是否开机、IP 是否变化。"
		case !res.Capabilities.SupportsPDF():
			message = "这台打印机不支持直接接收 PDF。请在一台装有它驱动的 Windows 电脑上安装打印助手。"
		default:
			message = "已保存，但设备状态异常：" + res.Error
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"printer": updated,
		"probe":   res,
		"direct":  direct,
		"mode":    "direct",
		"message": message,
	})
	if in.CandidateID != "" {
		_ = s.Store.SetCandidateStatus(r.Context(), in.CandidateID, "approved", p.ID)
	}
}

func (s *Server) createAgentForPrinter(r *http.Request, p *store.Printer, windowsQueue string) (map[string]any, error) {
	// Keep the generated name short and human-readable; the page shows it as
	// "which Windows PC is relaying this printer". A random suffix is still
	// needed because agent names are unique.
	name := "转发：" + p.DisplayName + "-" + strings.ToUpper(auth.RandomToken(2))
	token := auth.RandomToken(32)
	command := `powershell -NoProfile -Command Start-Process -FilePath %s -Verb Print`
	if windowsQueue != "" {
		command = fmt.Sprintf(`powershell -NoProfile -Command Start-Process -FilePath %%s -Verb PrintTo -ArgumentList '%s'`,
			strings.ReplaceAll(windowsQueue, "'", "''"))
	}
	agent, err := s.Store.CreateAgent(r.Context(), name, auth.HashToken(token), command)
	if err != nil {
		return nil, err
	}
	ep, err := s.Store.RebindAgentEndpoint(r.Context(), p.ID, agent.ID, windowsQueue)
	if err != nil {
		_ = s.Store.DeactivateAgent(r.Context(), agent.ID)
		return nil, err
	}
	base := s.baseURL(r)
	download := base + "/downloads/cloudprint-agent.exe"
	setup := agentSetupScript(base, token, ep.ID)
	return map[string]any{
		"id":           agent.ID,
		"name":         agent.Name,
		"token":        token,
		"endpoint_id":  ep.ID,
		"download_url": download,
		"setup_script": setup,
	}, nil
}

// agentSetupScript is shared by quick-add and token rotation. The print
// command stays on the server and is delivered when a job is claimed, so no
// user path can break it by embedding spaces or quotes in a scheduled task.
func agentSetupScript(base, token, endpointID string) string {
	taskName := "CloudPrintAgent-" + endpointID
	if len(endpointID) > 6 {
		taskName = "CloudPrintAgent-" + endpointID[len(endpointID)-6:]
	}
return fmt.Sprintf(`# 在这台 Windows 电脑上运行一次
$ErrorActionPreference = "Stop"
$dir = Join-Path $env:LOCALAPPDATA "CloudPrintAgent"
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$exe = Join-Path $dir "cloudprint-agent.exe"
Invoke-WebRequest "%s" -OutFile $exe
$arg = '-server "%s" -token "%s" -endpoint "%s"'
$action = New-ScheduledTaskAction -Execute $exe -Argument $arg
$trigger = New-ScheduledTaskTrigger -AtLogOn
Register-ScheduledTask -TaskName '%s' -Action $action -Trigger $trigger -Description 'CloudPrint 打印转发' -Force | Out-Null
Start-ScheduledTask -TaskName '%s'
Write-Host '打印转发已安装并开始运行。' -ForegroundColor Green
`, base+"/downloads/cloudprint-agent.exe", base, token, endpointID, taskName, taskName)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
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
