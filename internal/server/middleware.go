package server

import (
	"context"
	"crypto/sha256"
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
// Scripts are restricted to 'self' (no inline handlers), so an HTML-injection
// bug in the UI cannot execute script. Styles keep 'unsafe-inline' because the
// bundled UI uses inline style attributes heavily.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	const csp = "default-src 'self'; img-src 'self' data:; " +
		"style-src 'self' 'unsafe-inline'; script-src 'self'; " +
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

// Unwrap lets http.ResponseController reach the underlying connection (to
// adjust per-request deadlines) through this wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

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
//
// Large-upload paths (see isLargeUploadPath) are exempt from maxBytes. MLflow
// artifact uploads are instead capped at maxArtifactBytes (config
// MaxArtifactSize) — the artifact handler streams the body straight to the
// backend without any limit of its own, so without this any editor could
// fill the disk / bucket with a single unbounded PUT. Dataset version uploads
// install their own MaxBytesReader in the handler.
func bodyLimitMiddleware(maxBytes, maxArtifactBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			limit := maxBytes
			if isLargeUploadPath(r.Method, r.URL.Path) {
				limit = 0
				if isArtifactUploadPath(r.Method, r.URL.Path) {
					limit = maxArtifactBytes
				}
			}
			if r.Body != nil && limit > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// isArtifactUploadPath reports whether the request is an MLflow artifact
// upload, which is bounded by MaxArtifactSize rather than MaxRequestSize.
func isArtifactUploadPath(method, path string) bool {
	return (method == http.MethodPost || method == http.MethodPut) &&
		strings.HasPrefix(path, "/api/2.0/mlflow-artifacts/artifacts")
}

// isLargeTransferPath reports requests whose body or response can legitimately
// take far longer than the server-wide Read/WriteTimeout: artifact uploads and
// downloads and dataset version uploads.
func isLargeTransferPath(method, path string) bool {
	if isLargeUploadPath(method, path) {
		return true
	}
	return method == http.MethodGet && strings.HasPrefix(path, "/api/2.0/mlflow-artifacts/artifacts/")
}

// largeTransferTimeout bounds a single large artifact / dataset transfer.
const largeTransferTimeout = 6 * time.Hour

// largeTransferDeadlineMiddleware lifts the server-wide read/write deadlines
// for large artifact / dataset transfers. http.Server's ReadTimeout and
// WriteTimeout (30 s by default) cover the WHOLE request body and response,
// so without this a multi-GiB artifact upload or download over an ordinary
// link is cut off mid-stream despite MaxArtifactSize advertising 5 GiB. It
// runs after auth + RBAC so only authorized callers get the extended window;
// body size stays bounded by bodyLimitMiddleware / the dataset handler.
func largeTransferDeadlineMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLargeTransferPath(r.Method, r.URL.Path) {
			// A generous but finite window: long enough for multi-GiB
			// transfers, still bounded so a stalled client cannot pin a
			// connection forever.
			deadline := time.Now().Add(largeTransferTimeout)
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(deadline)
			_ = rc.SetWriteDeadline(deadline)
		}
		next.ServeHTTP(w, r)
	})
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
func authMiddlewareWithSessions(cfg config.Config, sessions SessionLookup, authLimiter *authRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Strip identity header so clients cannot smuggle it.
			r.Header.Del("X-LiteMLflow-User")
			r.Header.Del("X-LiteMLflow-Auth-Method")
			// The role header is only set by rbacMiddleware when a role was
			// actually resolved; in open mode it would otherwise pass through
			// verbatim from the client.
			r.Header.Del("X-LiteMLflow-Role")

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
				if !auth.VerifyBasicCredentials(cfg.BasicUser, cfg.BasicPassHash, user, pass) {
					// Basic credentials are checked on every request, so this
					// is the real brute-force surface (not just /auth/login).
					// Charge each FAILED attempt to the per-IP limiter; a
					// successful auth never consumes a token, so legitimate
					// clients are unaffected. Once the budget is spent, reject
					// further failures with 429 (independent-review).
					if authLimiter != nil && !authLimiter.allow(authLimiter.clientKey(r)) {
						w.Header().Set("Retry-After", "60")
						writeError(w, http.StatusTooManyRequests, CodeTooManyRequests,
							"too many failed authentication attempts; slow down and retry later")
						return
					}
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
					// QueryEscape: an unescaped RequestURI carrying its own
					// query string would be split at '&' and the tail
					// re-interpreted as parameters of /oidc/start.
					http.Redirect(w, r, "/api/v1/auth/oidc/start?return_to="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
					return
				}
				writeError(w, http.StatusUnauthorized, CodeUnauthenticated, "OIDC authentication required; visit /api/v1/auth/oidc/start")

			default:
				writeError(w, http.StatusInternalServerError, CodeInternalError, "unknown auth mode")
			}
		})
	}
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
				if _, err := st.GetWorkspace(r.Context(), wsID); err != nil && isWorkspaceAgnosticPath(r.URL.Path) {
					// A stale lmf_workspace cookie (workspace deleted) must
					// not lock the browser out of the UI shell, health checks,
					// or login/logout — those never act on a workspace.
					wsID = "default"
				} else if err != nil {
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

// isWorkspaceAgnosticPath reports public paths that never read workspace
// data. Federation peer endpoints are public too but DO scope their results
// by the requested workspace, so they keep strict validation.
func isWorkspaceAgnosticPath(p string) bool {
	switch p {
	case "/api/v1/federate/echo", "/api/v1/federate/search":
		return false
	}
	return isPublicPath(p)
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

			// Public paths (login/logout/OIDC callback, health, UI assets,
			// federation peer endpoints) carry no workspace role requirement.
			// Without this, a browser holding an lmf_workspace cookie for a
			// non-default workspace could not even reach /auth/login or load
			// the UI after its session expired: the anonymous caller is not a
			// member, so every request was rejected with 403.
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			ws, _ := r.Context().Value(ctxKeyWorkspace).(string)
			if ws == "" {
				ws = "default"
			}

			// Resolve user identity.
			user, _ := r.Context().Value(ctxKeyUser).(string)
			if user == "" {
				user = r.Header.Get("X-LiteMLflow-User")
			}

			// Workspace-management routes (/api/v1/workspaces/{id}/...) act on
			// the workspace named in the PATH, not the one selected by the
			// X-Workspace header / cookie. Authorize against the target:
			// otherwise an admin of their own workspace — or any caller in
			// the open-mode default workspace — could rename/delete another
			// workspace or grant themselves admin in it. A target with no
			// members yet is still bootstrappable from the caller's current
			// workspace (that is how the first admin is added).
			if target := workspacePathTarget(r.URL.Path); target != "" && target != ws {
				members, err := st.ListMembers(r.Context(), target)
				if err != nil {
					writeError(w, http.StatusInternalServerError, CodeInternalError, "membership lookup failed")
					return
				}
				if len(members) > 0 {
					role, err := st.GetMemberRole(r.Context(), target, user)
					if err != nil {
						writeError(w, http.StatusForbidden, CodePermissionDenied,
							"you are not a member of workspace "+target)
						return
					}
					if required := requiredRole(r.Method, r.URL.Path); required != "" && !roleAtLeast(role, required) {
						writeError(w, http.StatusForbidden, CodePermissionDenied,
							"role "+role+" in workspace "+target+" cannot perform this operation (requires "+required+")")
						return
					}
					// Authorized against the target, which is the only
					// workspace the handler acts on.
					r = r.WithContext(context.WithValue(r.Context(), ctxKeyRole, role))
					r.Header.Set("X-LiteMLflow-Role", role)
					next.ServeHTTP(w, r)
					return
				}
			}

			// 2. default workspace with zero members → open mode. A lookup
			// error fails closed: treating it as "no members" would silently
			// disable RBAC whenever the database hiccups.
			if ws == "default" {
				members, err := st.ListMembers(r.Context(), ws)
				if err != nil {
					writeError(w, http.StatusInternalServerError, CodeInternalError, "membership lookup failed")
					return
				}
				if len(members) == 0 {
					next.ServeHTTP(w, r)
					return
				}
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

// workspacePathTarget returns the {id} of a /api/v1/workspaces/{id}[/...]
// route, or "" for any other path (including /api/v1/workspaces/current,
// which reports on the header-selected workspace).
func workspacePathTarget(p string) string {
	rest, ok := strings.CutPrefix(p, "/api/v1/workspaces/")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	if id == "current" {
		return ""
	}
	return id
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

			// Label cardinality must stay bounded no matter what clients
			// send: use the chi route pattern (never the raw path, which
			// carries run/experiment ids or attacker-chosen junk), collapse
			// unmatched routes to one label, and bucket non-standard methods.
			path := "unmatched"
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if p := rctx.RoutePattern(); p != "" {
					path = p
				}
			}
			method := metricsMethod(r.Method)

			status := strconv.Itoa(ww.status)
			std.HTTPRequestsTotal.Inc(method, path, status)
			std.HTTPRequestDurationSeconds.Observe(dur, method, path)
		})
	}
}

// metricsMethod maps the request method to a bounded label set; arbitrary
// client-chosen method tokens would otherwise mint unbounded series.
func metricsMethod(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}
