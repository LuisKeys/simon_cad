package ui

import (
	"bytes"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxScreenshotBytes bounds a viewport capture (a 4K PNG of a busy scene is well under it).
const MaxScreenshotBytes = 64 << 20

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// screenshotName is "<project>_<YYYYMMDD-HHMMSS>.png" with the project name made safe for any file system.
func (s *Service) screenshotName(project string) string {
	name := strings.Trim(unsafeName.ReplaceAllString(strings.TrimSpace(project), "_"), "._-")
	if name == "" {
		name = "simoncad"
	}
	return fmt.Sprintf("%s_%s.png", name, s.Clock().Format("20060102-150405"))
}

// downloadsDir is ~/Downloads (or DownloadsDir when set), created if missing.
func (s *Service) downloadsDir() (string, error) {
	dir := s.DownloadsDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("no home folder to find Downloads in: %w", err)
		}
		dir = filepath.Join(home, "Downloads")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// SaveScreenshot writes a PNG capture of the viewport to the Downloads folder, named after
// the project and the time, and returns where it went. It never overwrites a file.
func (s *Service) SaveScreenshot(data []byte) (map[string]any, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the capture is not a PNG image")
	}
	s.mu.Lock()
	project := s.app.Meta.Name
	s.mu.Unlock()
	dir, err := s.downloadsDir()
	if err != nil {
		return nil, err
	}
	name := s.screenshotName(project)
	path := filepath.Join(dir, name)
	for i := 2; ; i++ { // two captures in the same second: name_..._2.png
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			path = filepath.Join(dir, fmt.Sprintf("%s_%d.png", strings.TrimSuffix(name, ".png"), i))
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(path)
			return nil, err
		}
		return map[string]any{"ok": true, "path": path, "width": cfg.Width, "height": cfg.Height, "bytes": len(data)}, nil
	}
}
