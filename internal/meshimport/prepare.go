package meshimport

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
)

// Options tunes Prepare.
type Options struct {
	MaxTriangles int // 0 means DefaultMaxTriangles
	// FillHoles closes the holes of the mesh before validating it (see FillHoles). Off by default:
	// it invents surface, so the caller asks for it.
	FillHoles bool
	// DecimateTo, when positive and below the mesh's triangle count, reduces the accepted mesh to
	// about that many triangles (see Reduce).
	DecimateTo int
}

// Prepared is an accepted mesh in its canonical form.
type Prepared struct {
	ID     string // mesh_<hash of Data>: content-addressed, the same for ASCII and binary input
	Data   []byte // canonical binary STL
	Mesh   *Mesh
	Report Report
}

// IDPrefix starts every mesh id.
const IDPrefix = "mesh_"

// ValidID reports whether id has the shape of a mesh id, so it is safe to use in a file name.
func ValidID(id string) bool {
	h, ok := strings.CutPrefix(id, IDPrefix)
	if !ok || len(h) != 24 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && h == strings.ToLower(h)
}

// Prepare parses and validates an STL file. A mesh that is not an acceptable solid returns an
// *Error that says what is wrong and where; nothing of it is kept.
func Prepare(data []byte, opt Options) (*Prepared, error) {
	m, format, err := Parse(data, opt.MaxTriangles)
	if err != nil {
		return nil, err
	}
	var fill FillReport
	if opt.FillHoles {
		max := opt.MaxTriangles
		if max <= 0 {
			max = DefaultMaxTriangles
		}
		if fill, err = FillHoles(m, max); err != nil {
			return nil, err
		}
	}
	rep, err := Validate(m)
	if err != nil {
		return nil, err
	}
	rep.Format = format
	if fill.Holes > 0 {
		rep.Filled = &fill
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d holes were closed with %d triangles (the biggest has %d border edges)", fill.Holes, fill.Triangles, fill.Largest))
	}
	if opt.DecimateTo > 0 && opt.DecimateTo < m.Triangles() {
		red, err := Reduce(m, opt.DecimateTo)
		if err != nil {
			return nil, err
		}
		rep.Reduced, rep.Triangles, rep.Vertices, rep.Volume = red.Report.Reduced, red.Report.Triangles, red.Report.Vertices, red.Report.Volume
		rep.Min, rep.Max = red.Report.Min, red.Report.Max
		rep.Warnings = append(rep.Warnings, red.Report.Warnings...)
		m = red.Mesh
		return &Prepared{ID: red.ID, Data: red.Data, Mesh: m, Report: rep}, nil
	}
	return finish(m, rep), nil
}

func finish(m *Mesh, rep Report) *Prepared {
	canon := Encode(m)
	sum := sha256.Sum256(canon)
	return &Prepared{ID: IDPrefix + hex.EncodeToString(sum[:12]), Data: canon, Mesh: m, Report: rep}
}

// Reduction says what decimating a mesh did.
type Reduction struct {
	From         int     `json:"from"`
	To           int     `json:"to"`
	Requested    int     `json:"requested"`
	VolumeChange float64 `json:"volume_change"` // fraction of the original volume, signed
	Attempts     int     `json:"attempts,omitempty"`
}

// Reduce decimates a valid mesh to about target triangles and returns the result validated and in
// canonical form, like Prepare does. A reduction that makes the surface cross itself is retried
// with 30% more triangles, up to three times; if none works, or no edge can be collapsed any
// further, the error says so. m is not changed.
func Reduce(m *Mesh, target int) (*Prepared, error) {
	from := m.Triangles()
	v0 := Volume(m)
	var last error
	for attempt, t := 1, target; attempt <= 4 && t < from; attempt, t = attempt+1, int(float64(t)*1.3)+1 {
		d, err := Decimate(m, t)
		if err != nil {
			return nil, err
		}
		rep, err := Validate(d)
		if err != nil {
			last = err
			continue
		}
		rep.Format = "binary"
		rep.Reduced = &Reduction{From: from, To: d.Triangles(), Requested: target, VolumeChange: (rep.Volume - v0) / v0, Attempts: attempt}
		if d.Triangles() > target+target/10 {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("the mesh could only be reduced to %d triangles, not the %d asked for", d.Triangles(), target))
		}
		return finish(d, rep), nil
	}
	if last != nil {
		return nil, reject("reducing the mesh to about %d triangles damaged it (%v); ask for more triangles", target, last)
	}
	return nil, reject("the mesh has %d triangles already, not more than the %d asked for", from, target)
}

// Encode writes the canonical binary STL: fixed header, normals recomputed from the winding,
// zero attribute bytes.
func Encode(m *Mesh) []byte {
	n := m.Triangles()
	out := make([]byte, 84+50*n)
	copy(out, "simon_cad canonical STL")
	binary.LittleEndian.PutUint32(out[80:], uint32(n))
	put := func(o int, v float64) { binary.LittleEndian.PutUint32(out[o:], math.Float32bits(float32(v))) }
	for t := 0; t < n; t++ {
		p := m.tri(t)
		nv := p[1].sub(p[0]).cross(p[2].sub(p[0]))
		if l := nv.norm(); l > 0 {
			nv = vec{nv[0] / l, nv[1] / l, nv[2] / l}
		}
		o := 84 + 50*t
		for k := 0; k < 3; k++ {
			put(o+4*k, nv[k])
			for j := 0; j < 3; j++ {
				put(o+12+12*j+4*k, p[j][k])
			}
		}
	}
	return out
}

// Decode reads back a canonical mesh. The id is the content hash, so a stored file that was
// edited or corrupted is refused instead of silently changing the model.
func Decode(id string, data []byte) (*Mesh, error) {
	sum := sha256.Sum256(data)
	if got := IDPrefix + hex.EncodeToString(sum[:12]); got != id {
		return nil, fmt.Errorf("mesh %s: stored data does not match its id (found %s)", id, got)
	}
	m, _, err := Parse(data, 1<<22)
	return m, err
}
