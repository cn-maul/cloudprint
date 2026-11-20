package discovery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"cloudprint/internal/ipp"
)

type Scanner struct {
	Concurrency int
	Timeout     time.Duration
}

// ScanCIDR finds network printers on the usual ports and asks every open IPP
// device for its real model name. A bare open TCP port is not enough: NAS,
// cameras and web consoles also listen on 631/9100 in some networks.
func (s Scanner) ScanCIDR(ctx context.Context, cidr string) ([]Found, error) {
	normalized, err := NormalizeCIDR(cidr)
	if err != nil {
		return nil, err
	}
	_, network, err := net.ParseCIDR(normalized)
	if err != nil {
		return nil, fmt.Errorf("无效网段: %w", err)
	}
	ips := hosts(network)
	if len(ips) == 0 {
		return nil, fmt.Errorf("网段内没有可扫描的地址")
	}
	if s.Concurrency <= 0 {
		s.Concurrency = 48
	}
	if s.Timeout <= 0 {
		s.Timeout = 450 * time.Millisecond
	}

	var mu sync.Mutex
	var found []Found
	sem := make(chan struct{}, s.Concurrency)
	var wg sync.WaitGroup
	for _, ip := range ips {
		select {
		case <-ctx.Done():
			wg.Wait()
			sortFound(found)
			return found, ctx.Err()
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			f, ok := s.probeHost(ctx, ip, normalized)
			if !ok {
				return
			}
			mu.Lock()
			found = append(found, f)
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	sortFound(found)
	return found, nil
}

func (s Scanner) probeHost(ctx context.Context, ip, cidr string) (Found, bool) {
	open := map[int]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, port := range []int{631, 9100} {
		wg.Add(1)
		go func(port int) {
			defer wg.Done()
			if tcpOpen(ctx, ip, port, s.Timeout) {
				mu.Lock()
				open[port] = true
				mu.Unlock()
			}
		}(port)
	}
	wg.Wait()
	if !open[631] {
		if open[9100] {
			// RAW 9100 alone cannot be driven with PDF by the server, but it is
			// almost always a printer. Surface it so the admin can attach it as
			// a CUPS queue on the server.
			return Found{
				Source: "scan",
				URI:    "socket://" + net.JoinHostPort(ip, "9100"),
				Kind:   "socket",
				Host:   ip,
				Port:   9100,
				Path:   "",
				Raw: map[string]any{
					"cidr": cidr, "raw_only": true,
				},
			}, true
		}
		return Found{}, false
	}
	f := Found{
		Source: "scan",
		URI:    "ipp://" + net.JoinHostPort(ip, "631") + "/ipp/print",
		Kind:   "ipp",
		Host:   ip,
		Port:   631,
		Path:   "/ipp/print",
		Raw:    map[string]any{"cidr": cidr},
	}
	// A device that speaks IPP is a printer; fill in the model when the device
	// answers. Devices that merely expose a port stay out of the result list.
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client, err := ipp.NewClient(f.URI, 3*time.Second)
	if err != nil {
		return Found{}, false
	}
	msg, err := client.GetPrinterAttributes(probeCtx, []string{
		"printer-make-and-model",
		"printer-info",
		"printer-location",
		"printer-state",
		"document-format-supported",
		"printer-uuid",
	})
	if err != nil {
		// Port 631 is open but IPP did not answer. Cameras, NAS and web
		// consoles also expose 631, and the admin cannot add such a device
		// without already knowing it is a printer. Leave it out instead of
		// showing a nameless "printer" to a non-technical user.
		return Found{}, false
	}
	f.Model = strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-make-and-model"))
	if f.Model == "" {
		f.Model = strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-info"))
	}
	f.Vendor = guessVendor(f.Model)
	formats := msg.Strings(ipp.TagPrinterAttributes, "document-format-supported")
	direct := false
	for _, format := range formats {
		if format == "application/pdf" || format == "*/*" {
			direct = true
			break
		}
	}
	location := strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-location"))
	f.Raw["print_directly"] = direct
	driverless := false
	for _, format := range formats {
		if format == "image/pwg-raster" || format == "image/urf" {
			driverless = true
			break
		}
	}
	f.Raw["needs_driver"] = !direct && !driverless
	if location != "" {
		f.Raw["location"] = location
	}
	if uuid := strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-uuid")); uuid != "" {
		f.Serial = strings.TrimPrefix(uuid, "urn:uuid:")
	}
	return f, true
}

func tcpOpen(ctx context.Context, host string, port int, timeout time.Duration) bool {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func sortFound(list []Found) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Host != list[j].Host {
			return list[i].Host < list[j].Host
		}
		return list[i].Port < list[j].Port
	})
}

func hosts(n *net.IPNet) []string {
	var out []string
	ip := n.IP.Mask(n.Mask).To4()
	if ip == nil {
		return nil
	}
	for {
		incIP(ip)
		if !n.Contains(ip) {
			break
		}
		// Skip network and broadcast addresses of IPv4 networks.
		if ip[3] == 0 || ip[3] == 255 {
			continue
		}
		out = append(out, ip.String())
		if len(out) > 4096 {
			return out
		}
	}
	return out
}

func incIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}

func guessVendor(model string) string {
	lower := strings.ToLower(model)
	for _, v := range []struct{ key, name string }{
		{"pantum", "奔图 Pantum"},
		{"hp", "HP"},
		{"hewlett", "HP"},
		{"canon", "Canon"},
		{"epson", "Epson"},
		{"brother", "Brother"},
		{"ricoh", "Ricoh"},
		{"kyocera", "Kyocera"},
		{"xerox", "Xerox"},
		{"lexmark", "Lexmark"},
		{"fuji", "Fujifilm"},
		{"sharp", "Sharp"},
		{"toshiba", "Toshiba"},
		{"konica", "Konica Minolta"},
		{"deli", "Deli"},
	} {
		if strings.Contains(lower, v.key) {
			return v.name
		}
	}
	return ""
}

// ProbeDirect asks a discovered IPP endpoint whether it can accept a PDF.
// Split out of the scanner so mDNS results get the same answer.
func ProbeDirect(ctx context.Context, uri string) (direct bool, model, vendor string, ok bool) {
	client, err := ipp.NewClient(uri, 3*time.Second)
	if err != nil {
		return false, "", "", false
	}
	msg, err := client.GetPrinterAttributes(ctx, []string{
		"printer-make-and-model",
		"printer-info",
		"document-format-supported",
	})
	if err != nil {
		return false, "", "", false
	}
	model = strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-make-and-model"))
	if model == "" {
		model = strings.TrimSpace(msg.String(ipp.TagPrinterAttributes, "printer-info"))
	}
	vendor = guessVendor(model)
	for _, format := range msg.Strings(ipp.TagPrinterAttributes, "document-format-supported") {
		if format == "application/pdf" || format == "*/*" {
			direct = true
			break
		}
	}
	return direct, model, vendor, true
}
