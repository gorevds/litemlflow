// Package ui embeds the static UI bundle.
//
// The bundle is plain HTML/CSS/JS shipped as `static/` and served at /ui/*
// by the server. This keeps deployment a single binary.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Files returns the embedded UI as an fs.FS rooted at the static directory.
func Files() (fs.FS, error) {
	return fs.Sub(static, "static")
}
