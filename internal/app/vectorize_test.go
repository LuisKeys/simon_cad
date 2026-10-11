package app

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// drawing helpers: dark shapes on white, antialiased by 3x3 supersampling
type paper struct{ img *image.Gray }

func newPaper(w, h int) *paper {
	p := &paper{image.NewGray(image.Rect(0, 0, w, h))}
	for i := range p.img.Pix {
		p.img.Pix[i] = 255
	}
	return p
}

func (p *paper) ink(f func(x, y float64) bool) {
	b := p.img.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			n := 0
			for sy := 0; sy < 3; sy++ {
				for sx := 0; sx < 3; sx++ {
					if f(float64(x)+(float64(sx)+0.5)/3, float64(y)+(float64(sy)+0.5)/3) {
						n++
					}
				}
			}
			if n > 0 {
				v := int(p.img.GrayAt(x, y).Y) - n*255/9
				if v < 0 {
					v = 0
				}
				p.img.SetGray(x, y, color.Gray{uint8(v)})
			}
		}
	}
}

func (p *paper) disc(cx, cy, r float64) {
	p.ink(func(x, y float64) bool { return math.Hypot(x-cx, y-cy) <= r })
}

func (p *paper) hole(cx, cy, r float64) {
	b := p.img.Bounds()
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy) < r {
				p.img.SetGray(x, y, color.Gray{255})
			}
		}
	}
}

func segDist(px, py, ax, ay, bx, by float64) float64 {
	dx, dy := bx-ax, by-ay
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}

func (p *paper) stroke(pts [][2]float64, width float64) {
	p.ink(func(x, y float64) bool {
		for i := 0; i+1 < len(pts); i++ {
			if segDist(x, y, pts[i][0], pts[i][1], pts[i+1][0], pts[i+1][1]) <= width/2 {
				return true
			}
		}
		return false
	})
}

func (p *paper) save(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, p.img); err != nil {
		t.Fatal(err)
	}
	return path
}

func ellipsePts(cx, cy, rx, ry, rot float64) [][2]float64 {
	var out [][2]float64
	for a := 0.0; a <= 2*math.Pi+0.05; a += 0.12 {
		x, y := rx*math.Cos(a), ry*math.Sin(a)
		out = append(out, [2]float64{cx + x*math.Cos(rot) - y*math.Sin(rot), cy + x*math.Sin(rot) + y*math.Cos(rot)})
	}
	return out
}

// A filled logo with a counter becomes one sketch with a hole; extruding it gives the traced
// area times the depth.
func TestImageToPathsFilledRegionsToSolid(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	p := newPaper(400, 400)
	p.disc(200, 200, 150)
	p.hole(200, 200, 70)
	p.disc(200, 200, 30)
	path := p.save(t, "logo.png")

	r := resultMap(t, sw(t, a, "vectorize_image", fmt.Sprintf(`{"path":%q,"mode":"filled_regions","width_mm":40,"preview":true}`, path)))
	if r["regions"].(float64) != 2 || r["holes"].(float64) != 1 {
		t.Fatalf("trace: %v", r)
	}
	if !strings.Contains(r["preview_svg"].(string), "<svg") {
		t.Fatal("no preview")
	}
	within(t, "image size", r["size_mm"].([]any)[0].(float64), 40, 1e-9)

	o := sw(t, a, "image_to_paths", fmt.Sprintf(`{"id":"logo","path":%q,"mode":"filled_regions","width_mm":40}`, path))
	if !o.Committed {
		t.Fatal("a clean trace commits at once")
	}
	if n := a.Graph.Get("logo"); n == nil || n.Type != "sketch" || n.Group != "logo" {
		t.Fatalf("node %+v", n)
	}
	sw(t, a, "extrude_sketch", `{"id":"plaque","sketch":"logo","depth":3}`)
	// ring (r 15 mm outer, 7 mm inner) + a 3 mm disc inside the hole
	want := (math.Pi*(15*15-7*7) + math.Pi*3*3) * 3
	within(t, "plaque volume", vol(t, a), want, 0.02)
	assertWatertight(t, a, "plaque")
	// the traced shape is editable: the sketch regions can be extruded separately
	if o := a.ApplyTool("extrude_sketch", []byte(`{"sketch":"logo","region":"r2","depth":1,"id":"dot"}`), PolicyCommit); !o.OK {
		t.Fatalf("separate region: %s", o.Error)
	}
}

// A handwritten flower (strokes) becomes centerline paths carrying stroke widths; a wire swept
// along them is a valid solid.
func TestFlowerCenterlinesToSweptWires(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	p := newPaper(500, 600)
	p.stroke([][2]float64{{250, 580}, {245, 400}, {255, 300}}, 7)
	for k := 0; k < 5; k++ {
		ang := float64(k) * 2 * math.Pi / 5
		p.stroke(ellipsePts(250+85*math.Cos(ang), 230+85*math.Sin(ang), 55, 28, ang), 6)
	}
	path := p.save(t, "flower.png")

	r := resultMap(t, sw(t, a, "vectorize_image", fmt.Sprintf(`{"path":%q,"mode":"centerline","width_mm":100}`, path)))
	if r["regions"].(float64) != 0 {
		t.Fatalf("centerline must not return regions: %v", r)
	}
	if s := r["strokes"].(float64); s < 5 || s > 16 {
		t.Fatalf("strokes %v", s)
	}
	within(t, "pen width (mm)", r["median_stroke_width"].(float64), 6.5*100/500, 0.35)

	o := sw(t, a, "image_to_paths", fmt.Sprintf(`{"id":"flower","path":%q,"mode":"centerline","width_mm":100}`, path))
	if !o.Committed {
		t.Fatalf("%v", o.ChangeSet.Warnings)
	}
	var paths []string
	for _, n := range a.Graph.Nodes {
		if n.Type == "path" {
			paths = append(paths, n.ID)
			if n.Parameters["type"] != "bezier" {
				t.Fatalf("path %s type %v", n.ID, n.Parameters["type"])
			}
		}
	}
	if len(paths) < 5 {
		t.Fatalf("paths %v", paths)
	}
	sw(t, a, "create_profile", `{"id":"pen","shape":"circle","radius":0.6}`)
	made := 0
	for _, id := range paths {
		o := a.ApplyTool("create_sweep", []byte(fmt.Sprintf(`{"id":"w_%s","path":%q,"profile":"pen","cap_start":"round","cap_end":"round"}`, id, id)), PolicyCommit)
		if o.OK {
			made++
		}
	}
	if made < len(paths)/2 {
		t.Fatalf("only %d of %d traced strokes could be swept", made, len(paths))
	}
}

func TestImageToPathsOutlineAndSVG(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	p := newPaper(300, 300)
	p.disc(150, 150, 100)
	p.hole(150, 150, 50)
	path := p.save(t, "ring.png")
	o := sw(t, a, "image_to_paths", fmt.Sprintf(`{"id":"ring","path":%q,"mode":"outline","px_per_mm":10}`, path))
	if !o.Committed {
		t.Fatal("not committed")
	}
	closed := 0
	for _, n := range a.Graph.Nodes {
		if n.Type == "path" && n.Parameters["closed"] == true {
			closed++
		}
	}
	if closed != 2 {
		t.Fatalf("outline gives the outer and the inner boundary: %d closed paths", closed)
	}
	l := pathLength(t, a, "ring_path")
	if l < 2*math.Pi*5*0.95 || l > 2*math.Pi*10*1.05 {
		t.Fatalf("outline length %v", l)
	}

	// inline SVG → sketch → extrude
	svg := `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 60'><rect x='10' y='10' width='80' height='40' fill='#000'/><circle cx='50' cy='30' r='10' fill='#fff'/></svg>`
	_ = svg
	holeSVG := `<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 60'><path fill-rule='evenodd' d='M10 10H90V50H10Z M40 30A10 10 0 1 0 60 30A10 10 0 1 0 40 30Z'/></svg>`
	sw(t, a, "image_to_paths", fmt.Sprintf(`{"id":"plate","svg":%q,"mode":"filled_regions","width_mm":100}`, holeSVG))
	sw(t, a, "extrude_sketch", `{"id":"slab","sketch":"plate","depth":2}`)
	within(t, "slab volume", vol(t, a), (80*40-math.Pi*100)*2, 0.01)
}

func TestImageToPathsErrorsAreActionable(t *testing.T) {
	a := New("demo", clock)
	defer a.Close()
	p := newPaper(100, 100)
	p.disc(50, 50, 30)
	path := p.save(t, "dot.png")
	blank := newPaper(100, 100).save(t, "blank.png")
	h := a.Graph.Hash()
	for _, c := range []struct{ tool, args, code string }{
		{"image_to_paths", fmt.Sprintf(`{"path":%q}`, path), "SCALE_REQUIRED"},
		{"image_to_paths", fmt.Sprintf(`{"path":%q,"width_mm":10,"px_per_mm":5}`, path), ""},
		{"image_to_paths", fmt.Sprintf(`{"path":%q,"width_mm":10}`, blank), "VECTORIZE_EMPTY"},
		{"image_to_paths", `{"width_mm":10}`, ""},
		{"image_to_paths", fmt.Sprintf(`{"path":%q,"image_id":"img_x","width_mm":10}`, path), ""},
		{"vectorize_image", `{"path":"/nonexistent/x.png"}`, ""},
		{"vectorize_image", `{"path":"notes.txt"}`, ""},
		{"vectorize_image", `{"image_id":"img_nope"}`, ""},
		{"vectorize_image", `{"svg":"<html/>"}`, ""},
		{"vectorize_image", fmt.Sprintf(`{"path":%q,"mode":"blobs"}`, path), ""},
	} {
		o := a.ApplyTool(c.tool, []byte(c.args), PolicyCommit)
		if o.OK {
			t.Errorf("%s %s should fail", c.tool, c.args)
			continue
		}
		if c.code != "" && o.Code() != c.code {
			t.Errorf("%s: code %q want %q (%s)", c.tool, o.Code(), c.code, o.Error)
		}
		if strings.TrimSpace(o.Error) == "" {
			t.Errorf("%s: empty error", c.tool)
		}
	}
	if a.Graph.Hash() != h {
		t.Fatal("failed traces must not change the model")
	}
	// the preview works without a scale (pixels) and never changes the model
	r := resultMap(t, sw(t, a, "vectorize_image", fmt.Sprintf(`{"path":%q}`, path)))
	if r["units"] != "px" || a.Graph.Hash() != h {
		t.Fatalf("%v", r)
	}
}

func TestTracedGeometrySurvivesUndoAndReload(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	a := New("demo", clock)
	p := newPaper(300, 300)
	p.disc(150, 150, 100)
	path := p.save(t, "dot.png")
	h0 := a.Graph.Hash()
	sw(t, a, "image_to_paths", fmt.Sprintf(`{"id":"dot","path":%q,"width_mm":30}`, path))
	h1 := a.Graph.Hash()
	if _, err := a.Undo(); err != nil || a.Graph.Hash() != h0 {
		t.Fatalf("undo: %v", err)
	}
	if _, err := a.Redo(); err != nil || a.Graph.Hash() != h1 {
		t.Fatalf("redo: %v", err)
	}
	sw(t, a, "extrude_sketch", `{"id":"disc","sketch":"dot","depth":2}`)
	v := vol(t, a)
	if err := a.Save(dir); err != nil {
		t.Fatal(err)
	}
	a.Close()
	b, err := Open(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !near(vol(t, b), v) {
		t.Fatal("reloaded project differs")
	}
	within(t, "disc volume", v, math.Pi*10*10*2, 0.015)
}
