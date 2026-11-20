package webapi

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.UploadBytes()+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "UPLOAD_TOO_LARGE",
			"文件过大或上传格式错误，单文件上限 "+strconv.FormatInt(s.Cfg.MaxUploadMB, 10)+" MB")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "FILE_REQUIRED", "请选择要上传的文件")
		return
	}
	defer file.Close()
	if hdr.Size > s.Cfg.UploadBytes() {
		writeErr(w, http.StatusRequestEntityTooLarge, "UPLOAD_TOO_LARGE",
			"文件超过 "+strconv.FormatInt(s.Cfg.MaxUploadMB, 10)+" MB 上限")
		return
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(file, head)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		writeErr(w, http.StatusBadRequest, "READ_FAILED", "读取文件失败")
		return
	}
	head = head[:n]
	originalName := sanitizeFilename(hdr.Filename)
	mime, err := s.Pipeline.DetectMIME(originalName, head)
	if err != nil {
		writeErr(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_FILE", err.Error())
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		writeErr(w, http.StatusBadRequest, "READ_FAILED", "读取文件失败")
		return
	}
	path, sha, size, err := s.Pipeline.SaveUpload(r.Context(), file, originalName, mime)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	prepared, err := s.Pipeline.Prepare(r.Context(), path, originalName, mime)
	if err != nil {
		_ = os.Remove(path)
		writeErr(w, http.StatusUnprocessableEntity, "CONVERT_FAILED", err.Error())
		return
	}
	u := currentUser(r)
	orig, err := s.Store.CreateFile(r.Context(), u.ID, "original", originalName, mime, sha,
		path, size, prepared.PageCount, s.Cfg.RetentionDays)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	pdf, err := s.Store.CreateFile(r.Context(), u.ID, "pdf", originalName, "application/pdf",
		prepared.SHA256, prepared.PDFPath, prepared.SizeBytes, prepared.PageCount, s.Cfg.RetentionDays)
	if err != nil {
		_ = s.Store.DeleteFile(r.Context(), orig.ID)
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "upload", "file", pdf.ID, map[string]any{
		"name": originalName, "size": size, "pages": prepared.PageCount, "sha256": sha,
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":          pdf.ID,
		"file_id":     pdf.ID,
		"name":        originalName,
		"mime":        mime,
		"size_bytes":  size,
		"page_count":  prepared.PageCount,
		"converted":   prepared.Converted,
		"preview_url": "/api/files/" + pdf.ID + "/preview",
	})
}

func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	f, ok := s.authorizedFile(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if f.Kind != "pdf" {
		writeErr(w, http.StatusNotFound, "NO_PREVIEW", "该文件没有预览")
		return
	}
	// "page" lets the detail page flip through the document. The response is
	// still one page per request so a 200-page PDF does not render 200 PNGs
	// before the first image can be shown.
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > f.PageCount {
			writeErr(w, http.StatusBadRequest, "PAGE_INVALID", "页码超出文件范围")
			return
		}
		page = n
	}
	img, err := s.Pipeline.Preview(r.Context(), f.StoragePath, f.ID, page)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "PREVIEW_UNAVAILABLE", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, img)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	f, ok := s.authorizedFile(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.OriginalName))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, f.StoragePath)
	s.Store.Audit(r.Context(), currentUser(r).ID, "download", "file", f.ID, nil)
}

func (s *Server) authorizedFile(w http.ResponseWriter, r *http.Request, id string) (*store.StoredFile, bool) {
	f, err := s.Store.GetFile(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "FILE_NOT_FOUND", "文件不存在或已过期")
		return nil, false
	}
	if f.StoragePath == "" {
		writeErr(w, http.StatusGone, "FILE_EXPIRED", "文件已超过保留期并被清理")
		return nil, false
	}
	u := currentUser(r)
	if f.OwnerID != u.ID && u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "FILE_FORBIDDEN", "无权访问该文件")
		return nil, false
	}
	return f, true
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\\|?*`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "document"
	}
	runes := []rune(name)
	if len(runes) > 180 {
		ext := filepath.Ext(name)
		base := []rune(strings.TrimSuffix(name, ext))
		name = string(base[:170]) + ext
	}
	return name
}
