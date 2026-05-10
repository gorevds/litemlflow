// Federation v1.3 native API.
//
// Endpoints under /api/v1/federate/*:
//
//	POST   /api/v1/federate/peers              — admin: add peer (returns secret once)
//	GET    /api/v1/federate/peers              — admin: list peers (no secret)
//	DELETE /api/v1/federate/peers/{id}         — admin: remove peer
//	POST   /api/v1/federate/peers/{id}/echo    — admin: probe peer; updates status
//	POST   /api/v1/federate/search             — peer-callable: local-only search,
//	                                              HMAC-validated. Mounted with no
//	                                              session auth — the HMAC IS the auth.
//
// The user-facing /api/v1/search is augmented with ?federated=1 to fan
// out to all connected peers in parallel (handler in handlers.go).
//
// Auth model:
//   - Peer-mgmt routes ride the existing RBAC chain (admin role).
//   - /api/v1/federate/search is exempt from session auth; the HMAC
//     header IS the credential. RBAC is bypassed via isPublicPath in
//     middleware.go (entry added below).
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/federation"
	"github.com/gorevds/litemlflow/internal/model"
	"github.com/gorevds/litemlflow/internal/store"
)

// addPeerReq is the request body for POST /api/v1/federate/peers.
type addPeerReq struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Secret is optional on create — server generates a fresh 32-byte
	// secret if omitted. Returned once in the response so the operator
	// can configure the same secret on the remote side.
	Secret string `json:"secret,omitempty"`
}

// peerDTO is the wire shape (omits the secret on list/get).
type peerDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	WorkspaceID string `json:"workspace_id"`
	AddedAt     int64  `json:"added_at"`
	LastSeen    *int64 `json:"last_seen,omitempty"`
	Status      string `json:"status"`
	LastError   string `json:"last_error,omitempty"`
	HasSecret   bool   `json:"has_secret"`
}

func peerToDTO(p *model.Peer) peerDTO {
	return peerDTO{
		ID: p.ID, Name: p.Name, URL: p.URL,
		WorkspaceID: p.WorkspaceID, AddedAt: p.AddedAt, LastSeen: p.LastSeen,
		Status: p.Status, LastError: p.LastError,
		HasSecret: p.Secret != "",
	}
}

// AddPeer handles POST /api/v1/federate/peers.
//
// Generates a fresh secret if the request omits one and returns it in
// the response. The secret is shown ONCE — on subsequent reads only
// the metadata is returned. Operator must paste the secret into the
// peer side's configuration.
func (h *Handler) AddPeer(w http.ResponseWriter, r *http.Request) {
	var req addPeerReq
	if err := decodeJSON(r, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Name == "" || req.URL == "" {
		writeMissingField(w, "name+url")
		return
	}
	secret := req.Secret
	if secret == "" {
		var err error
		secret, err = federation.NewSecret()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
				"failed to generate secret: "+err.Error())
			return
		}
	} else if len(secret) != 64 {
		writeError(w, http.StatusBadRequest, codeInvalidParameter,
			"secret must be 64 hex chars (32 bytes)")
		return
	}

	p := &model.Peer{
		Name:        req.Name,
		URL:         req.URL,
		Secret:      secret,
		WorkspaceID: workspaceFromReq(r),
	}
	id, err := h.Store.CreatePeer(r.Context(), p)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	p.ID = id
	// Response: include the secret ONCE (operator needs to install it on
	// the peer side). Subsequent GET responses do not.
	writeJSON(w, map[string]any{
		"peer":   peerToDTO(p),
		"secret": secret,
	})
}

// ListPeers handles GET /api/v1/federate/peers.
func (h *Handler) ListPeers(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromReq(r)
	peers, err := h.Store.ListPeers(r.Context(), ws)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	out := make([]peerDTO, 0, len(peers))
	for _, p := range peers {
		out = append(out, peerToDTO(p))
	}
	writeJSON(w, map[string]any{"peers": out})
}

// DeletePeer handles DELETE /api/v1/federate/peers/{id}.
func (h *Handler) DeletePeer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "id must be an integer")
		return
	}
	if err := h.Store.DeletePeer(r.Context(), workspaceFromReq(r), id); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// EchoPeer handles POST /api/v1/federate/peers/{id}/echo.
//
// Sends an HMAC-signed POST to the peer's /api/v1/federate/echo
// endpoint and updates the peer's status based on the result. Returns
// the new status to the caller.
func (h *Handler) EchoPeer(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "id must be an integer")
		return
	}
	ws := workspaceFromReq(r)
	p, err := h.Store.GetPeer(r.Context(), ws, id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	ourName := h.federationOurName()
	client, cerr := federation.NewClient(p.URL, ourName, p.Secret, 5*time.Second)
	if cerr != nil {
		_ = h.Store.UpdatePeerStatus(r.Context(), id, "error", "client init: "+cerr.Error(), 0)
		writeError(w, http.StatusBadRequest, codeInvalidParameter, cerr.Error())
		return
	}

	body := []byte(fmt.Sprintf(`{"from":%q,"ts":%d}`, ourName, time.Now().UnixMilli()))
	resp, respBody, derr := client.Do("POST", "/api/v1/federate/echo", body)
	if derr != nil {
		_ = h.Store.UpdatePeerStatus(r.Context(), id, "error", "echo: "+derr.Error(), 0)
		writeJSON(w, map[string]any{"status": "error", "error": derr.Error()})
		return
	}
	if resp.StatusCode != http.StatusOK {
		// Surface the peer's response body so misconfigured secrets / clock-skew
		// errors are visible to the operator instead of just "HTTP 401". Cap
		// to 1 KiB — a malicious or misconfigured peer can return arbitrary
		// content and we don't want to flood the operator's UI / logs.
		snippet := string(respBody)
		if len(snippet) > 1024 {
			snippet = snippet[:1024] + "…"
		}
		msg := fmt.Sprintf("peer responded HTTP %d: %s", resp.StatusCode, snippet)
		_ = h.Store.UpdatePeerStatus(r.Context(), id, "error", msg, 0)
		writeJSON(w, map[string]any{"status": "error", "error": msg})
		return
	}
	now := time.Now().UnixMilli()
	_ = h.Store.UpdatePeerStatus(r.Context(), id, "connected", "", now)
	writeJSON(w, map[string]any{"status": "connected", "last_seen": now})
}

// FederateEcho handles POST /api/v1/federate/echo. Peer-callable; HMAC
// validates the inbound. We just echo back — no body shape to validate
// beyond JSON parseability.
func (h *Handler) FederateEcho(w http.ResponseWriter, r *http.Request) {
	if _, err := h.validateFederationRequest(r); err != nil {
		writeFederationAuthError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "ts": time.Now().UnixMilli()})
}

// writeFederationAuthError responds with a single uniform 401 body and
// logs the underlying cause server-side. Uniformity prevents an
// unauthenticated attacker from learning whether a peer name is
// registered (vs whether their HMAC was wrong vs their clock skewed) —
// see independent review finding H2.
func writeFederationAuthError(w http.ResponseWriter, r *http.Request, cause error) {
	slog.Warn("federation auth failure",
		"peer", r.Header.Get(federation.HeaderPeer),
		"path", r.URL.Path,
		"err", cause.Error())
	writeError(w, http.StatusUnauthorized, codeUnauthenticated, "federation auth failed")
}

// FederateSearch handles POST /api/v1/federate/search. Peer-callable;
// HMAC validates the inbound. Returns local-only results (no recursive
// fan-out — the calling peer is doing the fan-out).
//
// Body shape: { "q": "...", "kind": "all|runs|experiments|prompts", "max": N }
type federateSearchReq struct {
	Q    string `json:"q"`
	Kind string `json:"kind,omitempty"`
	Max  int    `json:"max,omitempty"`
}

func (h *Handler) FederateSearch(w http.ResponseWriter, r *http.Request) {
	body, err := h.validateFederationRequest(r)
	if err != nil {
		writeFederationAuthError(w, r, err)
		return
	}
	var req federateSearchReq
	if err := json.Unmarshal(body, &req); err != nil {
		writeBadRequest(w, err)
		return
	}
	if req.Q == "" {
		writeMissingField(w, "q")
		return
	}
	if req.Max <= 0 {
		req.Max = 10
	}

	// Reuse the local-search code path. Workspace is taken from the
	// peer's request header (set by the federating client to its OWN
	// workspace name) — this lets cross-workspace federation work in
	// the future, falls back to "default".
	results := h.runLocalSearch(r.Context(), workspaceFromReq(r), req.Q, req.Kind, req.Max)
	writeJSON(w, map[string]any{
		"instance": h.federationOurName(),
		"results":  results,
	})
}

// validateFederationRequest reads r.Body once, checks the HMAC, and
// returns the body bytes (for the handler to unmarshal) along with an
// error. On any auth failure the error wraps the federation.Err*
// sentinel; the handler maps them all to the same opaque 401 so peer
// names cannot be enumerated.
func (h *Handler) validateFederationRequest(r *http.Request) ([]byte, error) {
	peerName := r.Header.Get(federation.HeaderPeer)
	sig := r.Header.Get(federation.HeaderSignature)
	tsStr := r.Header.Get(federation.HeaderTimestamp)
	if peerName == "" || sig == "" || tsStr == "" {
		return nil, fmt.Errorf("missing federation headers")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp header: %w", err)
	}

	// Read body up-front so we can verify the HMAC and hand the same
	// bytes to the handler without a second read.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	// Look up the peer by name in the request's workspace.
	p, err := h.Store.GetPeerByName(r.Context(), workspaceFromReq(r), peerName)
	if err != nil {
		return nil, federation.ErrPeerUnknown
	}

	if err := federation.VerifyRequest(p.Secret, r.Method, r.URL.Path, ts, body, sig); err != nil {
		return nil, fmt.Errorf("federation: %w (peer=%s)", err, peerName)
	}
	return body, nil
}

// federationOurName returns the name this server presents to peers.
// Read from cfg.FederationName, falling back to "lmf-self" so unconfigured
// deploys still function (the receiver only checks the secret).
func (h *Handler) federationOurName() string {
	if h.Cfg.FederationName != "" {
		return h.Cfg.FederationName
	}
	return "lmf-self"
}

// runLocalSearch is a minimal slice of the existing GlobalSearch logic
// invoked locally by FederateSearch. We don't recurse: the calling peer
// is responsible for its own fan-out.
//
// The full GlobalSearch handler is in handlers.go; here we only need
// the local part. Cleaner option in v1.4: refactor GlobalSearch to
// expose the local-only path as a public store-backed helper.
func (h *Handler) runLocalSearch(ctx context.Context, workspaceID, q, kind string, max int) []searchResultItem {
	if max <= 0 {
		max = 10
	}
	out := []searchResultItem{}

	// Reuse the existing search infrastructure. We call the same store
	// methods the GlobalSearch handler uses. Caps below match the
	// handler's caps.
	if kind == "" || kind == "all" || kind == "runs" {
		runs, err := h.Store.SearchRunsByName(ctx, workspaceID, q, max)
		if err == nil {
			for _, r := range runs {
				out = append(out, searchResultItem{
					Kind:  "run",
					ID:    r.ID,
					Title: r.Name,
					URL:   "#/experiments/" + strconv.FormatInt(r.ExperimentID, 10) + "/runs/" + r.ID,
				})
			}
		}
	}
	if kind == "" || kind == "all" || kind == "experiments" {
		exps, err := h.Store.SearchExperiments(ctx, store.SearchOptions{
			Filter: "name LIKE '%" + escapeLikeBangSafe(q) + "%'",
			MaxResults: max, WorkspaceID: workspaceID,
		})
		if err == nil {
			for _, e := range exps.Items {
				out = append(out, searchResultItem{
					Kind:  "experiment",
					ID:    strconv.FormatInt(e.ID, 10),
					Title: e.Name,
					URL:   "#/experiments/" + strconv.FormatInt(e.ID, 10),
				})
			}
		}
	}
	if kind == "" || kind == "all" || kind == "prompts" {
		// Prompts are workspace-global in the current schema; ListPrompts
		// returns the latest version per name. Server-side substring match
		// over q (case-insensitive) — peers don't have access to the
		// caller's localStorage prompt-name index that GlobalSearch relies
		// on, so the federated path matches against all known names.
		prompts, err := h.Store.ListPrompts(ctx)
		if err == nil {
			ql := strings.ToLower(q)
			matched := 0
			for _, p := range prompts {
				if matched >= max {
					break
				}
				if ql != "" && !strings.Contains(strings.ToLower(p.Name), ql) {
					continue
				}
				out = append(out, searchResultItem{
					Kind:  "prompt",
					ID:    p.Name,
					Title: p.Name,
					URL:   "#/prompts/" + p.Name,
				})
				matched++
			}
		}
	}
	return out
}

// escapeLikeBangSafe escapes single-quotes for the SQL LIKE filter we
// build. The store's filter parser allows simple wildcards; this is
// shared with the GlobalSearch handler in handlers.go, kept as a small
// duplicate to avoid yet another refactor.
func escapeLikeBangSafe(q string) string {
	return strings.ReplaceAll(q, "'", "''")
}

// ----- federated fan-out invoked by /api/v1/search?federated=1 -----

// federatedFanOut runs the same query against every connected peer in
// parallel and returns the merged result list. Cached entries are
// returned without hitting the network.
//
// errors per-peer don't fail the overall query; the caller gets whatever
// peers responded. A `partial` boolean is returned if any peer errored.
func (h *Handler) federatedFanOut(ctx context.Context, workspaceID, q, kind string, max int) (
	merged []searchResultItem, instances []string, partial bool,
) {
	peers, err := h.Store.ListPeers(ctx, workspaceID)
	if err != nil {
		return nil, nil, true
	}
	type chRes struct {
		instance string
		results  []searchResultItem
		err      error
	}
	body, _ := json.Marshal(federateSearchReq{Q: q, Kind: kind, Max: max})
	cacheKey := federation.QueryHash(map[string]any{"q": q, "kind": kind, "max": max, "ws": workspaceID})

	resCh := make(chan chRes, len(peers))
	var wg sync.WaitGroup
	for _, p := range peers {
		if p.Status != "connected" {
			continue
		}
		wg.Add(1)
		go func(p *model.Peer) {
			defer wg.Done()
			// Cache hit?
			if h.FederationCache != nil {
				if cached, ok := h.FederationCache.Get(federation.Key(p.ID, cacheKey)); ok {
					var hit struct {
						Instance string         `json:"instance"`
						Results  []searchResultItem `json:"results"`
					}
					if err := json.Unmarshal(cached, &hit); err == nil {
						resCh <- chRes{instance: hit.Instance, results: hit.Results}
						return
					}
				}
			}
			client, err := federation.NewClient(p.URL, h.federationOurName(), p.Secret, 5*time.Second)
			if err != nil {
				resCh <- chRes{instance: p.Name, err: err}
				return
			}
			// Propagate the parent request's ctx so that if the user
			// closes the browser / cancels the search, in-flight peer
			// fetches are cancelled too instead of running to the 5s
			// client timeout.
			resp, payload, err := client.DoCtx(ctx, "POST", "/api/v1/federate/search", body)
			if err != nil {
				resCh <- chRes{instance: p.Name, err: err}
				return
			}
			if resp.StatusCode != http.StatusOK {
				resCh <- chRes{instance: p.Name, err: fmt.Errorf("HTTP %d", resp.StatusCode)}
				return
			}
			var got struct {
				Instance string         `json:"instance"`
				Results  []searchResultItem `json:"results"`
			}
			if err := json.Unmarshal(payload, &got); err != nil {
				resCh <- chRes{instance: p.Name, err: err}
				return
			}
			if h.FederationCache != nil {
				h.FederationCache.Put(federation.Key(p.ID, cacheKey), payload)
			}
			resCh <- chRes{instance: got.Instance, results: got.Results}
		}(p)
	}
	wg.Wait()
	close(resCh)

	for r := range resCh {
		if r.err != nil {
			partial = true
			continue
		}
		// Tag each result with its origin instance.
		for i := range r.results {
			r.results[i].Instance = r.instance
		}
		merged = append(merged, r.results...)
		instances = append(instances, r.instance)
	}
	return merged, instances, partial
}
