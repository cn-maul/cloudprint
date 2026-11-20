package webapi

import (
	"net/http"
	"strings"

	"cloudprint/internal/auth"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
)

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"public_url":     s.Cfg.PublicURL,
		"max_upload_mb":  s.Cfg.MaxUploadMB,
		"enable_mdns":    s.Cfg.EnableMDNS,
		"scan_cidrs":     s.Cfg.ScanCIDRs,
		"pdf_preview":    s.Pipeline.PDFToPPM != "",
		"retention_days": s.Cfg.RetentionDays,
		"daily_quota":    s.Cfg.DefaultDailyQuota,
	})
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	d, err := s.Store.Dashboard(r.Context(), u.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	d.MaxUploadMB = s.Cfg.MaxUploadMB
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	d, err := s.Store.Dashboard(r.Context(), u.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	d.MaxUploadMB = s.Cfg.MaxUploadMB
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.jsonError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		Quota       int    `json:"daily_page_quota"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	if in.Role == "" {
		in.Role = "user"
	}
	if in.Quota == 0 {
		in.Quota = s.Cfg.DefaultDailyQuota
	}
	u, err := s.Store.CreateUser(r.Context(), in.Username, in.DisplayName, hash, in.Role, in.Quota)
	if err != nil {
		if err == store.ErrConflict {
			writeErr(w, http.StatusConflict, "USER_EXISTS", "用户名已存在")
			return
		}
		writeErr(w, http.StatusBadRequest, "USER_INVALID", err.Error())
		return
	}
	s.Store.Audit(r.Context(), currentUser(r).ID, "create_user", "user", u.ID, map[string]string{"username": u.Username, "role": u.Role})
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Role        *string `json:"role"`
		Quota       *int    `json:"daily_page_quota"`
		IsActive    *bool   `json:"is_active"`
		Password    string  `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	if in.DisplayName != nil && strings.TrimSpace(*in.DisplayName) == "" {
		writeErr(w, http.StatusBadRequest, "NAME_REQUIRED", "显示名不能为空")
		return
	}
	if err := s.Store.UpdateUser(r.Context(), id, in.DisplayName, in.Role, in.Quota, in.IsActive); err != nil {
		writeErr(w, http.StatusBadRequest, "USER_INVALID", err.Error())
		return
	}
	if in.Password != "" {
		hash, err := auth.HashPassword(in.Password)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
			return
		}
		if err := s.Store.SetPassword(r.Context(), id, hash, true); err != nil {
			s.jsonError(w, err)
			return
		}
	}
	// Audit which fields changed, never their values: the payload contains a
	// password and would otherwise be stored in plain text.
	s.Store.Audit(r.Context(), currentUser(r).ID, "update_user", "user", id, map[string]any{
		"changed": changedUserFields(in.DisplayName, in.Role, in.Quota, in.IsActive, in.Password),
	})
	u, _ := s.Store.GetUser(r.Context(), id)
	writeJSON(w, http.StatusOK, u)
}

func changedUserFields(displayName, role *string, quota *int, active *bool, password string) []string {
	var out []string
	if displayName != nil {
		out = append(out, "display_name")
	}
	if role != nil {
		out = append(out, "role")
	}
	if quota != nil {
		out = append(out, "daily_page_quota")
	}
	if active != nil {
		out = append(out, "is_active")
	}
	if password != "" {
		out = append(out, "password")
	}
	return out
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	u := currentUser(r)
	if u.ID == id {
		writeErr(w, http.StatusBadRequest, "SELF_DELETE", "不能删除当前登录的账号")
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "USER_NOT_FOUND", "账号不存在")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, "USER_DELETE_FAILED", err.Error())
		return
	}
	s.Store.Audit(r.Context(), u.ID, "delete_user", "user", id, map[string]string{"username": target.Username})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
