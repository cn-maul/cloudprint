package printer

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"time"

	"cloudprint/internal/cups"
	"cloudprint/internal/ipp"
	"cloudprint/internal/store"
)

type Service struct {
	Store *store.Store
	CUPS  *cups.Client
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
	switch ep.Kind {
	case "ipp":
		reachable := tcpReachable(ctx, ep.URI, "631", 5*time.Second)
		if !reachable {
			res.Error = "打印机现在连不上网络，请检查它是否开机、网线或 Wi-Fi 是否正常"
			s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_fail", map[string]any{"error": res.Error})
			return res
		}
		s.probeIPP(ctx, ep, &res)
	case "cups":
		s.probeCUPS(ctx, ep, &res)
	default:
		res.Error = "无法识别这台打印机的连接方式"
	}
	return res
}

func (s *Service) probeIPP(ctx context.Context, ep *store.Endpoint, res *ProbeResult) {
	client, err := ipp.NewClient(ep.URI, 15*time.Second)
	if err != nil {
		res.Error = err.Error()
		s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_fail", map[string]any{"error": res.Error})
		return
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
		return
	}
	caps := extractCaps(msg)
	res.Capabilities = caps
	res.Health = "healthy"
	if !caps.SupportsPDF() {
		// The device speaks IPP but only a raster/PCL dialect. Linux can still
		// print to it, but only through a CUPS queue with a vendor PPD, so the
		// endpoint itself must not be scheduled.
		res.Health = "degraded"
		res.Error = "这台打印机不直接接收 PDF，请在服务器上装好驱动后用 CUPS 队列接入"
	}
	if caps.State == "stopped" {
		res.Health = "degraded"
		res.Error = "打印机处于暂停状态，请在打印机面板上恢复"
	}
	_ = s.Store.UpdateEndpointHealth(ctx, ep.ID, res.Health, caps, res.Error)
	s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_ok", map[string]any{
		"health": res.Health,
		"caps":   caps,
		"ms":     res.DurationMS,
	})
}

// probeCUPS asks the spooler, not the device, whether the queue can print.
// CUPS already knows the driver and reports a stopped queue when the device is
// unreachable, which is the same condition that would swallow a job.
func (s *Service) probeCUPS(ctx context.Context, ep *store.Endpoint, res *ProbeResult) {
	if s.CUPS == nil {
		res.Error = "CUPS 未初始化"
		return
	}
	q, err := s.CUPS.Queue(ctx, ep.QueueName)
	if err != nil {
		res.Error = "CUPS 队列不可用：" + err.Error()
		_ = s.Store.UpdateEndpointHealth(ctx, ep.ID, "down", store.Capabilities{}, res.Error)
		return
	}
	caps := store.Capabilities{
		DocumentFormats: []string{"application/pdf"},
		MakeModel:       q.MakeModel,
		State:           q.State,
		Media:           []string{"A4", "A3", "Letter", "Legal"},
		MaxCopies:       99,
		CheckedAt:       store.Now(),
	}
	// The queue's PPD is not consulted: rendering happens before the document
	// reaches CUPS. Ask the device what resolution it wants instead.
	if uri, uerr := s.CUPS.DeviceURI(ctx, ep.QueueName); uerr == nil && uri != "" {
		if client, cerr := ipp.NewClient(uri, 8*time.Second); cerr == nil {
			if msg, merr := client.GetPrinterAttributes(ctx, []string{
				"printer-resolution-default", "document-format-supported",
			}); merr == nil {
				if v, ok := msg.Resolution(ipp.TagPrinterAttributes, "printer-resolution-default"); ok {
					caps.Resolution = v
				}
				caps.DocumentFormats = msg.Strings(ipp.TagPrinterAttributes, "document-format-supported")
			}
		}
	}
	res.Capabilities = caps
	switch {
	case !q.Enabled:
		res.Health = "down"
		res.Error = "CUPS 队列已停用，请到 CUPS 里启用它"
	case q.State == "stopped":
		res.Health = "down"
		res.Error = firstNonEmpty(q.Message, "打印机已暂停，CUPS 无法出纸")
	default:
		res.Health = "healthy"
	}
	_ = s.Store.UpdateEndpointHealth(ctx, ep.ID, res.Health, caps, res.Error)
	s.Store.RecordEndpointEvent(ctx, ep.ID, "probe_ok", map[string]any{
		"health": res.Health, "queue": q.Name, "driver": q.MakeModel,
	})
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
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
func SelectEndpoint(printer *store.Printer, duplex string) *store.Endpoint {
	if printer == nil {
		return nil
	}
	candidates := make([]store.Endpoint, 0, len(printer.Endpoints))
	for _, ep := range printer.Endpoints {
		if !ep.IsEnabled || ep.Health != "healthy" {
			continue
		}
		if ep.Kind == "ipp" && !ep.Capabilities.SupportsPDF() {
			continue
		}
		if ep.Kind != "ipp" && ep.Kind != "cups" {
			continue
		}
		if !supportsDuplex(ep.Capabilities, duplex) {
			continue
		}
		candidates = append(candidates, ep)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
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
