package discovery

import "testing"

func TestSuggestCIDRsPrefersExistingPrinterNetwork(t *testing.T) {
	got := SuggestCIDRs([]string{"10.20.0.0/24"}, []string{"172.20.5.10"}, "")
	if len(got) < 2 {
		t.Fatalf("expected suggestions, got %v", got)
	}
	if got[0].CIDR != "10.20.0.0/24" {
		t.Fatalf("configured CIDR must come first, got %v", got)
	}
	if got[1].CIDR != "172.20.5.0/24" {
		t.Fatalf("existing printer network must be second, got %v", got)
	}
	if got[1].Label != "已有打印机所在网段" {
		t.Fatalf("label=%q", got[1].Label)
	}
}

func TestSuggestCIDRsUsesClientAddressAndDeduplicates(t *testing.T) {
	got := SuggestCIDRs(nil, []string{"172.20.5.10"}, "172.20.5.200:5000")
	// The local interfaces are appended too, so the assertion is about the
	// printer/client subnet appearing exactly once rather than about the total.
	count := 0
	for _, c := range got {
		if c.CIDR == "172.20.5.0/24" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("printer subnet must appear exactly once, got %v", got)
	}
}

func TestSuggestCIDRsTurnsArrayShorthandIntoNetwork(t *testing.T) {
	got := SuggestCIDRs([]string{"172.20.5.x"}, nil, "")
	if len(got) == 0 || got[0].CIDR != "172.20.5.0/24" {
		t.Fatalf("shorthand should normalize to a /24: %v", got)
	}
}

func TestFilterCandidateCIDRsDropsPublicRanges(t *testing.T) {
	got := FilterCandidateCIDRs([]CandidateCIDR{
		{CIDR: "172.20.5.0/24", Label: "ok"},
		{CIDR: "8.8.8.0/24", Label: "public"},
		{CIDR: "nonsense", Label: "bad"},
		{CIDR: "10.0.0.0/24", Label: "ok"},
	})
	if len(got) != 2 {
		t.Fatalf("public and invalid ranges must be dropped: %v", got)
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	if got := ClientIP("172.17.0.1:41234", "198.51.100.7, 172.17.0.1"); got != "198.51.100.7" {
		t.Fatalf("client ip=%q", got)
	}
	if got := ClientIP("198.51.100.8:5000", ""); got != "198.51.100.8" {
		t.Fatalf("client ip=%q", got)
	}
}
