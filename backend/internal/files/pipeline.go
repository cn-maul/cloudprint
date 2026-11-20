package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
		return &Prepared{PDFPath: out, PageCount: pages, SHA256: hash, SizeBytes: st.Size(), MIME: KindPDF}, nil
	case KindJPEG, KindPNG:
		out, err := p.imageToPDF(inputPath, inputHash, mime)
		if err != nil {
			return nil, err
		}
		st, _ := os.Stat(out)
		hash, _ := fileSHA256(out)
		return &Prepared{PDFPath: out, PageCount: 1, SHA256: hash, SizeBytes: st.Size(), MIME: KindPDF, Converted: true}, nil
	default:
		return nil, fmt.Errorf("不支持的文档类型")
	}
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

func (p *Pipeline) Preview(ctx context.Context, pdfPath, fileID string) (string, error) {
	outDir := filepath.Join(p.Root, "previews")
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		return "", err
	}
	out := filepath.Join(outDir, fileID+".png")
	if _, err := os.Stat(out); err == nil {
		return out, nil
	}
	if p.PDFToPPM == "" {
		return "", fmt.Errorf("未配置 PDF 预览工具 pdftoppm")
	}
	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	prefix := filepath.Join(outDir, fileID+"-p")
	cmd := exec.CommandContext(pctx, p.PDFToPPM, "-f", "1", "-l", "1", "-scale-to", "900", "-png", pdfPath, prefix)
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

func (p *Pipeline) SavePDFCopy(ctx context.Context, src string) (string, error) {
	dir := filepath.Join(p.Root, "pdf")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, "pdf-"+filepath.Base(src))
	return dst, copyFile(src, dst)
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

func ImageDimensions(path string) (image.Rectangle, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Rectangle{}, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return image.Rectangle{}, err
	}
	return image.Rect(0, 0, cfg.Width, cfg.Height), nil
}
