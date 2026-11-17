package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

type Found struct {
	Source string
	URI    string
	Kind   string
	Host   string
	Port   int
	Path   string
	MAC    string
	Vendor string
	Model  string
	Serial string
	Raw    map[string]any
}

type BrowseResult struct {
	Services []Found
	Errors   []string
}

func BrowseMDNS(ctx context.Context, timeout time.Duration) BrowseResult {
	result := BrowseResult{}
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	// Only IPP services. _printer._tcp is LPD (port 515); a user cannot add it
	// as a working printer, and it produced duplicate rows for the same box.
	services := []string{"_ipp._tcp", "_ipps._tcp"}
	seen := map[string]bool{}
	for _, svc := range services {
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		entries := make(chan *zeroconf.ServiceEntry, 64)
		cctx, cancel := context.WithTimeout(ctx, timeout)
		err = resolver.Browse(cctx, svc, "local.", entries)
		if err != nil {
			cancel()
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", svc, err))
			continue
		}
		for entry := range entries {
			f, ok := fromEntry(svc, entry)
			if !ok {
				continue
			}
			// WSD/scan services advertise 443 etc. They are not IPP print
			// endpoints; keeping them only confuses the admin.
			if f.Kind == "ipp" && f.Port != 631 {
				continue
			}
			key := f.URI
			if seen[key] {
				continue
			}
			seen[key] = true
			result.Services = append(result.Services, f)
		}
		cancel()
	}
	return result
}

func fromEntry(service string, e *zeroconf.ServiceEntry) (Found, bool) {
	if e == nil {
		return Found{}, false
	}
	host := ""
	if len(e.AddrIPv4) > 0 {
		host = e.AddrIPv4[0].String()
	} else if len(e.AddrIPv6) > 0 {
		host = e.AddrIPv6[0].String()
	} else {
		host = strings.TrimSuffix(e.HostName, ".")
	}
	if host == "" {
		return Found{}, false
	}
	txt := map[string]string{}
	for _, t := range e.Text {
		kv := strings.SplitN(t, "=", 2)
		if len(kv) == 2 {
			txt[strings.ToLower(kv[0])] = kv[1]
		}
	}
	path := txt["rp"]
	if path == "" {
		path = "ipp/print"
	}
	path = "/" + strings.TrimPrefix(path, "/")
	scheme := "ipp"
	kind := "ipp"
	if strings.Contains(service, "_ipps") {
		scheme = "ipps"
	}
	port := e.Port
	if port == 0 {
		port = 631
	}
	uri := fmt.Sprintf("%s://%s%s", scheme, net.JoinHostPort(host, fmt.Sprint(port)), path)
	model := txt["ty"]
	vendor := ""
	for _, candidate := range []string{"Pantum", "HP", "Canon", "Epson", "Brother", "Ricoh", "Kyocera", "Xerox", "Lexmark"} {
		if strings.Contains(strings.ToLower(model), strings.ToLower(candidate)) {
			vendor = candidate
			break
		}
	}
	return Found{
		Source: "mdns",
		URI:    uri,
		Kind:   kind,
		Host:   host,
		Port:   port,
		Path:   path,
		Vendor: vendor,
		Model:  model,
		Serial: txt["usb_mdl"],
		Raw: map[string]any{
			"service":   service,
			"name":      e.Instance,
			"hostname":  e.HostName,
			"port":      e.Port,
			"txt":       txt,
			"addresses": entryAddresses(e),
		},
	}, true
}

func entryAddresses(e *zeroconf.ServiceEntry) []string {
	var out []string
	for _, a := range e.AddrIPv4 {
		out = append(out, a.String())
	}
	for _, a := range e.AddrIPv6 {
		out = append(out, a.String())
	}
	return out
}
