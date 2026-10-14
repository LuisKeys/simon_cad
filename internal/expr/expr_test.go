package expr

import (
	"fmt"
	"math"
	"strings"
	"sync"
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

func TestParseCacheServesIdenticalResultsAndErrors(t *testing.T) {
	env := func(n string) (float64, bool) { return map[string]float64{"base.dx": 10}[n], n == "base.dx" }
	for i := 0; i < 3; i++ { // the second and third calls come from the cache
		v, err := Eval("base.dx * 0.5 + sqrt(16)", env)
		if err != nil || v != 9 {
			t.Fatalf("call %d: %v %v", i, v, err)
		}
		if _, err := Eval("1 +", env); err == nil {
			t.Fatalf("call %d: a syntax error must stay an error", i)
		}
		if got := Variables("a.x + b.y"); len(got) != 2 {
			t.Fatalf("call %d: variables %v", i, got)
		}
	}
}

func TestParseCacheIsBounded(t *testing.T) {
	for i := 0; i < 2*maxCached+10; i++ {
		if _, err := Eval(fmt.Sprintf("%d + 1", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	parseMu.Lock()
	n := len(parseCache)
	parseMu.Unlock()
	if n > maxCached {
		t.Fatalf("cache holds %d entries, limit %d", n, maxCached)
	}
}

func TestParseCacheIsSafeForConcurrentUse(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				v, err := Eval(fmt.Sprintf("%d * 2", i%50), nil)
				if err != nil || v != float64(i%50*2) {
					t.Errorf("g%d i%d: %v %v", g, i, v, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkEvalRepeated(b *testing.B) {
	env := func(n string) (float64, bool) { return 10, true }
	for i := 0; i < b.N; i++ {
		if _, err := Eval("base.dx * 0.5 + sqrt(16) - max(1, 2, base.dy) / 4", env); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkParseUncached is what every Eval paid before parses were remembered.
func BenchmarkParseUncached(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := parse("base.dx * 0.5 + sqrt(16) - max(1, 2, base.dy) / 4"); err != nil {
			b.Fatal(err)
		}
	}
}
