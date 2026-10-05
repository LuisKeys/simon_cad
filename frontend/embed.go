// Package frontend embeds the viewport and panels served by the Simon CAD UI. It is plain
// ES modules with a vendored Three.js, so there is no build step; the same files are what
// the Wails shell loads in its webview.
package frontend

import (
	"embed"
	"io/fs"
)

//go:embed index.html style.css *.js vendor
var files embed.FS

// FS returns the frontend files rooted at the package directory.
func FS() fs.FS { return files }
