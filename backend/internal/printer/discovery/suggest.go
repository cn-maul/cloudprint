package discovery

import (
	"net"
	"os"
	"strings"
)

// CandidateCIDR is a subnet worth scanning together with a human-readable
// explanation of why it is suggested. CloudPrint usually runs in a Docker
// container, where the only local interface is the Docker bridge; without
// these hints an "automatic" scan would look at 172.17.0.0/16 and never see
// the office LAN.
type CandidateCIDR struct {
	CIDR  string `json:"cidr"`
	Label string `json:"label"`
}

// SuggestCIDRs orders subnets by how likely they are to contain the office
// printers: explicitly configured first, then subnets we already have printers
// on, then the caller's own network, then whatever interfaces the host has.
func SuggestCIDRs(configured, printerHosts []string, clientIP string) []CandidateCIDR {
	out := []CandidateCIDR{}
	seen := map[string]bool{}

	add := func(raw, label string) {
		cidr, err := NormalizeCIDR(raw)
		if err != nil || seen[cidr] {
			return
		}
		seen[cidr] = true
		out = append(out, CandidateCIDR{CIDR: cidr, Label: label})
	}

	for _, raw := range configured {
		add(raw, "已配置的网段")
	}
	for _, host := range printerHosts {
		if ip := net.ParseIP(strings.TrimSpace(host)); ip != nil && ip.To4() != nil {
			add(hostSubnet(ip.To4()), "已有打印机所在网段")
		}
	}
	if ip := net.ParseIP(strings.TrimSpace(clientIP)); ip != nil && ip.To4() != nil && !ip.IsLoopback() {
		add(hostSubnet(ip.To4()), "你当前的网段")
	}
	// Inside Docker the only attached network is the bridge, which never
	// contains a printer. Offering it makes the picker look broken, so rely on
	// the caller's own address and existing printers instead.
	if !InContainer() {
		for _, cidr := range LocalCIDRs() {
			add(cidr, "服务器所在网段")
		}
	}
	return out
}

// InContainer reports whether this process runs inside a container, where the
// host's LAN interfaces are not visible.
func InContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		for _, marker := range []string{"docker", "kubepods", "containerd", "lxc"} {
			if strings.Contains(s, marker) {
				return true
			}
		}
	}
	return false
}

// hostSubnet turns an address into the /24 that most office LANs use. Scanning
// a whole /16 by accident would be slow and rude; the scanner caps at /22.
func hostSubnet(ip net.IP) string {
	mask := net.CIDRMask(24, 32)
	return (&net.IPNet{IP: ip.Mask(mask), Mask: mask}).String()
}

// ClientIP extracts the address a request came from, honouring the proxy
// header Docker/nginx set when the app is published behind one.
func ClientIP(remoteAddr, forwardedFor string) string {
	if v := strings.TrimSpace(forwardedFor); v != "" {
		first := strings.TrimSpace(strings.Split(v, ",")[0])
		if ip := net.ParseIP(first); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

// FilterCandidateCIDRs keeps the given order but drops anything that is not a
// usable private range.
func FilterCandidateCIDRs(list []CandidateCIDR) []CandidateCIDR {
	out := make([]CandidateCIDR, 0, len(list))
	for _, c := range list {
		_, network, err := net.ParseCIDR(c.CIDR)
		if err != nil {
			continue
		}
		ip := network.IP.To4()
		if ip == nil || !ip.IsPrivate() {
			continue
		}
		out = append(out, c)
	}
	return out
}
