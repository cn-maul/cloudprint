package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-pdf/fpdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	KindPDF     = "application/pdf"
	KindJPEG    = "image/jpeg"
	KindPNG     = "image/png"
	MaxPDFPages = 200
)

// Pipeline turns an upload into a PDF that can be rasterized and sent to a
// printer. V1 accepts PDF, JPEG and PNG only: Office documents would need a
// LibreOffice runtime plus CJK fonts in the image, which is a large and
// fragile dependency for a single-printer tool.
type Pipeline struct {
	Root     string
	PDFToPPM string
	MaxPages int
}

func NewPipeline(root, pdftoppm string) *Pipeline {
	if strings.TrimSpace(pdftoppm) == "" {
		pdftoppm = findExecutable("pdftoppm", "pdftoppm")
	}
	return &Pipeline{
		Root:     root,
		PDFToPPM: pdftoppm,
		MaxPages: MaxPDFPages,
	}
}

func findExecutable(names ...string) string {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

type Prepared struct {
	PDFPath   string
	PageCount int
	SHA256    string
	SizeBytes int64
	MIME      string
	Converted bool
	// Paper is the paper size this document was laid out for, as detected from
	// the first page's MediaBox ("A4", "A3", "A5", "Letter", "Legal"), or ""
	// when the page is none of those. The UI shows it so a teacher can see up
	// front that the file is not A4 and will be scaled down to fit.
	Paper string
	// PaperMixed reports that later pages do not use the same paper as the
	// first one. Those pages are scaled individually, which is correct but
	// worth telling the user: a mixed document comes out with different
	// margins per page.
	PaperMixed bool
}

func (p *Pipeline) DetectMIME(name string, head []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(name))
	httpType := detectType(head)
	switch ext {
	case ".pdf":
		if httpType != KindPDF && httpType != "application/octet-stream" {
			if !strings.HasPrefix(string(head), "%PDF-") {
				return "", fmt.Errorf("文件内容不是有效的 PDF")
			}
		}
		return KindPDF, nil
	case ".jpg", ".jpeg":
		if httpType != KindJPEG {
			return "", fmt.Errorf("文件内容不是有效的 JPEG")
		}
		return KindJPEG, nil
	case ".png":
		if httpType != KindPNG {
			return "", fmt.Errorf("文件内容不是有效的 PNG")
		}
		return KindPNG, nil
	case ".docx":
		return "", fmt.Errorf("暂不支持 Word 文档，请先另存为 PDF 再上传")
	case ".xlsx":
		return "", fmt.Errorf("暂不支持 Excel 表格，请先另存为 PDF 再上传")
	case ".pptx":
		return "", fmt.Errorf("暂不支持 PowerPoint，请先另存为 PDF 再上传")
	default:
		return "", fmt.Errorf("不支持的文件类型，请上传 PDF、JPG 或 PNG")
	}
}

func (p *Pipeline) SaveUpload(ctx context.Context, r io.Reader, originalName, mime string) (string, string, int64, error) {
	dir := filepath.Join(p.Root, "originals")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", 0, err
	}
	tmp, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return "", "", 0, err
	}
	tmpName := tmp.Name()
	closed := false
	defer func() {
		if !closed {
			tmp.Close()
		}
		_ = os.Remove(tmpName)
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		return "", "", 0, err
	}
	if err := tmp.Sync(); err != nil {
		return "", "", 0, err
	}
	if err := tmp.Close(); err != nil {
		return "", "", 0, err
	}
	closed = true
	hash := hex.EncodeToString(h.Sum(nil))
	final := filepath.Join(dir, hash[:2], hash)
	if err := os.MkdirAll(filepath.Dir(final), 0o750); err != nil {
		return "", "", 0, err
	}
	if _, err := os.Stat(final); err == nil {
		return final, hash, n, nil
	}
	if err := os.Rename(tmpName, final); err != nil {
		return "", "", 0, err
	}
	_ = os.Chmod(final, 0o600)
	_ = originalName
	_ = mime
	return final, hash, n, nil
}

func (p *Pipeline) Prepare(ctx context.Context, inputPath, originalName, mime string) (*Prepared, error) {
	inputHash := filepath.Base(inputPath)
	switch mime {
	case KindPDF:
		pages, err := p.validatePDF(inputPath)
		if err != nil {
			return nil, err
		}
		out, _ := pdfPath(p.Root, inputHash)
		if err := copyFile(inputPath, out); err != nil {
			return nil, err
		}
		st, _ := os.Stat(out)
		hash, _ := fileSHA256(out)
		paper, mixed := detectPaperSizes(out)
		return &Prepared{
			PDFPath: out, PageCount: pages, SHA256: hash, SizeBytes: st.Size(),
			MIME: KindPDF, Paper: paper, PaperMixed: mixed,
		}, nil
	case KindJPEG, KindPNG:
		out, err := p.imageToPDF(inputPath, inputHash, mime)
		if err != nil {
			return nil, err
		}
		st, _ := os.Stat(out)
		hash, _ := fileSHA256(out)
		return &Prepared{PDFPath: out, PageCount: 1, SHA256: hash, SizeBytes: st.Size(), MIME: KindPDF, Converted: true, Paper: "A4"}, nil
	default:
		return nil, fmt.Errorf("不支持的文档类型")
	}
}

// Named page sizes in PDF points, portrait and landscape. The tolerance is
// deliberately loose: Word, WPS and phone scanner apps all emit sizes a few
// points off, and those must still be treated as the nominal paper so they
// print 1:1 instead of being rescaled.
var namedPapers = []struct {
	name          string
	width, height float64
}{
	{"A4", 595.276, 841.890},
	{"A3", 841.890, 1190.551},
	{"A5", 419.528, 595.276},
	{"Letter", 612, 792},
	{"Legal", 612, 1008},
}

const paperTolerance = 18.0 // ~6 mm; larger than any normal rounding drift

// detectPaperSizes reports the nominal paper size of the first page and
// whether any later page uses a different one. An unreadable or custom-sized
// first page returns ""; mixed is reported even when the first page is custom.
//
// Every page is checked, not just the first: a scanned cover page is often A4
// while the notice behind it is A3, and the user would otherwise be told
// "A4" for a document that needs scaling on page 2.
func detectPaperSizes(path string) (paper string, mixed bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	dims, err := api.PageDims(context.Background(), f, model.NewDefaultConfiguration())
	if err != nil || len(dims) == 0 {
		return "", false
	}
	firstKey := paperKey(dims[0].Width, dims[0].Height)
	paper = namedPaper(dims[0].Width, dims[0].Height)
	for _, d := range dims[1:] {
		if paperKey(d.Width, d.Height) != firstKey {
			mixed = true
			break
		}
	}
	return paper, mixed
}

// paperKey identifies a page size for comparison. Named sizes compare by
// name; a custom size compares by its rounded portrait dimensions, so two
// pages of the same unknown size are not reported as mixed.
func paperKey(width, height float64) string {
	if name := namedPaper(width, height); name != "" {
		return name
	}
	w, h := width, height
	if w > h {
		w, h = h, w
	}
	return fmt.Sprintf("custom:%.0fx%.0f", w, h)
}

// namedPaper maps a width/height pair in points to a paper name, or "" when
// the page is some custom size.
func namedPaper(width, height float64) string {
	w, h := width, height
	if w > h {
		w, h = h, w
	}
	for _, p := range namedPapers {
		if abs(w-p.width) <= paperTolerance && abs(h-p.height) <= paperTolerance {
			return p.name
		}
	}
	return ""
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func (p *Pipeline) validatePDF(path string) (int, error) {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if err := api.Validate(context.Background(), f, conf, nil); err != nil {
		return 0, fmt.Errorf("PDF 校验失败: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	pages, err := api.PageCount(context.Background(), f, conf)
	if err != nil {
		return 0, fmt.Errorf("读取 PDF 页数失败: %w", err)
	}
	if pages <= 0 {
		return 0, fmt.Errorf("PDF 没有可打印页面")
	}
	if p.MaxPages > 0 && pages > p.MaxPages {
		return 0, fmt.Errorf("文档共 %d 页，超过上限 %d 页", pages, p.MaxPages)
	}
	return pages, nil
}

func (p *Pipeline) imageToPDF(input, hash, mime string) (string, error) {
	st, err := os.Stat(input)
	if err != nil {
		return "", err
	}
	outDir := filepath.Join(p.Root, "pdf")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, "img-"+hash+".pdf")
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(10, 10, 10)
	pdf.AddPage()
	imageType := "jpg"
	if mime == KindPNG {
		imageType = "png"
	}
	opt := fpdf.ImageOptions{ImageType: imageType, ReadDpi: true}
	info := pdf.RegisterImageOptions(input, opt)
	if info == nil {
		return "", fmt.Errorf("无法读取图片")
	}
	pageW, pageH := pdf.GetPageSize()
	maxW, maxH := pageW-20, pageH-20
	w, h := info.Width(), info.Height()
	scale := minFloat(maxW/w, maxH/h)
	dw, dh := w*scale, h*scale
	pdf.ImageOptions(input, (pageW-dw)/2, (pageH-dh)/2, dw, dh, false, opt, 0, "")
	if err := pdf.OutputFileAndClose(out); err != nil {
		return "", err
	}
	if st.Size() == 0 {
		return "", fmt.Errorf("图片为空")
	}
	return out, nil
}

// Preview renders one page of a PDF as a PNG. The page is part of the cache
// key: without it, flipping to page 2 of a short document would silently show
// page 1 again because the first render already exists on disk.
func (p *Pipeline) Preview(ctx context.Context, pdfPath, fileID string, page int) (string, error) {
	outDir := filepath.Join(p.Root, "previews")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return "", err
	}
	if page < 1 {
		page = 1
	}
	out := filepath.Join(outDir, fmt.Sprintf("%s-p%d.png", fileID, page))
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if p.PDFToPPM == "" {
		return "", fmt.Errorf("未配置 PDF 预览工具 pdftoppm")
	}
	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	prefix := filepath.Join(outDir, fmt.Sprintf("%s-p%d-", fileID, page))
	cmd := exec.CommandContext(pctx, p.PDFToPPM, "-f", strconv.Itoa(page), "-l", strconv.Itoa(page),
		"-scale-to", "900", "-png", pdfPath, prefix)
	if _, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("生成预览失败")
	}
	matches, _ := filepath.Glob(prefix + "*.png")
	if len(matches) == 0 {
		return "", fmt.Errorf("没有生成预览图")
	}
	if err := os.Rename(matches[0], out); err != nil {
		return "", err
	}
	return out, nil
}

func pdfPath(root, hash string) (string, error) {
	dir := filepath.Join(root, "pdf")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return filepath.Join(dir, "pdf-"+hash), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile exposes the same SHA-256 digest for callers outside the pipeline,
// such as jobs that print a page subset written by ExtractPages.
func HashFile(path string) (string, error) { return fileSHA256(path) }

func detectType(head []byte) string {
	if len(head) >= 5 && string(head[:5]) == "%PDF-" {
		return KindPDF
	}
	if len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF {
		return KindJPEG
	}
	if len(head) >= 8 && string(head[:8]) == "\x89PNG\r\n\x1a\n" {
		return KindPNG
	}
	if isZIP(head) {
		return "application/zip"
	}
	return "application/octet-stream"
}

func isZIP(head []byte) bool {
	return len(head) >= 4 && head[0] == 'P' && head[1] == 'K' && (head[2] == 3 || head[2] == 5)
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
