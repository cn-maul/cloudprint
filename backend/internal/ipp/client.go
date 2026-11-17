package ipp

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Client struct {
	BaseURL    string
	PrinterURI string
	HTTP       *http.Client
	reqID      uint32
}

func NewClient(uri string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("解析 IPP 地址失败: %w", err)
	}
	if u.Scheme != "ipp" && u.Scheme != "ipps" {
		return nil, fmt.Errorf("仅支持 ipp:// 或 ipps:// 地址")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("IPP 地址缺少主机")
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		if u.Scheme == "ipps" {
			host = net.JoinHostPort(host, "631")
		} else {
			host = net.JoinHostPort(host, "631")
		}
	}
	scheme := "http"
	if u.Scheme == "ipps" {
		scheme = "https"
	}
	path := u.EscapedPath()
	if path == "" || path == "/" {
		path = "/ipp/print"
	}
	base := scheme + "://" + host + path
	if u.RawQuery != "" {
		base += "?" + u.RawQuery
	}
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true},
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 8 * time.Second,
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		BaseURL:    base,
		PrinterURI: uri,
		HTTP:       &http.Client{Transport: tr, Timeout: timeout},
	}, nil
}

func (c *Client) nextID() uint32 {
	return atomic.AddUint32(&c.reqID, 1)
}

func (c *Client) roundTrip(ctx context.Context, op uint16, groups []Group, body []byte) (*Message, error) {
	id := c.nextID()
	payload := EncodeRequest(id, op, groups)
	if body != nil {
		payload = append(payload, body...)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ipp")
	req.Header.Set("Accept", "application/ipp")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("IPP 请求失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 IPP 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("IPP HTTP %d: %s", resp.StatusCode, snippet(data))
	}
	msg, err := ParseResponse(data)
	if err != nil {
		return nil, err
	}
	if msg.Status >= 0x0400 {
		detail := msg.String(TagOperationAttributes, "status-message")
		return msg, &StatusError{Code: msg.Status, Message: detail}
	}
	return msg, nil
}

func snippet(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return strings.TrimSpace(string(b))
}

func attr(name string, tag byte, values ...Value) Attribute {
	return Attribute{Name: name, Values: values}
}

func text(tag byte, s string) Value { return Value{Tag: tag, Data: []byte(s)} }
func integer(v int32) Value         { return Value{Tag: ValueInteger, Int: v} }
func enum(v int32) Value            { return Value{Tag: ValueEnum, Int: v} }

func (c *Client) operationGroup(extra ...Attribute) Group {
	args := []Attribute{
		attr("attributes-charset", ValueCharset, text(ValueCharset, "utf-8")),
		attr("attributes-natural-language", ValueNaturalLanguage, text(ValueNaturalLanguage, "zh-cn")),
	}
	args = append(args, extra...)
	return Group{Tag: TagOperationAttributes, Attributes: args}
}

func (c *Client) GetPrinterAttributes(ctx context.Context, requested []string) (*Message, error) {
	vals := make([]Value, 0, len(requested))
	for _, s := range requested {
		vals = append(vals, text(ValueKeyword, s))
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("requested-attributes", ValueKeyword, vals...),
	)
	return c.roundTrip(ctx, OpGetPrinterAttributes, []Group{g}, nil)
}

type PrintOptions struct {
	JobName   string
	UserName  string
	Format    string
	Copies    int
	Duplex    string
	Media     string
	ColorMode string
	Document  []byte
}

func (c *Client) PrintJob(ctx context.Context, opt PrintOptions) (int, string, error) {
	if opt.Format == "" {
		opt.Format = "application/pdf"
	}
	if opt.Copies <= 0 {
		opt.Copies = 1
	}
	if opt.UserName == "" {
		opt.UserName = "cloudprint"
	}
	if opt.JobName == "" {
		opt.JobName = "CloudPrint"
	}
	jobAttrs := []Attribute{
		attr("copies", ValueInteger, integer(int32(opt.Copies))),
	}
	if opt.Duplex != "" {
		jobAttrs = append(jobAttrs, attr("sides", ValueKeyword, text(ValueKeyword, opt.Duplex)))
	}
	if opt.Media != "" {
		jobAttrs = append(jobAttrs, attr("media", ValueKeyword, text(ValueKeyword, opt.Media)))
	}
	if opt.ColorMode != "" {
		jobAttrs = append(jobAttrs, attr("print-color-mode", ValueKeyword, text(ValueKeyword, opt.ColorMode)))
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, opt.UserName)),
		attr("job-name", ValueNameWithoutLang, text(ValueNameWithoutLang, opt.JobName)),
		attr("document-format", ValueMimeType, text(ValueMimeType, opt.Format)),
	)
	msg, err := c.roundTrip(ctx, OpPrintJob, []Group{
		g,
		{Tag: TagJobAttributes, Attributes: jobAttrs},
	}, opt.Document)
	if err != nil {
		return 0, "", err
	}
	jobID, _ := msg.Int(TagJobAttributes, "job-id")
	jobURI := msg.String(TagJobAttributes, "job-uri")
	return int(jobID), jobURI, nil
}

func (c *Client) ValidateJob(ctx context.Context, opt PrintOptions) error {
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, opt.UserName)),
		attr("job-name", ValueNameWithoutLang, text(ValueNameWithoutLang, opt.JobName)),
		attr("document-format", ValueMimeType, text(ValueMimeType, opt.Format)),
	)
	jobAttrs := Group{Tag: TagJobAttributes, Attributes: []Attribute{
		attr("copies", ValueInteger, integer(int32(opt.Copies))),
	}}
	_, err := c.roundTrip(ctx, OpValidateJob, []Group{g, jobAttrs}, nil)
	return err
}

// ParseURI validates and normalizes an IPP endpoint URI.
func ParseURI(raw string) (scheme, host, port, path string, err error) {
	u, perr := url.Parse(strings.TrimSpace(raw))
	if perr != nil {
		return "", "", "", "", fmt.Errorf("无效的 IPP 地址: %w", perr)
	}
	if u.Scheme != "ipp" && u.Scheme != "ipps" {
		return "", "", "", "", fmt.Errorf("IPP 地址必须以 ipp:// 或 ipps:// 开头")
	}
	host = u.Hostname()
	if host == "" {
		return "", "", "", "", fmt.Errorf("IPP 地址缺少主机")
	}
	port = u.Port()
	if port == "" {
		port = "631"
	}
	path = u.Path
	if path == "" {
		path = "/ipp/print"
	}
	if _, e := strconv.Atoi(port); e != nil {
		return "", "", "", "", fmt.Errorf("IPP 端口无效")
	}
	return u.Scheme, host, port, path, nil
}

func EncodeInt(v int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return b[:]
}
