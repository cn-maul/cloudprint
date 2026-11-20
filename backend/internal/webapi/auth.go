package webapi

import (
	"net/http"
	"strings"
	"time"

	"cloudprint/internal/auth"
	"cloudprint/internal/store"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	key := s.clientIP(r) + "|" + strings.ToLower(in.Username)
	if !s.Limiter.Allowed(key) {
		writeErr(w, http.StatusTooManyRequests, "LOGIN_LOCKED", "尝试次数过多，请 15 分钟后再试")
		return
	}
	u, err := s.Store.GetUserByUsername(r.Context(), in.Username)
	if err != nil || !auth.VerifyPassword(in.Password, u.PasswordHash) || !u.IsActive {
		s.Limiter.Fail(key)
		writeErr(w, http.StatusUnauthorized, "BAD_CREDENTIALS", "用户名或密码错误")
		return
	}
	s.Limiter.Reset(key)
	sessID := auth.RandomToken(32)
	csrf := auth.RandomToken(32)
	if err := s.Store.CreateSession(r.Context(), u.ID, sessID, csrf, s.Cfg.SessionHours,
		r.UserAgent(), s.clientIP(r)); err != nil {
		s.jsonError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "cp_session",
		Value:    sessID,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.Cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   s.Cfg.SessionHours * 3600,
	})
	s.Store.Audit(r.Context(), u.ID, "login", "user", u.ID, map[string]string{"ip": s.clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"user": u, "csrf_token": csrf})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	se, _ := r.Context().Value(ctxSession).(*store.Session)
	csrf := ""
	if se != nil {
		csrf = se.CSRF
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user": u, "csrf_token": csrf, "anonymous": s.Cfg.AnonymousMode,
	})
}

func (s *Server) handleCSRF(w http.ResponseWriter, r *http.Request) {
	se, _ := r.Context().Value(ctxSession).(*store.Session)
	if se == nil {
		writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "会话无效")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrf_token": se.CSRF})
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName string `json:"display_name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	if err := s.Store.UpdateDisplayName(r.Context(), u.ID, in.DisplayName); err != nil {
		writeErr(w, http.StatusBadRequest, "PROFILE_INVALID", err.Error())
		return
	}
	updated, err := s.Store.GetUser(r.Context(), u.ID)
	if err != nil {
		s.jsonError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "update_profile", "user", u.ID, nil)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("cp_session"); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	u := currentUser(r)
	if !auth.VerifyPassword(in.Current, u.PasswordHash) {
		writeErr(w, http.StatusBadRequest, "BAD_PASSWORD", "当前密码不正确")
		return
	}
	hash, err := auth.HashPassword(in.New)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "WEAK_PASSWORD", err.Error())
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, hash, false); err != nil {
		s.jsonError(w, err)
		return
	}
	if c, err := r.Cookie("cp_session"); err == nil {
		_ = s.Store.DeleteSession(r.Context(), c.Value)
	}
	clearSessionCookie(w)
	s.Store.Audit(r.Context(), u.ID, "change_password", "user", u.ID, nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "cp_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}
