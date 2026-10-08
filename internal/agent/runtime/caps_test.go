package runtime

import (
	"testing"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/tools"
)

// alwaysCap does not implement Conditional, so SelectCaps must always keep it.
type alwaysCap struct{ name string }

func (c alwaysCap) Name() string                             { return c.name }
func (c alwaysCap) SystemPrompt() string                     { return c.name + " prompt" }
func (c alwaysCap) ToolSubset(*tools.Registry) []*tools.Tool { return nil }
func (c alwaysCap) BuildContext(*app.App) (string, error)    { return "", nil }

// condCap is a capability that declares whether it is worth loading.
type condCap struct {
	alwaysCap
	relevant bool
	sawImgs  []string
}

func (c *condCap) Relevant(_ *app.App, images []string) bool {
	c.sawImgs = images
	return c.relevant
}

func capNames(caps []Capability) []string {
	out := make([]string, len(caps))
	for i, c := range caps {
		out[i] = c.Name()
	}
	return out
}

func TestSelectCapsDropsOnlyIrrelevantConditionalOnes(t *testing.T) {
	got := capNames(SelectCaps(nil, nil,
		alwaysCap{name: "geometry"},
		&condCap{alwaysCap{name: "wanted"}, true, nil},
		&condCap{alwaysCap{name: "idle"}, false, nil}))
	if len(got) != 2 || got[0] != "geometry" || got[1] != "wanted" {
		t.Fatalf("selected %v, want [geometry wanted]", got)
	}
}

func TestSelectCapsPassesTheTurnsImagesAndKeepsOrder(t *testing.T) {
	a, b := &condCap{alwaysCap{name: "a"}, true, nil}, &condCap{alwaysCap{name: "b"}, true, nil}
	got := capNames(SelectCaps(nil, []string{"img1"}, a, b))
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("order changed: %v", got)
	}
	for _, c := range []*condCap{a, b} {
		if len(c.sawImgs) != 1 || c.sawImgs[0] != "img1" {
			t.Fatalf("%s was not told about the attached image: %v", c.name, c.sawImgs)
		}
	}
}

func TestSelectCapsCanDropEverythingConditional(t *testing.T) {
	if got := SelectCaps(nil, nil, &condCap{alwaysCap{name: "a"}, false, nil}); len(got) != 0 {
		t.Fatalf("selected %v, want none", capNames(got))
	}
}
