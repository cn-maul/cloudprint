package files

import "testing"

func TestPageSelectionCoversCommonInput(t *testing.T) {
	cases := []struct {
		in    string
		total int
		want  int
		sel   string
	}{
		{"", 5, 5, ""},
		{"1", 5, 1, "1"},
		{"1-3", 5, 3, "1-3"},
		{"1,3,5", 5, 3, "1,3,5"},
		{"1-2,4-5", 5, 4, "1-2,4-5"},
		{"3-", 5, 3, "3-5"},
		{"-2", 5, 2, "1-2"},
		{" 2 - 3 ", 5, 2, "2-3"},
		{"1，3", 5, 2, "1,3"},
		{"1-3,2-4", 6, 4, "1-4"},
	}
	for _, c := range cases {
		sel, count, err := PageSelection(c.in, c.total)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if count != c.want {
			t.Fatalf("%q: count=%d want %d", c.in, count, c.want)
		}
		if c.sel != "" && joinSelection(sel) != c.sel {
			t.Fatalf("%q: sel=%v want %s", c.in, sel, c.sel)
		}
	}
}

func TestPageSelectionRejectsBadInput(t *testing.T) {
	for _, in := range []string{"0", "9", "5-3", "abc", ""} {
		if in == "" {
			if _, _, err := PageSelection("", 0); err == nil {
				t.Fatal("empty selection with no pages must fail")
			}
			continue
		}
		if _, _, err := PageSelection(in, 4); err == nil {
			t.Fatalf("%q must be rejected", in)
		}
	}
}

func joinSelection(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
