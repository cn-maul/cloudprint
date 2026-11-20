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
	// CUPS exposes the same operations over plain HTTP at /jobs/{id} and
	// /printers/{name}; accept those too so the spooler can be queried without
	// rewriting every URL to an ipp:// form.
	scheme := ""
	switch u.Scheme {
	case "ipp", "http":
		scheme = "http"
	case "ipps", "https":
		scheme = "https"
	default:
		return nil, fmt.Errorf("仅支持 ipp://、ipps:// 或 http(s):// 地址")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("IPP 地址缺少主机")
	}
	host := u.Host
	// An explicit port in the URL must be kept as-is; only add the IPP default
	// when the address has none.
	if u.Port() == "" {
		host = net.JoinHostPort(host, "631")
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

// GetJobAttributes asks the spooler what actually happened to a job.
// lpstat -W completed lists aborted jobs too, so it cannot tell "paper came
// out" from "the driver rejected it"; job-state can.
func (c *Client) GetJobAttributes(ctx context.Context, jobURI string) (*Message, error) {
	if strings.TrimSpace(jobURI) == "" {
		return nil, fmt.Errorf("缺少 job-uri")
	}
	g := c.operationGroup(
		attr("job-uri", ValueURI, text(ValueURI, jobURI)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, "cloudprint")),
		attr("requested-attributes", ValueKeyword,
			text(ValueKeyword, "job-state"), text(ValueKeyword, "job-state-reasons"),
			text(ValueKeyword, "job-name"),
			text(ValueKeyword, "job-impressions-completed")),
	)
	return c.roundTrip(ctx, OpGetJobAttributes, []Group{g}, nil)
}

// GetJobs lists jobs the device still knows about. "completed" includes
// aborted jobs on most devices, so the caller must also check job-state and
// job-impressions-completed before claiming paper came out.
func (c *Client) GetJobs(ctx context.Context, whichJobs string, requested []string) (*Message, error) {
	if whichJobs == "" {
		whichJobs = "all"
	}
	vals := make([]Value, 0, len(requested))
	for _, s := range requested {
		vals = append(vals, text(ValueKeyword, s))
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("which-jobs", ValueKeyword, text(ValueKeyword, whichJobs)),
		attr("requested-attributes", ValueKeyword, vals...),
	)
	return c.roundTrip(ctx, OpGetJobs, []Group{g}, nil)
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

// FindJobURI looks a just-submitted job up by name. Some devices (the Pantum
// here is one) answer Print-Job with only job-id and omit job-uri, which would
// leave the job unpollable. Get-Jobs does report the URI, so resolve it there.
func (c *Client) FindJobURI(ctx context.Context, jobName string) (string, int, bool) {
	name := strings.TrimSpace(jobName)
	if name == "" {
		return "", 0, false
	}
	for _, which := range []string{"not-completed", "completed"} {
		msg, err := c.GetJobs(ctx, which, []string{"job-id", "job-uri", "job-name", "job-state"})
		if err != nil {
			continue
		}
		var bestURI string
		var bestID int
		// Responses carry one job-attributes group per job and the newest is
		// last, so keep overwriting to end up with the highest id.
		for _, g := range msg.Groups {
			if g.Tag != TagJobAttributes {
				continue
			}
			var gName, gURI string
			var gID int64 = -1
			for _, a := range g.Attributes {
				if len(a.Values) == 0 {
					continue
				}
				switch a.Name {
				case "job-name":
					gName = strings.TrimSpace(string(a.Values[0].Data))
				case "job-uri":
					gURI = strings.TrimSpace(string(a.Values[0].Data))
				case "job-id":
					gID = int64(a.Values[0].Int)
				}
			}
			if gName != name || gURI == "" {
				continue
			}
			if gID > int64(bestID) {
				bestURI, bestID = gURI, int(gID)
			}
		}
		if bestURI != "" {
			return bestURI, bestID, true
		}
	}
	return "", 0, false
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
