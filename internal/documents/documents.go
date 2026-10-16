// Package documents keeps the reference documents attached to the conversation (text
// specifications and PDFs). Like images, they are stored inside the project
// (assets/documents/) and, once there, read from the project's copy.
package documents

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Document is an attached reference document.
type Document struct {
	ID   string // doc_<hash of the name and the original bytes>
	Name string // the file name it was attached as
	Data []byte // the original file
	// Text is what the agent reads: the document itself, or what a model extracted from a PDF.
	Text        string
	ExtractedBy string // the model that extracted Text ("" for a text document)
	// Active documents are still attached to the conversation (replayed to the agent every
	// turn); inactive ones are kept only because a sent message shows them.
	Active bool
	// Path is the original's copy inside the project, "" while the project has no directory.
	Path string
}

// ID identifies a document by its name and content: the same file attached again is the
// same document, the same bytes under another name are another one.
func ID(name string, data []byte) string {
	sum := sha256.Sum256(append([]byte(name+"\x00"), data...))
	return "doc_" + hex.EncodeToString(sum[:])[:16]
}

var exts = map[string]bool{".txt": true, ".md": true, ".markdown": true, ".json": true, ".csv": true, ".yaml": true,
	".yml": true, ".xml": true, ".tsv": true, ".ini": true, ".toml": true, ".pdf": true}

// Ext is the extension the original is stored under: its own when it is a known document
// type, .pdf for a PDF, .txt otherwise (a file name never chooses an arbitrary extension).
func Ext(name string, data []byte) string {
	if strings.HasPrefix(string(data), "%PDF-") {
		return ".pdf"
	}
	if e := strings.ToLower(filepath.Ext(name)); exts[e] && e != ".pdf" {
		return e
	}
	return ".txt"
}

// MediaType is the content type the original is served with.
func (d *Document) MediaType() string {
	if Ext(d.Name, d.Data) == ".pdf" {
		return "application/pdf"
	}
	return "text/plain; charset=utf-8"
}

// Store holds the documents of one project.
type Store struct {
	mu    sync.Mutex
	items map[string]*Document
	order []string
}

func NewStore() *Store { return &Store{items: map[string]*Document{}} }

// Put adds a document. One already there (the same file attached again) is kept as it is,
// only gaining d's project copy if it had none.
func (s *Store) Put(d Document) *Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.items[d.ID]; ok {
		if cur.Path == "" {
			cur.Path = d.Path
		}
		return cur
	}
	c := d
	s.items[d.ID] = &c
	s.order = append(s.order, d.ID)
	return &c
}

func (s *Store) Get(id string) (*Document, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.items[id]
	return d, ok
}

// Attach attaches a document to the conversation with the text the agent reads.
func (s *Store) Attach(id, text, extractedBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.items[id]; ok {
		d.Text, d.ExtractedBy, d.Active = text, extractedBy, true
	}
}

// SetActive attaches a document to the conversation or detaches it.
func (s *Store) SetActive(id string, on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.items[id]; ok {
		d.Active = on
	}
}

// SetPath records the original's copy inside the project.
func (s *Store) SetPath(id, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.items[id]; ok {
		d.Path = path
	}
}

// Content returns the original's bytes, read from the project's copy when there is one.
func (s *Store) Content(id string) (*Document, []byte, bool) {
	s.mu.Lock()
	d, ok := s.items[id]
	var path string
	var data []byte
	if ok {
		path, data = d.Path, d.Data
	}
	s.mu.Unlock()
	if !ok {
		return nil, nil, false
	}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return d, b, true
		}
	}
	return d, data, true
}

// All lists the documents in the order they were attached.
func (s *Store) All() []*Document {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Document, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.items[id])
	}
	return out
}
