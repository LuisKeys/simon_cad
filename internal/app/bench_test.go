package app

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Performance targets (docs/performance.md): a simple sweep < 100 ms, tracing a 1024 px image
// < 1 s, a simple stamp < 2 s. Run with: go test ./internal/app -run xxx -bench . -benchtime 5x

func benchApp(b *testing.B) *App {
	b.Helper()
	return New("bench", clock)
}

func BenchmarkSimpleSweep(b *testing.B) {
	for i := 0; i < b.N; i++ {
		a := benchApp(b)
		o := a.ApplyTool("create_tube", []byte(`{"id":"t","points":[[0,0,0],[20,10,0],[40,0,5],[60,-10,0],[80,0,0]],"outer_diameter":4}`), PolicyCommit)
		if !o.OK {
			b.Fatal(o.Error)
		}
		if _, err := a.Model(); err != nil {
			b.Fatal(err)
		}
		a.Close()
	}
}

func BenchmarkSweepRebuildAfterPathEdit(b *testing.B) {
	a := benchApp(b)
	defer a.Close()
	if o := a.ApplyTool("create_tube", []byte(`{"id":"t","points":[[0,0,0],[20,10,0],[40,0,5],[60,-10,0],[80,0,0]],"outer_diameter":4}`), PolicyCommit); !o.OK {
		b.Fatal(o.Error)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		o := a.ApplyTool("update_path", []byte(fmt.Sprintf(`{"path":"t_path","ops":[{"op":"move","index":2,"position":[40,%d,5]}]}`, i%7+1)), PolicyCommit)
		if !o.OK {
			b.Fatal(o.Error)
		}
		if _, err := a.Model(); err != nil {
			b.Fatal(err)
		}
	}
}

func benchGlyphFile(b *testing.B) string {
	b.Helper()
	img := image.NewGray(image.Rect(0, 0, 300, 400))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < 400; y++ {
		for x := 0; x < 300; x++ {
			if (x >= 60 && x <= 110 && y >= 50 && y <= 350) || (x >= 60 && x <= 240 && y >= 50 && y <= 100) || (x >= 60 && x <= 200 && y >= 190 && y <= 235) {
				img.Pix[y*img.Stride+x] = 0
			}
		}
	}
	path := filepath.Join(b.TempDir(), "glyph.png")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		b.Fatal(err)
	}
	return path
}

func BenchmarkSimpleStamp(b *testing.B) {
	img := benchGlyphFile(b)
	for i := 0; i < b.N; i++ {
		a := benchApp(b)
		o := a.ApplyTool("create_stamp", []byte(fmt.Sprintf(`{"id":"stamp","path":%q,"width":50,"height":50,"relief_height":1.5}`, img)), PolicyCommit)
		if !o.OK {
			b.Fatal(o.Error)
		}
		if _, err := a.Model(); err != nil {
			b.Fatal(err)
		}
		a.Close()
	}
}

func BenchmarkTextExtrude(b *testing.B) {
	for i := 0; i < b.N; i++ {
		a := benchApp(b)
		o := a.ApplyTool("create_text_geometry", []byte(`{"id":"word","text":"Simon CAD","size":12,"mode":"extrude","depth":3}`), PolicyCommit)
		if !o.OK {
			b.Fatal(o.Error)
		}
		if _, err := a.Model(); err != nil {
			b.Fatal(err)
		}
		a.Close()
	}
}
