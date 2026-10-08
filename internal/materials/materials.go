// Package materials is the visual (PBR) material library. Visual materials are
// presentation only: they are referenced by id from cadir.Appearance and never reach the
// geometry kernel. Engineering properties (density, strength) are a separate concept.
package materials

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Textures names the optional PBR maps of a material, relative to its folder.
type Textures struct {
	BaseColor string `json:"baseColor,omitempty"`
	Normal    string `json:"normal,omitempty"`
	Roughness string `json:"roughness,omitempty"`
	Metalness string `json:"metalness,omitempty"`
	AO        string `json:"ao,omitempty"`
}

// License records where a material's textures came from; redistributable only.
type License struct {
	Source  string `json:"source,omitempty"`
	License string `json:"license,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Material is one entry of the library (the contents of material.json).
type Material struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Category     string   `json:"category"`
	Tags         []string `json:"tags,omitempty"`
	BaseColor    string   `json:"baseColor"`
	Metalness    float64  `json:"metalness"`
	Roughness    float64  `json:"roughness"`
	Opacity      *float64 `json:"opacity,omitempty"`
	Textures     Textures `json:"textures,omitempty"`
	TextureScale float64  `json:"textureScale,omitempty"`
	// Optional physically based extras (rendered with a physical material). All unitless
	// 0..1 unless noted; absent means "not used".
	Clearcoat          *float64 `json:"clearcoat,omitempty"`
	ClearcoatRoughness *float64 `json:"clearcoatRoughness,omitempty"`
	Transmission       *float64 `json:"transmission,omitempty"`
	IOR                *float64 `json:"ior,omitempty"`       // 1..2.333
	Thickness          *float64 `json:"thickness,omitempty"` // mm, for transmission
	Sheen              *float64 `json:"sheen,omitempty"`
	Anisotropy         *float64 `json:"anisotropy,omitempty"`
	Emissive           string   `json:"emissive,omitempty"`        // #RRGGBB
	EnvMapIntensity    *float64 `json:"envMapIntensity,omitempty"` // >= 0
	License            License  `json:"license,omitempty"`
	// Project is true for materials that come from a project's assets/materials folder.
	Project bool `json:"project,omitempty"`

	fsys fs.FS
	dir  string
}

// Categories in display order.
var Categories = []string{"Metals", "Plastics", "Rubber", "Carbon Fiber", "Glass", "Electronics", "Wood", "Generic"}

// Fallback is what an unknown or broken material id resolves to.
var Fallback = Material{ID: "generic_gray", Name: "Generic Gray", Category: "Generic",
	BaseColor: "#9AA0A6", Metalness: 0.25, Roughness: 0.45, TextureScale: 1}

var hexPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Validate checks a parsed material.json. Missing textures are not checked here (see Library.Warnings).
func (m *Material) Validate() error {
	switch {
	case m.ID == "" || strings.ContainsAny(m.ID, "/\\ ") || m.ID == "." || m.ID == "..":
		return fmt.Errorf("invalid material id %q", m.ID)
	case m.Name == "":
		return fmt.Errorf("material %q: name is required", m.ID)
	case !hexPattern.MatchString(m.BaseColor):
		return fmt.Errorf("material %q: baseColor %q is not #RRGGBB", m.ID, m.BaseColor)
	case !(m.Metalness >= 0 && m.Metalness <= 1):
		return fmt.Errorf("material %q: metalness %v out of [0,1]", m.ID, m.Metalness)
	case !(m.Roughness >= 0 && m.Roughness <= 1):
		return fmt.Errorf("material %q: roughness %v out of [0,1]", m.ID, m.Roughness)
	case m.Opacity != nil && !(*m.Opacity >= 0 && *m.Opacity <= 1):
		return fmt.Errorf("material %q: opacity out of [0,1]", m.ID)
	case m.TextureScale < 0:
		return fmt.Errorf("material %q: textureScale must be positive", m.ID)
	case m.Emissive != "" && !hexPattern.MatchString(m.Emissive):
		return fmt.Errorf("material %q: emissive %q is not #RRGGBB", m.ID, m.Emissive)
	}
	for _, f := range []struct {
		name     string
		v        *float64
		min, max float64
	}{
		{"clearcoat", m.Clearcoat, 0, 1}, {"clearcoatRoughness", m.ClearcoatRoughness, 0, 1},
		{"transmission", m.Transmission, 0, 1}, {"sheen", m.Sheen, 0, 1}, {"anisotropy", m.Anisotropy, 0, 1},
		{"ior", m.IOR, 1, 2.333}, {"thickness", m.Thickness, 0, math.MaxFloat64},
		{"envMapIntensity", m.EnvMapIntensity, 0, math.MaxFloat64},
	} {
		if f.v != nil && !(*f.v >= f.min && *f.v <= f.max) {
			return fmt.Errorf("material %q: %s %v out of [%v,%v]", m.ID, f.name, *f.v, f.min, f.max)
		}
	}
	return nil
}

// TextureFiles lists the declared texture file names by map kind.
func (m *Material) TextureFiles() map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{"baseColor": m.Textures.BaseColor, "normal": m.Textures.Normal,
		"roughness": m.Textures.Roughness, "metalness": m.Textures.Metalness, "ao": m.Textures.AO} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// Library is the set of known materials: built-in plus the open project's own, where a
// project material overrides a built-in one with the same id.
type Library struct {
	mu       sync.RWMutex
	builtin  fs.FS
	project  fs.FS
	byID     map[string]*Material
	warnings []string
}

// New loads the built-in library (fsys may be nil for an empty one).
func New(builtin fs.FS) *Library {
	l := &Library{builtin: builtin}
	l.Reload()
	return l
}

// SetProject sets (or with nil clears) the open project's materials and reloads.
func (l *Library) SetProject(fsys fs.FS) {
	l.mu.Lock()
	l.project = fsys
	l.mu.Unlock()
	l.Reload()
}

// Reload rescans both sources. Invalid materials are skipped and reported in Warnings.
func (l *Library) Reload() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byID = map[string]*Material{}
	l.warnings = nil
	l.scan(l.builtin, false)
	l.scan(l.project, true)
}

func (l *Library) scan(fsys fs.FS, project bool) {
	if fsys == nil {
		return
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		l.warnings = append(l.warnings, "read library: "+err.Error())
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(fsys, path.Join(e.Name(), "material.json"))
		if err != nil {
			continue
		}
		var m Material
		if err := json.Unmarshal(raw, &m); err != nil {
			l.warnings = append(l.warnings, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if err := m.Validate(); err != nil {
			l.warnings = append(l.warnings, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if m.ID != e.Name() {
			l.warnings = append(l.warnings, fmt.Sprintf("%s: id %q does not match folder name", e.Name(), m.ID))
			continue
		}
		if m.TextureScale == 0 {
			m.TextureScale = 1
		}
		m.BaseColor = strings.ToUpper(m.BaseColor)
		m.Project, m.fsys, m.dir = project, fsys, e.Name()
		for kind, f := range m.TextureFiles() {
			if _, err := fs.Stat(fsys, path.Join(e.Name(), f)); err != nil {
				l.warnings = append(l.warnings, fmt.Sprintf("%s: missing %s texture %q (falling back to scalars)", m.ID, kind, f))
				switch kind {
				case "baseColor":
					m.Textures.BaseColor = ""
				case "normal":
					m.Textures.Normal = ""
				case "roughness":
					m.Textures.Roughness = ""
				case "metalness":
					m.Textures.Metalness = ""
				case "ao":
					m.Textures.AO = ""
				}
			}
		}
		l.byID[m.ID] = &m
	}
}

// Warnings returns problems found while loading (skipped materials, missing textures).
func (l *Library) Warnings() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]string(nil), l.warnings...)
}

// Get returns the material with the given id.
func (l *Library) Get(id string) (Material, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	m, ok := l.byID[id]
	if !ok {
		return Material{}, false
	}
	return *m, true
}

// Resolve always returns a usable material: unknown ids give Fallback and ok=false.
func (l *Library) Resolve(id string) (m Material, ok bool) {
	if m, ok = l.Get(id); ok {
		return m, true
	}
	return Fallback, false
}

// List returns materials sorted by category order then name; category "" means all
// (matched case-insensitively otherwise).
func (l *Library) List(category string) []Material {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Material
	for _, m := range l.byID {
		if category == "" || strings.EqualFold(m.Category, category) {
			out = append(out, *m)
		}
	}
	rank := func(c string) int {
		for i, k := range Categories {
			if k == c {
				return i
			}
		}
		return len(Categories)
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i].Category), rank(out[j].Category); ri != rj {
			return ri < rj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ReadFile returns a texture file of material id. Only files declared in material.json
// are served, which also rules out path traversal.
func (l *Library) ReadFile(id, name string) ([]byte, error) {
	l.mu.RLock()
	m, ok := l.byID[id]
	l.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown material %q", id)
	}
	for _, f := range m.TextureFiles() {
		if f == name {
			return fs.ReadFile(m.fsys, path.Join(m.dir, f))
		}
	}
	return nil, fmt.Errorf("material %q has no file %q", id, name)
}

// EngineeringDefaults maps an engineering material id to its default visual material.
// The user can override the visual material without touching the engineering one.
var EngineeringDefaults = map[string]string{
	"aluminum_6061": "brushed_aluminum",
	"abs":           "black_abs",
	"petg":          "petg_black",
	"steel":         "stainless_steel",
	"rubber":        "black_rubber",
}
