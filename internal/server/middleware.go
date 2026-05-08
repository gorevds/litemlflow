package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/litemlflow/litemlflow/internal/config"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyUser
)

// requestIDMiddleware attaches a short request id to context and response.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = shortHash(time.Now().UnixNano())
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loggingMiddleware emits a one-line slog record per request.
func loggingMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)
			dur := time.Since(start)
			id, _ := r.Context().Value(ctxKeyRequestID).(string)
			logger.LogAttrs(r.Context(), levelFor(ww.status),
				"http",
				slog.String("id", id),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.status),
				slog.Int64("bytes", ww.bytesWritten),
				slog.Duration("dur", dur),
			)
		})
	}
}

func levelFor(status int) slog.Level {
	switch {
	case status >= 500:
		return slog.LevelError
	case status >= 400:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

type statusWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.status = code
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	return n, err
}

// recoveryMiddleware catches panics and returns 500 without leaking stack.
func recoveryMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					id, _ := r.Context().Value(ctxKeyRequestID).(string)
					logger.LogAttrs(r.Context(), slog.LevelError, "panic",
						slog.String("id", id), slog.Any("err", rec))
					writeError(w, http.StatusInternalServerError, CodeInternalError, "internal error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// bodyLimitMiddleware caps request body size with http.MaxBytesReader.
func bodyLimitMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && maxBytes > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authMiddleware enforces config.Auth. "none" is a no-op; "basic" requires
// HTTP basic auth with the configured user/pass; "oidc" returns 501 in v0.1
// (placeholder; full OIDC lands in v0.2).
func authMiddleware(cfg config.Config) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always strip the user-identity header from inbound requests
			// so a client cannot smuggle it past auth.
			r.Header.Del("X-LiteMLflow-User")
			// Always allow operational endpoints.
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			switch cfg.Auth {
			case "none":
				ctx := context.WithValue(r.Context(), ctxKeyUser, "anonymous")
				r.Header.Set("X-LiteMLflow-User", "anonymous")
				next.ServeHTTP(w, r.WithContext(ctx))
			case "basic":
				user, pass, ok := r.BasicAuth()
				if !ok {
					w.Header().Set("WWW-Authenticate", `Basic realm="LiteMLflow"`)
					writeError(w, http.StatusUnauthorized, CodeUnauthenticated, "basic authentication required")
					return
				}
				if !verifyBasic(cfg, user, pass) {
					writeError(w, http.StatusUnauthorized, CodeUnauthenticated, "invalid credentials")
					return
				}
				ctx := context.WithValue(r.Context(), ctxKeyUser, user)
				r.Header.Set("X-LiteMLflow-User", user)
				next.ServeHTTP(w, r.WithContext(ctx))
			case "oidc":
				writeError(w, http.StatusNotImplemented, CodeNotImplemented, "OIDC is planned for v0.2")
			default:
				writeError(w, http.StatusInternalServerError, CodeInternalError, "unknown auth mode")
			}
		})
	}
}

func isPublicPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/version":
		return true
	}
	if strings.HasPrefix(p, "/ui/") || p == "/ui" || p == "/" {
		return true
	}
	return false
}

// verifyBasic compares user/pass against the configured user and password
// hash (hex-encoded SHA-256). subtle.ConstantTimeCompare avoids leaking
// password length via timing.
func verifyBasic(cfg config.Config, user, pass string) bool {
	if subtle.ConstantTimeCompare([]byte(user), []byte(cfg.BasicUser)) != 1 {
		return false
	}
	got := sha256.Sum256([]byte(pass))
	want, err := hex.DecodeString(cfg.BasicPassHash)
	if err != nil || len(want) != len(got) {
		return false
	}
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

func shortHash(seed int64) string {
	h := sha256.New()
	h.Write([]byte{byte(seed), byte(seed >> 8), byte(seed >> 16), byte(seed >> 24),
		byte(seed >> 32), byte(seed >> 40), byte(seed >> 48), byte(seed >> 56)})
	return hex.EncodeToString(h.Sum(nil))[:12]
}
