// Package server contains the HTTP server, middleware, and the public API
// surfaces (MLflow compat and native).
package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorevds/litemlflow/internal/store"
)

// MLflow ErrorCode values used in API responses.
const (
	CodeBadRequest            = "BAD_REQUEST"
	CodeInvalidParameter      = "INVALID_PARAMETER_VALUE"
	CodeResourceAlreadyExists = "RESOURCE_ALREADY_EXISTS"
	CodeResourceDoesNotExist  = "RESOURCE_DOES_NOT_EXIST"
	CodeResourceConflict      = "RESOURCE_CONFLICT"
	CodePermissionDenied      = "PERMISSION_DENIED"
	CodeUnauthenticated       = "UNAUTHENTICATED"
	CodeInternalError         = "INTERNAL_ERROR"
	CodeNotImplemented        = "NOT_IMPLEMENTED"
	CodeTooManyRequests       = "RESOURCE_EXHAUSTED"
)

// errorResponse is the MLflow-compatible error body.
type errorResponse struct {
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
}

// writeError writes a JSON error response with the given HTTP status, code, and message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{ErrorCode: code, Message: message})
}

// writeStoreError converts a store error into the appropriate HTTP response.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeResourceDoesNotExist, err.Error())
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusBadRequest, CodeResourceAlreadyExists, err.Error())
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, CodeResourceConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error())
	}
}

// writeJSON writes any JSON-serializable value as a 200 response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// At this point the headers may already be flushed; logging only.
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
