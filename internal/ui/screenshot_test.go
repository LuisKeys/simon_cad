package ui

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScreenshotEndpointWritesToDownloads(t *testing.T) {
	srv, ts := testServer(t)
	dir := t.TempDir()
	srv.Svc.DownloadsDir = filepath.Join(dir, "Downloads")
	srv.Svc.Clock = func() time.Time { return time.Date(2026, 10, 28, 14, 5, 9, 0, time.Local) }
	srv.Svc.mu.Lock()
	srv.Svc.app.Meta.Name = "my bracket/v2"
	srv.Svc.mu.Unlock()

	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 4))); err != nil {
		t.Fatal(err)
	}
	if code, _ := post(t, ts.URL, "", "/api/screenshot", buf.Bytes()); code != 403 {
		t.Fatalf("a screenshot needs the token: %d", code)
	}
	if code, out := post(t, ts.URL, srv.Token, "/api/screenshot", []byte("not a png")); code != 400 {
		t.Fatalf("non-PNG accepted: %d %v", code, out)
	}
	want := filepath.Join(dir, "Downloads", "my_bracket_v2_20261028-140509.png")
	for _, p := range []string{want, want[:len(want)-4] + "_2.png"} { // the same second twice: no overwrite
		code, out := post(t, ts.URL, srv.Token, "/api/screenshot", buf.Bytes())
		if code != 200 || out["path"] != p || out["width"] != float64(8) {
			t.Fatalf("screenshot: %d %v (want %s)", code, out, p)
		}
		if b, err := os.ReadFile(p); err != nil || !bytes.Equal(b, buf.Bytes()) {
			t.Fatalf("%s not written as sent: %v", p, err)
		}
	}
}
