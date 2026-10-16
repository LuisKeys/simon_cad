package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/accelone/simoncad/internal/documents"
	"github.com/accelone/simoncad/internal/persistence"
	"github.com/accelone/simoncad/internal/vision"
)

// Attachments (images and reference documents) live inside the project: assets/images/ and
// assets/documents/. A project that already has a directory gets a copy the moment something
// is attached, and from then on the attachment is read from that copy (chat previews,
// processing). A project without a directory yet keeps them in memory until its first save,
// which copies them in and switches to the project's copies.

// StoreImage adds an uploaded image and copies it into the project when it has a directory.
func (a *App) StoreImage(data []byte) (*vision.Image, error) {
	im, err := a.Images.Put(data)
	if err != nil {
		return nil, err
	}
	if a.dir != "" {
		p := persistence.ImagePath(a.dir, im.ID, im.Ext())
		if writeAsset(p, data) == nil {
			a.Images.SetPath(im.ID, p)
		}
	}
	return im, nil
}

// StoreDocument adds an attached document and copies its original into the project when it
// has a directory. The returned document's original should be read back with Docs.Content.
func (a *App) StoreDocument(d documents.Document) *documents.Document {
	if d.ID == "" {
		d.ID = documents.ID(d.Name, d.Data)
	}
	if a.dir != "" {
		p := persistence.DocumentPath(a.dir, d.ID, documents.Ext(d.Name, d.Data))
		if writeAsset(p, d.Data) == nil {
			d.Path = p
		}
	}
	return a.Docs.Put(d)
}

// ImageInConversation reports whether an image was attached to a prompt (so it is always
// stored with the project).
func (a *App) ImageInConversation(id string) bool {
	for _, p := range a.AH.Prompts {
		for _, x := range p.Images {
			if x == id {
				return true
			}
		}
	}
	return false
}

// writeAsset writes a content-addressed asset unless it is already there.
func writeAsset(path string, data []byte) error {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// savedAttachments lists what a save stores: every image attached to a prompt or retained
// by the user, and every document still attached or shown on a sent prompt.
func (a *App) savedAttachments() ([]persistence.Image, []persistence.Document) {
	usedDoc := map[string]bool{}
	for _, p := range a.AH.Prompts {
		for _, id := range p.Documents {
			usedDoc[id] = true
		}
	}
	var imgs []persistence.Image
	for _, im := range a.Images.All() {
		if !im.Retained && !a.ImageInConversation(im.ID) {
			continue
		}
		if data, _, ok := a.Images.Content(im.ID); ok && len(data) > 0 {
			imgs = append(imgs, persistence.Image{ID: im.ID, Ext: im.Ext(), Data: data})
		}
	}
	var docs []persistence.Document
	for _, d := range a.Docs.All() {
		if !d.Active && !usedDoc[d.ID] {
			continue
		}
		_, data, ok := a.Docs.Content(d.ID)
		if !ok || len(data) == 0 {
			continue
		}
		pd := persistence.Document{ID: d.ID, Name: d.Name, Ext: documents.Ext(d.Name, data), Data: data,
			ExtractedBy: d.ExtractedBy, Active: d.Active}
		if d.ExtractedBy != "" {
			pd.Extracted = d.Text
		}
		docs = append(docs, pd)
	}
	return imgs, docs
}

// attachmentsSavedIn points the stored attachments at their copies in dir.
func (a *App) attachmentsSavedIn(dir string, imgs []persistence.Image, docs []persistence.Document) {
	for _, im := range imgs {
		a.Images.SetPath(im.ID, persistence.ImagePath(dir, im.ID, im.Ext))
	}
	for _, d := range docs {
		a.Docs.SetPath(d.ID, persistence.DocumentPath(dir, d.ID, d.Ext))
	}
}

// loadAttachments puts a loaded project's stored images and documents in the stores, read
// from (and pointing at) the project's copies.
func (a *App) loadAttachments(dir string, p *persistence.Project) {
	for _, im := range p.Images {
		if put, err := a.Images.Put(im.Data); err == nil {
			_ = a.Images.Retain(put.ID, true)
			a.Images.SetPath(put.ID, persistence.ImagePath(dir, im.ID, im.Ext))
		}
	}
	for _, d := range p.Documents {
		text := d.Extracted
		if d.ExtractedBy == "" {
			text = strings.TrimSpace(string(d.Data))
		}
		a.Docs.Put(documents.Document{ID: d.ID, Name: d.Name, Data: d.Data, Text: text, ExtractedBy: d.ExtractedBy,
			Active: d.Active, Path: persistence.DocumentPath(dir, d.ID, d.Ext)})
	}
}
