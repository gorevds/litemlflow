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
		Addr:            ":5000",
		Auth:            "none",
		OIDCScopes:      "openid email profile",
		SessionTTL:      7 * 24 * time.Hour,
		MaxArtifactSize: 5 << 30,
		MaxRequestSize:  100 << 20,
		ReadTimeout:     30 * time.Second,
		WriteTimeout:    30 * time.Second,
		IdleTimeout:     120 * time.Second,
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
