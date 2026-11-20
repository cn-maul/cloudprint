// Package cups talks to the local CUPS spooler. CUPS is the Linux printing
// layer: it owns the PPD, runs the vendor filter and hands the final raster
// (or PCL/PS) to the device. CloudPrint only queues a PDF and watches the job.
package cups

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cloudprint/internal/ipp"
)

type Client struct {
	Host    string
	Lp      string
	Lpstat  string
	Lpadmin string
	Cancel  string

	mu       sync.Mutex
	ppds     []PPD
	ppdsRead time.Time
	ippBase  string
}

func New(host string) *Client {
	host = strings.TrimSpace(host)
	if host == "" {
		host = "127.0.0.1:631"
	}
	// Talking to a local cupsd over TCP requires a password for admin
	// operations. The Unix domain socket carries the peer uid, so group
	// membership (lpadmin) is enough. An empty Host means "use the local
	// socket", which is what the CUPS clients do by default.
	if isLocalCUPS(host) {
		host = ""
	}
	c := &Client{Host: host, ippBase: ipBase(host)}
	if p, err := exec.LookPath("lp"); err == nil {
		c.Lp = p
	}
	if p, err := exec.LookPath("lpstat"); err == nil {
		c.Lpstat = p
	}
	if p, err := exec.LookPath("lpadmin"); err == nil {
		c.Lpadmin = p
	}
	if p, err := exec.LookPath("cancel"); err == nil {
		c.Cancel = p
	}
	return c
}

func ipBase(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "http://127.0.0.1:631"
	}
	if strings.Contains(host, "://") {
		return strings.TrimRight(host, "/")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "631")
	}
	return "http://" + host
}

// attrURI converts an HTTP CUPS URL into the URI form IPP attributes carry.
// CUPS accepts either, but "ipp://" is what every device and test expects, and
// a mismatched job-uri makes Get-Job-Attributes answer no-such-job.
func attrURI(httpURL string) string {
	s := strings.TrimSpace(httpURL)
	switch {
	case strings.HasPrefix(s, "https://"):
		return "ipps://" + strings.TrimPrefix(s, "https://")
	case strings.HasPrefix(s, "http://"):
		return "ipp://" + strings.TrimPrefix(s, "http://")
	}
	return s
}

func isLocalCUPS(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if name, _, err := net.SplitHostPort(h); err == nil {
		h = name
	}
	h = strings.Trim(h, "[]")
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func (c *Client) HostArgs() []string {
	if c.Host == "" {
		return nil
	}
	return []string{"-h", c.Host}
}

func (c *Client) run(ctx context.Context, bin string, args ...string) (string, error) {
	if bin == "" {
		return "", errors.New("系统未安装 CUPS 客户端（lp / lpstat / lpadmin）")
	}
	full := append(append([]string{}, c.HostArgs()...), args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var out, stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("%s: %s", filepath.Base(bin), msg)
	}
	return out.String(), nil
}

// Available reports whether this host can drive CUPS and what is missing.
func (c *Client) Available() (bool, string) {
	var missing []string
	if c.Lp == "" {
		missing = append(missing, "lp")
	}
	if c.Lpstat == "" {
		missing = append(missing, "lpstat")
	}
	if c.Lpadmin == "" {
		missing = append(missing, "lpadmin")
	}
	if len(missing) > 0 {
		return false, "系统缺少 CUPS 客户端：" + strings.Join(missing, "、") +
			"（Debian/Ubuntu 执行 apt install cups cups-client cups-filters）"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.run(ctx, c.Lpstat, "-r"); err != nil {
		return false, "CUPS 服务没有运行：" + err.Error()
	}
	return true, ""
}

type Queue struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Enabled   bool   `json:"enabled"`
	Accepting bool   `json:"accepting"`
	DeviceURI string `json:"device_uri,omitempty"`
	MakeModel string `json:"make_model,omitempty"`
	Jobs      int    `json:"jobs"`
	Message   string `json:"message,omitempty"`
}

var (
	rePrinterLine = regexp.MustCompile(`^printer (\S+) is (.+)$`)
	// Some CUPS builds and wrappers do print the address; treating it as a
	// fallback costs nothing and keeps remote-spooler setups working.
	reDeviceURI = regexp.MustCompile(`(?m)^\s*Device URI:\s*(\S+)\s*$`)
	reMakeModel = regexp.MustCompile(`(?m)^\s*Make and Model:\s*(.+?)\s*$`)
	reInterface = regexp.MustCompile(`(?m)^\s*Interface:\s*(\S+)\s*$`)
)

// Queues lists every queue configured in CUPS.
func (c *Client) Queues(ctx context.Context) ([]Queue, error) {
	out, err := c.run(ctx, c.Lpstat, "-p")
	if err != nil {
		// lpstat exits 1 with this message when the spooler simply has no
		// destinations configured, which is the normal starting state.
		if strings.Contains(strings.ToLower(err.Error()), "no destinations added") {
			return []Queue{}, nil
		}
		return nil, err
	}
	byName := map[string]*Queue{}
	order := []string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		m := rePrinterLine.FindStringSubmatch(strings.TrimSpace(sc.Text()))
		if m == nil {
			continue
		}
		q := &Queue{Name: m[1], Enabled: true, Accepting: true}
		msg := strings.ToLower(m[2])
		switch {
		case strings.Contains(msg, "now printing"), strings.Contains(msg, "is printing"):
			q.State = "printing"
		case strings.Contains(msg, "disabled"), strings.Contains(msg, "stopped"):
			q.State = "stopped"
			q.Enabled = false
		case strings.Contains(msg, "idle"):
			q.State = "idle"
		default:
			q.State = "unknown"
			q.Message = strings.TrimSpace(m[2])
		}
		byName[q.Name] = q
		order = append(order, q.Name)
	}
	list := make([]Queue, 0, len(order))
	for _, name := range order {
		q := byName[name]
		if detail, err := c.run(ctx, c.Lpstat, "-l", "-p", name); err == nil {
			if mm := reDeviceURI.FindStringSubmatch(detail); mm != nil {
				q.DeviceURI = mm[1]
			}
			if mm := reMakeModel.FindStringSubmatch(detail); mm != nil {
				q.MakeModel = strings.TrimSpace(mm[1])
			}
		}
		// Real cupsd's lpstat -l prints "Interface:" (the PPD path) and no
		// device URI at all, so the text scan above can come up empty. Ask
		// CUPS for the attribute itself; without it every driverless path
		// believes the queue has no device behind it.
		if uri, err := c.deviceURIIPP(ctx, name); err == nil && uri != "" {
			q.DeviceURI = uri
		}
		if active, err := c.run(ctx, c.Lpstat, "-o", name); err == nil {
			n := 0
			s := bufio.NewScanner(strings.NewReader(active))
			for s.Scan() {
				if strings.TrimSpace(s.Text()) != "" {
					n++
				}
			}
			q.Jobs = n
		}
		list = append(list, *q)
	}
	return list, nil
}

func (c *Client) Queue(ctx context.Context, name string) (*Queue, error) {
	all, err := c.Queues(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(all[i].Name, name) {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("CUPS 队列 %q 不存在", name)
}

type CreateOptions struct {
	Name      string
	DeviceURI string
	PPD       string
	Location  string
	Info      string
}

// Create adds a queue bound to a PPD. The PPD is what makes a vendor-specific
// device work: CUPS runs its filter instead of guessing a raster dialect.
func (c *Client) Create(ctx context.Context, opt CreateOptions) error {
	if strings.TrimSpace(opt.Name) == "" {
		return errors.New("队列名不能为空")
	}
	if strings.TrimSpace(opt.DeviceURI) == "" {
		return errors.New("设备地址不能为空")
	}
	if opt.PPD != "" {
		if _, err := os.Stat(opt.PPD); err != nil {
			return fmt.Errorf("找不到 PPD 文件 %s", opt.PPD)
		}
	}
	args := []string{"-p", opt.Name, "-E", "-v", opt.DeviceURI}
	if opt.PPD != "" {
		args = append(args, "-P", opt.PPD)
	} else {
		args = append(args, "-m", "everywhere")
	}
	if opt.Location != "" {
		args = append(args, "-L", opt.Location)
	}
	if opt.Info != "" {
		args = append(args, "-D", opt.Info)
	}
	args = append(args, "-o", "printer-is-shared=false", "-o", "printer-error-policy=abort-job")
	if _, err := c.run(ctx, c.Lpadmin, args...); err != nil {
		return err
	}
	// A freshly added queue can be stopped if the device was unreachable at
	// creation time; accepting jobs now means the first upload is not lost.
	_, _ = c.run(ctx, c.Lpadmin, "-E", "-p", opt.Name)
	return nil
}

func (c *Client) Delete(ctx context.Context, name string) error {
	_, err := c.run(ctx, c.Lpadmin, "-x", name)
	return err
}

type SubmitOptions struct {
	Copies    int
	Duplex    string
	Media     string
	ColorMode string
	Title     string
}

// Submit queues a PDF. CUPS converts it with pdftoraster and then the vendor
// filter; we never need to know the device's native language.
func (c *Client) Submit(ctx context.Context, queue, path string, opt SubmitOptions) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("打印文件不存在: %w", err)
	}
	if opt.Copies <= 0 {
		opt.Copies = 1
	}
	if opt.Title == "" {
		opt.Title = filepath.Base(path)
	}
	args := []string{"-d", queue, "-n", strconv.Itoa(opt.Copies), "-t", opt.Title}
	if opt.Media != "" {
		args = append(args, "-o", "media="+opt.Media)
	}
	switch opt.Duplex {
	case "two-sided-long-edge", "two-sided-short-edge":
		args = append(args, "-o", "sides="+opt.Duplex)
	case "":
	default:
		args = append(args, "-o", "sides=one-sided")
	}
	if opt.ColorMode != "" {
		args = append(args, "-o", "print-color-mode="+opt.ColorMode)
	}
	args = append(args, "--", path)
	out, err := c.run(ctx, c.Lp, args...)
	if err != nil {
		return "", err
	}
	m := reRequestID.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("CUPS 没有返回任务号: %s", strings.TrimSpace(out))
	}
	return m[1], nil
}

var reRequestID = regexp.MustCompile(`request id is (\S+)`)

// JobState reports what the spooler says happened to a job.
//
// "completed" must mean paper came out. `lpstat -W completed` also lists jobs
// the filter aborted, so it cannot make that distinction; the IPP job-state
// can. The lpstat path is only a fallback for older/partial CUPS setups.
func (c *Client) JobState(ctx context.Context, queue, job string) (string, error) {
	if state, err := c.ippJobState(ctx, job); err == nil && state != "" {
		return state, nil
	}
	active, err := c.run(ctx, c.Lpstat, "-W", "not-completed", "-o", queue)
	if err != nil {
		return "", err
	}
	if jobListed(active, job) {
		return "printing", nil
	}
	done, err := c.run(ctx, c.Lpstat, "-W", "completed", "-o", queue)
	if err != nil {
		return "", err
	}
	if jobListed(done, job) {
		return "completed", nil
	}
	return "unknown", nil
}

func (c *Client) ippJobState(ctx context.Context, job string) (string, error) {
	num := job
	if i := strings.LastIndex(job, "-"); i >= 0 {
		num = job[i+1:]
	}
	if _, err := strconv.Atoi(num); err != nil {
		return "", fmt.Errorf("任务号 %q 不是 CUPS 编号", job)
	}
	base := c.ippBase
	if base == "" {
		base = "http://127.0.0.1:631"
	}
	jobURI := attrURI(base) + "/jobs/" + num
	client, err := ipp.NewClient(jobURI, 10*time.Second)
	if err != nil {
		return "", err
	}
	msg, err := client.GetJobAttributes(ctx, jobURI)
	if err != nil {
		return "", err
	}
	st, ok := msg.Int(ipp.TagJobAttributes, "job-state")
	if !ok {
		return "", errors.New("CUPS 没有返回 job-state")
	}
	switch st {
	case 3, 4, 5:
		return "printing", nil
	case 6:
		// processing-stopped: the spooler gave up on the job (the device
		// reported an error such as media-empty). Nothing came out.
		return "failed", nil
	case 7:
		return "canceled", nil
	case 8:
		return "failed", nil
	case 9:
		return "completed", nil
	}
	return "unknown", nil
}

// PrinterState reports the spooler's view of a queue: its state plus the
// reasons the device gave, such as media-empty or media-jam. A job can sit in
// "printing" forever while the printer waits for paper, and the user needs to
// see why instead of guessing.
func (c *Client) PrinterState(ctx context.Context, queue string) (string, []string, error) {
	base := c.ippBase
	if base == "" {
		base = "http://127.0.0.1:631"
	}
	uri := attrURI(base) + "/printers/" + queue
	client, err := ipp.NewClient(uri, 10*time.Second)
	if err != nil {
		return "", nil, err
	}
	msg, err := client.GetPrinterAttributes(ctx, []string{
		"printer-state", "printer-state-reasons",
	})
	if err != nil {
		return "", nil, err
	}
	st, _ := msg.Int(ipp.TagPrinterAttributes, "printer-state")
	state := "unknown"
	switch st {
	case 3:
		state = "idle"
	case 4:
		state = "printing"
	case 5:
		state = "stopped"
	}
	var reasons []string
	for _, r := range msg.Strings(ipp.TagPrinterAttributes, "printer-state-reasons") {
		r = strings.TrimSpace(r)
		if r != "" && r != "none" {
			reasons = append(reasons, r)
		}
	}
	return state, reasons, nil
}

// DevicePrinterState asks the device itself for its blocking reasons. cupsd
// caches what it last heard from the printer, so a queue can still look
// "idle" while the hardware is out of paper and holding every job.
func (c *Client) DevicePrinterState(ctx context.Context, queue string) (string, []string, error) {
	deviceURI, err := c.DeviceURI(ctx, queue)
	if err != nil {
		return "", nil, err
	}
	client, err := ipp.NewClient(deviceURI, 10*time.Second)
	if err != nil {
		return "", nil, err
	}
	msg, err := client.GetPrinterAttributes(ctx, []string{
		"printer-state", "printer-state-reasons",
	})
	if err != nil {
		return "", nil, err
	}
	st, _ := msg.Int(ipp.TagPrinterAttributes, "printer-state")
	state := "unknown"
	switch st {
	case 3:
		state = "idle"
	case 4:
		state = "printing"
	case 5:
		state = "stopped"
	}
	var reasons []string
	for _, r := range msg.Strings(ipp.TagPrinterAttributes, "printer-state-reasons") {
		if r = strings.TrimSpace(r); r != "" && r != "none" {
			reasons = append(reasons, r)
		}
	}
	return state, reasons, nil
}

// DeviceVerdict asks the printer itself what happened to a job.
//
// cupsd drops a job from its queue as soon as the backend finished sending it,
// even when the device then aborts it (paper out, jam, unsupported raster).
// The device keeps its own job history, so for IPP queues it is the only
// reliable witness that paper came out.
//
// It returns "completed", "failed", "pending", or "" together with whether a
// matching device job was found. An empty verdict means the device has no
// record (history rolled over, queue not IPP), and the caller keeps the
// spooler's answer.
func (c *Client) DeviceVerdict(ctx context.Context, queue, jobName string) (string, bool) {
	q, err := c.Queue(ctx, queue)
	if err != nil || q == nil {
		return "", false
	}
	uri := strings.TrimSpace(q.DeviceURI)
	if !strings.HasPrefix(uri, "ipp://") && !strings.HasPrefix(uri, "ipps://") {
		return "", false
	}
	client, err := ipp.NewClient(uri, 8*time.Second)
	if err != nil {
		return "", false
	}
	// Not every device accepts which-jobs=all (this Pantum answers
	// client-error-attributes-or-values-not-supported), so ask for "not-
	// completed" first: if the device still owns the newest job with this name,
	// the printer has not finished and the spooler's "completed" is premature.
	// Only then look at the completed/canceled history.
	name := strings.TrimSpace(jobName)
	fields := []string{"job-id", "job-name", "job-state", "job-impressions-completed"}
	for _, which := range []string{"not-completed", "completed"} {
		msg, err := client.GetJobs(ctx, which, fields)
		if err != nil {
			continue
		}
		if ok, known := deviceJobVerdict(msg, name); known {
			return ok, true
		}
	}
	return "", false
}

// deviceJobVerdict picks the newest job in the response that carries this
// name and reports what the device says about it: "completed", "failed",
// "pending", or "" when the response has no job with this name.
func deviceJobVerdict(msg *ipp.Message, name string) (string, bool) {
	var bestState int64 = -1
	var bestID int64 = -1
	// Responses carry one job-attributes group per job; Message.String only
	// reads the first group, so walk the groups directly.
	for _, g := range msg.Groups {
		if g.Tag != ipp.TagJobAttributes {
			continue
		}
		var id, state int64
		var jobName string
		for _, a := range g.Attributes {
			switch a.Name {
			case "job-id":
				if len(a.Values) > 0 {
					id = int64(a.Values[0].Int)
				}
			case "job-state":
				if len(a.Values) > 0 {
					state = int64(a.Values[0].Int)
				}
			case "job-name":
				if len(a.Values) > 0 {
					jobName = string(a.Values[0].Data)
				}
			}
		}
		if name != "" && jobName != name {
			continue
		}
		if id >= bestID {
			bestID, bestState = id, state
		}
	}
	if bestState < 0 {
		return "", false
	}
	switch bestState {
	case 9:
		return "completed", true
	case 7, 8:
		return "failed", true
	}
	// 3 pending, 4 held, 5 processing: the device still owns the job.
	return "pending", true
}

func jobListed(out, job string) bool {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 && fields[0] == job {
			return true
		}
	}
	return false
}

// ResolveDeviceJob finds the device-side job belonging to a CloudPrint job
// name and returns its job-uri. Some devices omit job-uri from the Print-Job
// response, which would otherwise leave a submitted job unpollable forever.
// QueuePPD returns the PPD file CUPS uses for a queue, plus the make/model it
// records. The PPD is what tells us which vendor filter turns raster into the
// device's own language.
func (c *Client) QueuePPD(ctx context.Context, queue string) (path, makeModel string, err error) {
	if strings.TrimSpace(queue) == "" {
		return "", "", fmt.Errorf("缺少队列名")
	}
	// CUPS keeps the live PPD next to the queue name; lpstat reports the same
	// path, but reading it directly works even when the client is remote.
	for _, dir := range []string{"/etc/cups/ppd", "/usr/local/etc/cups/ppd"} {
		p := filepath.Join(dir, queue+".ppd")
		if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
			return p, "", nil
		}
	}
	// Fall back to the Interface line from lpstat for non-standard layouts.
	if out, lerr := c.run(ctx, c.Lpstat, "-l", "-p", queue); lerr == nil {
		if mm := reInterface.FindStringSubmatch(out); mm != nil {
			p := strings.TrimSpace(mm[1])
			if st, serr := os.Stat(p); serr == nil && !st.IsDir() {
				if m := reMakeModel.FindStringSubmatch(out); m != nil {
					makeModel = strings.TrimSpace(m[1])
				}
				return p, makeModel, nil
			}
		}
	}
	return "", "", fmt.Errorf("队列 %q 没有可读的 PPD", queue)
}

func (c *Client) ResolveDeviceJob(ctx context.Context, queue, jobName string) (string, int, bool) {
	deviceURI, err := c.DeviceURI(ctx, queue)
	if err != nil {
		return "", 0, false
	}
	client, err := ipp.NewClient(deviceURI, 10*time.Second)
	if err != nil {
		return "", 0, false
	}
	return client.FindJobURI(ctx, jobName)
}

// DeviceJobState reports what the printer itself says about one of its jobs.
//
// CloudPrint now renders documents locally and submits them straight to the
// device, so the job reference is the device's own job-uri. It also returns
// the printer's state reasons so the UI can explain a stall.
//
// jobName guards against id reuse: this Pantum restarts job-id at 1 after a
// power cycle, so "job-id:00000064" can point at an unrelated old job. A
// mismatched name means the id no longer identifies our document, and the
// caller must look the job up by name instead of believing the old record.
//
// The returned state is one of "pending", "processing", "completed",
// "partial", "failed", "canceled", or "unknown". "partial" means the device
// reports a canceled job that still put ink on paper: this hardware can stop
// mid-document, and claiming "canceled" would tell the user nothing printed.
func (c *Client) DeviceJobState(ctx context.Context, queue, jobURI, jobName string) (string, []string, error) {
	jobURI = strings.TrimSpace(jobURI)
	if jobURI == "" {
		return "unknown", nil, fmt.Errorf("任务没有记录设备地址")
	}
	deviceURI, err := c.DeviceURI(ctx, queue)
	if err != nil {
		return "unknown", nil, err
	}
	client, err := ipp.NewClient(deviceURI, 10*time.Second)
	if err != nil {
		return "unknown", nil, err
	}
	msg, err := client.GetJobAttributes(ctx, jobURI)
	if err != nil {
		// A finished job disappears from the device's active list; that is a
		// normal end state, not a failure to talk to the printer.
		if se, ok := err.(*ipp.StatusError); ok && se.Code == 0x0406 {
			return "completed", nil, nil
		}
		return "unknown", nil, err
	}
	var state int64
	if v, ok := msg.Int(ipp.TagJobAttributes, "job-state"); ok {
		state = v
	}
	if want := strings.TrimSpace(jobName); want != "" {
		got := strings.TrimSpace(msg.String(ipp.TagJobAttributes, "job-name"))
		// This Pantum answers Get-Job-Attributes for an id it no longer owns
		// with a bare job-id (no name) and a canceled state. Treating that as
		// "our job was canceled" is wrong twice over: it hides jobs that
		// actually printed, and it reports a cancellation the user never made.
		// Only a record carrying our name can decide a terminal outcome.
		if got != want && (state == 7 || state == 8) {
			return "unknown", nil, nil
		}
	}
	impressions, _ := msg.Int(ipp.TagJobAttributes, "job-impressions-completed")
	reasons := msg.Strings(ipp.TagJobAttributes, "job-state-reasons")
	switch state {
	case 3, 4:
		return "pending", reasons, nil
	case 5, 6:
		return "processing", reasons, nil
	case 7:
		if impressions > 0 {
			return "partial", reasons, nil
		}
		return "canceled", reasons, nil
	case 8:
		if impressions > 0 {
			return "partial", reasons, nil
		}
		return "failed", reasons, nil
	case 9:
		return "completed", reasons, nil
	}
	return "unknown", reasons, nil
}

// DeviceJobProgress reports how many impressions the device has finished for
// a job.
//
// "printing" is a long and opaque state on a slow device. The queue can only
// say "正在打印" for many minutes, which looks like a hang; this returns the
// device's own counter so the page shows 2/4 instead. Impressions are sheets,
// not PDF pages, so copies and duplex change the divisor — the caller stores
// the target it asked for.
func (c *Client) DeviceJobProgress(ctx context.Context, queue, jobURI string) (done int, total int, ok bool) {
	jobURI = strings.TrimSpace(jobURI)
	if jobURI == "" {
		return 0, 0, false
	}
	deviceURI, err := c.DeviceURI(ctx, queue)
	if err != nil {
		return 0, 0, false
	}
	client, err := ipp.NewClient(deviceURI, 8*time.Second)
	if err != nil {
		return 0, 0, false
	}
	msg, err := client.GetJobAttributes(ctx, jobURI)
	if err != nil {
		return 0, 0, false
	}
	finished, hasDone := msg.Int(ipp.TagJobAttributes, "job-impressions-completed")
	expected, hasTotal := msg.Int(ipp.TagJobAttributes, "job-impressions")
	if !hasDone {
		return 0, 0, false
	}
	if !hasTotal || expected <= 0 {
		expected = 0
	}
	return int(finished), int(expected), true
}

// DeviceURI resolves the ipp:// address a CUPS queue prints to. CloudPrint
// uses it to bypass cupsd: the spooler still knows where the printer is, but
// it no longer handles the document.
func (c *Client) DeviceURI(ctx context.Context, queue string) (string, error) {
	uri, err := c.deviceURIIPP(ctx, queue)
	if err != nil {
		return "", err
	}
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(uri, "ipp://") && !strings.HasPrefix(uri, "ipps://") {
		return "", fmt.Errorf("队列 %q 的地址 %q 不是 IPP", queue, uri)
	}
	return uri, nil
}

// deviceURIIPP reads a queue's device-uri attribute from cupsd. CUPS exposes
// it on its own printer object, which works both locally and against a remote
// spooler, unlike parsing lpstat text or /etc/cups/printers.conf.
func (c *Client) deviceURIIPP(ctx context.Context, queue string) (string, error) {
	base := c.ippBase
	if base == "" {
		base = "http://127.0.0.1:631"
	}
	uri := attrURI(base) + "/printers/" + queue
	client, err := ipp.NewClient(uri, 10*time.Second)
	if err != nil {
		return "", err
	}
	msg, err := client.GetPrinterAttributes(ctx, []string{"device-uri"})
	if err != nil {
		return "", err
	}
	vals := msg.Strings(ipp.TagPrinterAttributes, "device-uri")
	if len(vals) == 0 {
		return "", fmt.Errorf("CUPS 没有返回队列 %q 的设备地址", queue)
	}
	return strings.TrimSpace(vals[0]), nil
}

func (c *Client) CancelJob(ctx context.Context, job string) error {
	_, err := c.run(ctx, c.Cancel, job)
	return err
}

// CancelDeviceJob cancels a job that was submitted straight to the printer.
// The queue is only used to find the device URI; the Cancel-Job operation goes
// to the printer, because cupsd never accepted this document.
func (c *Client) CancelDeviceJob(ctx context.Context, queue string, jobID int32, userName string) error {
	deviceURI, err := c.DeviceURI(ctx, queue)
	if err != nil {
		return err
	}
	client, err := ipp.NewClient(deviceURI, 15*time.Second)
	if err != nil {
		return err
	}
	return client.CancelJob(ctx, jobID, userName)
}

// DeviceControl runs a printer-level IPP operation (pause/resume/purge)
// against the hardware behind a queue. cupsd cannot do this for us: the queued
// documents live on the device, not in the local spooler.
func (c *Client) DeviceControl(ctx context.Context, queue, action, userName string) error {
	deviceURI, err := c.DeviceURI(ctx, queue)
	if err != nil {
		return err
	}
	client, err := ipp.NewClient(deviceURI, 15*time.Second)
	if err != nil {
		return err
	}
	switch action {
	case "pause":
		return client.PausePrinter(ctx, userName)
	case "resume":
		return client.ResumePrinter(ctx, userName)
	case "purge":
		return client.PurgeJobs(ctx, userName)
	}
	return fmt.Errorf("不支持的操作 %q", action)
}

type PPD struct {
	Path         string `json:"path"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Model        string `json:"model,omitempty"`
}

func ppdDirs() []string {
	if v := strings.TrimSpace(os.Getenv("CLOUDPRINT_PPD_DIRS")); v != "" {
		return strings.Split(v, string(os.PathListSeparator))
	}
	return []string{
		"/usr/share/cups/model",
		"/usr/share/ppd",
		"/usr/local/share/cups/model",
		"/opt/pantum/share/cups/model",
	}
}

// PPDs lists installed PPDs, including vendor drivers dropped under
// /usr/share/cups/model/Pantum. The list is cached because driver sets are
// large and change only on install.
func (c *Client) PPDs() []PPD {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ppds != nil && time.Since(c.ppdsRead) < 5*time.Minute {
		return c.ppds
	}
	var out []PPD
	seen := map[string]bool{}
	for _, dir := range ppdDirs() {
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(d.Name()), ".ppd") || seen[path] {
				return nil
			}
			seen[path] = true
			if p, ok := readPPD(path); ok {
				out = append(out, p)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Manufacturer != out[j].Manufacturer {
			return out[i].Manufacturer < out[j].Manufacturer
		}
		return out[i].Name < out[j].Name
	})
	c.ppds = out
	c.ppdsRead = time.Now()
	return out
}

var rePPDAttr = regexp.MustCompile(`^\*([A-Za-z][A-Za-z0-9_.-]*):\s*"?(.*?)"?\s*$`)

func readPPD(path string) (PPD, bool) {
	f, err := os.Open(path)
	if err != nil {
		return PPD{}, false
	}
	defer f.Close()
	attrs := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "*%") || line == "*End" {
			continue
		}
		if m := rePPDAttr.FindStringSubmatch(line); m != nil {
			if _, ok := attrs[m[1]]; !ok {
				attrs[m[1]] = strings.TrimSpace(m[2])
			}
		}
		if len(attrs) > 80 {
			break
		}
	}
	name := firstNonEmpty(attrs["NickName"], attrs["ModelName"], attrs["ShortNickName"])
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return PPD{
		Path:         path,
		Name:         name,
		Manufacturer: attrs["Manufacturer"],
		Model:        firstNonEmpty(attrs["ModelName"], attrs["Product"]),
	}, true
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// MatchPPD picks the driver whose name best covers the model reported by the
// device, e.g. "MS6000NW series" -> "Pantum MS6000NW Series".
func (c *Client) MatchPPD(model string) *PPD {
	model = normalize(model)
	if model == "" {
		return nil
	}
	bestScore := 0
	var best *PPD
	for i, p := range c.PPDs() {
		score := ppdScore(model, normalize(p.Name+" "+p.Model))
		if score > bestScore {
			bestScore = score
			best = &c.PPDs()[i]
		}
	}
	if bestScore < 10 {
		return nil
	}
	return best
}

func normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' || r == '/' {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func ppdScore(model, candidate string) int {
	mt := strings.Fields(model)
	ct := strings.Fields(candidate)
	score := 0
	matched := make([]bool, len(ct))
	for _, m := range mt {
		if len(m) < 4 || m == "series" || m == "printer" {
			continue
		}
		for i, c := range ct {
			if c == m {
				score += 40
				matched[i] = true
			} else if len(m) >= 6 && (strings.Contains(c, m) || strings.Contains(m, c)) {
				score += 25
				matched[i] = true
			}
		}
	}
	// "M6500NW Plus" and "M6500NW" both match the same model token; without
	// this penalty the alphabetically earlier variant wins and an admin sees
	// a driver for a different machine.
	for i, c := range ct {
		if matched[i] || len(c) < 3 || c == "pantum" || c == "series" || c == "printer" {
			continue
		}
		score -= 12
	}
	return score
}

// QueueName turns a human name into a CUPS-safe queue name.
func QueueName(display, fallbackID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(display) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(strings.Join(strings.FieldsFunc(b.String(), func(r rune) bool { return r == '-' }), "-"), "-")
	// A Chinese-only display name collapses to "" or a bare number. Both are
	// poor queue names and collide across devices, so fall back to the id.
	letters := 0
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	if letters < 3 {
		tail := fallbackID
		if len(tail) > 8 {
			tail = tail[len(tail)-8:]
		}
		s = "cloudprint-" + strings.ToLower(tail)
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// UniqueQueueName returns a queue name that is free, or already bound to this
// same device. lpadmin -p overwrites an existing queue verbatim, so reusing a
// name that belongs to another printer would silently repoint that printer.
// An error means the queue list could not be read and it is not safe to pick
// a name blindly.
func (c *Client) UniqueQueueName(ctx context.Context, base, fallbackID, deviceURI string) (string, error) {
	name := QueueName(base, fallbackID)
	list, err := c.Queues(ctx)
	if err != nil {
		return "", err
	}
	used := make(map[string]*Queue, len(list))
	for i := range list {
		used[strings.ToLower(list[i].Name)] = &list[i]
	}
	if q, ok := used[strings.ToLower(name)]; !ok {
		return name, nil
	} else if sameDevice(q.DeviceURI, deviceURI) {
		return name, nil
	}
	tail := fallbackID
	if len(tail) > 6 {
		tail = tail[len(tail)-6:]
	}
	tail = strings.ToLower(strings.TrimPrefix(tail, "-"))
	for i := 0; i < 50; i++ {
		candidate := name
		if i == 0 {
			candidate = name + "-" + tail
		} else {
			candidate = fmt.Sprintf("%s-%s-%d", name, tail, i+1)
		}
		if _, taken := used[strings.ToLower(candidate)]; !taken {
			return candidate, nil
		}
	}
	return name + "-" + strconv.FormatInt(time.Now().Unix(), 36), nil
}

func sameDevice(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == "" || b == "" {
		return false
	}
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}
