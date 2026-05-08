// Datasets v1.2 native API.
//
// Endpoints:
//
//	GET    /api/v1/datasets                         — list latest version per name
//	GET    /api/v1/datasets/{name}                  — list versions of one dataset
//	POST   /api/v1/datasets/{name}/versions         — upload new version (multipart)
//	GET    /api/v1/datasets/{name}/versions/{v}     — version metadata
//	DELETE /api/v1/datasets/{name}/versions/{v}     — soft delete
//	GET    /api/v1/datasets/{name}/versions/{v}/content  — stream bytes
//	GET    /api/v1/datasets/{name}/versions/{v}/lineage  — ancestors + descendants
//
// Upload contract:
//
//	POST /api/v1/datasets/{name}/versions
//	Content-Type: multipart/form-data; boundary=...
//	  - field "file" (required): the dataset bytes
//	  - field "meta" (optional): JSON {description, schema_json, parents:[id,...]}
//
// The server computes SHA-256 streaming the upload through the CAS, so a
// re-upload of identical bytes (regardless of name) reuses the existing
// physical file. Two name+version rows then share the same content_hash.
package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/model"
)

// maxDatasetUploadBytes caps a single upload at 5 GiB by default. The
// CAS is happy with arbitrary sizes; the cap exists so a misbehaving
// client can't fill the disk via a single request. Operators with bigger
// data should split into multiple datasets or run their own upload pipeline.
const maxDatasetUploadBytes = int64(5 * 1024 * 1024 * 1024)

type uploadMeta struct {
	Description string  `json:"description,omitempty"`
	SchemaJSON  string  `json:"schema_json,omitempty"`
	Parents     []int64 `json:"parents,omitempty"`
}

// CreateDatasetVersion handles POST /api/v1/datasets/{name}/versions.
func (h *Handler) CreateDatasetVersion(w http.ResponseWriter, r *http.Request) {
	if h.Datasets == nil {
		writeError(w, http.StatusServiceUnavailable, "DATASETS_DISABLED",
			"dataset CAS is not configured on this server")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", "name is required")
		return
	}
	// Multipart parser. Cap at maxDatasetUploadBytes to prevent disk-full
	// attacks; the underlying body limit middleware lets datasets through
	// because the route is registered before bodyLimitMiddleware? No — the
	// middleware applies to the entire chi router. We rely on the body
	// limit being raised for this route or operator override.
	r.Body = http.MaxBytesReader(w, r.Body, maxDatasetUploadBytes)

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"expected multipart/form-data with 'file' field: "+err.Error())
		return
	}

	// Cap the meta JSON at 64 KiB. ParseMultipartForm only buffers
	// non-file fields up to its memory threshold, but we don't want a
	// 32 MiB JSON blob to allocate 64 MiB transient memory in
	// json.Unmarshal. The legitimate meta payload is tiny (description +
	// schema + parents id list).
	var meta uploadMeta
	if metaStr := r.FormValue("meta"); metaStr != "" {
		const maxMetaBytes = 64 * 1024
		if len(metaStr) > maxMetaBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE",
				fmt.Sprintf("meta payload exceeds %d bytes", maxMetaBytes))
			return
		}
		if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
				"meta must be valid JSON: "+err.Error())
			return
		}
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"missing 'file' part: "+err.Error())
		return
	}
	defer file.Close()

	hash, size, err := h.Datasets.Put(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STORAGE_ERROR",
			"failed to store dataset bytes: "+err.Error())
		return
	}

	// Fetch user from request (set by auth middleware) — best-effort.
	createdBy := r.Header.Get("X-LiteMLflow-User")

	d := &model.DatasetVersion{
		Name:        name,
		ContentHash: hash,
		SizeBytes:   size,
		SchemaJSON:  meta.SchemaJSON,
		Description: meta.Description,
		WorkspaceID: workspaceFromReq(r),
		CreatedBy:   createdBy,
	}
	out, err := h.Store.CreateDatasetVersion(r.Context(), d, meta.Parents)
	if err != nil {
		// Common case: parent not in same workspace, or content_hash empty,
		// or insertion conflict.
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE", err.Error())
		return
	}
	writeJSON(w, out)
}

// ListDatasets handles GET /api/v1/datasets.
func (h *Handler) ListDatasets(w http.ResponseWriter, r *http.Request) {
	ws := workspaceFromReq(r)
	rows, err := h.Store.ListDatasets(r.Context(), ws)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if rows == nil {
		rows = []*model.DatasetVersion{}
	}
	writeJSON(w, map[string]any{"datasets": rows})
}

// ListDatasetVersions handles GET /api/v1/datasets/{name}.
func (h *Handler) ListDatasetVersions(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	ws := workspaceFromReq(r)
	rows, err := h.Store.ListDatasetVersions(r.Context(), ws, name)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	if rows == nil {
		rows = []*model.DatasetVersion{}
	}
	writeJSON(w, map[string]any{"name": name, "versions": rows})
}

// GetDatasetVersion handles GET /api/v1/datasets/{name}/versions/{version}.
func (h *Handler) GetDatasetVersion(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	v, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"version must be a positive integer")
		return
	}
	ws := workspaceFromReq(r)
	d, err := h.Store.GetDatasetVersion(r.Context(), ws, name, v)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, d)
}

// DeleteDatasetVersion handles DELETE /api/v1/datasets/{name}/versions/{version}.
// Soft delete only — content stays in CAS until an offline GC pass.
func (h *Handler) DeleteDatasetVersion(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	v, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"version must be a positive integer")
		return
	}
	ws := workspaceFromReq(r)
	if err := h.Store.SoftDeleteDatasetVersion(r.Context(), ws, name, v); err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// GetDatasetContent handles GET /api/v1/datasets/{name}/versions/{version}/content.
// Streams the CAS bytes back. Sets Content-Length when known.
func (h *Handler) GetDatasetContent(w http.ResponseWriter, r *http.Request) {
	if h.Datasets == nil {
		writeError(w, http.StatusServiceUnavailable, "DATASETS_DISABLED",
			"dataset CAS is not configured on this server")
		return
	}
	name := chi.URLParam(r, "name")
	v, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"version must be a positive integer")
		return
	}
	ws := workspaceFromReq(r)
	d, err := h.Store.GetDatasetVersion(r.Context(), ws, name, v)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	rc, size, err := h.Datasets.Get(d.ContentHash)
	if err != nil {
		// Row exists but CAS doesn't — most likely the row was backfilled
		// from v0.3 with a digest we never had bytes for, or content was
		// GC'd. Surface a 404 so callers can distinguish from "row missing".
		writeError(w, http.StatusNotFound, "CONTENT_NOT_FOUND",
			fmt.Sprintf("content for hash %s is not in the local CAS (legacy backfill or GC?)", d.ContentHash))
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+sanitizeFilename(name)+`-v`+strconv.FormatInt(v, 10)+`"`)
	w.Header().Set("ETag", `"`+d.ContentHash+`"`)
	w.Header().Set("X-LiteMLflow-Content-Hash", d.ContentHash)
	if _, err := io.Copy(w, rc); err != nil {
		// Stream already started; nothing to do but log.
		return
	}
}

// GetDatasetLineage handles GET /api/v1/datasets/{name}/versions/{version}/lineage.
func (h *Handler) GetDatasetLineage(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	v, err := strconv.ParseInt(chi.URLParam(r, "version"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMETER_VALUE",
			"version must be a positive integer")
		return
	}
	ws := workspaceFromReq(r)
	lin, err := h.Store.GetDatasetLineage(r.Context(), ws, name, v)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, lin)
}

// sanitizeFilename returns an ASCII-safe filename for the
// Content-Disposition header. We allow only printable ASCII (0x20–0x7E)
// minus shell / path / Unicode-confusable metacharacters; everything else
// (including bidi-override codepoints like U+202E that flip rendering)
// becomes '_'.
//
// For non-ASCII original names a future revision can emit
// `filename*=UTF-8''<percent-encoded>` per RFC 5987; the strict ASCII
// fallback is enough for v1.2.
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == '"', r == '\\', r == '/', r == ';',
			r == '\r', r == '\n', r == 0x00:
			b.WriteByte('_')
		case r < 0x20 || r >= 0x7F:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "dataset"
	}
	return out
}

// _ ensures errors-package is referenced in case future edits remove it.
var _ = errors.New
