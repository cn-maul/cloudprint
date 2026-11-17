package printer

import (
	"context"
	"fmt"
	"os"
	"time"

	"cloudprint/internal/ipp"
	"cloudprint/internal/store"
)

type Executor struct {
	Store *store.Store
}

type SendResult struct {
	OK         bool
	Code       string
	Message    string
	IPPJobID   int
	DurationMS int64
}

func (e *Executor) PrintIPP(ctx context.Context, ep *store.Endpoint, job *store.Job, pdfPath string) (res SendResult) {
	start := time.Now()
	res = SendResult{Code: "IPP_SEND_FAILED"}
	defer func() { res.DurationMS = time.Since(start).Milliseconds() }()
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
	ep := SelectEndpoint(p, job.Duplex, false)
	if ep == nil {
		return SendResult{Code: "NO_ENDPOINT", Message: "没有健康的可用端点"}
	}
	if ep.Kind != "ipp" {
		return SendResult{Code: "AGENT_REQUIRED", Message: "该端点需要 Windows 代理执行"}
	}
	f, err := e.Store.GetFile(ctx, job.PDFFile)
	if err != nil {
		return SendResult{Code: "FILE_MISSING", Message: "打印文件不存在"}
	}
	claimed, err := e.Store.ClaimQueuedJob(ctx, job.ID, ep.ID, "", 5*time.Minute)
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
	res := e.PrintIPP(ctx, ep, job, f.StoragePath)
	_ = e.Store.CompleteJob(ctx, job.ID, ep.ID, res.OK, res.Code, res.Message)
	return res
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
