package config

import (
	"testing"
	"time"
)

func TestValidateOIDCRequiresRedirectURL(t *testing.T) {
	c := Config{DataDir: "/tmp/x", Auth: "oidc", OIDCIssuer: "https://idp", OIDCClientID: "id"}
	if err := c.Validate(); err == nil {
		t.Fatal("oidc without redirect URL must be rejected")
	}
	c.OIDCRedirectURL = "https://host/api/v1/auth/oidc/callback"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid oidc config rejected: %v", err)
	}
}

func TestValidateRejectsNegativeLimits(t *testing.T) {
	base := Config{DataDir: "/tmp/x", Auth: "none"}
	for name, mut := range map[string]func(*Config){
		"max-request":  func(c *Config) { c.MaxRequestSize = -1 },
		"max-artifact": func(c *Config) { c.MaxArtifactSize = -1 },
		"read-timeout": func(c *Config) { c.ReadTimeout = -time.Second },
		"session-ttl":  func(c *Config) { c.SessionTTL = -time.Hour },
	} {
		c := base
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: negative value must be rejected", name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("base config rejected: %v", err)
	}
}
