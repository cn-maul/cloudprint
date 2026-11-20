// Vendor filter chains.
//
// Most network printers in the field are not IPP Everywhere devices: their PPD
// declares a proprietary filter that converts CUPS raster into whatever the
// device actually understands (Pantum's GDI stream, PCL, ESC/P ...). CUPS would
// run that filter for us, but cupsd writes the job file as 0600 root:lp while
// filters run as lp, so every job dies with "Permission denied" before the
// device is touched.
//
// The fix is to run the same chain ourselves: rasterize the PDF to CUPS raster,
// then hand it to the filter named in the queue's *cupsFilter line. That keeps
// the code driver-agnostic - a Pantum, an HP and a Brother all work as long as
// CUPS has a PPD for them.
package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FilterSpec is one *cupsFilter / *cupsFilter2 entry from a PPD.
type FilterSpec struct {
	// Source is the MIME type the filter consumes, e.g. application/vnd.cups-raster.
	Source string
	// Dest is the MIME type it produces, e.g. application/octet-stream.
	Dest string
	// Cost orders competing filters; CUPS picks the cheapest.
	Cost int
	// Program is the filter binary that does the conversion.
	Program string
}

// Options for one vendor-filtered job.
type DriverOptions struct {
	PDFPath   string
	PPDPath   string
	DPI       int
	Paper     string
	Copies    int
	JobID     string
	UserName  string
	Title     string
	MediaName string
}

// DriverResult is the converted document plus the MIME type to declare to the
// device.
type DriverResult struct {
	Data   []byte
	Format string
	// Program names the filter that produced Data, for logging.
	Program string
}

// ParseFilters extracts every *cupsFilter and *cupsFilter2 line from a PPD.
func ParseFilters(ppd string) []FilterSpec {
	var out []FilterSpec
	for _, line := range strings.Split(ppd, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "*cupsFilter2:"):
			// *cupsFilter2: "src dest cost program"
			if v := quoted(line); v != "" {
				f := strings.Fields(v)
				if len(f) >= 4 {
					cost, _ := strconv.Atoi(f[2])
					out = append(out, FilterSpec{
						Source: f[0], Dest: f[1], Cost: cost, Program: f[3],
					})
				}
			}
		case strings.HasPrefix(line, "*cupsFilter:"):
			// *cupsFilter: "src cost program"
			// The three-field form predates cupsFilter2 and has no explicit
			// destination: the filter's output is the device's native format,
			// which is what we send over IPP as octet-stream.
			if v := quoted(line); v != "" {
				f := strings.Fields(v)
				if len(f) >= 3 {
					cost, _ := strconv.Atoi(f[1])
					out = append(out, FilterSpec{
						Source: f[0], Dest: "application/octet-stream",
						Cost: cost, Program: f[2],
					})
				}
			}
		}
	}
	return out
}

// quoted returns the first double-quoted substring of a PPD line.
func quoted(line string) string {
	start := strings.IndexByte(line, '"')
	if start < 0 {
		return ""
	}
	end := strings.IndexByte(line[start+1:], '"')
	if end < 0 {
		return ""
	}
	return line[start+1 : start+1+end]
}

// Filter input types. Vendor GDI drivers (Pantum and most cheap lasers) read
// CUPS raster; PCL/PCL-XL drivers read PDF or PostScript; a PostScript printer
// has no filter at all and receives the document unchanged.
const (
	SourceRaster     = "application/vnd.cups-raster"
	SourcePDF        = "application/pdf"
	SourceCUPSPDF    = "application/vnd.cups-pdf"
	SourcePostScript = "application/postscript"
	SourceCUPSPS     = "application/vnd.cups-postscript"
)

// Raster first: only that path lets us control resolution and paper exactly.
// The rest are ordered from least to most conversion work.
var filterPreference = []string{SourceRaster, SourcePDF, SourceCUPSPDF, SourcePostScript, SourceCUPSPS}

// PickFilter chooses the filter CUPS would run, preferring one that consumes
// raster and falling back to PDF or PostScript. A PPD with no filter at all is
// a PostScript printer; see ppdPostScript.
func PickFilter(specs []FilterSpec) (FilterSpec, bool) {
	for _, want := range filterPreference {
		var best FilterSpec
		found := false
		for _, s := range specs {
			if s.Source != want {
				continue
			}
			if !found || s.Cost < best.Cost {
				best, found = s, true
			}
		}
		if found {
			return best, true
		}
	}
	return FilterSpec{}, false
}

// ppdPostScript reports whether the device takes PostScript directly. Such a
// PPD declares a language level and no filter, which is how HP/Brother/Epson
// PostScript queues are installed.
func ppdPostScript(ppd string) bool {
	for _, line := range strings.Split(ppd, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "*PSVersion:") || strings.HasPrefix(line, "*LanguageLevel:") {
			return true
		}
	}
	return false
}

// cupsServerBin is the directory cups-filters wrappers expect their helpers
// in; Debian puts the filters themselves there.
func cupsServerBin() string {
	for _, dir := range []string{"/usr/lib/cups", "/usr/libexec/cups", "/usr/local/lib/cups"} {
		if st, err := os.Stat(filepath.Join(dir, "filter")); err == nil && st.IsDir() {
			return dir
		}
	}
	return ""
}

// FilterPath resolves a filter program name to a runnable path, searching the
// same directories CUPS uses.
func FilterPath(name string) (string, error) {
	if strings.ContainsRune(name, '/') {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("过滤器不存在: %s", name)
	}
	for _, dir := range []string{
		"/usr/lib/cups/filter",
		"/usr/libexec/cups/filter",
		"/usr/local/lib/cups/filter",
	} {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("找不到过滤器 %s", name)
}

// ErrNoFilter means the PPD names no filter and is not a PostScript printer,
// so there is nothing for us to run. Callers should fall back to generic
// PWG-Raster instead.
var ErrNoFilter = errors.New("这个 PPD 没有可执行的过滤器")

// Inspect reports how a PPD would be driven: "raster", "pdf", "postscript",
// or "" when it has no usable path.
func Inspect(ppd string) string {
	if spec, ok := PickFilter(ParseFilters(ppd)); ok {
		switch spec.Source {
		case SourceRaster:
			return "raster"
		case SourcePDF, SourceCUPSPDF:
			return "pdf"
		default:
			return "postscript"
		}
	}
	if ppdPostScript(ppd) {
		return "postscript"
	}
	return ""
}

// CanDrive reports whether a queue PPD can be driven by this package.
func CanDrive(ppdPath string) bool {
	body, err := os.ReadFile(ppdPath)
	if err != nil {
		return false
	}
	return Inspect(string(body)) != ""
}

// DriverChain rasterizes the PDF and pipes it through the vendor filter.
func DriverChain(ctx context.Context, opt DriverOptions) (*DriverResult, error) {
	if opt.PDFPath == "" || opt.PPDPath == "" {
		return nil, fmt.Errorf("缺少 PDF 或 PPD")
	}
	if opt.DPI <= 0 {
		opt.DPI = 600
	}
	if opt.Copies <= 0 {
		opt.Copies = 1
	}
	ppdBytes, err := os.ReadFile(opt.PPDPath)
	if err != nil {
		return nil, fmt.Errorf("读取 PPD 失败: %w", err)
	}
	specs := ParseFilters(string(ppdBytes))
	spec, ok := PickFilter(specs)
	if !ok {
		if ppdPostScript(string(ppdBytes)) {
			return postScriptChain(ctx, opt)
		}
		return nil, ErrNoFilter
	}
	filterPath, err := FilterPath(spec.Program)
	if err != nil {
		return nil, err
	}

	source := opt.PDFPath
	if opt.Copies > 1 {
		repeated, cleanup, rerr := repeatPDF(ctx, opt.PDFPath, opt.Copies)
		if rerr != nil {
			return nil, rerr
		}
		defer cleanup()
		source = repeated
	}

	dir := filepath.Dir(opt.PDFPath)
	switch spec.Source {
	case SourcePDF, SourceCUPSPDF, SourcePostScript, SourceCUPSPS:
		// These filters read a page description, not a bitmap. Handing them
		// the original PDF (or a PostScript rendering of it) keeps text sharp
		// and lets the filter do its own dithering.
		filterInput := source
		if spec.Source == SourcePostScript || spec.Source == SourceCUPSPS {
			ps, cleanup, perr := toPostScript(ctx, opt, source)
			if perr != nil {
				return nil, perr
			}
			defer cleanup()
			filterInput = ps
		}
		return runVendorFilter(ctx, opt, spec, filterPath, filterInput)
	}

	width, height, ok := paperPoints(opt.Paper)
	if !ok {
		return nil, fmt.Errorf("不支持的纸张尺寸 %q", opt.Paper)
	}
	rasterFile, err := os.CreateTemp(dir, ".raster-*.tmp")
	if err != nil {
		return nil, err
	}
	rasterPath := rasterFile.Name()
	_ = rasterFile.Close()
	defer os.Remove(rasterPath)

	gs, err := exec.LookPath("gs")
	if err != nil {
		return nil, fmt.Errorf("镜像里没有 Ghostscript，无法转换文件")
	}
	// cupsColorSpace 0 is DeviceGray and cupsBitsPerColor 8 matches what the
	// vendor PPDs declare; monochrome printers halftone it themselves.
	args := []string{
		"-q", "-dNOPAUSE", "-dBATCH", "-dSAFER",
		"-sDEVICE=cups",
		"-r" + strconv.Itoa(opt.DPI),
		"-dDEVICEWIDTHPOINTS=" + strconv.FormatFloat(width, 'f', 3, 64),
		"-dDEVICEHEIGHTPOINTS=" + strconv.FormatFloat(height, 'f', 3, 64),
		"-dcupsBitsPerColor=8", "-dcupsColorSpace=0", "-dcupsColorOrder=0",
		"-sOutputFile=" + rasterPath,
		source,
	}
	if err := runWithTimeout(ctx, gs, args, 5*time.Minute); err != nil {
		return nil, err
	}
	return runVendorFilter(ctx, opt, spec, filterPath, rasterPath)
}

// runVendorFilter invokes a vendor filter the way cupsd would and returns the
// stream it produced.
func runVendorFilter(ctx context.Context, opt DriverOptions, spec FilterSpec, filterPath, input string) (*DriverResult, error) {
	dir := filepath.Dir(opt.PDFPath)
	tmp, err := os.CreateTemp(dir, ".driver-*.tmp")
	if err != nil {
		return nil, err
	}
	destPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(destPath)

	media := opt.MediaName
	if media == "" {
		media = "iso_a4_210x297mm"
	}
	options := "Resolution=" + strconv.Itoa(opt.DPI) + "dpi media=" + media
	jobID := firstNonEmpty(opt.JobID, "0")
	user := firstNonEmpty(opt.UserName, "cloudprint")
	title := firstNonEmpty(opt.Title, "CloudPrint")

	// CUPS calls a filter as: job-id user title copies options [file]. The
	// vendor filters read the PPD to learn paper and resolution, exactly as
	// cupsd would arrange for them.
	cmdArgs := []string{jobID, user, title, strconv.Itoa(opt.Copies), options, input}
	out, err := runFilter(ctx, filterPath, cmdArgs, destPath, opt.PPDPath, 3*time.Minute)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if len(data) < 16 {
		return nil, fmt.Errorf("厂商过滤器没有产出有效数据（%d 字节）", len(data))
	}
	mime := spec.Dest
	if mime == "" || mime == "application/vnd.cups-raster" {
		return nil, fmt.Errorf("过滤器 %s 没有产出设备能直接接收的格式", spec.Program)
	}
	return &DriverResult{Data: data, Format: mime, Program: spec.Program}, nil
}

// toPostScript converts the job PDF to PostScript for PPDs whose filter reads
// PostScript rather than PDF.
func toPostScript(ctx context.Context, opt DriverOptions, source string) (string, func(), error) {
	f, err := os.CreateTemp(filepath.Dir(opt.PDFPath), ".ps-*.ps")
	if err != nil {
		return "", nil, err
	}
	out := f.Name()
	_ = f.Close()
	cleanup := func() { _ = os.Remove(out) }

	// pdftops keeps selectable text and is much faster than a Ghostscript
	// raster round-trip.
	args := []string{"-q", "-level3", source, out}
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if bin, lerr := exec.LookPath("pdftops"); lerr == nil {
		cmd := exec.CommandContext(runCtx, bin, args...)
		cmd.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
		if o, rerr := cmd.CombinedOutput(); rerr != nil {
			cleanup()
			return "", nil, fmt.Errorf("转成 PostScript 失败: %s", tail(string(o), 300))
		}
	} else {
		gs, gerr := exec.LookPath("gs")
		if gerr != nil {
			cleanup()
			return "", nil, fmt.Errorf("镜像里没有 pdftops 或 Ghostscript，无法转成 PostScript")
		}
		cmd := exec.CommandContext(runCtx, gs, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER",
			"-sDEVICE=ps2write", "-sOutputFile="+out, source)
		if o, rerr := cmd.CombinedOutput(); rerr != nil {
			cleanup()
			return "", nil, fmt.Errorf("转成 PostScript 失败: %s", tail(string(o), 300))
		}
	}
	if st, serr := os.Stat(out); serr != nil || st.Size() < 64 {
		cleanup()
		return "", nil, fmt.Errorf("PostScript 转换没有产出有效文件")
	}
	return out, cleanup, nil
}

// postScriptChain handles a PostScript printer, whose PPD names no filter:
// the device's interpreter does the rendering, so the job is a plain PS file.
func postScriptChain(ctx context.Context, opt DriverOptions) (*DriverResult, error) {
	source := opt.PDFPath
	if opt.Copies > 1 {
		repeated, cleanup, err := repeatPDF(ctx, opt.PDFPath, opt.Copies)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		source = repeated
	}
	ps, cleanup, err := toPostScript(ctx, opt, source)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	data, err := os.ReadFile(ps)
	if err != nil {
		return nil, err
	}
	return &DriverResult{Data: data, Format: "application/postscript", Program: "postscript"}, nil
}

// runFilter executes a vendor filter with its output redirected to destPath.
func runFilter(ctx context.Context, filter string, args []string, destPath, ppdPath string, timeout time.Duration) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, filter, args...)
	// Filters locate the PPD through PPD; without it they either segfault or
	// emit nothing, which looks like a broken driver rather than a missing
	// environment variable.
	// The cups-filters wrappers (gstopxl, gstoraster, ...) also locate their
	// helpers through CUPS_SERVERBIN; cupsd normally sets it, so a direct call
	// fails with "/filter/gstoraster: not found" unless it is supplied here.
	env := append(os.Environ(), "PPD="+ppdPath, "LANG=C", "LC_ALL=C")
	if strings.TrimSpace(os.Getenv("CUPS_SERVERBIN")) == "" {
		if bin := cupsServerBin(); bin != "" {
			env = append(env, "CUPS_SERVERBIN="+bin)
		}
	}
	cmd.Env = env
	f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	cmd.Stdout = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("厂商过滤器 %s 运行失败: %s",
			filepath.Base(filter), tail(firstNonEmpty(stderr.String(), err.Error()), 300))
	}
	return destPath, nil
}

func runWithTimeout(ctx context.Context, bin string, args []string, timeout time.Duration) error {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Ghostscript 转换失败: %s", tail(string(out), 400))
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
