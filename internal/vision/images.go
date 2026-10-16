package vision

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg" // registers the JPEG decoder for image.DecodeConfig
	_ "image/png"  // registers the PNG decoder for image.DecodeConfig
	"os"
	"sync"
)

// MaxImageBytes and MaxImageSide bound what the server accepts.
const (
	MaxImageBytes = 20 << 20
	MaxImageSide  = 16384
)

// Image is an uploaded source image.
type Image struct {
	ID        string `json:"id"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Retained  bool   `json:"retained"`
	Data      []byte `json:"-"`
	// Path is the image's copy inside the project (assets/images/), "" while the project has
	// no directory yet. Once set, the image is read from there.
	Path string `json:"-"`
}

// Ext is the file extension the image is stored under.
func (im *Image) Ext() string {
	return map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp"}[im.MediaType]
}

// ImageStore keeps uploaded images for the session. They reach the project (assets/images/)
// when they are attached to a prompt or explicitly retained.
type ImageStore struct {
	mu    sync.Mutex
	items map[string]*Image
	order []string
}

func NewImageStore() *ImageStore { return &ImageStore{items: map[string]*Image{}} }

// Sniff identifies PNG, JPEG and WebP by content (never by file name) and reads the pixel size.
func Sniff(data []byte) (mediaType string, w, h int, err error) {
	switch {
	case len(data) == 0:
		return "", 0, 0, fmt.Errorf("empty image")
	case len(data) > MaxImageBytes:
		return "", 0, 0, fmt.Errorf("image is larger than %d MB", MaxImageBytes>>20)
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		mediaType = "image/png"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		mediaType = "image/jpeg"
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		mediaType = "image/webp"
	default:
		return "", 0, 0, fmt.Errorf("unsupported image type: use PNG, JPEG or WebP")
	}
	if mediaType == "image/webp" {
		w, h, err = webpSize(data)
	} else {
		var cfg image.Config
		cfg, _, err = image.DecodeConfig(bytes.NewReader(data))
		w, h = cfg.Width, cfg.Height
	}
	if err != nil {
		return "", 0, 0, fmt.Errorf("unreadable %s: %w", mediaType, err)
	}
	if w <= 0 || h <= 0 || w > MaxImageSide || h > MaxImageSide {
		return "", 0, 0, fmt.Errorf("image size %dx%d is outside 1..%d px", w, h, MaxImageSide)
	}
	return mediaType, w, h, nil
}

// webpSize reads the canvas size from the RIFF header (VP8, VP8L or VP8X chunk).
func webpSize(d []byte) (int, int, error) {
	if len(d) < 30 {
		return 0, 0, fmt.Errorf("truncated WebP")
	}
	switch string(d[12:16]) {
	case "VP8 ":
		if d[23] != 0x9d || d[24] != 0x01 || d[25] != 0x2a {
			return 0, 0, fmt.Errorf("bad VP8 frame header")
		}
		return int(binary.LittleEndian.Uint16(d[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(d[28:30]) & 0x3fff), nil
	case "VP8L":
		if d[20] != 0x2f {
			return 0, 0, fmt.Errorf("bad VP8L signature")
		}
		b := binary.LittleEndian.Uint32(d[21:25])
		return int(b&0x3fff) + 1, int((b>>14)&0x3fff) + 1, nil
	case "VP8X":
		w := int(d[24]) | int(d[25])<<8 | int(d[26])<<16
		h := int(d[27]) | int(d[28])<<8 | int(d[29])<<16
		return w + 1, h + 1, nil
	}
	return 0, 0, fmt.Errorf("unknown WebP chunk %q", d[12:16])
}

// Put stores an image (idempotent: the id is the content hash).
func (s *ImageStore) Put(data []byte) (*Image, error) {
	mt, w, h, err := Sniff(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	id := "img_" + hex.EncodeToString(sum[:])[:16]
	s.mu.Lock()
	defer s.mu.Unlock()
	if im, ok := s.items[id]; ok {
		return im, nil
	}
	im := &Image{ID: id, MediaType: mt, Width: w, Height: h, Data: append([]byte(nil), data...)}
	s.items[id] = im
	s.order = append(s.order, id)
	return im, nil
}

func (s *ImageStore) Get(id string) (*Image, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.items[id]
	return im, ok
}

// Retain marks an image to be saved with the project.
func (s *ImageStore) Retain(id string, keep bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.items[id]
	if !ok {
		return fmt.Errorf("unknown image %q", id)
	}
	im.Retained = keep
	return nil
}

// SetPath records the image's copy inside the project.
func (s *ImageStore) SetPath(id, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if im, ok := s.items[id]; ok {
		im.Path = path
	}
}

// Content returns the image's bytes: read from its copy inside the project when it has one
// (falling back to the uploaded bytes only if that file is gone), the uploaded bytes otherwise.
func (s *ImageStore) Content(id string) ([]byte, string, bool) {
	s.mu.Lock()
	im, ok := s.items[id]
	var path, mt string
	var data []byte
	if ok {
		path, mt, data = im.Path, im.MediaType, im.Data
	}
	s.mu.Unlock()
	if !ok {
		return nil, "", false
	}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			return b, mt, true
		}
	}
	return data, mt, true
}

// All lists images in upload order.
func (s *ImageStore) All() []*Image {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Image, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.items[id])
	}
	return out
}
