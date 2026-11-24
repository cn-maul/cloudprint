package printer

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"cloudprint/internal/cups"
	"cloudprint/internal/ipp"
	"cloudprint/internal/render"
	"cloudprint/internal/store"
)

type Executor struct {
	Store *store.Store
	CUPS  *cups.Client
	// DeviceURI resolves the physical device behind a CUPS queue. Rendering
	// happens here and the raster goes straight to that URI, so cupsd never
	// touches the document.
	DeviceURI func(ctx context.Context, queue string) (string, error)
	// QueuePPD resolves the PPD that tells us which vendor filter converts
	// raster into the device's own language. Nil means "skip vendor drivers".
	QueuePPD func(ctx context.Context, queue string) (path, makeModel string, err error)
}

type SendResult struct {
	OK         bool
	Code       string
	Message    string
	IPPJobID   int
	CUPSJobID  string
	DurationMS int64
	// Untracked means the device was written to over RAW 9100, which has no
	// job protocol: there is no id to poll and the job is finished once the
	// bytes are accepted.
	Untracked bool
}

func (e *Executor) PrintIPP(ctx context.Context, ep *store.Endpoint, job *store.Job, pdfPath string) (res SendResult) {
	start := time.Now()
	res = SendResult{Code: "IPP_SEND_FAILED"}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	// A direct IPP submission only works if the device parses PDF itself.
	// Printers that only take raster/PCL must go through a CUPS queue.
	if !ep.Capabilities.SupportsPDF() {
		res.Code = "ENDPOINT_NEEDS_DRIVER"
		res.Message = "这台打印机不直接接收 PDF，请用 CUPS 队列接入"
		return res
	}
	doc, err := os.Open(pdfPath)
	if err != nil {
		res.Message = "读取 PDF 失败: " + err.Error()
		return res
	}
	defer doc.Close()
	st, err := doc.Stat()
	if err != nil {
		res.Message = "读取 PDF 失败: " + err.Error()
		return res
	}
	if st.Size() < 100 {
		res.Message = "PDF 文件为空"
		return res
	}
	e.markDispatched(ctx, job)
	client, err := ipp.NewClient(ep.URI, render.ClampToDeadline(ctx, 10*time.Minute))
	if err != nil {
		res.Message = err.Error()
		return res
	}
	media := ippMedia(job.PaperSize)
	colorMode := job.ColorMode
	if colorMode == "" {
		colorMode = "monochrome"
	}
	id, uri, err := client.PrintJob(ctx, ipp.PrintOptions{
		JobName:      job.FileName,
		UserName:     job.Username,
		Format:       "application/pdf",
		Copies:       job.Copies,
		Duplex:       job.Duplex,
		Media:        media,
		ColorMode:    colorMode,
		Document:     doc,
		DocumentSize: st.Size(),
	})
	if err != nil {
		res.Message = err.Error()
		res.Code = classifyIPPError(err)
		e.Store.RecordEndpointEvent(ctx, ep.ID, "print_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	res.OK = true
	res.Code = "OK"
	res.Message = "已提交到打印机"
	res.IPPJobID = id
	if uri != "" {
		res.Message = "已提交到打印机 (" + uri + ")"
	}
	res.DurationMS = time.Since(start).Milliseconds()
	e.Store.RecordEndpointEvent(ctx, ep.ID, "print_ok", map[string]any{
		"job": job.ID, "ipp_job_id": id, "ms": res.DurationMS,
	})
	return res
}

// DispatchBudget returns how long one job may take from claim to accepted.
//
// Rendering dominates: Ghostscript costs roughly 1-3 s per page at 600 dpi on
// NAS-class CPUs, so the fixed 3-minute cap this used to have marked every
// document longer than about 60 pages as failed before it ever reached the
// printer. The budget grows with the page count and stops well short of leaving
// a printer blocked forever.
func DispatchBudget(pages int) time.Duration {
	if pages < 1 {
		pages = 1
	}
	budget := 2*time.Minute + time.Duration(pages)*3*time.Second
	if budget > 20*time.Minute {
		return 20 * time.Minute
	}
	return budget
}

// ClaimLease is how long a job may hold its printer's only slot. It matches the
// dispatch budget: the claim is taken before rendering starts, so a shorter
// lease would let the requeue sweep hand the same printer a second job while
// the first one is still rasterizing.
func ClaimLease(pages int) time.Duration {
	return DispatchBudget(pages)
}

// JobPages is how many pages a job will actually put on paper, which is what
// the dispatch budget and the claim lease are sized against.
func JobPages(job *store.Job) int {
	if job == nil {
		return 1
	}
	if job.TotalPages > 0 {
		return job.TotalPages
	}
	if job.PageCount > 0 {
		return job.PageCount
	}
	return 1
}

// markDispatched stamps the handoff to the device. It is deliberately
// best-effort: the stamp only refines which timeout applies, it must never
// abort a print that is otherwise ready to go.
func (e *Executor) markDispatched(ctx context.Context, job *store.Job) {
	if job == nil {
		return
	}
	_ = e.Store.MarkDispatched(context.WithoutCancel(ctx), job.ID)
}

func classifyIPPError(err error) string {
	if se, ok := err.(*ipp.StatusError); ok {
		switch se.Code {
		case 0x0406:
			return "IPP_DOCUMENT_FORMAT_UNSUPPORTED"
		case 0x0408:
			return "IPP_ATTRIBUTE_VALUE_UNSUPPORTED"
		case 0x0402, 0x0405:
			return "IPP_BUSY"
		case 0x0401:
			return "IPP_FORBIDDEN"
		}
	}
	return "IPP_SEND_FAILED"
}

func ippMedia(paper string) string {
	switch paper {
	case "", "A4":
		return "iso_a4_210x297mm"
	case "A3":
		return "iso_a3_297x420mm"
	case "Letter":
		return "na_letter_8.5x11in"
	case "Legal":
		return "na_legal_8.5x14in"
	default:
		return ""
	}
}

func (e *Executor) Dispatch(ctx context.Context, job *store.Job) SendResult {
	p, err := e.Store.GetPrinter(ctx, job.PrinterID)
	if err != nil {
		return SendResult{Code: "PRINTER_MISSING", Message: "打印机不存在"}
	}
	ep := SelectEndpoint(p, job.Duplex)
	if ep == nil {
		return SendResult{Code: "NO_ENDPOINT", Message: "打印机现在不可用，请检查它是否开机、CUPS 队列是否正常"}
	}
	f, err := e.Store.GetFile(ctx, job.PDFFile)
	if err != nil {
		return SendResult{Code: "FILE_MISSING", Message: "打印文件不存在"}
	}
	claimed, err := e.Store.ClaimQueuedJob(ctx, job.ID, ep.ID, ClaimLease(JobPages(job)))
	if err != nil {
		return SendResult{Code: "STATE_CONFLICT", Message: err.Error()}
	}
	if claimed == nil {
		return SendResult{Code: "PRINTER_BUSY", Message: "该打印机已有任务正在处理"}
	}
	if err := e.Store.MarkPrinting(ctx, job.ID, ep.ID); err != nil {
		_ = e.Store.ReleaseClaim(ctx, job.ID)
		return SendResult{Code: "STATE_CONFLICT", Message: err.Error()}
	}
	var res SendResult
	switch ep.Kind {
	case "cups":
		res = e.PrintRaster(ctx, ep, job, f.StoragePath)
	case "ipp":
		res = e.PrintIPP(ctx, ep, job, f.StoragePath)
	default:
		res = SendResult{Code: "ENDPOINT_UNSUPPORTED", Message: "不支持的连接方式"}
	}
	// A raster submit only means the device accepted the job. The worker asks
	// the device for its verdict until the job leaves its queue.
	if res.OK && ep.Kind == "cups" && !res.Untracked {
		return res
	}
	_ = e.Store.CompleteJob(ctx, job.ID, ep.ID, res.OK, res.Code, res.Message)
	return res
}

// PrintRaster renders the PDF to PWG Raster and sends it to the device behind a
// CUPS queue.
//
// cupsd keeps job documents as 0600 root:lp while its filters and the ipp
// backend run unprivileged, so every spooled PDF fails with "Permission
// denied" before reaching the printer. Rendering here and talking IPP straight
// to the device removes CUPS from the print path; the queue is only used to
// discover where the device lives.
func (e *Executor) PrintRaster(ctx context.Context, ep *store.Endpoint, job *store.Job, pdfPath string) (res SendResult) {
	start := time.Now()
	res = SendResult{Code: "RASTER_SEND_FAILED"}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	queue := firstNonEmpty(ep.QueueName, ep.URI)
	if e.DeviceURI == nil {
		res.Message = "没有配置设备地址解析"
		return res
	}
	uri, err := e.DeviceURI(ctx, queue)
	if err != nil {
		res.Code = "DEVICE_URI_UNKNOWN"
		res.Message = "找不到队列对应的打印机地址：" + err.Error()
		return res
	}
	if uri == "" {
		res.Code = "DEVICE_URI_UNKNOWN"
		res.Message = "队列 " + queue + " 没有绑定打印机地址"
		return res
	}
	dpi := ep.Capabilities.ResolutionDPI()
	// A queue bound to RAW 9100 (socket://) has no IPP job protocol. Render
	// and write the bytes; the device prints them, and there is no id to poll.
	if addr, ok := cups.SocketAddress(uri); ok {
		return e.printSocket(ctx, ep, job, pdfPath, uri, addr, dpi)
	}
	// A vendor PPD is the authoritative statement of what this device eats.
	// Prefer it: most network printers here are not IPP Everywhere devices and
	// reject generic raster with document-format-error.
	if e.QueuePPD != nil {
		if ppdPath, _, perr := e.QueuePPD(ctx, queue); perr == nil && ppdPath != "" {
			// A driverless queue ("everywhere" PPD) has no filter of its own;
			// its device accepts PWG-Raster, which render.PWG produces. Only
			// try the vendor chain when the PPD actually names one.
			if render.CanDrive(ppdPath) {
				res := e.printViaDriver(ctx, ep, job, pdfPath, uri, ppdPath, dpi)
				if res.Code != "DRIVER_NO_FILTER" {
					return res
				}
			}
		}
	}
	rasterPath := render.OutputName(pdfPath)
	rctx, rcancel := context.WithTimeout(ctx, render.ClampToDeadline(ctx, 15*time.Minute))
	err = render.PWG(rctx, render.Options{
		PDFPath:    pdfPath,
		OutputPath: rasterPath,
		DPI:        dpi,
		Paper:      job.PaperSize,
		Copies:     job.Copies,
	})
	rcancel()
	if err != nil {
		res.Code = "RASTER_RENDER_FAILED"
		res.Message = err.Error()
		e.Store.RecordEndpointEvent(ctx, ep.ID, "render_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	defer os.Remove(rasterPath)
	rasterFile, err := os.Open(rasterPath)
	if err != nil {
		res.Code = "RASTER_READ_FAILED"
		res.Message = "读取光栅文件失败：" + err.Error()
		return res
	}
	defer rasterFile.Close()
	rasterStat, err := rasterFile.Stat()
	if err != nil {
		res.Code = "RASTER_READ_FAILED"
		res.Message = "读取光栅文件失败：" + err.Error()
		return res
	}
	e.markDispatched(ctx, job)
	client, err := ipp.NewClient(uri, render.ClampToDeadline(ctx, 20*time.Minute))
	if err != nil {
		res.Message = err.Error()
		return res
	}
	// The device already got the correct page count from the raster and the
	// sheet count was baked in by the renderer, so ask for one copy here. Some
	// devices multiply the raster by the copies attribute and would otherwise
	// print the document N*N times.
	id, jobURI, err := client.PrintJob(ctx, ipp.PrintOptions{
		JobName:      job.ID,
		UserName:     job.Username,
		Format:       "image/pwg-raster",
		Copies:       1,
		Media:        ippMedia(job.PaperSize),
		ColorMode:    firstNonEmpty(job.ColorMode, "monochrome"),
		Document:     rasterFile,
		DocumentSize: rasterStat.Size(),
	})
	if err != nil {
		res.Message = "发送到打印机失败：" + err.Error()
		res.Code = classifyIPPError(err)
		e.Store.RecordEndpointEvent(ctx, ep.ID, "print_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	res.OK = true
	res.Code = "OK"
	res.IPPJobID = id
	res.Message = "已发送到打印机"
	if jobURI != "" {
		// Stored in the CUPS id column because that is what the poller reads.
		res.CUPSJobID = jobURI
		_ = e.Store.SetJobCUPSID(ctx, job.ID, jobURI)
	} else if found, foundID, ok := client.FindJobURI(ctx, job.ID); ok {
		// This device answers Print-Job without job-uri, and a job with no
		// reference can never be confirmed, so resolve it from the queue.
		res.CUPSJobID = found
		if id == 0 {
			res.IPPJobID = foundID
		}
		_ = e.Store.SetJobCUPSID(ctx, job.ID, found)
	}
	e.Store.RecordEndpointEvent(ctx, ep.ID, "print_ok", map[string]any{
		"job": job.ID, "device_job": jobURI, "uri": uri, "ms": res.DurationMS,
	})
	return res
}

// printViaDriver rasters the PDF and runs the queue's own vendor filter, then
// submits the resulting device-language stream over IPP.
//
// This is the path that works with ordinary network printers: they advertise
// image/pwg-raster, but several (the Pantum MS6000NW is one) answer a generic
// raster with client-error-document-format-error and only accept the stream
// produced by their bundled filter.
func (e *Executor) printViaDriver(ctx context.Context, ep *store.Endpoint, job *store.Job, pdfPath, uri, ppdPath string, dpi int) (res SendResult) {
	start := time.Now()
	res = SendResult{Code: "DRIVER_SEND_FAILED"}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	dctx, dcancel := context.WithTimeout(ctx, render.ClampToDeadline(ctx, 15*time.Minute))
	out, err := render.DriverChain(dctx, render.DriverOptions{
		PDFPath:   pdfPath,
		PPDPath:   ppdPath,
		DPI:       dpi,
		Paper:     job.PaperSize,
		Copies:    job.Copies,
		JobID:     job.ID,
		UserName:  job.Username,
		Title:     job.FileName,
		MediaName: ippMedia(job.PaperSize),
	})
	dcancel()
	if err != nil {
		if errors.Is(err, render.ErrNoFilter) {
			res.Code = "DRIVER_NO_FILTER"
			res.Message = err.Error()
			return res
		}
		res.Code = "DRIVER_RENDER_FAILED"
		res.Message = err.Error()
		e.Store.RecordEndpointEvent(ctx, ep.ID, "render_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	defer out.Close()
	e.Store.RecordEndpointEvent(ctx, ep.ID, "driver_ok", map[string]any{
		"job": job.ID, "filter": out.Program, "format": out.Format, "bytes": out.Size,
	})
	client, err := ipp.NewClient(uri, render.ClampToDeadline(ctx, 20*time.Minute))
	if err != nil {
		res.Message = err.Error()
		return res
	}
	doc, err := os.Open(out.Path)
	if err != nil {
		res.Code = "DRIVER_READ_FAILED"
		res.Message = "读取转换结果失败：" + err.Error()
		return res
	}
	defer doc.Close()
	e.markDispatched(ctx, job)
	// Copies are already baked into the converted stream.
	id, jobURI, err := client.PrintJob(ctx, ipp.PrintOptions{
		JobName:      job.ID,
		UserName:     job.Username,
		Format:       out.Format,
		Copies:       1,
		Media:        ippMedia(job.PaperSize),
		ColorMode:    firstNonEmpty(job.ColorMode, "monochrome"),
		Document:     doc,
		DocumentSize: out.Size,
	})
	if err != nil {
		res.Message = "发送到打印机失败：" + err.Error()
		res.Code = classifyIPPError(err)
		e.Store.RecordEndpointEvent(ctx, ep.ID, "print_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	res.OK = true
	res.Code = "OK"
	res.IPPJobID = id
	res.Message = "已发送到打印机"
	if jobURI != "" {
		res.CUPSJobID = jobURI
		_ = e.Store.SetJobCUPSID(ctx, job.ID, jobURI)
	} else if found, foundID, ok := client.FindJobURI(ctx, job.ID); ok {
		res.CUPSJobID = found
		if id == 0 {
			res.IPPJobID = foundID
		}
		_ = e.Store.SetJobCUPSID(ctx, job.ID, found)
	}
	e.Store.RecordEndpointEvent(ctx, ep.ID, "print_ok", map[string]any{
		"job": job.ID, "device_job": jobURI, "uri": uri, "ms": res.DurationMS,
		"format": out.Format,
	})
	return res
}

// printSocket writes the rendered device stream straight to a RAW 9100 port.
//
// This is the oldest printer protocol and the only one some cheap network
// printers support. It has no job ids, no status and no cancellation, so a job
// is done as soon as the bytes are accepted; a paper jam can only be noticed
// by the person standing next to the printer.
func (e *Executor) printSocket(ctx context.Context, ep *store.Endpoint, job *store.Job, pdfPath, uri, addr string, dpi int) (res SendResult) {
	start := time.Now()
	res = SendResult{Code: "SOCKET_SEND_FAILED", Untracked: true}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()

	// The device stream is kept on disk and copied to the socket, so a long
	// document never has to fit in memory.
	var streamPath string
	var format string
	var cleanup func()
	if e.QueuePPD != nil {
		if ppdPath, _, perr := e.QueuePPD(ctx, firstNonEmpty(ep.QueueName, ep.URI)); perr == nil && ppdPath != "" && render.CanDrive(ppdPath) {
			dctx, dcancel := context.WithTimeout(ctx, render.ClampToDeadline(ctx, 15*time.Minute))
			out, derr := render.DriverChain(dctx, render.DriverOptions{
				PDFPath:   pdfPath,
				PPDPath:   ppdPath,
				DPI:       dpi,
				Paper:     job.PaperSize,
				Copies:    job.Copies,
				JobID:     job.ID,
				UserName:  job.Username,
				Title:     job.FileName,
				MediaName: ippMedia(job.PaperSize),
			})
			dcancel()
			if derr == nil {
				streamPath, format = out.Path, out.Format
				cleanup = out.Close
				e.Store.RecordEndpointEvent(ctx, ep.ID, "driver_ok", map[string]any{
					"job": job.ID, "filter": out.Program, "format": out.Format, "bytes": out.Size,
				})
			} else if !errors.Is(derr, render.ErrNoFilter) {
				res.Code = "DRIVER_RENDER_FAILED"
				res.Message = derr.Error()
				return res
			}
		}
	}
	if streamPath == "" {
		// No usable PPD: a RAW port expects the device's own language, and
		// PWG-Raster is the best generic answer (most modern lasers take it).
		rasterPath := render.OutputName(pdfPath)
		rctx, rcancel := context.WithTimeout(ctx, render.ClampToDeadline(ctx, 15*time.Minute))
		err := render.PWG(rctx, render.Options{
			PDFPath: pdfPath, OutputPath: rasterPath, DPI: dpi,
			Paper: job.PaperSize, Copies: job.Copies,
		})
		rcancel()
		if err != nil {
			res.Code = "RASTER_RENDER_FAILED"
			res.Message = err.Error()
			return res
		}
		cleanup = func() { _ = os.Remove(rasterPath) }
		streamPath = rasterPath
		format = "image/pwg-raster"
	}
	if cleanup != nil {
		defer cleanup()
	}

	d := net.Dialer{Timeout: 15 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		res.Message = "连不上打印机的 9100 端口：" + err.Error()
		e.Store.RecordEndpointEvent(ctx, ep.ID, "print_fail", map[string]any{
			"error": res.Message, "job": job.ID, "uri": uri,
		})
		return res
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Minute))
	doc, err := os.Open(streamPath)
	if err != nil {
		res.Code = "RASTER_READ_FAILED"
		res.Message = "读取待发送文件失败：" + err.Error()
		return res
	}
	defer doc.Close()
	e.markDispatched(ctx, job)
	written, err := io.Copy(conn, doc)
	if err != nil {
		res.Message = "发送到打印机失败：" + err.Error()
		e.Store.RecordEndpointEvent(ctx, ep.ID, "print_fail", map[string]any{
			"error": res.Message, "job": job.ID, "uri": uri,
		})
		return res
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	res.OK = true
	res.Code = "OK"
	res.Message = "已发送到打印机（RAW 9100，无法回读状态）"
	e.Store.RecordEndpointEvent(ctx, ep.ID, "print_ok", map[string]any{
		"job": job.ID, "uri": uri, "format": format, "bytes": written, "ms": res.DurationMS,
	})
	return res
}

// PrintCUPS hands the PDF to the local spooler. Kept for queues whose filters
// can read the spool file (a host with a different CUPS policy).
func (e *Executor) PrintCUPS(ctx context.Context, ep *store.Endpoint, job *store.Job, pdfPath string) (res SendResult) {
	start := time.Now()
	res = SendResult{Code: "CUPS_SUBMIT_FAILED"}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
	if e.CUPS == nil {
		res.Message = "CUPS 未初始化"
		return res
	}
	queue := firstNonEmpty(ep.QueueName, ep.URI)
	id, err := e.CUPS.Submit(ctx, queue, pdfPath, cups.SubmitOptions{
		Copies:    job.Copies,
		Duplex:    job.Duplex,
		Media:     job.PaperSize,
		ColorMode: job.ColorMode,
		Title:     job.FileName,
	})
	if err != nil {
		res.Message = "提交到 CUPS 失败：" + err.Error()
		res.Code = classifyCUPSError(err)
		e.Store.RecordEndpointEvent(ctx, ep.ID, "print_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	res.OK = true
	res.Code = "OK"
	res.CUPSJobID = id
	res.Message = "已提交到打印队列 " + queue
	_ = e.Store.SetJobCUPSID(ctx, job.ID, id)
	e.Store.RecordEndpointEvent(ctx, ep.ID, "print_ok", map[string]any{
		"job": job.ID, "cups_job": id, "queue": queue, "ms": res.DurationMS,
	})
	return res
}

func classifyCUPSError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "no such file"), strings.Contains(msg, "not found"):
		return "CUPS_QUEUE_MISSING"
	case strings.Contains(msg, "forbidden"), strings.Contains(msg, "not authorized"):
		return "CUPS_FORBIDDEN"
	case strings.Contains(msg, "is not accepting"), strings.Contains(msg, "disabled"):
		return "CUPS_QUEUE_DISABLED"
	case strings.Contains(msg, "unable to connect"), strings.Contains(msg, "no route"):
		return "CUPS_UNAVAILABLE"
	}
	return "CUPS_SUBMIT_FAILED"
}

func (e *Executor) EndpointURI(ctx context.Context, job *store.Job) string {
	if job.EndpointID == "" {
		return ""
	}
	ep, err := e.Store.GetEndpoint(ctx, job.EndpointID)
	if err != nil {
		return ""
	}
	return ep.URI
}
