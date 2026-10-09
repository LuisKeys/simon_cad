package profile

import (
	"math"
	"strings"
	"testing"
)

func TestGenericIsValidAndLabelled(t *testing.T) {
	p := GenericFDM()
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Label, "not verified hardware") {
		t.Fatalf("default profile must be labelled as a simulation profile, got %q", p.Label)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*PrintProfile){
		"layer>nozzle": func(p *PrintProfile) { p.LayerHeight = 0.5 },
		"nan":          func(p *PrintProfile) { p.BuildX = math.NaN() },
		"walls":        func(p *PrintProfile) { p.Walls = 0 },
		"infill":       func(p *PrintProfile) { p.InfillPct = 101 },
		"pattern":      func(p *PrintProfile) { p.InfillPat = "zigzag" },
		"adhesion":     func(p *PrintProfile) { p.Adhesion = "raft" },
		"neg temp":     func(p *PrintProfile) { p.PrintTemp = -1 },
		"nozzle":       func(p *PrintProfile) { p.NozzleDiameter = 0 },
	}
	for name, mut := range cases {
		p := GenericFDM()
		mut(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestHashStability(t *testing.T) {
	a, b := GenericFDM(), GenericFDM()
	if a.Hash() != b.Hash() {
		t.Fatal("equal profiles must hash equal")
	}
	b.Label = "other"
	b.OverhangDeg = 30 // analysis-only fields must not change the slicing hash
	if a.Hash() != b.Hash() {
		t.Fatal("label and analysis thresholds must not affect the slicing hash")
	}
	b.InfillPct = 50
	if a.Hash() == b.Hash() {
		t.Fatal("a slicing setting must change the hash")
	}
}
