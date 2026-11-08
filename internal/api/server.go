// Package api 提供 HTTP API（/api/v1）与前端静态资源服务。
package api

import (
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kukushouhou/DKStore/internal/store"
	"github.com/kukushouhou/DKStore/internal/version"
)

// Server 组装路由。
type Server struct {
	log    *slog.Logger
	store  *store.Store
	assets fs.FS // nil 表示未内嵌前端（开发模式或未构建）
}

// New 创建 API 服务。
func New(log *slog.Logger, st *store.Store, assets fs.FS) *Server {
	return &Server{log: log, store: st, assets: assets}
}

// Handler 返回根 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在")
	})
	mux.Handle("/", s.staticHandler())
	return s.logMiddleware(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	schema, err := s.store.SchemaVersion(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", "数据库不可用")
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

// staticHandler 提供 SPA：存在的文件直接返回，其余路径回落到 index.html。
func (s *Server) staticHandler() http.Handler {
	if s.assets == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><title>DKStore</title>
<p>前端尚未构建。开发时请运行 <code>cd web &amp;&amp; npm run dev</code>，或执行 <code>npm run build</code> 后重新编译。</p>`))
		})
	}
	fileServer := http.FileServerFS(s.assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(s.assets, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.assets, "index.html")
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

// Flush 透传，保证后续 SSE 可用。
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
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 统一错误格式：{"error":{"code":"...","message":"中文说明"}}。
func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
