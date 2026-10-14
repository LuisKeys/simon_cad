package rendering

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

// The same vectors are asserted by frontend/mesh-codec.test.mjs: they pin the wire format
// (little-endian, standard base64) on both sides.
const (
	goldenF32 = `"AACAPwAAIMDNzMw9"`
	goldenU32 = `"AAAAAAEAAAACAAAA/////w=="`
)

func TestPackedGoldenVectors(t *testing.T) {
	f, _ := json.Marshal(F32{1, -2.5, 0.1})
	if string(f) != goldenF32 {
		t.Fatalf("F32 = %s, want %s", f, goldenF32)
	}
	u, _ := json.Marshal(U32{0, 1, 2, math.MaxUint32})
	if string(u) != goldenU32 {
		t.Fatalf("U32 = %s, want %s", u, goldenU32)
	}
}

func TestPackedRoundTrip(t *testing.T) {
	type wrap struct {
		P F32 `json:"p"`
		I U32 `json:"i"`
	}
	in := wrap{P: F32{0, 1.5, -3.25, float32(math.Pi), float32(math.Inf(1)), 1e-30}, I: U32{0, 7, 1 << 31, math.MaxUint32}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out wrap
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip changed the data: %v != %v", out, in)
	}
}

func TestPackedEmptyAndNull(t *testing.T) {
	b, _ := json.Marshal(F32(nil))
	if string(b) != `""` {
		t.Fatalf("an empty buffer is the empty string, got %s", b)
	}
	var f F32
	if err := json.Unmarshal([]byte(`""`), &f); err != nil || len(f) != 0 {
		t.Fatalf("empty string: %v %v", f, err)
	}
	if err := json.Unmarshal([]byte(`null`), &f); err != nil || len(f) != 0 {
		t.Fatalf("null: %v %v", f, err)
	}
}

func TestPackedAcceptsLegacyArrays(t *testing.T) {
	var f F32
	var u U32
	if err := json.Unmarshal([]byte(`[1, 2.5, -3]`), &f); err != nil || !reflect.DeepEqual(f, F32{1, 2.5, -3}) {
		t.Fatalf("float array: %v %v", f, err)
	}
	if err := json.Unmarshal([]byte(`[0, 1, 4294967295]`), &u); err != nil || !reflect.DeepEqual(u, U32{0, 1, math.MaxUint32}) {
		t.Fatalf("uint array: %v %v", u, err)
	}
}

func TestPackedRejectsBadInput(t *testing.T) {
	for name, tc := range map[string]struct {
		in  string
		dst any
	}{
		"not base64":      {`"@@@"`, new(F32)},
		"odd byte count":  {`"AAAA"`, new(F32)}, // 3 bytes
		"wrong type":      {`42`, new(U32)},
		"negative index":  {`[-1]`, new(U32)},
		"fractional":      {`[1.5]`, new(U32)},
		"too large index": {`[4294967296]`, new(U32)},
	} {
		if err := json.Unmarshal([]byte(tc.in), tc.dst); err == nil {
			t.Errorf("%s: %s was accepted", name, tc.in)
		}
	}
}

func TestBodyMeshUsesPackedBuffers(t *testing.T) {
	b, err := json.Marshal(&BodyMesh{ID: "a", Positions: []float32{1, -2.5, 0.1}, Indices: []uint32{0, 1, 2, math.MaxUint32}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"positions":`+goldenF32) || !strings.Contains(s, `"indices":`+goldenU32) {
		t.Fatalf("mesh buffers must be packed: %s", s)
	}
}

// realisticMesh is a lattice of n vertices with float-heavy coordinates, like a tessellated surface.
func realisticMesh(n int) *BodyMesh {
	m := &BodyMesh{ID: "b", Positions: make(F32, 3*n), Normals: make(F32, 3*n), Indices: make(U32, 0, 6*n)}
	for i := 0; i < n; i++ {
		// irrational factors: OCCT vertices are arbitrary doubles, so their float32 values need
		// 8-9 significant digits as text (short decimals would flatter the array form)
		x := float64(i%300) * math.Sqrt2 * 0.1371
		y := float64(i/300) * math.Pi * 0.0917
		z := math.Sin(x*0.3) * math.Cos(y*0.2) * math.E * 2.71
		m.Positions[3*i], m.Positions[3*i+1], m.Positions[3*i+2] = float32(x), float32(y), float32(z)
		m.Normals[3*i], m.Normals[3*i+1], m.Normals[3*i+2] = float32(math.Sin(x)), float32(math.Cos(y)), float32(math.Sqrt(0.5))
		if i%300 != 299 && i+301 < n {
			m.Indices = append(m.Indices, uint32(i), uint32(i+1), uint32(i+301), uint32(i), uint32(i+301), uint32(i+300))
		}
	}
	return m
}

// legacy is the mesh as the server used to send it: JSON arrays of numbers.
func legacy(m *BodyMesh) []byte {
	b, _ := json.Marshal(map[string]any{"positions": []float32(m.Positions), "normals": []float32(m.Normals), "indices": []uint32(m.Indices)})
	return b
}

func TestPackedMeshIsSmallerThanArrays(t *testing.T) {
	m := realisticMesh(60000)
	packed, _ := json.Marshal(m)
	old := legacy(m)
	t.Logf("%d triangles: %d KB as arrays, %d KB packed (%.0f%%)", len(m.Indices)/3, len(old)>>10, len(packed)>>10, 100*float64(len(packed))/float64(len(old)))
	if float64(len(packed)) > 0.75*float64(len(old)) {
		t.Fatalf("packed payload is %d bytes, more than 75%% of the %d bytes of the array form", len(packed), len(old))
	}
}

func BenchmarkMeshEncode(b *testing.B) {
	m := realisticMesh(60000)
	b.Run("arrays", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			legacy(m)
		}
	})
	b.Run("packed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = json.Marshal(m)
		}
	})
}
