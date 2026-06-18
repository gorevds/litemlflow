package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/auth"
	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/metrics"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyUser
	// AUTH-OIDC: session carried in context so handlers can access it.
	ctxKeySession
	// TENANCY: workspace id carried in context for downstream scoping.
	ctxKeyWorkspace
	// RBAC: resolved role for the current user in the current workspace.
	ctxKeyRole
)

// apiV2AliasMiddleware rewrites /api/v2/... → /api/v1/... before the
// router matches. This lets v2 clients use the explicit LTS namespace
// without forcing handlers to register every route twice. See ADR 0003.
//
// The rewrite happens BEFORE logging/auth so downstream handlers, audit
// logs, and metrics all see the canonical v1 path. The `X-API-Version`
// response header records that the request entered through v2. Query
// string is preserved unchanged.
//
// Percent-encoded segments: we rewrite EscapedPath() (raw, still encoded)
// so a path like /api/v2/prompts/my%2Fname does NOT silently route to
// /api/v1/prompts/my/name. Without this, the slice would operate on the
// already-decoded r.URL.Path and an encoded forward-slash would split
// into two path segments — a different prompt name than the v1 caller
// would see (independent-review H6 for v2.0-rc1).
func apiV2AliasMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esc := r.URL.EscapedPath()
		if strings.HasPrefix(esc, "/api/v2/") {
			newEsc := "/api/v1/" + esc[len("/api/v2/"):]
			r.URL.RawPath = newEsc
			if decoded, err := url.PathUnescape(newEsc); err == nil {
				r.URL.Path = decoded
			} else {
				// Malformed encoding — surface as 400 rather than silently
				// passing through with a corrupt path.
				http.Error(w, "invalid percent-encoding in path", http.StatusBadRequest)
				return
			}
			w.Header().Set("X-API-Version", "2")
		}
		next.ServeHTTP(w, r)
	})
}

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

// securityHeadersMiddleware sets baseline security response headers
// (independent-review: none were set). HSTS is emitted only for requests that
// arrived over TLS (directly or via a trusted proxy) so local plaintext dev is
// not pinned to HTTPS.
//
// The CSP keeps 'unsafe-inline' for scripts and styles because the bundled UI
// uses inline onclick handlers and inline style attributes; it still restricts
// every resource origin to 'self' and forbids framing (frame-ancestors 'none').
// Removing 'unsafe-inline' requires refactoring the UI and is a follow-up.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; " +
		"style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", csp)
		if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
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
			// Dataset uploads (POST /api/v1/datasets/{name}/versions) and
			// artifact uploads stream gigabytes; the dataset handler installs
			// its own MaxBytesReader. The MLflow artifact subrouter does the
			// same. Skip the global limit for those paths.
			if isLargeUploadPath(r.Method, r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body != nil && maxBytes > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isLargeUploadPath returns true for routes that legitimately stream more
// than the global body cap. The list is small and explicit so a typo in a
// route can't accidentally lift the cap for everything.
func isLargeUploadPath(method, path string) bool {
	if method != http.MethodPost && method != http.MethodPut {
		return false
	}
	// Dataset version upload.
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/api/v1/datasets/") &&
		strings.HasSuffix(path, "/versions") {
		return true
	}
	// MLflow artifact upload (existing behaviour, made explicit).
	if strings.HasPrefix(path, "/api/2.0/mlflow-artifacts/artifacts") {
		return true
	}
	return false
}

// SessionLookup is the interface authMiddleware uses to validate session cookies.
// AUTH-OIDC: defined here so server.go can wire *store.SQLiteStore without a
// full import of the native package's SessionStore alias.
type SessionLookup interface {
	GetSession(ctx context.Context, id string) (*model.Session, error)
	TouchSession(ctx context.Context, id string, lastSeen int64) error
}

// authMiddleware enforces config.Auth.
//
// AUTH-OIDC: Session cookie support has been added. Regardless of cfg.Auth,
// a valid session cookie is always accepted — this lets users who logged in via
// basic auth continue using their session without re-sending credentials on
// every request. Auth order:
//
//  1. Strip inbound X-LiteMLflow-User (anti-smuggling).
//  2. If a valid session cookie is present, use the session identity.
//  3. Else if cfg.Auth=="basic", require HTTP Basic credentials.
//  4. Else if cfg.Auth=="oidc" and the request accepts HTML, redirect to IdP start.
//  5. Else if cfg.Auth=="none", user = "anonymous".
func authMiddlewareWithSessions(cfg config.Config, sessions SessionLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Strip identity header so clients cannot smuggle it.
			r.Header.Del("X-LiteMLflow-User")
			r.Header.Del("X-LiteMLflow-Auth-Method")

			// Public paths bypass auth entirely.
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			// 2. Session cookie — accepted regardless of cfg.Auth mode.
			if sessions != nil {
				if sessID, err := auth.GetSessionID(r); err == nil {
					if sess, err := sessions.GetSession(r.Context(), sessID); err == nil {
						// Touch last_seen asynchronously. We deliberately
						// don't piggy-back on r.Context(): the request
						// may be returning right now, and we still want
						// the touch to complete. A 5s ceiling prevents
						// a wedged DB from leaking goroutines.
						go func() {
							//nolint:contextcheck // intentional detached ctx: outlive request, see comment above
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer cancel()
							_ = sessions.TouchSession(ctx, sessID, time.Now().UnixMilli())
						}()
						ctx := context.WithValue(r.Context(), ctxKeyUser, sess.UserID)
						ctx = context.WithValue(ctx, ctxKeySession, sess)
						r.Header.Set("X-LiteMLflow-User", sess.UserID)
						r.Header.Set("X-LiteMLflow-Auth-Method", sess.AuthMethod)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			// 3-5. No valid session; fall back to cfg.Auth mode.
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
				r.Header.Set("X-LiteMLflow-Auth-Method", "basic")
				next.ServeHTTP(w, r.WithContext(ctx))

			case "oidc":
				// If the client accepts HTML (browser), redirect to OIDC start.
				// Otherwise return 401 so API clients get a machine-readable error.
				if strings.Contains(r.Header.Get("Accept"), "text/html") {
					http.Redirect(w, r, "/api/v1/auth/oidc/start?return_to="+r.URL.RequestURI(), http.StatusFound)
					return
				}
				writeError(w, http.StatusUnauthorized, CodeUnauthenticated, "OIDC authentication required; visit /api/v1/auth/oidc/start")

			default:
				writeError(w, http.StatusInternalServerError, CodeInternalError, "unknown auth mode")
			}
		})
	}
}

// authMiddleware is the original single-argument version for callers that don't
// have a session store (tests, etc.). It delegates to authMiddlewareWithSessions
// with a nil store, which skips cookie checks.
func authMiddleware(cfg config.Config) func(http.Handler) http.Handler {
	return authMiddlewareWithSessions(cfg, nil)
}

func isPublicPath(p string) bool {
	switch p {
	case "/healthz", "/readyz", "/version", "/metrics":
		return true
	}
	if strings.HasPrefix(p, "/ui/") || p == "/ui" || p == "/" {
		return true
	}
	// AUTH-OIDC: the login, logout, and OIDC redirect endpoints are public
	// because they are the entry points for unauthenticated users. Whoami is
	// NOT public — it reports the resolved identity, so the middleware must run.
	switch p {
	case "/api/v1/auth/login", "/api/v1/auth/logout",
		"/api/v1/auth/oidc/start", "/api/v1/auth/oidc/callback":
		return true
	}
	// FEDERATION (v1.3): peer-callable endpoints validate themselves via
	// the X-LiteMLflow-Federate-Sig HMAC header inside their handler.
	// Skipping the session-auth middleware here is intentional — the
	// HMAC IS the credential. RBAC also bypasses these by virtue of
	// the no-role mapping in rbac.go.
	switch p {
	case "/api/v1/federate/echo", "/api/v1/federate/search":
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

// workspaceMiddleware resolves the current workspace from the request and
// injects it into the context. Resolution order:
//  1. X-Workspace HTTP header
//  2. lmf_workspace cookie
//  3. "default" fallback
//
// If the requested workspace is unknown, a 400 is returned. This middleware
// must run after authMiddleware.
//
// The resolved workspace id is also set as the X-LiteMLflow-Workspace request
// header so downstream handlers that cannot import this package can read it
// without needing access to the unexported ctxKeyWorkspace.
func workspaceMiddleware(st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wsID := r.Header.Get("X-Workspace")
			if wsID == "" {
				if c, err := r.Cookie("lmf_workspace"); err == nil {
					wsID = c.Value
				}
			}
			if wsID == "" {
				wsID = "default"
			}
			// Validate the workspace exists to prevent spoofing arbitrary IDs.
			if wsID != "default" {
				if _, err := st.GetWorkspace(r.Context(), wsID); err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{
						"error_code": "INVALID_PARAMETER_VALUE",
						"message":    "unknown workspace: " + wsID,
					})
					return
				}
			}
			ctx := context.WithValue(r.Context(), ctxKeyWorkspace, wsID)
			// Set header so downstream handlers in other packages can read it
			// without importing the server package (avoids circular deps).
			r.Header.Set("X-LiteMLflow-Workspace", wsID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CurrentWorkspace extracts the current workspace ID from the request context.
// Falls back to "default" if not set (e.g., in tests that bypass the middleware).
func CurrentWorkspace(r *http.Request) string {
	if ws, ok := r.Context().Value(ctxKeyWorkspace).(string); ok && ws != "" {
		return ws
	}
	return "default"
}

// rbacMiddleware enforces role-based access control after workspaceMiddleware.
//
// Open-mode rules (pass-through with no role gate):
//  1. cfg.Auth == "none": single-user mode, RBAC inactive.
//  2. Workspace is "default" AND it has zero configured members: fresh-install
//     open mode — preserves backward compat for MLflow clients and solo users.
//
// For all other requests:
//   - The user's role in the workspace is resolved via store.GetMemberRole.
//   - If the user is not a member: 403 Forbidden.
//   - The resolved role is stashed in ctxKeyRole and forwarded as the
//     X-LiteMLflow-Role request header so downstream handlers can read it
//     without importing the server package (avoids circular deps).
//   - requiredRole (see rbac.go) maps (method, path) to a minimum role;
//     if the user's role is insufficient: 403 Forbidden.
func rbacMiddleware(cfg config.Config, st store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. auth=none → RBAC inactive.
			if cfg.Auth == "none" {
				next.ServeHTTP(w, r)
				return
			}

			ws, _ := r.Context().Value(ctxKeyWorkspace).(string)
			if ws == "" {
				ws = "default"
			}

			// 2. default workspace with zero members → open mode.
			if ws == "default" {
				members, err := st.ListMembers(r.Context(), ws)
				if err != nil || len(members) == 0 {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Resolve user identity.
			user, _ := r.Context().Value(ctxKeyUser).(string)
			if user == "" {
				user = r.Header.Get("X-LiteMLflow-User")
			}

			// Look up membership.
			role, err := st.GetMemberRole(r.Context(), ws, user)
			if err != nil {
				writeError(w, http.StatusForbidden, "PERMISSION_DENIED",
					"you are not a member of workspace "+ws)
				return
			}

			// Stash role in context and header.
			ctx := context.WithValue(r.Context(), ctxKeyRole, role)
			r = r.WithContext(ctx)
			r.Header.Set("X-LiteMLflow-Role", role)

			// Check whether the route requires a higher role.
			required := requiredRole(r.Method, r.URL.Path)
			if required != "" && !roleAtLeast(role, required) {
				writeError(w, http.StatusForbidden, "PERMISSION_DENIED",
					"role "+role+" cannot perform this operation (requires "+required+")")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// roleAtLeast returns true if actual satisfies the minimum required role.
// Role hierarchy: admin > editor > viewer.
func roleAtLeast(actual, required string) bool {
	rank := map[string]int{"viewer": 1, "editor": 2, "admin": 3}
	return rank[actual] >= rank[required]
}

// metricsMiddleware records HTTP request counts and latency into the provided
// Standard metrics set.
//
// Path normalization: after the handler runs, chi.RouteContext holds the
// matched route pattern (e.g. "/api/v1/prompts/{name}"). Using that instead
// of r.URL.Path prevents cardinality explosion from run-IDs, experiment-IDs,
// or any other path variables. If no route was matched (e.g. 404) we fall
// back to the literal path, but only the first two path segments to bound
// cardinality.
func metricsMiddleware(std *metrics.Standard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)
			dur := time.Since(start).Seconds()

			// Prefer the chi route pattern to avoid label cardinality explosion.
			path := r.URL.Path
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if p := rctx.RoutePattern(); p != "" {
					path = p
				}
			}
			// Fallback truncation: keep only the first two path segments for
			// unmatched routes so we don't create unbounded label values.
			if path == r.URL.Path {
				path = truncatePath(path, 2)
			}

			status := strconv.Itoa(ww.status)
			std.HTTPRequestsTotal.Inc(r.Method, path, status)
			std.HTTPRequestDurationSeconds.Observe(dur, r.Method, path)
		})
	}
}

// truncatePath returns the first n segments of a slash-delimited path,
// preserving the leading slash. E.g. truncatePath("/a/b/c/d", 2) → "/a/b".
func truncatePath(p string, n int) string {
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return "/" + strings.Join(parts, "/")
}
