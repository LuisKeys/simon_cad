// Package prusaslicer adapts the PrusaSlicer command line as a slicer provider.
//
// PrusaSlicer is AGPL-3.0. It is run as a separate program the user installed; SimonCAD
// neither links, bundles nor redistributes it. Its G-code is treated as untrusted input
// and parsed by the g-code interpreter, never executed.
package prusaslicer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/printsim/gcode"
	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/slicing"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

const (
	// DefaultTimeout bounds one slicing run.
	DefaultTimeout = 10 * time.Minute
	// logTail is how much of the slicer's output is kept for error messages.
	logTail = 8 << 10
)

// Slicer runs PrusaSlicer. The zero value looks the binary up on PATH and in the usual
// install locations.
type Slicer struct {
	// Path is the configured binary ("" to search).
	Path string
	// Timeout overrides DefaultTimeout.
	Timeout time.Duration
}

func (s *Slicer) Name() string { return slicing.ProviderPrusaSlicer }

var candidates = func() []string {
	c := []string{"prusa-slicer", "PrusaSlicer", "prusa-slicer-console"}
	if runtime.GOOS == "darwin" {
		c = append(c, "/Applications/PrusaSlicer.app/Contents/MacOS/PrusaSlicer")
	}
	return c
}()

// Locate finds the PrusaSlicer binary: the configured path first, then PATH and the
// standard macOS app bundle.
func (s *Slicer) Locate() (string, error) {
	if s.Path != "" {
		st, err := os.Stat(s.Path)
		if err != nil {
			return "", fmt.Errorf("%w: the configured PrusaSlicer path %q does not exist", slicing.ErrUnavailable, s.Path)
		}
		if st.IsDir() || st.Mode()&0o111 == 0 {
			return "", fmt.Errorf("%w: the configured PrusaSlicer path %q is not an executable file", slicing.ErrUnavailable, s.Path)
		}
		return s.Path, nil
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: PrusaSlicer was not found. %s", slicing.ErrUnavailable, installHint())
}

func installHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "Install it from https://www.prusa3d.com/page/prusaslicer_424/ (it is expected in /Applications/PrusaSlicer.app), or set the slicer path in the print settings. The built-in preview slicer can be used meanwhile."
	default:
		return "Install it (for example the AppImage from https://www.prusa3d.com/page/prusaslicer_424/ or your distribution's package), put prusa-slicer on PATH, or set the slicer path in the print settings. The built-in preview slicer can be used meanwhile."
	}
}

func (s *Slicer) Available() error {
	_, err := s.Locate()
	return err
}

// Version asks the binary for its version banner.
func (s *Slicer) Version(ctx context.Context) string {
	bin, err := s.Locate()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--help")
	isolate(cmd)
	out, _ := cmd.CombinedOutput()
	line, _, _ := strings.Cut(string(out), "\n")
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}

// tail keeps the last n bytes written to it.
type tail struct {
	buf []byte
	n   int
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (s *Slicer) Slice(ctx context.Context, in slicing.Input, p profile.PrintProfile) (*toolpath.SliceResult, error) {
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("invalid print profile: %w", err)
	}
	bin, err := s.Locate()
	if err != nil {
		return nil, err
	}
	if in.WorkDir == "" {
		return nil, errors.New("slicer work directory is not set")
	}
	cfg := filepath.Join(in.WorkDir, "profile.ini")
	out := filepath.Join(in.WorkDir, "out.gcode")
	data := filepath.Join(in.WorkDir, "datadir")
	if err := os.MkdirAll(data, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cfg, []byte(Config(p)), 0o600); err != nil {
		return nil, err
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Arguments are fixed flags plus paths we created: no shell, nothing user-controlled.
	cmd := exec.CommandContext(rctx, bin,
		"--datadir", data,
		"--export-gcode", "--dont-arrange",
		"--load", cfg,
		"--output", out,
		in.STLPath)
	cmd.Dir = in.WorkDir
	cmd.WaitDelay = 5 * time.Second
	isolate(cmd)
	log := &tail{n: logTail}
	cmd.Stdout, cmd.Stderr = log, log
	runErr := cmd.Run()
	switch {
	case ctx.Err() != nil: // cancelled by the caller
		return nil, ctx.Err()
	case errors.Is(rctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("PrusaSlicer did not finish within %s and was stopped", timeout)
	case runErr != nil:
		return nil, fmt.Errorf("PrusaSlicer failed: %v\n%s", runErr, bytes.TrimSpace(log.buf))
	}
	f, err := os.Open(out)
	if err != nil {
		return nil, fmt.Errorf("PrusaSlicer produced no G-code:\n%s", bytes.TrimSpace(log.buf))
	}
	defer f.Close()
	res, err := gcode.Parse(ctx, f, gcode.Options{})
	if err != nil {
		return nil, err
	}
	res.Provider = s.Name()
	if res.Version == "" {
		res.Version = s.Version(ctx)
	}
	return res, nil
}
