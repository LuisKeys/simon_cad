package visual

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
	"strings"
)

// Render modes.
const (
	ModeShaded     = "shaded"
	ModeSilhouette = "silhouette" // object white, background black
)

// Mesh is one body to draw: float32 triangle soup as the tessellator produces it.
type Mesh struct {
	ID        string
	Positions []float32
	Normals   []float32 // optional, per vertex
	Indices   []uint32
	Color     string  // "#RRGGBB" ("" = default grey)
	Opacity   float64 // 0 means opaque
}

// DefaultColor is used for bodies without an appearance color.
const DefaultColor = "#B4BBC4"

// Options control one render.
type Options struct {
	Width, Height int
	Mode          string
}

// Image is a rendered view: pixels plus the per-pixel coverage mask.
type Image struct {
	Camera Camera
	RGBA   *image.RGBA
	Mask   *Mask // pixels covered by the model
}

// PNG encodes the image.
func (im *Image) PNG() ([]byte, error) {
	var b bytes.Buffer
	if err := png.Encode(&b, im.RGBA); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Rasterize renders meshes from a camera with a z-buffer. Shading is Lambert with a key and
// a camera headlight plus ambient; silhouette edges and creases are outlined so shape reads
// clearly for a vision model.
func Rasterize(meshes []Mesh, cam Camera, frame Frame, opt Options) (*Image, error) {
	w, h := opt.Width, opt.Height
	if w <= 0 || h <= 0 || w > 4096 || h > 4096 {
		return nil, fmt.Errorf("resolution %dx%d is out of range (1..4096)", w, h)
	}
	mode := opt.Mode
	if mode == "" {
		mode = ModeShaded
	}
	if mode != ModeShaded && mode != ModeSilhouette {
		return nil, fmt.Errorf("unknown render mode %q (shaded or silhouette)", mode)
	}
	b := cam.basis(frame, float64(w)/float64(h))
	depth := make([]float64, w*h)
	shade := make([]float64, w*h)
	owner := make([]int32, w*h)
	for i := range depth {
		depth[i] = math.Inf(1)
		owner[i] = -1
	}
	key := norm([3]float64{0.45, 0.35, 0.82})
	for mi, m := range meshes {
		n := len(m.Positions) / 3
		sx := make([]float64, n)
		sy := make([]float64, n)
		sz := make([]float64, n)
		vis := make([]bool, n)
		for i := 0; i < n; i++ {
			p := [3]float64{float64(m.Positions[3*i]), float64(m.Positions[3*i+1]), float64(m.Positions[3*i+2])}
			x, y, d, ok := b.project(p)
			sx[i], sy[i], sz[i], vis[i] = (x+1)/2*float64(w), (1-y)/2*float64(h), d, ok
		}
		for t := 0; t+2 < len(m.Indices); t += 3 {
			i0, i1, i2 := int(m.Indices[t]), int(m.Indices[t+1]), int(m.Indices[t+2])
			if i0 >= n || i1 >= n || i2 >= n || !vis[i0] || !vis[i1] || !vis[i2] {
				continue
			}
			// Face normal from the triangle (both sides lit: meshes may be open).
			p0 := vec(m.Positions, i0)
			fn := norm(cross(sub(vec(m.Positions, i1), p0), sub(vec(m.Positions, i2), p0)))
			if dot(fn, b.fwd) > 0 {
				fn = [3]float64{-fn[0], -fn[1], -fn[2]}
			}
			light := 0.28 + 0.5*math.Max(0, dot(fn, key)) + 0.32*math.Max(0, -dot(fn, b.fwd))
			rasterTri(sx[i0], sy[i0], sz[i0], sx[i1], sy[i1], sz[i1], sx[i2], sy[i2], sz[i2], w, h, func(idx int, z float64) {
				if z < depth[idx] {
					depth[idx], shade[idx], owner[idx] = z, light, int32(mi)
				}
			})
		}
	}
	out := &Image{Camera: cam, RGBA: image.NewRGBA(image.Rect(0, 0, w, h)), Mask: NewMask(w, h)}
	cols := make([][3]float64, len(meshes))
	for i, m := range meshes {
		cols[i] = parseColor(m.Color)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			o := owner[idx]
			if o >= 0 {
				out.Mask.Set(x, y, true)
			}
			if mode == ModeSilhouette {
				if o >= 0 {
					out.RGBA.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
				} else {
					out.RGBA.SetRGBA(x, y, color.RGBA{0, 0, 0, 255})
				}
				continue
			}
			if o < 0 {
				g := uint8(236 - 22*y/h) // soft vertical gradient background
				out.RGBA.SetRGBA(x, y, color.RGBA{g, g, g + 3, 255})
				continue
			}
			c, l := cols[o], shade[idx]
			if edge(depth, owner, w, h, x, y, frame.Radius) {
				l *= 0.35
			}
			out.RGBA.SetRGBA(x, y, color.RGBA{clamp8(c[0] * l), clamp8(c[1] * l), clamp8(c[2] * l), 255})
		}
	}
	return out, nil
}

// edge marks outline pixels: the model's boundary, a change of body, or a depth step.
func edge(depth []float64, owner []int32, w, h, x, y int, radius float64) bool {
	idx := y*w + x
	for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
		nx, ny := x+d[0], y+d[1]
		if nx < 0 || ny < 0 || nx >= w || ny >= h {
			continue
		}
		j := ny*w + nx
		if owner[j] != owner[idx] || math.Abs(depth[j]-depth[idx]) > radius*0.04 {
			return true
		}
	}
	return false
}

// rasterTri fills a screen-space triangle (pixel centres), interpolating depth.
func rasterTri(x0, y0, z0, x1, y1, z1, x2, y2, z2 float64, w, h int, put func(idx int, z float64)) {
	area := (x1-x0)*(y2-y0) - (x2-x0)*(y1-y0)
	if math.Abs(area) < 1e-12 {
		return
	}
	minX := int(math.Max(0, math.Floor(math.Min(x0, math.Min(x1, x2)))))
	maxX := int(math.Min(float64(w-1), math.Ceil(math.Max(x0, math.Max(x1, x2)))))
	minY := int(math.Max(0, math.Floor(math.Min(y0, math.Min(y1, y2)))))
	maxY := int(math.Min(float64(h-1), math.Ceil(math.Max(y0, math.Max(y1, y2)))))
	for py := minY; py <= maxY; py++ {
		cy := float64(py) + 0.5
		for px := minX; px <= maxX; px++ {
			cx := float64(px) + 0.5
			w0 := ((x1-cx)*(y2-cy) - (x2-cx)*(y1-cy)) / area
			w1 := ((x2-cx)*(y0-cy) - (x0-cx)*(y2-cy)) / area
			w2 := 1 - w0 - w1
			if w0 < -1e-9 || w1 < -1e-9 || w2 < -1e-9 {
				continue
			}
			put(py*w+px, w0*z0+w1*z1+w2*z2)
		}
	}
}

func vec(p []float32, i int) [3]float64 {
	return [3]float64{float64(p[3*i]), float64(p[3*i+1]), float64(p[3*i+2])}
}
func sub(a, b [3]float64) [3]float64 { return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }

func clamp8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v)
}

func parseColor(s string) [3]float64 {
	if s == "" {
		s = DefaultColor
	}
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return parseColor(DefaultColor)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return parseColor(DefaultColor)
	}
	return [3]float64{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}
}
