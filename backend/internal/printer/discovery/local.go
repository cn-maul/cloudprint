package discovery

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// LocalCIDRs returns the IPv4 subnets this host is directly attached to.
// Virtual adapters are skipped so a Windows or NAS host scans the real LAN
// instead of Docker / WSL / Hyper-V networks.
func LocalCIDRs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			ones, bits := ipnet.Mask.Size()
			if bits != 32 || ones < 8 {
				continue
			}
			var network *net.IPNet
			if ones >= 24 {
				network = &net.IPNet{IP: ip4.Mask(ipnet.Mask), Mask: ipnet.Mask}
			} else {
				// A /16 or /23 is usually a big campus network. Scanning the
				// local /24 is enough to find the printers on this floor.
				mask := net.CIDRMask(24, 32)
				network = &net.IPNet{IP: ip4.Mask(mask), Mask: mask}
			}
			key := network.String()
			if key == "0.0.0.0/0" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func isVirtualInterface(name string) bool {
	n := strings.ToLower(name)
	for _, prefix := range []string{
		"vethernet", "docker", "br-", "veth", "vmnet", "virtualbox",
		"hyper-v", "loopback", "utun", "tun", "tap", "tailscale",
		"zerotier", "wsl", "bluetooth", "npcap", "hamachi", "radmin",
	} {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// LocalAddress returns the LAN IPv4 address a colleague can reach. Loopback is
// deliberately excluded: an install command generated from localhost must
// still work on another computer.
func LocalAddress() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		if isVirtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			return ip4.String()
		}
	}
	return ""
}

// NormalizeCIDR accepts the shapes a non-technical admin may type:
// "192.168.2.0/24", "192.168.2.91", "192.168.2.x", "192.168.2.*".
func NormalizeCIDR(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("请填写网段，例如 192.168.1.0/24")
	}
	raw = strings.ReplaceAll(raw, "，", ",")
	if i := strings.IndexAny(raw, " ,;"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSuffix(raw, ".")
	lower := strings.ToLower(raw)
	for _, suffix := range []string{".x", ".*", ".0/24"} {
		if strings.HasSuffix(lower, suffix) && !strings.Contains(raw, "/") {
			raw = strings.TrimSuffix(lower, suffix) + ".0/24"
			lower = raw
			break
		}
	}
	if !strings.Contains(raw, "/") {
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("网段格式不正确，请填写 192.168.1.0/24 这样的地址")
		}
		raw = ip.To4().String() + "/24"
	}
	ip, network, err := net.ParseCIDR(raw)
	if err != nil || ip.To4() == nil {
		return "", fmt.Errorf("网段格式不正确，请填写 192.168.1.0/24 这样的地址")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 16 {
		return "", fmt.Errorf("扫描范围太大，请使用 /16 到 /30 之间的网段")
	}
	hosts := 1 << (bits - ones)
	if hosts-2 > 1024 {
		return "", fmt.Errorf("单次最多扫描 1024 个地址，请使用 /22 或更小的网段")
	}
	return network.String(), nil
}

func ParseHostPort(rawURI string, defaultPort int) (string, int) {
	u := strings.TrimSpace(rawURI)
	if u == "" {
		return "", defaultPort
	}
	if strings.Contains(u, "://") {
		if parsed, err := url.Parse(u); err == nil {
			port := defaultPort
			if p := parsed.Port(); p != "" {
				if n, err := strconv.Atoi(p); err == nil {
					port = n
				}
			}
			return parsed.Hostname(), port
		}
	}
	if host, port, err := net.SplitHostPort(u); err == nil {
		if n, err := strconv.Atoi(port); err == nil {
			return host, n
		}
		return host, defaultPort
	}
	return u, defaultPort
}
