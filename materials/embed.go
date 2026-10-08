// Package materialsdata embeds the built-in visual material library (one folder per
// material with a material.json and optional PBR textures).
package materialsdata

import (
	"embed"
	"io/fs"
)

//go:embed *
var files embed.FS

// FS returns the library rooted at the materials directory.
func FS() fs.FS { return files }
