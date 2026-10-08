package visual

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg" // reference photos
	"image/png"
	"math"
	"sort"
)

// Mask is a binary image: true = object.
type Mask struct {
	W, H int
	Px   []bool
}

func NewMask(w, h int) *Mask { return &Mask{W: w, H: h, Px: make([]bool, w*h)} }

func (m *Mask) At(x, y int) bool {
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return false
	}
	return m.Px[y*m.W+x]
}

func (m *Mask) Set(x, y int, v bool) { m.Px[y*m.W+x] = v }

// Count is the number of object pixels.
func (m *Mask) Count() int {
	n := 0
	for _, v := range m.Px {
		if v {
			n++
		}
	}
	return n
}

// BBox is the object's bounding box in pixels (max exclusive); ok=false when empty.
func (m *Mask) BBox() (x0, y0, x1, y1 int, ok bool) {
	x0, y0, x1, y1 = m.W, m.H, -1, -1
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			if m.Px[y*m.W+x] {
				x0, y0 = min(x0, x), min(y0, y)
				x1, y1 = max(x1, x), max(y1, y)
			}
		}
	}
	if x1 < 0 {
		return 0, 0, 0, 0, false
	}
	return x0, y0, x1 + 1, y1 + 1, true
}

// Mirror flips the mask horizontally.
func (m *Mask) Mirror() *Mask {
	out := NewMask(m.W, m.H)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			out.Px[y*m.W+x] = m.Px[y*m.W+(m.W-1-x)]
		}
	}
	return out
}

// PNG renders the mask white on black.
func (m *Mask) PNG() ([]byte, error) {
	img := image.NewGray(image.Rect(0, 0, m.W, m.H))
	for i, v := range m.Px {
		if v {
			img.Pix[i] = 255
		}
	}
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}

// ---- reference segmentation -----------------------------------------------------------

// Segment extracts the object silhouette from a reference image (PNG or JPEG): transparent
// pixels are background when the image has alpha; otherwise the background colour is
// estimated from the border and pixels far from it are the object. Small specks are
// removed, the largest component is kept and its holes are filled.
func Segment(data []byte) (*Mask, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode reference image: %w (use PNG or JPEG)", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 8 || h < 8 {
		return nil, fmt.Errorf("reference image is too small")
	}
	// Work at most 512 px wide: plenty for silhouettes, and fast.
	step := int(math.Ceil(float64(max(w, h)) / 512))
	mw, mh := w/step, h/step
	px := func(x, y int) (r, g, bl, a float64) {
		c := color.NRGBAModel.Convert(img.At(b.Min.X+x*step, b.Min.Y+y*step)).(color.NRGBA)
		return float64(c.R), float64(c.G), float64(c.B), float64(c.A)
	}
	m := NewMask(mw, mh)
	transparent := 0
	for y := 0; y < mh; y++ {
		for x := 0; x < mw; x++ {
			if _, _, _, a := px(x, y); a < 128 {
				transparent++
			}
		}
	}
	if transparent > mw*mh/20 { // a cut-out: alpha is the silhouette
		for y := 0; y < mh; y++ {
			for x := 0; x < mw; x++ {
				_, _, _, a := px(x, y)
				m.Set(x, y, a >= 128)
			}
		}
	} else {
		var border [][3]float64
		for x := 0; x < mw; x++ {
			for _, y := range []int{0, 1, mh - 2, mh - 1} {
				r, g, bl, _ := px(x, y)
				border = append(border, [3]float64{r, g, bl})
			}
		}
		for y := 0; y < mh; y++ {
			for _, x := range []int{0, 1, mw - 2, mw - 1} {
				r, g, bl, _ := px(x, y)
				border = append(border, [3]float64{r, g, bl})
			}
		}
		bg := medianColor(border)
		dists := make([]float64, len(border))
		for i, c := range border {
			dists[i] = colorDist(c, bg)
		}
		sort.Float64s(dists)
		thr := math.Max(38, dists[len(dists)*9/10]*1.6)
		for y := 0; y < mh; y++ {
			for x := 0; x < mw; x++ {
				r, g, bl, _ := px(x, y)
				m.Set(x, y, colorDist([3]float64{r, g, bl}, bg) > thr)
			}
		}
	}
	m = m.close(2).largestComponent().fillHoles()
	if m.Count() < mw*mh/400 {
		return nil, fmt.Errorf("no object silhouette found in the reference image (use a plain background)")
	}
	return m, nil
}

func colorDist(a, b [3]float64) float64 {
	return math.Sqrt(sq(a[0]-b[0]) + sq(a[1]-b[1]) + sq(a[2]-b[2]))
}

func medianColor(cs [][3]float64) [3]float64 {
	var out [3]float64
	for ch := 0; ch < 3; ch++ {
		v := make([]float64, len(cs))
		for i, c := range cs {
			v[i] = c[ch]
		}
		sort.Float64s(v)
		out[ch] = v[len(v)/2]
	}
	return out
}

// close is a morphological closing (dilate then erode) with a square of radius r.
func (m *Mask) close(r int) *Mask { return m.morph(r, true).morph(r, false) }

func (m *Mask) morph(r int, dilate bool) *Mask {
	out := NewMask(m.W, m.H)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			v := !dilate
			for dy := -r; dy <= r && v != dilate; dy++ {
				for dx := -r; dx <= r; dx++ {
					nx, ny := x+dx, y+dy
					in := nx >= 0 && ny >= 0 && nx < m.W && ny < m.H
					if dilate && in && m.Px[ny*m.W+nx] {
						v = true
						break
					}
					if !dilate && (!in || !m.Px[ny*m.W+nx]) {
						v = false
						break
					}
				}
			}
			out.Px[y*m.W+x] = v
		}
	}
	return out
}

// largestComponent keeps the biggest 4-connected object region.
func (m *Mask) largestComponent() *Mask {
	label := make([]int, len(m.Px))
	best, bestN, next := 0, 0, 1
	stack := []int{}
	for i, v := range m.Px {
		if !v || label[i] != 0 {
			continue
		}
		n := 0
		stack = append(stack[:0], i)
		label[i] = next
		for len(stack) > 0 {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n++
			x, y := j%m.W, j/m.W
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := x+d[0], y+d[1]
				if nx >= 0 && ny >= 0 && nx < m.W && ny < m.H {
					k := ny*m.W + nx
					if m.Px[k] && label[k] == 0 {
						label[k] = next
						stack = append(stack, k)
					}
				}
			}
		}
		if n > bestN {
			best, bestN = next, n
		}
		next++
	}
	out := NewMask(m.W, m.H)
	for i := range out.Px {
		out.Px[i] = best != 0 && label[i] == best
	}
	return out
}

// fillHoles marks as object every background pixel not connected to the image border.
func (m *Mask) fillHoles() *Mask {
	outside := make([]bool, len(m.Px))
	stack := []int{}
	push := func(x, y int) {
		k := y*m.W + x
		if !m.Px[k] && !outside[k] {
			outside[k] = true
			stack = append(stack, k)
		}
	}
	for x := 0; x < m.W; x++ {
		push(x, 0)
		push(x, m.H-1)
	}
	for y := 0; y < m.H; y++ {
		push(0, y)
		push(m.W-1, y)
	}
	for len(stack) > 0 {
		j := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		x, y := j%m.W, j/m.W
		for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
			nx, ny := x+d[0], y+d[1]
			if nx >= 0 && ny >= 0 && nx < m.W && ny < m.H {
				push(nx, ny)
			}
		}
	}
	out := NewMask(m.W, m.H)
	for i := range out.Px {
		out.Px[i] = !outside[i]
	}
	return out
}

// ---- silhouette matching ---------------------------------------------------------------

// NormSize is the side of the canvas masks are normalized onto before comparison.
const NormSize = 256

// Normalize crops the mask to its bounding box and scales it (aspect preserved) into a
// NormSize×NormSize canvas, centred. Comparing normalized masks removes differences of
// position and scale; differences of proportion remain, which is what matters.
func (m *Mask) Normalize() *Mask {
	out := NewMask(NormSize, NormSize)
	x0, y0, x1, y1, ok := m.BBox()
	if !ok {
		return out
	}
	bw, bh := float64(x1-x0), float64(y1-y0)
	s := float64(NormSize-4) / math.Max(bw, bh)
	ox, oy := (float64(NormSize)-bw*s)/2, (float64(NormSize)-bh*s)/2
	for y := 0; y < NormSize; y++ {
		for x := 0; x < NormSize; x++ {
			sx := int(float64(x0) + (float64(x)+0.5-ox)/s)
			sy := int(float64(y0) + (float64(y)+0.5-oy)/s)
			if sx >= x0 && sx < x1 && sy >= y0 && sy < y1 {
				out.Px[y*NormSize+x] = m.Px[sy*m.W+sx]
			}
		}
	}
	return out
}

// SilhouetteScore compares a reference silhouette with a rendered one.
type SilhouetteScore struct {
	IoU      float64 `json:"iou"`
	Contour  float64 `json:"contour"`    // 1 - mean contour distance / (10% of the canvas)
	BBox     float64 `json:"bbox"`       // 1 - relative aspect-ratio difference
	Score    float64 `json:"score"`      // blend used for ranking and thresholds
	Mirrored bool    `json:"mirrored"`   // the reference faces the other way
	RefAR    float64 `json:"ref_aspect"` // height / width of the reference bounding box
	GenAR    float64 `json:"gen_aspect"`
}

// CompareSilhouettes normalizes both masks and scores them; for side views (allowMirror)
// the mirrored reference is also tried, since photos may face either way.
func CompareSilhouettes(ref, gen *Mask, allowMirror bool) SilhouetteScore {
	rn, gn := ref.Normalize(), gen.Normalize()
	best := compareNorm(rn, gn)
	best.RefAR, best.GenAR = aspect(ref), aspect(gen)
	best.BBox = 1 - math.Min(1, math.Abs(best.RefAR-best.GenAR)/math.Max(best.RefAR, 1e-6))
	best.Score = blend(best)
	if allowMirror {
		mr := compareNorm(rn.Mirror(), gn)
		mr.RefAR, mr.GenAR, mr.BBox, mr.Mirrored = best.RefAR, best.GenAR, best.BBox, true
		mr.Score = blend(mr)
		if mr.Score > best.Score {
			best = mr
		}
	}
	return best
}

func blend(s SilhouetteScore) float64 { return round3(0.6*s.IoU + 0.25*s.Contour + 0.15*s.BBox) }

func aspect(m *Mask) float64 {
	x0, y0, x1, y1, ok := m.BBox()
	if !ok || x1 == x0 {
		return 0
	}
	return float64(y1-y0) / float64(x1-x0)
}

func compareNorm(a, b *Mask) SilhouetteScore {
	inter, union := 0, 0
	for i := range a.Px {
		if a.Px[i] && b.Px[i] {
			inter++
		}
		if a.Px[i] || b.Px[i] {
			union++
		}
	}
	s := SilhouetteScore{}
	if union > 0 {
		s.IoU = round3(float64(inter) / float64(union))
	}
	da, db := distanceToContour(a), distanceToContour(b)
	ca, cb := contour(a), contour(b)
	if len(ca) > 0 && len(cb) > 0 {
		sum := 0.0
		for _, i := range ca {
			sum += db[i]
		}
		for _, i := range cb {
			sum += da[i]
		}
		mean := sum / float64(len(ca)+len(cb))
		s.Contour = round3(math.Max(0, 1-mean/(0.1*NormSize)))
	}
	return s
}

// contour lists boundary pixels (object pixels with a background 4-neighbour).
func contour(m *Mask) []int {
	var out []int
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			if m.At(x, y) && (!m.At(x+1, y) || !m.At(x-1, y) || !m.At(x, y+1) || !m.At(x, y-1)) {
				out = append(out, y*m.W+x)
			}
		}
	}
	return out
}

// distanceToContour is a two-pass chamfer distance transform to the mask's contour.
func distanceToContour(m *Mask) []float64 {
	inf := math.Inf(1)
	d := make([]float64, m.W*m.H)
	for i := range d {
		d[i] = inf
	}
	for _, i := range contour(m) {
		d[i] = 0
	}
	const a, b = 1.0, math.Sqrt2
	at := func(x, y int) float64 {
		if x < 0 || y < 0 || x >= m.W || y >= m.H {
			return inf
		}
		return d[y*m.W+x]
	}
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			v := d[y*m.W+x]
			v = math.Min(v, at(x-1, y)+a)
			v = math.Min(v, at(x, y-1)+a)
			v = math.Min(v, at(x-1, y-1)+b)
			v = math.Min(v, at(x+1, y-1)+b)
			d[y*m.W+x] = v
		}
	}
	for y := m.H - 1; y >= 0; y-- {
		for x := m.W - 1; x >= 0; x-- {
			v := d[y*m.W+x]
			v = math.Min(v, at(x+1, y)+a)
			v = math.Min(v, at(x, y+1)+a)
			v = math.Min(v, at(x+1, y+1)+b)
			v = math.Min(v, at(x-1, y+1)+b)
			d[y*m.W+x] = v
		}
	}
	return d
}

// DifferencePNG overlays two normalized masks: both = grey, reference only = red (missing
// in the model), model only = blue (extra in the model).
func DifferencePNG(ref, gen *Mask, mirrored bool) ([]byte, error) {
	rn, gn := ref.Normalize(), gen.Normalize()
	if mirrored {
		rn = rn.Mirror()
	}
	img := image.NewRGBA(image.Rect(0, 0, NormSize, NormSize))
	for i := range rn.Px {
		var c color.RGBA
		switch {
		case rn.Px[i] && gn.Px[i]:
			c = color.RGBA{150, 150, 150, 255}
		case rn.Px[i]:
			c = color.RGBA{230, 70, 70, 255}
		case gn.Px[i]:
			c = color.RGBA{60, 120, 235, 255}
		default:
			c = color.RGBA{18, 18, 20, 255}
		}
		img.Set(i%NormSize, i/NormSize, c)
	}
	var b bytes.Buffer
	err := png.Encode(&b, img)
	return b.Bytes(), err
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
