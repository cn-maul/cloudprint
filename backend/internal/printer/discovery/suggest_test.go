package discovery

import "testing"

func TestSuggestCIDRsPrefersExistingPrinterNetwork(t *testing.T) {
	got := SuggestCIDRs([]string{"10.20.0.0/24"}, []string{"192.168.2.91"}, "")
	if len(got) < 2 {
		t.Fatalf("expected suggestions, got %v", got)
	}
	if got[0].CIDR != "10.20.0.0/24" {
		t.Fatalf("configured CIDR must come first, got %v", got)
	}
	if got[1].CIDR != "192.168.2.0/24" {
		t.Fatalf("existing printer network must be second, got %v", got)
	}
	if got[1].Label != "已有打印机所在网段" {
		t.Fatalf("label=%q", got[1].Label)
	}
}

func TestSuggestCIDRsUsesClientAddressAndDeduplicates(t *testing.T) {
	got := SuggestCIDRs(nil, []string{"192.168.2.91"}, "192.168.2.200:5000")
	if len(got) != 1 || got[0].CIDR != "192.168.2.0/24" {
		t.Fatalf("client network should match the printer network without duplicates: %v", got)
	}
}

func TestSuggestCIDRsTurnsArrayShorthandIntoNetwork(t *testing.T) {
	got := SuggestCIDRs([]string{"192.168.2.x"}, nil, "")
	if len(got) != 1 || got[0].CIDR != "192.168.2.0/24" {
		t.Fatalf("shorthand should normalize to a /24: %v", got)
	}
}

func TestFilterCandidateCIDRsDropsPublicRanges(t *testing.T) {
	got := FilterCandidateCIDRs([]CandidateCIDR{
		{CIDR: "192.168.2.0/24", Label: "ok"},
		{CIDR: "8.8.8.0/24", Label: "public"},
		{CIDR: "nonsense", Label: "bad"},
		{CIDR: "10.0.0.0/24", Label: "ok"},
	})
	if len(got) != 2 {
		t.Fatalf("public and invalid ranges must be dropped: %v", got)
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	if got := ClientIP("172.17.0.1:41234", "192.168.2.50, 172.17.0.1"); got != "192.168.2.50" {
		t.Fatalf("client ip=%q", got)
	}
	if got := ClientIP("192.168.2.156:5000", ""); got != "192.168.2.156" {
		t.Fatalf("client ip=%q", got)
	}
}
