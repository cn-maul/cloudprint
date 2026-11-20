package printer

import (
	"context"
	"fmt"
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
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		res.Message = "读取 PDF 失败: " + err.Error()
		return res
	}
	if len(data) < 100 {
		res.Message = "PDF 文件为空"
		return res
	}
	client, err := ipp.NewClient(ep.URI, 60*time.Second)
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
		JobName:   job.FileName,
		UserName:  job.Username,
		Format:    "application/pdf",
		Copies:    job.Copies,
		Duplex:    job.Duplex,
		Media:     media,
		ColorMode: colorMode,
		Document:  data,
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
	claimed, err := e.Store.ClaimQueuedJob(ctx, job.ID, ep.ID, 5*time.Minute)
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
	if res.OK && ep.Kind == "cups" {
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
	// A vendor PPD is the authoritative statement of what this device eats.
	// Prefer it: most network printers here are not IPP Everywhere devices and
	// reject generic raster with document-format-error.
	if e.QueuePPD != nil {
		if ppdPath, _, perr := e.QueuePPD(ctx, queue); perr == nil && ppdPath != "" {
			return e.printViaDriver(ctx, ep, job, pdfPath, uri, ppdPath, dpi)
		}
	}
	rasterPath := render.OutputName(pdfPath)
	rctx, rcancel := context.WithTimeout(ctx, 5*time.Minute)
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
	data, err := os.ReadFile(rasterPath)
	if err != nil {
		res.Code = "RASTER_READ_FAILED"
		res.Message = "读取光栅文件失败：" + err.Error()
		return res
	}
	client, err := ipp.NewClient(uri, 3*time.Minute)
	if err != nil {
		res.Message = err.Error()
		return res
	}
	// The device already got the correct page count from the raster and the
	// sheet count was baked in by the renderer, so ask for one copy here. Some
	// devices multiply the raster by the copies attribute and would otherwise
	// print the document N*N times.
	id, jobURI, err := client.PrintJob(ctx, ipp.PrintOptions{
		JobName:   job.ID,
		UserName:  job.Username,
		Format:    "image/pwg-raster",
		Copies:    1,
		Media:     ippMedia(job.PaperSize),
		ColorMode: firstNonEmpty(job.ColorMode, "monochrome"),
		Document:  data,
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
	dctx, dcancel := context.WithTimeout(ctx, 6*time.Minute)
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
		res.Code = "DRIVER_RENDER_FAILED"
		res.Message = err.Error()
		e.Store.RecordEndpointEvent(ctx, ep.ID, "render_fail", map[string]any{
			"error": res.Message, "job": job.ID,
		})
		return res
	}
	e.Store.RecordEndpointEvent(ctx, ep.ID, "driver_ok", map[string]any{
		"job": job.ID, "filter": out.Program, "format": out.Format, "bytes": len(out.Data),
	})
	client, err := ipp.NewClient(uri, 3*time.Minute)
	if err != nil {
		res.Message = err.Error()
		return res
	}
	// Copies are already baked into the converted stream.
	id, jobURI, err := client.PrintJob(ctx, ipp.PrintOptions{
		JobName:   job.ID,
		UserName:  job.Username,
		Format:    out.Format,
		Copies:    1,
		Media:     ippMedia(job.PaperSize),
		ColorMode: firstNonEmpty(job.ColorMode, "monochrome"),
		Document:  out.Data,
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

func FormatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}
