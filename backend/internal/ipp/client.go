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
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	return c.roundTripStream(ctx, op, groups, r, int64(len(body)))
}

// roundTripStream posts the operation attributes followed by the document.
//
// The document is copied from the reader while the request is being written, so
// nothing larger than the operation attributes is buffered. ContentLength is
// set explicitly because some printers refuse a chunked request body.
func (c *Client) roundTripStream(ctx context.Context, op uint16, groups []Group, body io.Reader, bodySize int64) (*Message, error) {
	id := c.nextID()
	head := EncodeRequest(id, op, groups)
	size := int64(len(head))
	var payload io.Reader = bytes.NewReader(head)
	if body != nil {
		payload = io.MultiReader(bytes.NewReader(head), body)
		size += bodySize
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL, payload)
	if err != nil {
		return nil, err
	}
	req.ContentLength = size
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
			text(ValueKeyword, "job-impressions-completed"),
			text(ValueKeyword, "job-impressions"),
			text(ValueKeyword, "job-media-sheets-completed")),
	)
	return c.roundTrip(ctx, OpGetJobAttributes, []Group{g}, nil)
}

// CancelJob asks the device itself to stop a job. CloudPrint submits documents
// straight to the printer, so cupsd has never seen this job-id and its
// `cancel` command cannot match it.
//
// Cancel-Job names the job as printer-uri + job-id, not job-uri: the Pantum
// rejects a request carrying only job-uri with client-error-bad-request.
func (c *Client) CancelJob(ctx context.Context, jobID int32, userName string) error {
	if jobID <= 0 {
		return fmt.Errorf("缺少 job-id")
	}
	if strings.TrimSpace(userName) == "" {
		userName = "cloudprint"
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("job-id", ValueInteger, integer(jobID)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, userName)),
	)
	_, err := c.roundTrip(ctx, OpCancelJob, []Group{g}, nil)
	return err
}

// PausePrinter stops the device from claiming new jobs. It is how an operator
// parks a broken printer without walking over to it, so queued documents stay
// queued instead of failing one after another.
func (c *Client) PausePrinter(ctx context.Context, userName string) error {
	if strings.TrimSpace(userName) == "" {
		userName = "cloudprint"
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, userName)),
	)
	_, err := c.roundTrip(ctx, OpPausePrinter, []Group{g}, nil)
	return err
}

// ResumePrinter clears the paused state after the paper jam or empty tray has
// been dealt with.
func (c *Client) ResumePrinter(ctx context.Context, userName string) error {
	if strings.TrimSpace(userName) == "" {
		userName = "cloudprint"
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, userName)),
	)
	_, err := c.roundTrip(ctx, OpResumePrinter, []Group{g}, nil)
	return err
}

// PurgeJobs drops every job the device is holding. After a jam it is common to
// have a dozen stale documents queued; printing them all once paper returns
// wastes a ream.
func (c *Client) PurgeJobs(ctx context.Context, userName string) error {
	if strings.TrimSpace(userName) == "" {
		userName = "cloudprint"
	}
	g := c.operationGroup(
		attr("printer-uri", ValueURI, text(ValueURI, c.PrinterURI)),
		attr("requesting-user-name", ValueNameWithoutLang, text(ValueNameWithoutLang, userName)),
	)
	_, err := c.roundTrip(ctx, OpPurgeJobs, []Group{g}, nil)
	return err
}

// JobIDFromURI extracts the numeric job-id from a job URI. Two shapes occur in
// the field: the standard "ipp://host/jobs/42" and the Pantum's
// "ipp://host/ipp/print/job-id:0000006f", whose number is hexadecimal.
// Devices that reject job-uri on Cancel-Job still need the number.
func JobIDFromURI(jobURI string) (int32, bool) {
	u, err := url.Parse(strings.TrimSpace(jobURI))
	if err != nil {
		return 0, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		part := parts[i]
		if part == "" {
			continue
		}
		// job-id:0000006f carries the id as lowercase hex, not decimal.
		if v, ok := strings.CutPrefix(strings.ToLower(part), "job-id:"); ok {
			n, err := strconv.ParseInt(v, 16, 32)
			if err != nil || n <= 0 {
				return 0, false
			}
			return int32(n), true
		}
		n, err := strconv.ParseInt(part, 10, 32)
		if err != nil || n <= 0 {
			return 0, false
		}
		return int32(n), true
	}
	return 0, false
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
	// Document is streamed straight into the request body. Callers hand over an
	// *os.File so a long raster is never held in memory: a 200-page A4 job at
	// 600 dpi is far larger than the RAM a NAS is willing to spare, and
	// buffering it twice (read + request) was the difference between printing
	// and being killed by the OOM reaper.
	Document     io.Reader
	DocumentSize int64
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
	msg, err := c.roundTripStream(ctx, OpPrintJob, []Group{
		g,
		{Tag: TagJobAttributes, Attributes: jobAttrs},
	}, opt.Document, opt.DocumentSize)
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
