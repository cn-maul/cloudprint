package ipp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

const Version11 = 0x0101

const (
	OpGetPrinterAttributes = 0x000B
	OpPrintJob             = 0x0002
	OpValidateJob          = 0x0004
)

const (
	TagOperationAttributes = 0x01
	TagJobAttributes       = 0x02
	TagEndOfAttributes     = 0x03
	TagPrinterAttributes   = 0x04
	TagUnsupported         = 0x05
)

const (
	ValueInteger         = 0x21
	ValueBoolean         = 0x22
	ValueEnum            = 0x23
	ValueOctetString     = 0x30
	ValueDateTime        = 0x31
	ValueResolution      = 0x32
	ValueRangeOfInteger  = 0x33
	ValueBegCollection   = 0x34
	ValueTextLanguage    = 0x35
	ValueNameLanguage    = 0x36
	ValueEndCollection   = 0x37
	ValueTextWithoutLang = 0x41
	ValueNameWithoutLang = 0x42
	ValueKeyword         = 0x44
	ValueURI             = 0x45
	ValueURIScheme       = 0x46
	ValueCharset         = 0x47
	ValueNaturalLanguage = 0x48
	ValueMimeType        = 0x49
	ValueMemberName      = 0x4A
)

type Attribute struct {
	Name   string
	Values []Value
}

type Value struct {
	Tag  byte
	Data []byte
	Int  int32
	Bool bool
}

type Message struct {
	Version   uint16
	Status    uint16
	RequestID uint32
	Groups    []Group
}

type Group struct {
	Tag        byte
	Attributes []Attribute
}

func (m *Message) Group(tag byte) *Group {
	for i := range m.Groups {
		if m.Groups[i].Tag == tag {
			return &m.Groups[i]
		}
	}
	return nil
}

func (m *Message) Attr(groupTag byte, name string) *Attribute {
	g := m.Group(groupTag)
	if g == nil {
		return nil
	}
	for i := range g.Attributes {
		if g.Attributes[i].Name == name {
			return &g.Attributes[i]
		}
	}
	return nil
}

func (m *Message) String(groupTag byte, name string) string {
	a := m.Attr(groupTag, name)
	if a == nil || len(a.Values) == 0 {
		return ""
	}
	return string(a.Values[0].Data)
}

func (m *Message) Strings(groupTag byte, name string) []string {
	a := m.Attr(groupTag, name)
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.Values))
	for _, v := range a.Values {
		out = append(out, string(v.Data))
	}
	return out
}

func (m *Message) Int(groupTag byte, name string) (int64, bool) {
	a := m.Attr(groupTag, name)
	if a == nil || len(a.Values) == 0 {
		return 0, false
	}
	v := a.Values[0]
	switch v.Tag {
	case ValueInteger, ValueEnum:
		return int64(v.Int), true
	}
	return 0, false
}

type StatusError struct {
	Code    uint16
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("IPP 状态 0x%04X: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("IPP 状态 0x%04X", e.Code)
}

var statusNames = map[uint16]string{
	0x0000: "成功",
	0x0001: "成功但有警告",
	0x0400: "客户端错误",
	0x0401: "无权访问",
	0x0402: "设备忙",
	0x0403: "任务已取消",
	0x0404: "无法找到资源",
	0x0405: "请求被拒绝",
	0x0406: "文档格式不支持",
	0x0407: "属性不支持",
	0x0408: "属性取值不支持",
	0x0409: "文档格式错误",
	0x0500: "服务器错误",
	0x0501: "临时错误",
	0x0502: "版本不支持",
	0x0503: "设备错误",
	0x0504: "配置变更中",
	0x0505: "超时",
}

func StatusText(code uint16) string {
	if s, ok := statusNames[code]; ok {
		return s
	}
	return "未知错误"
}

func marshalValuePayload(tag byte, val Value) []byte {
	var buf bytes.Buffer
	if tag == ValueInteger || tag == ValueEnum {
		var tmp [4]byte
		binary.BigEndian.PutUint32(tmp[:], uint32(val.Int))
		buf.Write(tmp[:])
	} else if tag == ValueBoolean {
		if val.Bool {
			buf.WriteByte(1)
		} else {
			buf.WriteByte(0)
		}
	} else {
		var tmp [2]byte
		binary.BigEndian.PutUint16(tmp[:], uint16(len(val.Data)))
		buf.Write(tmp[:])
		buf.Write(val.Data)
	}
	return buf.Bytes()
}

func EncodeRequest(id uint32, op uint16, groups []Group) []byte {
	var buf bytes.Buffer
	var hdr [8]byte
	binary.BigEndian.PutUint16(hdr[0:2], Version11)
	binary.BigEndian.PutUint16(hdr[2:4], op)
	binary.BigEndian.PutUint32(hdr[4:8], id)
	buf.Write(hdr[:])
	for _, g := range groups {
		buf.WriteByte(g.Tag)
		for _, attr := range g.Attributes {
			buf.WriteByte(attr.Values[0].Tag)
			var tmp [2]byte
			binary.BigEndian.PutUint16(tmp[:], uint16(len(attr.Name)))
			buf.Write(tmp[:])
			buf.WriteString(attr.Name)
			for i, v := range attr.Values {
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

func ParseResponse(data []byte) (*Message, error) {
	return parseMessage(data)
}

// parseMessage decodes the common IPP frame used by both requests and
// responses. Keeping this separate makes it possible to round-trip a request in
// tests without relying on printer hardware.
func parseMessage(data []byte) (*Message, error) {
	if len(data) < 8 {
		return nil, errors.New("IPP 响应过短")
	}
	m := &Message{
		Version:   binary.BigEndian.Uint16(data[0:2]),
		Status:    binary.BigEndian.Uint16(data[2:4]),
		RequestID: binary.BigEndian.Uint32(data[4:8]),
	}
	pos := 8
	for pos < len(data) {
		groupTag := data[pos]
		pos++
		if groupTag == TagEndOfAttributes {
			break
		}
		if groupTag > 0x0F {
			return nil, fmt.Errorf("IPP 属性组标签无效: 0x%02X", groupTag)
		}
		g := Group{Tag: groupTag}
		for pos < len(data) && data[pos] > 0x0F {
			a, next, err := parseAttribute(data, pos)
			if err != nil {
				return nil, err
			}
			pos = next
			g.Attributes = append(g.Attributes, a)
		}
		m.Groups = append(m.Groups, g)
	}
	return m, nil
}

func parseAttribute(data []byte, pos int) (Attribute, int, error) {
	valueTag := data[pos]
	pos++
	if pos+2 > len(data) {
		return Attribute{}, pos, errors.New("IPP 属性名长度缺失")
	}
	nameLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
	pos += 2
	if pos+nameLen > len(data) {
		return Attribute{}, pos, errors.New("IPP 属性名越界")
	}
	name := string(data[pos : pos+nameLen])
	pos += nameLen
	attr := Attribute{Name: name}
	for {
		v, next, err := parseValue(data, pos, valueTag)
		if err != nil {
			return Attribute{}, pos, err
		}
		attr.Values = append(attr.Values, v)
		pos = next
		if pos >= len(data) {
			return attr, pos, nil
		}
		nextTag := data[pos]
		if nextTag <= 0x0F {
			return attr, pos, nil
		}
		// Every additional value has a value tag and a zero-length attribute
		// name. A non-zero name length starts a new attribute.
		if pos+3 > len(data) {
			return Attribute{}, pos, errors.New("IPP 属性名长度缺失")
		}
		nextNameLen := int(binary.BigEndian.Uint16(data[pos+1 : pos+3]))
		if nextNameLen != 0 {
			return attr, pos, nil
		}
		pos += 3
	}
}

func parseValue(data []byte, pos int, tag byte) (Value, int, error) {
	v := Value{Tag: tag}
	switch tag {
	case 0x10, 0x12, 0x13:
		return v, pos, nil
	case ValueInteger, ValueEnum:
		if pos+4 > len(data) {
			return v, pos, errors.New("IPP 整数值越界")
		}
		v.Int = int32(binary.BigEndian.Uint32(data[pos : pos+4]))
		return v, pos + 4, nil
	case ValueBoolean:
		if pos >= len(data) {
			return v, pos, errors.New("IPP 布尔值越界")
		}
		v.Bool = data[pos] != 0
		return v, pos + 1, nil
	case ValueRangeOfInteger, ValueResolution:
		if pos+8 > len(data) {
			return v, pos, errors.New("IPP 复合整数值越界")
		}
		v.Data = append([]byte(nil), data[pos:pos+8]...)
		return v, pos + 8, nil
	case ValueDateTime:
		if pos+2 > len(data) {
			return v, pos, errors.New("IPP 日期越界")
		}
		l := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+l > len(data) {
			return v, pos, errors.New("IPP 日期值越界")
		}
		v.Data = append([]byte(nil), data[pos:pos+l]...)
		return v, pos + l, nil
	case ValueBegCollection:
		return v, pos, nil
	case ValueMemberName:
		if pos+2 > len(data) {
			return v, pos, errors.New("IPP 成员名越界")
		}
		l := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+l > len(data) {
			return v, pos, errors.New("IPP 成员名值越界")
		}
		v.Data = append([]byte(nil), data[pos:pos+l]...)
		return v, pos + l, nil
	case ValueEndCollection:
		return v, pos, nil
	default:
		if pos+2 > len(data) {
			return v, pos, errors.New("IPP 字符串长度缺失")
		}
		l := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		pos += 2
		if pos+l > len(data) {
			return v, pos, errors.New("IPP 字符串越界")
		}
		v.Data = append([]byte(nil), data[pos:pos+l]...)
		return v, pos + l, nil
	}
}

func (m *Message) SortedAttrNames(groupTag byte) []string {
	g := m.Group(groupTag)
	if g == nil {
		return nil
	}
	names := make([]string, 0, len(g.Attributes))
	for _, a := range g.Attributes {
		names = append(names, a.Name)
	}
	sort.Strings(names)
	return names
}
