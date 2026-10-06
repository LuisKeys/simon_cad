package frontend

import (
	"io/fs"
	"strings"
	"testing"
)

// The help microsite is opened in a new tab from the header, so it must ship in the binary
// and every local file it references must exist.
func TestHelpMicrositeIsEmbedded(t *testing.T) {
	b, err := fs.ReadFile(FS(), "help.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"help.css", "help.js", "assets/icon.png", "assets/mark.png"} {
		if !strings.Contains(string(b), ref) {
			t.Errorf("help.html does not reference %s", ref)
		}
		if _, err := fs.Stat(FS(), ref); err != nil {
			t.Errorf("%s is not embedded: %v", ref, err)
		}
	}
	idx, _ := fs.ReadFile(FS(), "index.html")
	if !strings.Contains(string(idx), `href="help.html" target="_blank"`) {
		t.Error("index.html has no Help link opening in a new tab")
	}
}
