// Package server contains the HTTP server, middleware, and the public API
// surfaces (MLflow compat and native).
package server

import (
	"encoding/json"
	"net/http"
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
