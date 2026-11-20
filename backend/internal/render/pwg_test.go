package render

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPaperPointsCoversSupportedSizes(t *testing.T) {
	for _, paper := range []string{"A4", "a4", "A3", "Letter", "Legal", ""} {
		w, h, ok := paperPoints(paper)
		if !ok || w <= 0 || h <= 0 {
			t.Fatalf("paperPoints(%q) = %v,%v,%v", paper, w, h, ok)
		}
	}
	if _, _, ok := paperPoints("B5"); ok {
		t.Fatal("B5 should not be silently accepted")
	}
}

func TestPWGRejectsMissingInput(t *testing.T) {
	if err := PWG(context.Background(), Options{}); err == nil {
		t.Fatal("expected an error without a PDF path")
	}
}

func TestPWGRejectsUnsupportedPaper(t *testing.T) {
	err := PWG(context.Background(), Options{
		PDFPath:    "in.pdf",
		OutputPath: "out.pwg",
		Paper:      "B5",
	})
	if err == nil {
		t.Fatal("expected an error for an unsupported paper size")
	}
}

func TestPWGProducesRasterWithRequestedPageCount(t *testing.T) {
	if _, err := exec.LookPath("gs"); err != nil {
		t.Skip("ghostscript is not installed")
	}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(pdf, buildTestPDF(2), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.pwg")
	if err := PWG(context.Background(), Options{
		PDFPath:    pdf,
		OutputPath: out,
		DPI:        150,
		Paper:      "A4",
		Copies:     1,
	}); err != nil {
		t.Fatalf("PWG: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// A CUPS raster v2 stream carries one "PwgRaster" page-header marker per
	// page, which is the only reliable page counter in the format.
	if got := countPageHeaders(data); got != 2 {
		t.Fatalf("page headers = %d, want 2", got)
	}
}

func TestPWGExpandsCopiesIntoPages(t *testing.T) {
	if _, err := exec.LookPath("gs"); err != nil {
		t.Skip("ghostscript is not installed")
	}
	if _, err := exec.LookPath("pdfunite"); err != nil {
		t.Skip("pdfunite is not installed")
	}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(pdf, buildTestPDF(2), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.pwg")
	if err := PWG(context.Background(), Options{
		PDFPath:    pdf,
		OutputPath: out,
		DPI:        150,
		Paper:      "A4",
		Copies:     3,
	}); err != nil {
		t.Fatalf("PWG: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := countPageHeaders(data); got != 6 {
		t.Fatalf("page headers = %d, want 6 (2 pages x 3 copies)", got)
	}
}

func countPageHeaders(data []byte) int {
	count := 0
	needle := []byte("PwgRaster")
	for i := 0; ; {
		j := indexFrom(data, needle, i)
		if j < 0 {
			return count
		}
		count++
		i = j + 1
	}
}

func indexFrom(data, needle []byte, from int) int {
	if from >= len(data) {
		return -1
	}
	for i := from; i+len(needle) <= len(data); i++ {
		match := true
		for k := range needle {
			if data[i+k] != needle[k] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// buildTestPDF writes a minimal two-or-more page PDF without external tools.
func buildTestPDF(pages int) []byte {
	specs := make([]string, 0, pages+3)
	kids := ""
	for i := 0; i < pages; i++ {
		if i > 0 {
			kids += " "
		}
		kids += itoa(3+i) + " 0 R"
	}
	specs = append(specs, "<< /Type /Catalog /Pages 2 0 R >>")
	specs = append(specs, "<< /Type /Pages /Kids ["+kids+"] /Count "+itoa(pages)+" >>")
	for i := 0; i < pages; i++ {
		specs = append(specs, "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 "+
			itoa(3+pages*2)+" 0 R >> >> /Contents "+itoa(3+pages+i)+" 0 R >>")
	}
	for i := 0; i < pages; i++ {
		stream := "BT /F1 40 Tf 60 720 Td (PAGE " + itoa(i+1) + ") Tj ET\n"
		specs = append(specs, "<< /Length "+itoa(len(stream))+" >>\nstream\n"+stream+"endstream")
	}
	specs = append(specs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	out := []byte("%PDF-1.4\n")
	offsets := make([]int, 0, len(specs))
	for n, body := range specs {
		offsets = append(offsets, len(out))
		out = append(out, []byte(itoa(n+1)+" 0 obj\n"+body+"\nendobj\n")...)
	}
	xref := len(out)
	out = append(out, []byte("xref\n0 "+itoa(len(specs)+1)+"\n0000000000 65535 f \n")...)
	for _, off := range offsets {
		out = append(out, []byte(pad10(off)+" 00000 n \n")...)
	}
	out = append(out, []byte("trailer\n<< /Size "+itoa(len(specs)+1)+" /Root 1 0 R >>\nstartxref\n"+
		itoa(xref)+"\n%%EOF\n")...)
	return out
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func pad10(v int) string {
	s := itoa(v)
	for len(s) < 10 {
		s = "0" + s
	}
	return s
}
