package ipp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// resolutionValue builds the 9-byte IPP resolution encoding: two int32s plus
// the unit byte (3 = dpi). Real printers send this length.
func resolutionValue(x, y int32, unit byte) []byte {
	b := make([]byte, 9)
	binary.BigEndian.PutUint32(b[0:4], uint32(x))
	binary.BigEndian.PutUint32(b[4:8], uint32(y))
	b[8] = unit
	return b
}

// resolutionResponse wraps a printer-attributes group in a full IPP frame so
// the parser sees exactly what the device puts on the wire.
func resolutionResponse(value []byte) []byte {
	body := make([]byte, 8)
	binary.BigEndian.PutUint16(body[0:2], Version11)
	binary.BigEndian.PutUint16(body[2:4], 0)
	binary.BigEndian.PutUint32(body[4:8], 1)
	body = append(body, TagPrinterAttributes)
	name := []byte("printer-resolution-default")
	body = append(body, ValueResolution)
	body = binary.BigEndian.AppendUint16(body, uint16(len(name)))
	body = append(body, name...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(value)))
	body = append(body, value...)
	body = append(body, TagEndOfAttributes)
	return body
}

func TestResolutionNineByteValueParses(t *testing.T) {
	msg, err := ParseResponse(resolutionResponse(resolutionValue(600, 600, 3)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dpi, ok := msg.Resolution(TagPrinterAttributes, "printer-resolution-default")
	if !ok || dpi != 600 {
		t.Fatalf("resolution = %d, ok=%v; want 600, true", dpi, ok)
	}
}

func TestResolutionConvertsCentimetresToDPI(t *testing.T) {
	msg, err := ParseResponse(resolutionResponse(resolutionValue(60, 60, 4)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dpi, ok := msg.Resolution(TagPrinterAttributes, "printer-resolution-default")
	if !ok {
		t.Fatal("resolution not found")
	}
	// 60 dots/cm is 152.4 dpi; rounding keeps it usable for rendering.
	if dpi < 152 || dpi > 153 {
		t.Fatalf("dpi = %d, want about 152", dpi)
	}
}

func TestResolutionRejectsGarbageLength(t *testing.T) {
	msg, err := ParseResponse(resolutionResponse([]byte{0, 0, 2, 88}))
	if err == nil {
		if _, ok := msg.Resolution(TagPrinterAttributes, "printer-resolution-default"); ok {
			t.Fatal("a 4-byte resolution must not be accepted")
		}
	}
}

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

// Captured from a real Pantum MS6000NW Get-Printer-Attributes response.
// Integers are encoded as a 2-byte length prefix plus the value; reading the
// value straight at the cursor returned 262144 instead of 3/5 and made every
// job state lookup fail.
func TestRealDeviceIntegerAndMultiValueEncoding(t *testing.T) {
	raw := []byte{
		0x01, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01,
		0x01,
		0x47, 0x00, 0x12, 'a', 't', 't', 'r', 'i', 'b', 'u', 't', 'e', 's', '-', 'c', 'h', 'a', 'r', 's', 'e', 't',
		0x00, 0x05, 'u', 't', 'f', '-', '8',
		0x48, 0x00, 0x1b, 'a', 't', 't', 'r', 'i', 'b', 'u', 't', 'e', 's', '-', 'n', 'a', 't', 'u', 'r', 'a', 'l', '-', 'l', 'a', 'n', 'g', 'u', 'a', 'g', 'e',
		0x00, 0x02, 'z', 'h',
		0x04,
		0x23, 0x00, 0x0d, 'p', 'r', 'i', 'n', 't', 'e', 'r', '-', 's', 't', 'a', 't', 'e',
		0x00, 0x04, 0x00, 0x00, 0x00, 0x05,
		0x44, 0x00, 0x15, 'p', 'r', 'i', 'n', 't', 'e', 'r', '-', 's', 't', 'a', 't', 'e', '-', 'r', 'e', 'a', 's', 'o', 'n', 's',
		0x00, 0x0b, 'm', 'e', 'd', 'i', 'a', '-', 'e', 'm', 'p', 't', 'y',
		0x44, 0x00, 0x00,
		0x00, 0x0c, 'm', 'e', 'd', 'i', 'a', '-', 'n', 'e', 'e', 'd', 'e', 'd',
		0x03,
	}
	msg, err := ParseResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := msg.Int(TagPrinterAttributes, "printer-state")
	if !ok || state != 5 {
		t.Fatalf("printer-state=%d ok=%v want 5", state, ok)
	}
	reasons := msg.Strings(TagPrinterAttributes, "printer-state-reasons")
	if len(reasons) != 2 || reasons[0] != "media-empty" || reasons[1] != "media-needed" {
		t.Fatalf("reasons=%v", reasons)
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
