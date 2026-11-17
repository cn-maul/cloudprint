package printer

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"cloudprint/internal/ipp"
	"cloudprint/internal/store"
)

type Service struct {
	Store *store.Store
}

type ProbeResult struct {
	Health       string
	Capabilities store.Capabilities
	Error        string
	DurationMS   int64
}

func (s *Service) Probe(ctx context.Context, ep *store.Endpoint) (res ProbeResult) {
	start := time.Now()
	res = ProbeResult{Health: "down"}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	reachable := false
	switch ep.Kind {
	case "ipp":
		reachable = tcpReachable(ctx, ep.URI, "631", 5*time.Second)
		if !reachable {
			res.Error = "打印机现在连不上网络，请检查它是否开机、网线或 Wi-Fi 是否正常"
			s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_fail", map[string]any{"error": res.Error})
			return res
		}
		client, err := ipp.NewClient(ep.URI, 15*time.Second)
		if err != nil {
			res.Error = err.Error()
			s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_fail", map[string]any{"error": res.Error})
			return res
		}
		msg, err := client.GetPrinterAttributes(ctx, []string{
			"printer-make-and-model",
			"document-format-supported",
			"document-format-default",
			"sides-supported",
			"media-supported",
			"media-default",
			"copies-supported",
			"copies-default",
			"print-color-mode-supported",
			"printer-state",
			"printer-state-reasons",
			"printer-is-accepting-jobs",
		})
		if err != nil {
			res.Error = err.Error()
			s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_fail", map[string]any{"error": res.Error})
			return res
		}
		caps := extractCaps(msg)
		res.Capabilities = caps
		res.Health = "healthy"
		if !caps.SupportsPDF() {
			res.Health = "degraded"
			res.Error = "这台打印机不能直接接收网页上传的文件，需要 Windows 电脑上的驱动帮忙"
		}
		if caps.State == "stopped" {
			res.Health = "degraded"
			res.Error = "打印机处于暂停状态，请在打印机面板上恢复"
		}
		_ = s.Store.UpdateEndpointHealth(ctx, ep.ID, res.Health, caps, res.Error)
		s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_ok", map[string]any{
			"health": res.Health,
			"caps":   caps,
			"ms":     time.Since(start).Milliseconds(),
		})
	case "socket":
		reachable = tcpReachable(ctx, ep.URI, "9100", 5*time.Second)
		if reachable {
			res.Health = "degraded"
			res.Capabilities = store.Capabilities{
				DocumentFormats: []string{"application/octet-stream"},
				CheckedAt:       store.Now(),
			}
			res.Error = "这台打印机需要 Windows 电脑上的驱动转发"
		} else {
			res.Error = "打印机现在连不上网络，请检查它是否开机"
		}
		_ = s.Store.UpdateEndpointHealth(ctx, ep.ID, res.Health, res.Capabilities, res.Error)
	case "agent":
		reachable = agentReachable(ctx, s.Store, ep.AgentID)
		if reachable {
			res.Health = "healthy"
			res.Capabilities = store.Capabilities{
				DocumentFormats: []string{"application/pdf"},
				CheckedAt:       store.Now(),
			}
			res.Capabilities.Sides = []string{"one-sided", "two-sided-long-edge", "two-sided-short-edge"}
			res.Capabilities.Media = []string{"A4"}
			res.Capabilities.MaxCopies = 99
		} else {
			res.Error = "帮忙转发的 Windows 电脑现在不在线"
		}
		_ = s.Store.UpdateEndpointHealth(ctx, ep.ID, res.Health, res.Capabilities, res.Error)
	default:
		res.Error = "无法识别这台打印机的连接方式"
	}
	return res
}

func agentReachable(ctx context.Context, st *store.Store, agentID string) bool {
	if strings.TrimSpace(agentID) == "" {
		return false
	}
	a, err := st.GetAgent(ctx, agentID)
	if err != nil {
		return false
	}
	return a.Online && a.IsActive
}

// RefreshAgentEndpoints recalculates health for every endpoint backed by an
// agent. Agent liveness changes between the 5-minute full probes, and a stale
// "down" makes the print page grey out a printer that just came back online.
func (s *Service) RefreshAgentEndpoints(ctx context.Context) {
	printers, err := s.Store.ListPrinters(ctx, true)
	if err != nil {
		return
	}
	for _, p := range printers {
		for i := range p.Endpoints {
			ep := p.Endpoints[i]
			if ep.Kind != "agent" || !ep.IsEnabled {
				continue
			}
			aa, err := s.Store.GetAgent(ctx, ep.AgentID)
			online := err == nil && aa.Online && aa.IsActive
			if online == (ep.Health == "healthy") {
				continue
			}
			_ = s.Probe(ctx, &ep)
		}
	}
}

func tcpReachable(ctx context.Context, rawURI, defPort string, timeout time.Duration) bool {
	host, port, err := tcpTarget(rawURI, defPort)
	if err != nil {
		return false
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func tcpTarget(rawURI, defPort string) (string, string, error) {
	u, err := url.Parse(rawURI)
	if err != nil {
		return "", "", err
	}
	host := u.Hostname()
	if host == "" {
		// agent URIs are server-side identifiers, not socket targets.
		return "", "", fmt.Errorf("缺少主机")
	}
	port := u.Port()
	if port == "" {
		port = defPort
	}
	return host, port, nil
}

func extractCaps(msg *ipp.Message) store.Capabilities {
	c := store.Capabilities{
		MakeModel:       trimEmpty(msg.String(ipp.TagPrinterAttributes, "printer-make-and-model")),
		DocumentFormats: msg.Strings(ipp.TagPrinterAttributes, "document-format-supported"),
		Sides:           msg.Strings(ipp.TagPrinterAttributes, "sides-supported"),
		Media:           msg.Strings(ipp.TagPrinterAttributes, "media-supported"),
		ColorModes:      msg.Strings(ipp.TagPrinterAttributes, "print-color-mode-supported"),
		StateReasons:    msg.Strings(ipp.TagPrinterAttributes, "printer-state-reasons"),
		CheckedAt:       store.Now(),
	}
	if v, ok := msg.Int(ipp.TagPrinterAttributes, "copies-supported"); ok {
		c.MaxCopies = int(v)
	}
	if c.MaxCopies <= 1 {
		c.MaxCopies = 99
	}
	if v, ok := msg.Int(ipp.TagPrinterAttributes, "printer-state"); ok {
		switch v {
		case 3:
			c.State = "idle"
		case 4:
			c.State = "processing"
		case 5:
			c.State = "stopped"
		}
	}
	return c
}

func trimEmpty(s string) string { return strings.TrimSpace(s) }

func supportsDuplex(c store.Capabilities, mode string) bool {
	if mode == "" || mode == "one-sided" {
		return true
	}
	if len(c.Sides) == 0 {
		return true
	}
	for _, s := range c.Sides {
		if s == mode {
			return true
		}
	}
	return false
}

func (s *Service) ProbeAll(ctx context.Context) []ProbeResult {
	printers, err := s.Store.ListPrinters(ctx, true)
	if err != nil {
		return nil
	}
	var out []ProbeResult
	for _, p := range printers {
		for i := range p.Endpoints {
			ep := p.Endpoints[i]
			if !ep.IsEnabled {
				continue
			}
			out = append(out, s.Probe(ctx, &ep))
		}
	}
	return out
}

// SelectEndpoint picks the best healthy endpoint capable of the job parameters.
func SelectEndpoint(printer *store.Printer, duplex string, preferAgent bool) *store.Endpoint {
	if printer == nil {
		return nil
	}
	candidates := make([]store.Endpoint, 0, len(printer.Endpoints))
	for _, ep := range printer.Endpoints {
		if !ep.IsEnabled || ep.Health != "healthy" {
			continue
		}
		if ep.Kind == "socket" {
			continue
		}
		if ep.Kind == "ipp" && !ep.Capabilities.SupportsPDF() {
			continue
		}
		if !supportsDuplex(ep.Capabilities, duplex) {
			continue
		}
		candidates = append(candidates, ep)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if preferAgent {
			ai, aj := candidates[i].Kind == "agent", candidates[j].Kind == "agent"
			if ai != aj {
				return ai
			}
		}
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].LastSuccessAt > candidates[j].LastSuccessAt
	})
	if len(candidates) == 0 {
		return nil
	}
	return &candidates[0]
}
