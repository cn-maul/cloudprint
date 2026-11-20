// Package support holds the printer models this installation is allowed to
// drive.
//
// The print chain (vendor PPD, filter, paper handling) is verified per model,
// not per brand: two printers from the same vendor can need entirely different
// firmware dialects. Until a model has been tested end to end it must not be
// addable, otherwise a colleague uploads a document, the job fails silently,
// and nobody knows whether paper is coming out somewhere.
//
// Supporting a new model is one entry in supported below.
package support

import "strings"

// Model is one tested printer model.
type Model struct {
	ID string `json:"id"`
	// Name is what the UI shows, e.g. "奔图 MS6000NW".
	Name   string `json:"name"`
	Vendor string `json:"vendor,omitempty"`
	// Tokens must all appear in the reported model name. Matching is done on
	// a compact lowercase form, so "MS6000NW", "MS6000 NW" and
	// "Pantum-MS6000NW-Series" all match the same token.
	Tokens []string `json:"-"`
	// Driver is a fragment of the CUPS PPD that is known to work with this
	// model, e.g. "ms6000nw". It is matched case-insensitively against the
	// driver name and path and wins over the automatic best guess, so a model
	// whose driver name does not look like the device name still gets the
	// tested one. Empty means "use the automatic match".
	Driver string `json:"driver,omitempty"`
	// Notes is shown next to the model when it needs an explanation.
	Notes string `json:"notes,omitempty"`
}

var supported = []Model{
	{
		ID:     "pantum-ms6000nw",
		Name:   "奔图 MS6000NW",
		Vendor: "Pantum",
		Tokens: []string{"ms6000nw"},
		Driver: "ms6000nw",
	},
}

// Models returns the supported models. The slice is a copy so callers cannot
// change the table at runtime.
func Models() []Model {
	out := make([]Model, len(supported))
	copy(out, supported)
	return out
}

// compact lowercases a model name and drops everything but letters and digits.
// Device-reported names are inconsistent ("MS6000NW series",
// "Pantum-MS6000NW-Series", "Pantum MS6000NW"), and normalising the punctuation
// away is enough to make them comparable without a fuzzy match that would also
// accept a different model.
func compact(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Match reports which supported model a device-reported name refers to.
func Match(model string) (Model, bool) {
	c := compact(model)
	if c == "" {
		return Model{}, false
	}
	for _, m := range supported {
		ok := true
		for _, t := range m.Tokens {
			if !strings.Contains(c, t) {
				ok = false
				break
			}
		}
		// A token that is only part of a longer model number is a different
		// machine: "ms6000nw" must not match "ms6000nwx".
		if ok && !hasTokenBoundary(c, m.Tokens) {
			ok = false
		}
		if ok {
			return m, true
		}
	}
	return Model{}, false
}

// hasTokenBoundary rejects a match that runs into another digit or letter,
// e.g. "ms6000nw" inside "ms6000nwplus" or "ms6000nwx".
func hasTokenBoundary(c string, tokens []string) bool {
	for _, t := range tokens {
		at := strings.Index(c, t)
		if at < 0 {
			return false
		}
		end := at + len(t)
		if at > 0 && isDigit(c[at-1]) && isDigit(t[0]) {
			return false
		}
		if end < len(c) {
			next := c[end]
			// "series", "plus", "driver"... are descriptive suffixes.
			if (next >= 'a' && next <= 'z') && !strings.HasPrefix(c[end:], "series") &&
				!strings.HasPrefix(c[end:], "plus") && !strings.HasPrefix(c[end:], "printer") {
				return false
			}
		}
	}
	return true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Supported reports whether a device-reported model may be added.
func Supported(model string) bool {
	_, ok := Match(model)
	return ok
}

// Canonical returns the display name for a model, or the input unchanged when
// it is not on the list. Stored records written before the whitelist may hold
// the device's own spelling ("MS6000NW series"); showing one consistent name
// keeps the list readable and makes the stored value comparable.
func Canonical(model string) string {
	if m, ok := Match(model); ok {
		return m.Name
	}
	return model
}

// Names lists the supported models for an error message.
func Names() string {
	names := make([]string, 0, len(supported))
	for _, m := range supported {
		names = append(names, m.Name)
	}
	return strings.Join(names, "、")
}

// RejectMessage explains why a model cannot be added, naming what was found
// and what is supported.
func RejectMessage(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "无法识别这台打印机的型号。目前只支持：" + Names()
	}
	return "暂不支持 " + model + "。目前只支持：" + Names()
}

// DriverHint returns the driver fragment for a supported model.
func (m Model) DriverHint() string { return strings.TrimSpace(m.Driver) }
