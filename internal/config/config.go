// Package config models LiteMLflow runtime configuration.
//
// Config is built from a combination of CLI flags, environment variables,
// and defaults. Higher precedence: CLI flag > env var > default.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config is the resolved runtime configuration.
type Config struct {
	// DataDir is the root for the SQLite db and artifacts. Required.
	DataDir string
	// DBPath is the SQLite file. Defaults to $DataDir/litemlflow.db.
	DBPath string
	// ArtifactsDir is where artifact uploads live. Defaults to $DataDir/artifacts.
	ArtifactsDir string

	// Addr is the listening address (host:port).
	Addr string
	// Auth is one of: "none" (default), "basic", "oidc".
	Auth string
	// BasicUser/BasicPassHash are used when Auth=="basic".
	BasicUser     string
	BasicPassHash string

	// AUTH-OIDC: OIDC provider settings (all required when Auth=="oidc").
	OIDCIssuer       string // e.g. "https://accounts.google.com"
	OIDCClientID     string
	OIDCClientSecret string // empty for public clients (PKCE-only)
	OIDCRedirectURL  string // e.g. "https://my-host/api/v1/auth/oidc/callback"
	OIDCScopes       string // space-separated; default "openid email profile"

	// SessionTTL is the lifetime of session cookies (default 7 days).
	SessionTTL time.Duration

	// MaxArtifactSize bounds artifact uploads (default 5 GiB).
	MaxArtifactSize int64
	// MaxRequestSize bounds non-artifact requests (default 100 MiB).
	MaxRequestSize int64

	// ReadTimeout, WriteTimeout, IdleTimeout govern HTTP timeouts.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	// DevMode enables verbose error responses and pretty logs.
	DevMode bool

	// ArtifactBackend selects the artifact storage backend: "fs" (default) or "s3".
	ArtifactBackend string

	// S3* fields are used when ArtifactBackend == "s3".
	S3Endpoint  string // e.g. "https://s3.amazonaws.com" or "http://minio:9000"
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string
	S3Prefix    string // optional key prefix, e.g. "litemlflow/"
	// S3MultipartThreshold is the minimum upload size (bytes) that triggers
	// multipart upload. Default 100 MiB (100 << 20). Set to 0 to keep the
	// compiled-in default.
	S3MultipartThreshold int64

	// OTLPGRPCAddr is the listen address for the OTLP/gRPC receiver, e.g.
	// "127.0.0.1:4317". Empty (the default) disables the gRPC listener.
	OTLPGRPCAddr string
}

// FromEnv returns a Config populated from environment variables, then
// overlaid with the supplied non-empty fields from explicit.
func FromEnv(explicit Config) (Config, error) {
	c := defaults()
	c = overlayFromEnv(c)
	c = overlay(c, explicit)
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	c.fillDerivedPaths()
	return c, nil
}

func defaults() Config {
	return Config{
		Addr:                 ":5000",
		Auth:                 "none",
		OIDCScopes:           "openid email profile",
		SessionTTL:           7 * 24 * time.Hour,
		MaxArtifactSize:      5 << 30,
		MaxRequestSize:       100 << 20,
		ReadTimeout:          30 * time.Second,
		WriteTimeout:         30 * time.Second,
		IdleTimeout:          120 * time.Second,
		S3MultipartThreshold: 100 << 20, // 100 MiB
	}
}

func overlayFromEnv(c Config) Config {
	if v := os.Getenv("LITEMLFLOW_DATA"); v != "" {
		c.DataDir = v
	}
	if v := os.Getenv("LITEMLFLOW_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("LITEMLFLOW_AUTH"); v != "" {
		c.Auth = v
	}
	if v := os.Getenv("LITEMLFLOW_BASIC_USER"); v != "" {
		c.BasicUser = v
	}
	if v := os.Getenv("LITEMLFLOW_BASIC_PASS_HASH"); v != "" {
		c.BasicPassHash = v
	}
	if v := os.Getenv("LITEMLFLOW_MAX_ARTIFACT_SIZE"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.MaxArtifactSize = n
		}
	}
	// AUTH-OIDC: OIDC env vars
	if v := os.Getenv("LITEMLFLOW_OIDC_ISSUER"); v != "" {
		c.OIDCIssuer = v
	}
	if v := os.Getenv("LITEMLFLOW_OIDC_CLIENT_ID"); v != "" {
		c.OIDCClientID = v
	}
	if v := os.Getenv("LITEMLFLOW_OIDC_CLIENT_SECRET"); v != "" {
		c.OIDCClientSecret = v
	}
	if v := os.Getenv("LITEMLFLOW_OIDC_REDIRECT_URL"); v != "" {
		c.OIDCRedirectURL = v
	}
	if v := os.Getenv("LITEMLFLOW_OIDC_SCOPES"); v != "" {
		c.OIDCScopes = v
	}
	if v := os.Getenv("LITEMLFLOW_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.SessionTTL = d
		}
	}
	if v := os.Getenv("LITEMLFLOW_DEV"); v == "1" {
		c.DevMode = true
	}
	if v := os.Getenv("LITEMLFLOW_ARTIFACT_BACKEND"); v != "" {
		c.ArtifactBackend = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_ENDPOINT"); v != "" {
		c.S3Endpoint = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_BUCKET"); v != "" {
		c.S3Bucket = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_REGION"); v != "" {
		c.S3Region = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_ACCESS_KEY"); v != "" {
		c.S3AccessKey = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_SECRET_KEY"); v != "" {
		c.S3SecretKey = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_PREFIX"); v != "" {
		c.S3Prefix = v
	}
	if v := os.Getenv("LITEMLFLOW_S3_MULTIPART_THRESHOLD"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			c.S3MultipartThreshold = n
		}
	}
	if v := os.Getenv("LITEMLFLOW_OTLP_GRPC_ADDR"); v != "" {
		c.OTLPGRPCAddr = v
	}
	return c
}

func overlay(base, explicit Config) Config {
	if explicit.DataDir != "" {
		base.DataDir = explicit.DataDir
	}
	if explicit.DBPath != "" {
		base.DBPath = explicit.DBPath
	}
	if explicit.ArtifactsDir != "" {
		base.ArtifactsDir = explicit.ArtifactsDir
	}
	if explicit.Addr != "" {
		base.Addr = explicit.Addr
	}
	if explicit.Auth != "" {
		base.Auth = explicit.Auth
	}
	if explicit.BasicUser != "" {
		base.BasicUser = explicit.BasicUser
	}
	if explicit.BasicPassHash != "" {
		base.BasicPassHash = explicit.BasicPassHash
	}
	// AUTH-OIDC: overlay OIDC fields
	if explicit.OIDCIssuer != "" {
		base.OIDCIssuer = explicit.OIDCIssuer
	}
	if explicit.OIDCClientID != "" {
		base.OIDCClientID = explicit.OIDCClientID
	}
	if explicit.OIDCClientSecret != "" {
		base.OIDCClientSecret = explicit.OIDCClientSecret
	}
	if explicit.OIDCRedirectURL != "" {
		base.OIDCRedirectURL = explicit.OIDCRedirectURL
	}
	if explicit.OIDCScopes != "" {
		base.OIDCScopes = explicit.OIDCScopes
	}
	if explicit.SessionTTL != 0 {
		base.SessionTTL = explicit.SessionTTL
	}
	if explicit.MaxArtifactSize != 0 {
		base.MaxArtifactSize = explicit.MaxArtifactSize
	}
	if explicit.MaxRequestSize != 0 {
		base.MaxRequestSize = explicit.MaxRequestSize
	}
	if explicit.ReadTimeout != 0 {
		base.ReadTimeout = explicit.ReadTimeout
	}
	if explicit.WriteTimeout != 0 {
		base.WriteTimeout = explicit.WriteTimeout
	}
	if explicit.IdleTimeout != 0 {
		base.IdleTimeout = explicit.IdleTimeout
	}
	if explicit.DevMode {
		base.DevMode = true
	}
	if explicit.ArtifactBackend != "" {
		base.ArtifactBackend = explicit.ArtifactBackend
	}
	if explicit.S3Endpoint != "" {
		base.S3Endpoint = explicit.S3Endpoint
	}
	if explicit.S3Bucket != "" {
		base.S3Bucket = explicit.S3Bucket
	}
	if explicit.S3Region != "" {
		base.S3Region = explicit.S3Region
	}
	if explicit.S3AccessKey != "" {
		base.S3AccessKey = explicit.S3AccessKey
	}
	if explicit.S3SecretKey != "" {
		base.S3SecretKey = explicit.S3SecretKey
	}
	if explicit.S3Prefix != "" {
		base.S3Prefix = explicit.S3Prefix
	}
	if explicit.S3MultipartThreshold != 0 {
		base.S3MultipartThreshold = explicit.S3MultipartThreshold
	}
	if explicit.OTLPGRPCAddr != "" {
		base.OTLPGRPCAddr = explicit.OTLPGRPCAddr
	}
	return base
}

// Validate enforces invariants.
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return errors.New("data dir is required (set --data or LITEMLFLOW_DATA)")
	}
	switch c.Auth {
	case "none", "basic", "oidc":
	default:
		return errors.New("auth must be one of: none, basic, oidc")
	}
	if c.Auth == "basic" && (c.BasicUser == "" || c.BasicPassHash == "") {
		return errors.New("basic auth requires user and pass-hash to be set")
	}
	// AUTH-OIDC: oidc mode requires issuer and client_id at minimum.
	if c.Auth == "oidc" && (c.OIDCIssuer == "" || c.OIDCClientID == "") {
		return errors.New("oidc auth requires oidc-issuer and oidc-client-id to be set")
	}
	// STORAGE-S3: validate artifact backend selection.
	switch c.ArtifactBackend {
	case "", "fs":
		// filesystem backend; no extra validation required.
	case "s3":
		if c.S3Endpoint == "" {
			return errors.New("s3 backend requires --s3-endpoint (or LITEMLFLOW_S3_ENDPOINT)")
		}
		if c.S3Bucket == "" {
			return errors.New("s3 backend requires --s3-bucket (or LITEMLFLOW_S3_BUCKET)")
		}
		if c.S3Region == "" {
			return errors.New("s3 backend requires --s3-region (or LITEMLFLOW_S3_REGION)")
		}
		if c.S3AccessKey == "" {
			return errors.New("s3 backend requires --s3-access-key (or LITEMLFLOW_S3_ACCESS_KEY)")
		}
		if c.S3SecretKey == "" {
			return errors.New("s3 backend requires --s3-secret-key (or LITEMLFLOW_S3_SECRET_KEY)")
		}
	default:
		return errors.New("artifact-backend must be one of: fs, s3")
	}
	return nil
}

// fillDerivedPaths populates DBPath and ArtifactsDir if unset.
func (c *Config) fillDerivedPaths() {
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataDir, "litemlflow.db")
	}
	if c.ArtifactsDir == "" {
		c.ArtifactsDir = filepath.Join(c.DataDir, "artifacts")
	}
}
