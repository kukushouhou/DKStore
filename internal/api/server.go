// Package api 提供 HTTP API（/api/v1）与前端静态资源服务（实施文档 §10.1 / §10.4）。
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/auth"
	"github.com/kukushouhou/DKStore/internal/checker"
	"github.com/kukushouhou/DKStore/internal/host"
	"github.com/kukushouhou/DKStore/internal/i18n"
	"github.com/kukushouhou/DKStore/internal/ignore"
	"github.com/kukushouhou/DKStore/internal/notify"
	"github.com/kukushouhou/DKStore/internal/registry"
	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/transport"
	"github.com/kukushouhou/DKStore/internal/version"
)

// Deps 汇总 API 层所需的全部业务服务。
type Deps struct {
	Log     *slog.Logger
	Store   *store.Store
	Auth    *auth.Service
	Hosts   *host.Registry
	Reg     *registry.Client
	Ignore  *ignore.Service
	Notify  *notify.Service
	Checker *checker.Service
	Assets  fs.FS
}

// Server 组装 HTTP 路由。
type Server struct {
	d Deps
}

// New 创建 API 服务。
func New(d Deps) *Server {
	return &Server{d: d}
}

// Handler 返回根 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 无需登录的公开端点
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	// 需要认证的业务端点
	mux.HandleFunc("POST /api/v1/auth/password", s.requireAuth(s.handleSetPassword))
	mux.HandleFunc("GET /api/v1/overview", s.requireAuth(s.handleOverview))
	mux.HandleFunc("GET /api/v1/apps", s.requireAuth(s.handleGetApp))
	mux.HandleFunc("POST /api/v1/check", s.requireAuth(s.handleTriggerCheck))
	mux.HandleFunc("GET /api/v1/check/current", s.requireAuth(s.handleCurrentCheck))
	mux.HandleFunc("GET /api/v1/events", s.requireAuth(s.handleEvents))
	mux.HandleFunc("GET /api/v1/check-runs", s.requireAuth(s.handleCheckRuns))

	mux.HandleFunc("POST /api/v1/ignore", s.requireAuth(s.handleSetIgnore))
	mux.HandleFunc("DELETE /api/v1/ignore", s.requireAuth(s.handleDeleteIgnore))

	mux.HandleFunc("GET /api/v1/hosts", s.requireAuth(s.handleListHosts))
	mux.HandleFunc("POST /api/v1/hosts", s.requireAuth(s.handleCreateHost))
	mux.HandleFunc("PUT /api/v1/hosts/{id}", s.requireAuth(s.handleUpdateHost))
	mux.HandleFunc("DELETE /api/v1/hosts/{id}", s.requireAuth(s.handleDeleteHost))
	mux.HandleFunc("POST /api/v1/hosts/test", s.requireAuth(s.handleTestHost))
	mux.HandleFunc("POST /api/v1/hosts/keygen", s.requireAuth(s.handleKeygen))
	mux.HandleFunc("POST /api/v1/hosts/{id}/trust-hostkey", s.requireAuth(s.handleTrustHostKey))

	mux.HandleFunc("GET /api/v1/settings", s.requireAuth(s.handleGetSettings))
	mux.HandleFunc("PUT /api/v1/settings", s.requireAuth(s.handleSaveSettings))
	mux.HandleFunc("POST /api/v1/notify/test", s.requireAuth(s.handleTestNotify))

	mux.HandleFunc("GET /api/v1/registry-credentials", s.requireAuth(s.handleListRegCreds))
	mux.HandleFunc("POST /api/v1/registry-credentials", s.requireAuth(s.handleSaveRegCred))
	mux.HandleFunc("DELETE /api/v1/registry-credentials", s.requireAuth(s.handleDeleteRegCred))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		lang := s.langOf(r)
		writeError(w, lang, http.StatusNotFound, "not_found", "")
	})
	mux.Handle("/", s.staticHandler())
	return s.logMiddleware(mux)
}

func (s *Server) langOf(r *http.Request) i18n.Lang {
	cfg := s.d.Notify.LoadConfig(r.Context())
	return i18n.FromRequest(r, i18n.Normalize(cfg.Locale))
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.d.Auth.IsAuthenticated(r.Context(), r) {
			writeError(w, s.langOf(r), http.StatusUnauthorized, "unauthorized", "")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	schema, err := s.d.Store.SchemaVersion(r.Context())
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"version":       version.Version,
		"commit":        version.Commit,
		"schemaVersion": schema,
		"time":          time.Now().Unix(),
	})
}

func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	pwEnabled, authed := s.d.Auth.Status(r.Context(), r)
	writeJSON(w, http.StatusOK, map[string]any{
		"passwordEnabled": pwEnabled,
		"authenticated":   authed,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.d.Auth.Login(r.Context(), w, body.Password); err != nil {
		writeError(w, s.langOf(r), http.StatusUnauthorized, "invalid_credentials", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.d.Auth.Logout(w, r)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.d.Auth.SetPassword(r.Context(), w, body.OldPassword, body.NewPassword); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "invalid_credentials", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	hostID := strings.TrimSpace(r.URL.Query().Get("hostId"))
	ov, err := s.d.Checker.Overview(r.Context(), hostID, s.langOf(r))
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ov)
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", "missing key")
		return
	}
	card, err := s.d.Checker.GetApp(r.Context(), key, s.langOf(r))
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if card == nil {
		writeError(w, s.langOf(r), http.StatusNotFound, "not_found", "")
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Server) handleTriggerCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		HostID      string `json:"hostId"`
		BypassCache bool   `json:"bypassCache"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	p := s.d.Checker.Trigger("manual", body.HostID, body.BypassCache)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCurrentCheck(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Checker.CurrentProgress())
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "sse_unsupported", "SSE not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.d.Checker.Subscribe()
	defer unsub()

	keepAlive := time.NewTicker(20 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(p)
			_, _ = fmt.Fprintf(w, "event: progress\ndata: %s\n\n", string(b))
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = fmt.Fprintf(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) handleCheckRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.d.Checker.ListCheckRuns(r.Context(), 30)
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs})
}

func (s *Server) handleSetIgnore(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServiceKeys []string `json:"serviceKeys"`
		Mode        string   `json:"mode"`
		Note        string   `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.d.Ignore.Set(r.Context(), body.ServiceKeys, body.Mode, body.Note); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteIgnore(w http.ResponseWriter, r *http.Request) {
	svcKey := strings.TrimSpace(r.URL.Query().Get("serviceKey"))
	appKey := strings.TrimSpace(r.URL.Query().Get("appKey"))
	var err error
	if svcKey != "" {
		err = s.d.Ignore.Remove(r.Context(), svcKey)
	} else if appKey != "" {
		err = s.d.Ignore.RemoveByApp(r.Context(), appKey)
	} else {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", "missing serviceKey or appKey")
		return
	}
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Hosts.List(r.Context())
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleCreateHost(w http.ResponseWriter, r *http.Request) {
	var in host.CreateOrUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	h, err := s.d.Hosts.Create(r.Context(), in)
	if err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// 新增设备后自动触发对该设备的检查
	s.d.Checker.Trigger("manual", h.ID, false)
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleUpdateHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in host.CreateOrUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	h, err := s.d.Hosts.Update(r.Context(), id, in)
	if err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.d.Hosts.Delete(r.Context(), id); err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTestHost(w http.ResponseWriter, r *http.Request) {
	var in host.CreateOrUpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	res, err := s.d.Hosts.TestConnection(r.Context(), in, s.langOf(r))
	if err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleKeygen(w http.ResponseWriter, r *http.Request) {
	priv, pub, err := transport.GenerateEd25519KeyPair("dkstore")
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "keygen_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"privateKey":    priv,
		"authorizedKey": pub,
	})
}

func (s *Server) handleTrustHostKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		HostKey string `json:"hostKey"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.d.Hosts.TrustHostKey(r.Context(), id, body.HostKey); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.d.Notify.LoadConfig(r.Context())
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var cfg notify.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.d.Notify.SaveConfig(r.Context(), cfg); err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.d.Notify.LoadConfig(r.Context()))
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	var cfg notify.Config
	_ = json.NewDecoder(r.Body).Decode(&cfg)
	if err := s.d.Notify.SendTest(r.Context(), &cfg, s.langOf(r)); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListRegCreds(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Reg.ListCredentials(r.Context())
	if err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleSaveRegCred(w http.ResponseWriter, r *http.Request) {
	var in registry.Creds
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := s.d.Reg.SaveCredential(r.Context(), in); err != nil {
		writeError(w, s.langOf(r), http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDeleteRegCred(w http.ResponseWriter, r *http.Request) {
	reg := strings.TrimSpace(r.URL.Query().Get("registry"))
	if err := s.d.Reg.DeleteCredential(r.Context(), reg); err != nil {
		writeError(w, s.langOf(r), http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) staticHandler() http.Handler {
	if s.d.Assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>DKStore</title>
<p>Frontend not built. Run <code>cd web &amp;&amp; npm run dev</code> or <code>npm run build</code>.</p>`))
		})
	}
	fileServer := http.FileServerFS(s.d.Assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(s.d.Assets, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.d.Assets, "index.html")
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") && s.d.Log != nil {
			s.d.Log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, lang i18n.Lang, status int, code, fallback string) {
	msg := i18n.APIErrorMessage(lang, code, fallback)
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
