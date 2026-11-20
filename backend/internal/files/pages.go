package files

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// PageSelection parses what a user types in a "pages" box and returns the
// pdfcpu page selection plus how many pages it covers.
//
// Accepted: "1", "1-3", "1,3,5-7", "2-", "-4" and full-width punctuation.
// An empty value means every page. The result is bounded by totalPages so a
// typo like "1-999" cannot make pdfcpu silently pick pages that do not exist.
func PageSelection(raw string, totalPages int) ([]string, int, error) {
	text := strings.TrimSpace(raw)
	text = strings.ReplaceAll(text, "，", ",")
	text = strings.ReplaceAll(text, "－", "-")
	text = strings.ReplaceAll(text, "—", "-")
	text = strings.ReplaceAll(text, " ", "")
	if text == "" {
		if totalPages <= 0 {
			return nil, 0, fmt.Errorf("没有可打印的页面")
		}
		return nil, totalPages, nil
	}

	chosen := make([]bool, totalPages+1)
	count := 0
	for _, part := range strings.Split(text, ",") {
		if part == "" {
			return nil, 0, fmt.Errorf("页码格式不正确，例如 1,3-5")
		}
		lo, hi := part, part
		if i := strings.Index(part, "-"); i >= 0 {
			lo, hi = part[:i], part[i+1:]
		}
		from, err := pageNumber(lo, 1)
		if err != nil {
			return nil, 0, err
		}
		to, err := pageNumber(hi, totalPages)
		if err != nil {
			return nil, 0, err
		}
		if from > to {
			return nil, 0, fmt.Errorf("页码范围 %s 的起始页大于结束页", part)
		}
		if to > totalPages {
			return nil, 0, fmt.Errorf("这份文件只有 %d 页，页码 %d 超出范围", totalPages, to)
		}
		for p := from; p <= to; p++ {
			if !chosen[p] {
				chosen[p] = true
				count++
			}
		}
	}
	// Collapse the runs back into compact pdfcpu selections.
	var sel []string
	for p := 1; p <= totalPages; {
		if !chosen[p] {
			p++
			continue
		}
		start := p
		for p+1 <= totalPages && chosen[p+1] {
			p++
		}
		if start == p {
			sel = append(sel, strconv.Itoa(start))
		} else {
			sel = append(sel, fmt.Sprintf("%d-%d", start, p))
		}
		p++
	}
	return sel, count, nil
}

func pageNumber(v string, fallback int) (int, error) {
	if strings.TrimSpace(v) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 0, fmt.Errorf("页码 %q 不正确", v)
	}
	return n, nil
}

// ExtractPages writes a new PDF that only contains the selected pages. It is
// used when a job prints part of a document, so CUPS and the device only ever
// see the pages the user asked for.
func (p *Pipeline) ExtractPages(ctx context.Context, srcPath, outPath string, selected []string) error {
	if len(selected) == 0 {
		return fmt.Errorf("没有选择要打印的页面")
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return err
	}
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	if err := api.CollectFile(ctx, srcPath, outPath, selected, conf); err != nil {
		return fmt.Errorf("按页码提取 PDF 失败: %w", err)
	}
	return nil
}
