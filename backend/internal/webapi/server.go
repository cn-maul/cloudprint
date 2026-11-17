package webapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudprint/internal/auth"
	"cloudprint/internal/config"
	"cloudprint/internal/files"
	"cloudprint/internal/printer"
	"cloudprint/internal/store"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type Server struct {
	Cfg      config.Config
	Store    *store.Store
	Pipeline *files.Pipeline
	Printer  *printer.Service
	Exec     *printer.Executor
	Limiter  *auth.Limiter
	UI       fs.FS
	Hub      *EventHub
	Log      *slog.Logger
}

type ctxKey string

const (
	ctxUser    ctxKey = "user"
	ctxSession ctxKey = "session"
)

func New(cfg config.Config, st *store.Store, pipeline *files.Pipeline, log *slog.Logger, ui fs.FS) *Server {
	return &Server{
		Cfg:      cfg,
		Store:    st,
		Pipeline: pipeline,
		Printer:  &printer.Service{Store: st},
		Exec:     &printer.Executor{Store: st},
		Limiter:  auth.NewLimiter(),
		UI:       ui,
		Hub:      NewEventHub(),
		Log:      log,
	}
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	if s.Cfg.TrustedProxyHeaders {
		r.Use(middleware.RealIP)
	}
	r.Use(middleware.Recoverer)
	r.Use(s.securityHeaders)
	r.Use(middleware.Timeout(10 * time.Minute))
	r.Use(s.requestLog)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/readyz", s.handleReady)
	r.Get("/downloads/cloudprint-agent.exe", s.handleAgentBinary)
	r.Head("/downloads/cloudprint-agent.exe", s.handleAgentBinary)
	if s.UI != nil {
		if assets, err := fs.Sub(s.UI, "assets"); err == nil {
			r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(http.FS(assets))))
		}
	}

	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/login", s.handleLogin)
		r.Group(func(r chi.Router) {
			r.Use(s.sessionAuth)
			r.Get("/auth/me", s.handleMe)
			r.Get("/auth/csrf", s.handleCSRF)
			r.Post("/auth/logout", s.handleLogout)
			r.Patch("/auth/profile", s.handleUpdateProfile)
			r.Post("/auth/change-password", s.handleChangePassword)
			r.Get("/dashboard", s.handleDashboard)
			r.Get("/printers", s.handlePrintersPublic)
			r.Post("/files", s.handleUpload)
			r.Get("/files/{id}/preview", s.handlePreview)
			r.Get("/files/{id}/download", s.handleDownload)
			r.Post("/jobs", s.handleCreateJob)
			r.Get("/jobs", s.handleListJobs)
			r.Get("/jobs/events", s.handleEvents)
			r.Get("/jobs/{id}", s.handleGetJob)
			r.Post("/jobs/{id}/cancel", s.handleCancelJob)
			r.Group(func(r chi.Router) {
				r.Use(s.adminOnly)
				r.Get("/admin/dashboard", s.adminDashboard)
				r.Get("/admin/settings", s.handleSettings)
				r.Get("/admin/profiles", s.handleProfiles)
				r.Get("/admin/users", s.handleListUsers)
				r.Post("/admin/users", s.handleCreateUser)
				r.Patch("/admin/users/{id}", s.handleUpdateUser)
				r.Delete("/admin/users/{id}", s.handleDeleteUser)
				r.Get("/admin/groups", s.handleListGroups)
				r.Post("/admin/groups", s.handleCreateGroup)
				r.Get("/admin/groups/{id}", s.handleGetGroup)
				r.Patch("/admin/groups/{id}", s.handleUpdateGroup)
				r.Delete("/admin/groups/{id}", s.handleDeleteGroup)
				r.Put("/admin/groups/{id}/members", s.handleSetGroupMembers)
				r.Put("/admin/groups/{id}/printers", s.handleSetGroupPrinters)
				r.Get("/admin/printers", s.handleAdminPrinters)
				r.Post("/admin/printers", s.handleCreatePrinter)
				r.Get("/admin/printers/{id}", s.handleAdminPrinter)
				r.Patch("/admin/printers/{id}", s.handleUpdatePrinter)
				r.Delete("/admin/printers/{id}", s.handleDeletePrinter)
				r.Post("/admin/printers/{id}/endpoints", s.handleCreateEndpoint)
				r.Patch("/admin/endpoints/{id}", s.handleUpdateEndpoint)
				r.Post("/admin/endpoints/{id}/probe", s.handleProbeEndpoint)
				r.Post("/admin/printers/{id}/probe", s.handleProbePrinter)
				r.Get("/admin/candidates", s.handleCandidates)
				r.Post("/admin/candidates", s.handleAddCandidate)
				r.Post("/admin/candidates/{id}/approve", s.handleApproveCandidate)
				r.Post("/admin/candidates/{id}/ignore", s.handleIgnoreCandidate)
				r.Post("/admin/discover", s.handleDiscover)
				r.Post("/admin/printers/quick-add", s.handleQuickAddPrinter)
				r.Get("/admin/discovery-runs", s.handleDiscoveryRuns)
				r.Get("/admin/agents", s.handleAgents)
				r.Post("/admin/agents", s.handleCreateAgent)
				r.Post("/admin/agents/{id}/token", s.handleRotateAgentToken)
				r.Delete("/admin/agents/{id}", s.handleDeleteAgent)
			})
		})
	})

	r.Route("/api/agent", func(r chi.Router) {
		r.Use(s.agentAuth)
		r.Post("/heartbeat", s.handleAgentHeartbeat)
		r.Post("/claim", s.handleAgentClaim)
		r.Post("/jobs/{id}/start", s.handleAgentStart)
		r.Post("/jobs/{id}/result", s.handleAgentResult)
		r.Get("/files/{id}", s.handleAgentFile)
	})

	r.NotFound(s.handleSPA)
	r.Get("/*", s.handleSPA)
	return r
}

func (s *Server) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	exe, err := os.Executable()
	if err != nil {
		http.Error(w, "找不到程序目录", http.StatusInternalServerError)
		return
	}
	path := filepath.Join(filepath.Dir(exe), "cloudprint-agent.exe")
	if _, err := os.Stat(path); err != nil {
		http.Error(w, "服务端目录里没有 cloudprint-agent.exe，请重新构建并一起部署", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="cloudprint-agent.exe"`)
	http.ServeFile(w, r, path)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if !strings.HasPrefix(r.URL.Path, "/assets/") {
			s.Log.Info("http", "method", r.Method, "path", r.URL.Path,
				"status", ww.Status(), "bytes", ww.BytesWritten(),
				"ms", time.Since(start).Milliseconds())
		}
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.Store.DB.PingContext(ctx); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ready",
		"libreoffice":    s.Pipeline.LibreOffice != "",
		"pdf_preview":    s.Pipeline.PDFToPPM != "",
		"office_formats": s.Pipeline.LibreOffice != "",
		"max_upload_mb":  s.Cfg.MaxUploadMB,
	})
}

func (s *Server) sessionAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("cp_session")
		if err != nil || cookie.Value == "" {
			writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "请先登录")
			return
		}
		se, user, err := s.Store.GetSession(r.Context(), cookie.Value)
		if err != nil {
			clearSessionCookie(w)
			writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "会话已过期，请重新登录")
			return
		}
		if isMutating(r.Method) {
			token := r.Header.Get("X-CSRF-Token")
			if token == "" || token != se.CSRF {
				writeErr(w, http.StatusForbidden, "CSRF_INVALID", "请求校验失败，请刷新页面")
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxUser, user)
		ctx = context.WithValue(ctx, ctxSession, se)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil || u.Role != "admin" {
			writeErr(w, http.StatusForbidden, "FORBIDDEN", "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) agentAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			writeErr(w, http.StatusUnauthorized, "AGENT_UNAUTHENTICATED", "代理令牌缺失")
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		a, err := s.Store.GetAgentByToken(r.Context(), auth.HashToken(token))
		if err != nil || !a.IsActive {
			writeErr(w, http.StatusUnauthorized, "AGENT_UNAUTHENTICATED", "代理令牌无效")
			return
		}
		ctx := context.WithValue(r.Context(), ctxUser, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(ctxUser).(*store.User)
	return u
}

func currentAgent(r *http.Request) *store.Agent {
	a, _ := r.Context().Value(ctxUser).(*store.Agent)
	return a
}

func isMutating(m string) bool {
	return m == http.MethodPost || m == http.MethodPut || m == http.MethodPatch || m == http.MethodDelete
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "接口不存在")
		return
	}
	if s.UI == nil {
		http.Error(w, "前端未构建。请先在 web 目录运行 npm run build。", http.StatusServiceUnavailable)
		return
	}
	if strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css") ||
		strings.HasSuffix(r.URL.Path, ".svg") || strings.HasSuffix(r.URL.Path, ".png") ||
		strings.HasSuffix(r.URL.Path, ".ico") || strings.HasSuffix(r.URL.Path, ".woff2") {
		http.NotFound(w, r)
		return
	}
	body, err := fs.ReadFile(s.UI, "index.html")
	if err != nil {
		http.Error(w, "index.html 未找到", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	var e apiError
	e.Error.Code = code
	e.Error.Message = message
	writeJSON(w, status, e)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Header.Get("Content-Type") != "" && !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "CONTENT_TYPE", "请求必须是 JSON")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", "请求内容格式错误")
		return false
	}
	return true
}

func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustedProxyHeaders {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
		if real := r.Header.Get("X-Real-IP"); real != "" {
			return strings.TrimSpace(real)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) jsonError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "对象不存在")
	case errors.Is(err, store.ErrConflict):
		writeErr(w, http.StatusConflict, "CONFLICT", "对象已存在")
	default:
		writeErr(w, http.StatusInternalServerError, "INTERNAL", fmt.Sprintf("服务内部错误: %v", err))
	}
}
