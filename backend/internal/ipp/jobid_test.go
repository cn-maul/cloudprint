package ipp

import "testing"

func TestJobIDFromURI(t *testing.T) {
	cases := []struct {
		uri  string
		want int32
		ok   bool
	}{
		{"ipp://192.168.2.91:631/jobs/42", 42, true},
		{"ipp://print.local/printers/ms6000nw/jobs/7", 7, true},
		{"ipp://192.168.2.91:631/jobs/102/", 102, true},
		{"ipp://192.168.2.91/ipp/print/job-id:0000006f", 111, true},
		{"ipp://192.168.2.91/ipp/print/job-id:0000001B", 27, true},
		{"ipp://192.168.2.91:631/ipp/print", 0, false},
		{"", 0, false},
		{"ipp://host/jobs/abc", 0, false},
	}
	for _, c := range cases {
		got, ok := JobIDFromURI(c.uri)
		if got != c.want || ok != c.ok {
			t.Errorf("JobIDFromURI(%q) = (%d,%v), want (%d,%v)", c.uri, got, ok, c.want, c.ok)
		}
	}
}
