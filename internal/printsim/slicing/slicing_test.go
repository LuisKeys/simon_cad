package slicing

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/accelone/simoncad/internal/printsim/profile"
	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

type stub struct {
	name string
	err  error
}

func (s stub) Name() string     { return s.name }
func (s stub) Available() error { return s.err }
func (s stub) Slice(context.Context, Input, profile.PrintProfile) (*toolpath.SliceResult, error) {
	return nil, nil
}

func TestPickAutoFallsBackWithWarning(t *testing.T) {
	missing := fmt.Errorf("%w: not installed", ErrUnavailable)
	r := NewRegistry(stub{name: "prusaslicer", err: missing}, stub{name: "builtin"})
	s, d, err := r.Pick("auto")
	if err != nil || s.Name() != "builtin" {
		t.Fatalf("got %v %v", s, err)
	}
	if d == nil || d.Level != toolpath.LevelWarning {
		t.Fatalf("fallback must be reported: %+v", d)
	}
}

func TestPickPrefersFirstAvailable(t *testing.T) {
	r := NewRegistry(stub{name: "prusaslicer"}, stub{name: "builtin"})
	s, d, err := r.Pick("")
	if err != nil || s.Name() != "prusaslicer" || d != nil {
		t.Fatalf("%v %v %v", s, d, err)
	}
}

func TestPickExplicitUnavailableIsAnError(t *testing.T) {
	r := NewRegistry(stub{name: "prusaslicer", err: fmt.Errorf("%w: nope", ErrUnavailable)}, stub{name: "builtin"})
	if _, _, err := r.Pick("prusaslicer"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("an explicit choice must not silently fall back: %v", err)
	}
	if _, _, err := r.Pick("cura"); err == nil {
		t.Fatal("unknown provider must fail")
	}
}

func TestPickNothingAvailable(t *testing.T) {
	r := NewRegistry(stub{name: "prusaslicer", err: fmt.Errorf("%w: nope", ErrUnavailable)})
	if _, _, err := r.Pick("auto"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	st := r.Statuses()
	if len(st) != 1 || st[0].Available || st[0].Problem == "" {
		t.Fatalf("%+v", st)
	}
}
