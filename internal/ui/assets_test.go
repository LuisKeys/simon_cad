package ui

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/accelone/simoncad/internal/agent/runtime"
	"github.com/accelone/simoncad/internal/persistence"
)

// Attachments are stored inside the project when it is saved, and a reopened project shows
// and reads them from there.
func TestAttachmentsAreStoredWithTheProject(t *testing.T) {
	s := newSvc(t)
	s.NewProvider = func() (runtime.Provider, error) { return &capProvider{name: "claude", caps: allCaps}, nil }
	if _, err := s.AddAttachment("spec.md", []byte("plate 80 x 60 mm")); err != nil {
		t.Fatal(err)
	}
	img, err := s.UploadImage(testPNG(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PromptWith("make the plate", false, []string{img.ID}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, s)
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if _, err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	st, _ := s.State()
	doc := st.Specs[0]
	imgFile := persistence.ImagePath(dir, img.ID, ".png")
	docFile := persistence.DocumentPath(dir, doc.ID, ".md")
	for _, f := range []string{imgFile, docFile} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("not stored in the project (no retain needed): %v", err)
		}
	}

	// The previews read the project's copies, not what was uploaded.
	if err := os.WriteFile(docFile, []byte("edited in the project"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _, _, ok := s.DocumentData(doc.ID); !ok || string(b) != "edited in the project" {
		t.Fatalf("document served from %q", b)
	}

	// Reopen: the message keeps its image and document, and the document is still attached.
	if err := s.Open(dir); err != nil {
		t.Fatal(err)
	}
	st, _ = s.State()
	var user *ChatMessage
	for i := range st.Chat {
		if st.Chat[i].Role == "user" {
			user = &st.Chat[i]
		}
	}
	if user == nil || len(user.Images) != 1 || user.Images[0] != img.ID || len(user.Docs) != 1 || user.Docs[0].Name != "spec.md" {
		t.Fatalf("reopened message: %+v", user)
	}
	if len(st.Specs) != 1 || st.Specs[0].ID != doc.ID {
		t.Fatalf("attached documents after reopening: %+v", st.Specs)
	}
	if b, _, ok := s.ImageData(img.ID); !ok || !bytes.Equal(b, testPNG(t)) {
		t.Fatal("the image must be readable from the project")
	}
	if err := s.RetainImage(img.ID, false); err == nil {
		t.Fatal("an image sent in the conversation is always stored")
	}

	// Attached to a project that has a directory: copied in at once, and processed from there.
	if _, err := s.AddAttachment("more.txt", []byte("hole 5 mm")); err != nil {
		t.Fatal(err)
	}
	st, _ = s.State()
	if len(st.Specs) != 2 {
		t.Fatalf("specs: %+v", st.Specs)
	}
	if _, err := os.Stat(persistence.DocumentPath(dir, st.Specs[1].ID, ".txt")); err != nil {
		t.Fatalf("a document attached to a saved project goes into it right away: %v", err)
	}

	// Detached documents not shown by any message leave the project on the next save.
	if err := s.RemoveSpec("more.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearChat(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(persistence.DocumentPath(dir, st.Specs[1].ID, ".txt")); !os.IsNotExist(err) {
		t.Fatal("a detached document no message shows must not be kept")
	}
	if _, err := os.Stat(docFile); err != nil {
		t.Fatal("a document a sent message shows stays in the project")
	}
}

// A PDF is stored with the text extracted from it, so reopening never re-extracts it.
func TestPDFIsStoredWithItsExtractedText(t *testing.T) {
	s := newSvc(t)
	claude := &capProvider{name: "claude", caps: allCaps, resps: []runtime.Response{
		{Content: []runtime.Block{{Type: "text", Text: "Plate 80 x 60 x 4 mm"}}}}}
	s.NewProvider = func() (runtime.Provider, error) { return claude, nil }
	s.NewRouter = func() *runtime.Router { return &runtime.Router{Selected: claude} }
	if _, err := s.AddAttachment("plate.pdf", []byte("%PDF-1.4 fake")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "p.scadproj")
	if _, err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.Open(dir); err != nil {
		t.Fatal(err)
	}
	st, _ := s.State()
	if len(st.Specs) != 1 || st.Specs[0].Name != "plate.pdf" {
		t.Fatalf("specs: %+v", st.Specs)
	}
	if b, mt, _, ok := s.DocumentData(st.Specs[0].ID); !ok || mt != "application/pdf" || string(b) != "%PDF-1.4 fake" {
		t.Fatalf("pdf: %q %s", b, mt)
	}
	if ctx := s.specContext(); !bytes.Contains([]byte(ctx), []byte("Plate 80 x 60 x 4 mm")) {
		t.Fatalf("spec context: %s", ctx)
	}
}

// The chat's document link: GET with the token in the query, served as stored.
func TestDocumentEndpoint(t *testing.T) {
	srv, ts := testServer(t)
	code, out := post(t, ts.URL, srv.Token, "/api/spec?name=spec.md", []byte("plate 4 mm"))
	if code != 200 {
		t.Fatalf("attach: %d %v", code, out)
	}
	id := out["id"].(string)
	res, err := http.Get(ts.URL + "/api/document/" + id + "?token=" + srv.Token)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(got) != "plate 4 mm" || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(res.Header.Get("Content-Disposition"), "spec.md") {
		t.Fatalf("download: %d %q %q", res.StatusCode, got, res.Header)
	}
	if res, _ = http.Get(ts.URL + "/api/document/" + id); res.StatusCode != http.StatusForbidden {
		t.Fatalf("download without token: %d", res.StatusCode)
	}
	if res, _ = http.Get(ts.URL + "/api/document/doc_nope?token=" + srv.Token); res.StatusCode != 404 {
		t.Fatalf("unknown document: %d", res.StatusCode)
	}
}
