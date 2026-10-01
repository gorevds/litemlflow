package server

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/gorevds/litemlflow/internal/auth"
	"github.com/gorevds/litemlflow/internal/config"
	"github.com/gorevds/litemlflow/internal/grpcotlp"
	"github.com/gorevds/litemlflow/internal/store"
)

// grpcAuthStore is what the gRPC auth hooks need: workspace membership plus
// session lookup (*store.SQLiteStore satisfies it).
type grpcAuthStore interface {
	store.Store
	SessionLookup
}

// grpcOTLPOptions returns the auth options for the gRPC OTLP receiver so it
// enforces the same policy as the HTTP API. With auth=none the receiver
// stays open, exactly like the HTTP side.
func grpcOTLPOptions(cfg config.Config, st grpcAuthStore) []grpcotlp.Option {
	if cfg.Auth == "none" || cfg.Auth == "" {
		return nil
	}
	return []grpcotlp.Option{
		grpcotlp.WithAuthenticator(grpcAuthenticator(cfg, st)),
		grpcotlp.WithAuthorizer(grpcAuthorizer(st)),
	}
}

// grpcAuthenticator validates `authorization` metadata. It accepts the same
// credentials as authMiddlewareWithSessions:
//   - "Bearer <session-id>": a live session (the token the HTTP side reads
//     from the session cookie; issued by basic login or the OIDC callback),
//     accepted in every auth mode.
//   - "Basic <b64 user:pass>": only when cfg.Auth == "basic".
func grpcAuthenticator(cfg config.Config, sessions SessionLookup) grpcotlp.Authenticator {
	return func(ctx context.Context, authorization string) (string, error) {
		scheme, cred, ok := strings.Cut(strings.TrimSpace(authorization), " ")
		cred = strings.TrimSpace(cred)
		if !ok || cred == "" {
			return "", errors.New("malformed authorization")
		}
		switch strings.ToLower(scheme) {
		case "bearer":
			sess, err := sessions.GetSession(ctx, cred)
			if err != nil {
				return "", errors.New("invalid token")
			}
			return sess.UserID, nil
		case "basic":
			if cfg.Auth != "basic" {
				return "", errors.New("basic auth not enabled")
			}
			raw, err := base64.StdEncoding.DecodeString(cred)
			if err != nil {
				return "", errors.New("malformed basic credentials")
			}
			user, pass, ok := strings.Cut(string(raw), ":")
			if !ok || !auth.VerifyBasicCredentials(cfg.BasicUser, cfg.BasicPassHash, user, pass) {
				return "", errors.New("invalid credentials")
			}
			return user, nil
		}
		return "", errors.New("unsupported authorization scheme")
	}
}

// grpcAuthorizer mirrors rbacMiddleware for an OTLP export (POST /v1/traces
// requires "editor"): the member-less default workspace is open, otherwise
// the caller must be a member with at least the editor role.
func grpcAuthorizer(st store.Store) grpcotlp.Authorizer {
	return func(ctx context.Context, ws, user string) error {
		if ws == "default" {
			members, err := st.ListMembers(ctx, ws)
			if err != nil {
				return status.Error(codes.Internal, "membership lookup failed")
			}
			if len(members) == 0 {
				return nil
			}
		}
		role, err := st.GetMemberRole(ctx, ws, user)
		if err != nil {
			return status.Error(codes.PermissionDenied, "you are not a member of workspace "+ws)
		}
		if required := requiredRole("POST", "/v1/traces"); !roleAtLeast(role, required) {
			return status.Error(codes.PermissionDenied,
				"role "+role+" cannot perform this operation (requires "+required+")")
		}
		return nil
	}
}
