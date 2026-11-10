package server

import (
	"bufio"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
	"time"
)

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": a.version})
	})

	mux.HandleFunc("GET /api/setup/status", a.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", a.handleSetup)
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
	mux.HandleFunc("GET /api/me", a.handleMe)
	mux.HandleFunc("GET /api/config", a.handleUserConfig)

	mux.HandleFunc("GET /api/users", a.handleListUsers)
	mux.HandleFunc("POST /api/users", a.handleCreateUser)
	mux.HandleFunc("PATCH /api/users/{id}", a.handleUpdateUser)
	mux.HandleFunc("DELETE /api/users/{id}", a.handleDeleteUser)
	mux.HandleFunc("GET /api/admin/settings", a.handleGetSettings)
	mux.HandleFunc("PUT /api/admin/settings", a.handlePutSettings)

	mux.HandleFunc("GET /api/shares", a.handleListShares)
	mux.HandleFunc("POST /api/shares", a.handleCreateShare)
	mux.HandleFunc("GET /api/shares/{id}", a.handleGetShare)
	mux.HandleFunc("DELETE /api/shares/{id}", a.handleDeleteShare)
	mux.HandleFunc("POST /api/assets", a.handleUploadAsset)
	mux.HandleFunc("GET /api/assets/{id}/preview", a.handleOwnerAssetPreview)

	mux.HandleFunc("POST /api/public/shares/{publicId}/claim", a.handlePublicClaim)
	mux.HandleFunc("GET /api/public/shares/{publicId}/turnstile", a.handleTurnstileConfig)
	mux.HandleFunc("GET /api/public/claims/{claimId}", a.handleResumeClaim)
	mux.HandleFunc("POST /api/public/claims/{claimId}/start", a.handleStartClaim)
	mux.HandleFunc("GET /api/public/claims/{claimId}/assets/{assetId}", a.handleClaimAsset)
	mux.HandleFunc("GET /api/public/claims/{claimId}/ws", a.handleClaimWebSocket)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "接口不存在")
	})

	mux.HandleFunc("/", a.serveSPA)
	return a.recoverPanic(a.securityHeaders(a.requestLogger(mux)))
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob:; style-src 'self' 'unsafe-inline'; script-src 'self' https://challenges.cloudflare.com; connect-src 'self' ws: wss: https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if requestIsHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (a *App) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if !strings.HasPrefix(r.URL.Path, "/api/public/claims/") || sw.status >= 400 {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, sw.status, time.Since(start).Round(time.Millisecond))
		}
	})
}

func (a *App) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				log.Printf("panic serving %s %s", r.Method, path.Clean(r.URL.Path))
				writeError(w, http.StatusInternalServerError, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *App) serveSPA(w http.ResponseWriter, r *http.Request) {
	clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if clean == "" || clean == "." {
		clean = "index.html"
	}
	if strings.HasPrefix(clean, "api/") || clean == "healthz" {
		writeError(w, http.StatusNotFound, "接口不存在")
		return
	}
	if f, err := a.static.Open(clean); err == nil {
		if stat, statErr := f.Stat(); statErr == nil && !stat.IsDir() {
			defer f.Close()
			if contentType := mime.TypeByExtension(path.Ext(clean)); contentType != "" {
				w.Header().Set("Content-Type", contentType)
			} else if clean == "index.html" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			}
			if strings.Contains(clean, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			_, _ = io.Copy(w, f)
			return
		}
		f.Close()
	}
	if strings.HasPrefix(clean, "assets/") || path.Ext(clean) != "" {
		writeError(w, http.StatusNotFound, "资源不存在")
		return
	}
	index, err := a.static.Open("index.html")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "前端资源尚未构建")
		return
	}
	defer index.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = io.Copy(w, index)
}
