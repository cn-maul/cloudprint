package ipp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestEncodeMultipleValuesAndDecode(t *testing.T) {
	groups := []Group{{
		Tag: TagOperationAttributes,
		Attributes: []Attribute{
			attr("requested-attributes", ValueKeyword,
				text(ValueKeyword, "printer-make-and-model"),
				text(ValueKeyword, "document-format-supported"),
			),
			attr("copies", ValueInteger, integer(2)),
		},
	}}
	body := EncodeRequest(1, OpGetPrinterAttributes, groups)
	if n := bytes.Count(body, []byte("printer-make-and-model")); n != 1 {
		t.Fatalf("name encoded %d times", n)
	}
	// Decode our own request to verify the repeated value tag plus the required
	// zero-length attribute name.
	reqMsg, err := parseMessage(body)
	if err != nil {
		t.Fatal(err)
	}
	requested := reqMsg.Strings(TagOperationAttributes, "requested-attributes")
	if len(requested) != 2 || requested[1] != "document-format-supported" {
		t.Fatalf("requested=%v", requested)
	}

	resp := buildResponse(t, OpGetPrinterAttributes, Group{
		Tag: TagPrinterAttributes,
		Attributes: []Attribute{
			attr("document-format-supported", ValueMimeType,
				text(ValueMimeType, "application/pdf"),
				text(ValueMimeType, "image/jpeg"),
			),
			attr("printer-state", ValueEnum, enum(3)),
			attr("printer-make-and-model", ValueTextWithoutLang,
				text(ValueTextWithoutLang, "Pantum MS6000NW series"),
			),
		},
	})
	msg, err := ParseResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	formats := msg.Strings(TagPrinterAttributes, "document-format-supported")
	if len(formats) != 2 || formats[0] != "application/pdf" || formats[1] != "image/jpeg" {
		t.Fatalf("formats=%v", formats)
	}
	state, ok := msg.Int(TagPrinterAttributes, "printer-state")
	if !ok || state != 3 {
		t.Fatalf("state=%d ok=%v", state, ok)
	}
	if got := msg.String(TagPrinterAttributes, "printer-make-and-model"); got != "Pantum MS6000NW series" {
		t.Fatalf("model=%q", got)
	}
}

// Two adjacent attributes with the same value tag must stay separate. This is
// the common shape of real Get-Printer-Attributes responses.
func TestAdjacentSameTagAttributes(t *testing.T) {
	resp := buildResponse(t, OpGetPrinterAttributes, Group{
		Tag: TagPrinterAttributes,
		Attributes: []Attribute{
			attr("printer-info", ValueTextWithoutLang, text(ValueTextWithoutLang, "Office printer")),
			attr("printer-location", ValueTextWithoutLang, text(ValueTextWithoutLang, "3F")),
			attr("document-format-supported", ValueMimeType,
				text(ValueMimeType, "application/pdf"),
				text(ValueMimeType, "image/jpeg"),
			),
			attr("printer-make-and-model", ValueTextWithoutLang, text(ValueTextWithoutLang, "Pantum MS6000NW series")),
		},
	})
	msg, err := ParseResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if got := msg.String(TagPrinterAttributes, "printer-info"); got != "Office printer" {
		t.Fatalf("info=%q", got)
	}
	if got := msg.String(TagPrinterAttributes, "printer-location"); got != "3F" {
		t.Fatalf("location=%q", got)
	}
	if got := msg.String(TagPrinterAttributes, "printer-make-and-model"); got != "Pantum MS6000NW series" {
		t.Fatalf("model=%q", got)
	}
	if got := msg.Strings(TagPrinterAttributes, "document-format-supported"); len(got) != 2 {
		t.Fatalf("formats=%v", got)
	}
}

func buildResponse(t *testing.T, op uint16, groups ...Group) []byte {
	t.Helper()
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint16(hdr[0:2], Version11)
	binary.BigEndian.PutUint16(hdr[2:4], 0)
	binary.BigEndian.PutUint32(hdr[4:8], 1)
	buf.Write(hdr[:])
	for _, g := range groups {
		buf.WriteByte(g.Tag)
		for _, a := range g.Attributes {
			buf.WriteByte(a.Values[0].Tag)
			var tmp [2]byte
			binary.BigEndian.PutUint16(tmp[:], uint16(len(a.Name)))
			buf.Write(tmp[:])
			buf.WriteString(a.Name)
			for i, v := range a.Values {
				if i > 0 {
					buf.WriteByte(v.Tag)
					buf.Write([]byte{0, 0})
				}
				buf.Write(marshalValuePayload(v.Tag, v))
			}
		}
	}
	buf.WriteByte(TagEndOfAttributes)
	return buf.Bytes()
}
