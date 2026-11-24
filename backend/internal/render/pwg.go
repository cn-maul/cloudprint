// Package render turns a PDF into the raster format driverless printers
// accept.
//
// CUPS on this host cannot be trusted with the job: cupsd spools documents as
// 0600 root:lp, and both vendor filters and the ipp backend run unprivileged,
// so they fail with "Permission denied" before touching the device. Rendering
// here and submitting over IPP removes the spooler from the path entirely.
package render

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Options describes one rasterization request.
type Options struct {
	// PDFPath is the source document. Page selection already happened when the
	// job was created, so every page in this file is printed.
	PDFPath string
	// OutputPath receives the PWG Raster stream.
	OutputPath string
	// DPI must match what the device reports (600 for the Pantum here).
	DPI int
	// Paper is "A4", "A3", "Letter" or "Legal".
	Paper string
	// Copies duplicates every page inside the raster, which is what devices
	// that ignore the IPP copies attribute still honour.
	Copies int
}

// paperPoints returns the page size in PDF points (1/72 inch).
func paperPoints(paper string) (width, height float64, ok bool) {
	switch strings.ToUpper(strings.TrimSpace(paper)) {
	case "", "A4":
		return 595.276, 841.890, true
	case "A3":
		return 841.890, 1190.551, true
	case "LETTER":
		return 612, 792, true
	case "LEGAL":
		return 612, 1008, true
	}
	return 0, 0, false
}

// PWG renders the PDF to PWG Raster. Ghostscript's pwgraster device emits the
// exact "RaS2" stream that IPP Everywhere printers advertise, so no vendor
// filter is involved.
//
// Copies are expanded before rasterizing: a CUPS raster stream carries its
// page data as "one 1796-byte header, then rows" repeated per page, and the
// only sync word is at the very start of the file. Slicing that stream by hand
// is easy to get wrong, so the PDF is duplicated instead and Ghostscript walks
// it once.
func PWG(ctx context.Context, opt Options) error {
	if opt.PDFPath == "" || opt.OutputPath == "" {
		return fmt.Errorf("缺少输入或输出路径")
	}
	if opt.DPI <= 0 {
		opt.DPI = 600
	}
	if opt.Copies <= 1 {
		opt.Copies = 1
	}
	width, height, ok := paperPoints(opt.Paper)
	if !ok {
		return fmt.Errorf("不支持的纸张尺寸 %q", opt.Paper)
	}
	gs, err := exec.LookPath("gs")
	if err != nil {
		return fmt.Errorf("镜像里没有 Ghostscript，无法转换文件")
	}
	source := opt.PDFPath
	if opt.Copies > 1 {
		repeated, cleanup, rerr := repeatPDF(ctx, opt.PDFPath, opt.Copies)
		if rerr != nil {
			return rerr
		}
		defer cleanup()
		source = repeated
	}
	args := append([]string{"-sDEVICE=pwgraster"},
		mediaFitArgs(opt.DPI, width, height)...)
	args = append(args, "-sOutputFile="+opt.OutputPath, source)
	runCtx, cancel := context.WithTimeout(ctx, ClampToDeadline(ctx, 15*time.Minute))
	defer cancel()
	cmd := exec.CommandContext(runCtx, gs, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Ghostscript 转换失败: %s", tail(string(out), 400))
	}
	info, err := os.Stat(opt.OutputPath)
	if err != nil {
		return fmt.Errorf("转换结果不存在: %w", err)
	}
	if info.Size() < 1024 {
		return fmt.Errorf("转换结果异常（%d 字节）", info.Size())
	}
	return nil
}

// repeatPDF builds a PDF that contains Copies concatenated copies of src. The
// caller owns the returned cleanup function.
func repeatPDF(ctx context.Context, src string, copies int) (string, func(), error) {
	dir := filepath.Dir(src)
	// A unique name keeps two jobs on the same document from clobbering each
	// other's copy of the file.
	f, err := os.CreateTemp(dir, ".copies-*.pdf")
	if err != nil {
		return "", nil, fmt.Errorf("无法创建临时文件: %w", err)
	}
	tmp := f.Name()
	_ = f.Close()
	if err := os.Remove(tmp); err != nil {
		return "", nil, fmt.Errorf("无法创建临时文件: %w", err)
	}
	args := make([]string, 0, copies+1)
	for i := 0; i < copies; i++ {
		args = append(args, src)
	}
	args = append(args, tmp)
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "pdfunite", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return "", nil, fmt.Errorf("复制打印份数失败: %s", tail(string(out), 300))
	}
	cleanup := func() { _ = os.Remove(tmp) }
	if _, err := os.Stat(tmp); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("复制打印份数失败: %w", err)
	}
	return tmp, cleanup, nil
}

// mediaFitArgs returns the Ghostscript switches that pin the raster to one
// paper size and scale the document onto it.
//
// -dFIXEDMEDIA alone is not enough: Ghostscript then keeps every page at 1:1
// and clips whatever falls outside the media box. An A3 notice printed on A4
// came out as a blank sheet exactly that way. -dPDFFitPage scales larger pages
// down to fit (and leaves a page that already matches the target size
// pixel-identical), so both the PWG and the CUPS raster path share this.
func mediaFitArgs(dpi int, width, height float64) []string {
	return []string{
		"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER",
		"-r" + strconv.Itoa(dpi),
		"-dFIXEDMEDIA",
		"-dDEVICEWIDTHPOINTS=" + strconv.FormatFloat(width, 'f', 3, 64),
		"-dDEVICEHEIGHTPOINTS=" + strconv.FormatFloat(height, 'f', 3, 64),
		"-dPDFFitPage",
	}
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// OutputName builds a per-job raster path next to the source PDF.
func OutputName(pdfPath string) string {
	base := strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))
	return filepath.Join(filepath.Dir(pdfPath), base+".pwg")
}
