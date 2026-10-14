package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/accelone/simoncad/internal/materials"
	"github.com/accelone/simoncad/internal/vision"
)

// ---- images ----------------------------------------------------------------------------

// ImageInfo describes an uploaded image.
type ImageInfo struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Retained  bool   `json:"retained"`
}

func imageInfo(im *vision.Image) ImageInfo {
	return ImageInfo{ID: im.ID, MediaType: im.MediaType, Width: im.Width, Height: im.Height, Retained: im.Retained}
}

// UploadImage stores a PNG, JPEG or WebP image for this session. It does not touch the model;
// the image is only saved with the project if it is explicitly retained.
func (s *Service) UploadImage(data []byte) (ImageInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, err := s.app.Images.Put(data)
	if err != nil {
		return ImageInfo{}, err
	}
	return imageInfo(im), nil
}

// ImageData returns an uploaded image's bytes and media type.
func (s *Service) ImageData(id string) ([]byte, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.app.Images.Get(id)
	if !ok {
		return nil, "", false
	}
	return im.Data, im.MediaType, true
}

// MaterialsView is the material library as the panels show it.
type MaterialsView struct {
	Categories []string             `json:"categories"`
	Materials  []materials.Material `json:"materials"`
	Warnings   []string             `json:"warnings,omitempty"`
	// Defaults maps an engineering material id to its default visual material id.
	Defaults map[string]string `json:"engineering_defaults"`
}

// Materials lists the visual materials available to the open project.
func (s *Service) Materials() MaterialsView {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.app.Materials
	return MaterialsView{Categories: materials.Categories, Materials: l.List(""), Warnings: l.Warnings(),
		Defaults: materials.EngineeringDefaults}
}

// MaterialFile returns one declared texture of a material and its content type.
func (s *Service) MaterialFile(id, name string) ([]byte, string, bool) {
	s.mu.Lock()
	l := s.app.Materials
	s.mu.Unlock()
	b, err := l.ReadFile(id, name)
	if err != nil {
		return nil, "", false
	}
	ct := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		ct = "image/jpeg"
	case ".png":
		ct = "image/png"
	case ".webp":
		ct = "image/webp"
	}
	return b, ct, true
}

// RetainImage chooses whether an image is stored inside the project (assets/images/).
func (s *Service) RetainImage(id string, keep bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.app.Images.Retain(id, keep); err != nil {
		return err
	}
	s.dirty = true
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// CloseInterpretation discards an image interpretation: it stays in the project history but
// stops being offered to the user and the agent.
func (s *Service) CloseInterpretation(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.app.Vision.Get(id); !ok {
		return fmt.Errorf("unknown interpretation %q", id)
	}
	s.app.Vision.Close(id)
	s.dirty = true
	s.hub.Publish(Notice{Kind: "state"})
	return nil
}

// ProposalView is an image interpretation as the panels show it.
type ProposalView struct {
	ID       string           `json:"id"`
	ImageID  string           `json:"image_id"`
	ImageW   int              `json:"image_w"`
	ImageH   int              `json:"image_h"`
	Revision int              `json:"revision"`
	Ready    bool             `json:"ready"`
	Used     []string         `json:"used,omitempty"`
	Closed   bool             `json:"closed,omitempty"`
	Proposal *vision.Proposal `json:"proposal"`
	Report   *vision.Report   `json:"report"`
}

func (s *Service) proposals() []ProposalView {
	out := []ProposalView{}
	for _, it := range s.app.Vision.List() {
		v := ProposalView{ID: it.ID, ImageID: it.Proposal.ImageID, Revision: it.Revision, Used: it.Used, Closed: it.Closed, Proposal: it.Proposal, Report: it.Report()}
		v.Ready = v.Report.Ready
		if im, ok := s.app.Images.Get(it.Proposal.ImageID); ok {
			v.ImageW, v.ImageH = im.Width, im.Height
		}
		out = append(out, v)
	}
	return out
}
