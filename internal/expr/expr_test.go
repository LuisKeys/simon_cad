package expr

import (
	"math"
	"strings"
	"testing"
)

func env(m map[string]float64) func(string) (float64, bool) {
	return func(n string) (float64, bool) { v, ok := m[n]; return v, ok }
}

func TestEval(t *testing.T) {
	e := env(map[string]float64{"base.width": 80, "t": 4})
	cases := map[string]float64{
		"1+2*3":              7,
		"(1+2)*3":            9,
		"-2^2":               -4,
		"base.width * 0.5":   40,
		"max(t, 2)+min(1,2)": 5,
		"sqrt(16)/2":         2,
		"2^3^2":              512,
	}
	for s, want := range cases {
		got, err := Eval(s, e)
		if err != nil || math.Abs(got-want) > 1e-9 {
			t.Errorf("%q = %v, %v; want %v", s, got, err, want)
		}
	}
}

func TestRejects(t *testing.T) {
	bad := []string{"1 +", "foo(", "os.Exit(1)", "1; 2", "a = 3", strings.Repeat("(", 100) + "1" + strings.Repeat(")", 100), strings.Repeat("1+", 400) + "1", "1/0", "unknown + 1", "$x"}
	for _, s := range bad {
		if _, err := Eval(s, env(nil)); err == nil {
			t.Errorf("%q should fail", s)
		}
	}
}

func TestVariables(t *testing.T) {
	v := Variables("base.dx*2 + max(t, base.dx) + pi")
	if len(v) != 2 || v[0] != "base.dx" || v[1] != "t" {
		t.Fatalf("got %v", v)
	}
	if NodeOf("base.dx") != "base" || NodeOf("t") != "" {
		t.Fatal("NodeOf")
	}
}
