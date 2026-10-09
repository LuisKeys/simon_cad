package toolpath

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Binary layout (little endian), consumed directly by the front end:
//
//	header   16 B: magic "SCPS", version u32, nLayers u32, nSegments u32
//	summary  16 B: duration f32, filamentMM f32, filamentG f32, flags u32 (bit0 supports, bit1 preview)
//	bbox     24 B: 6 × f32
//	layers   16 B each: z f32, first u32, count u32, t0 f32
//	segments 40 B each: x0 y0 z0 x1 y1 z1 f32, t0 f32, dur f32, width f32, kind u8, tool u8, pad u16
const (
	magic       = "SCPS"
	version     = 1
	headerSize  = 16 + 16 + 24
	layerSize   = 16
	segmentSize = 40
)

// MaxSegments bounds what Encode/Decode and the parsers will handle, so a hostile or
// runaway G-code file cannot exhaust memory.
const MaxSegments = 8_000_000

// Encode serializes the result into the compact binary form.
func Encode(r *SliceResult) ([]byte, error) {
	if len(r.Segments) > MaxSegments {
		return nil, fmt.Errorf("toolpath has %d segments, over the limit of %d", len(r.Segments), MaxSegments)
	}
	buf := make([]byte, headerSize+len(r.Layers)*layerSize+len(r.Segments)*segmentSize)
	le := binary.LittleEndian
	copy(buf, magic)
	le.PutUint32(buf[4:], version)
	le.PutUint32(buf[8:], uint32(len(r.Layers)))
	le.PutUint32(buf[12:], uint32(len(r.Segments)))
	le.PutUint32(buf[16:], math.Float32bits(float32(r.Duration)))
	le.PutUint32(buf[20:], math.Float32bits(float32(r.FilamentMM)))
	le.PutUint32(buf[24:], math.Float32bits(float32(r.FilamentG)))
	var flags uint32
	if r.Supports {
		flags |= 1
	}
	if r.Preview {
		flags |= 2
	}
	le.PutUint32(buf[28:], flags)
	for i, v := range r.BBox {
		le.PutUint32(buf[32+4*i:], math.Float32bits(v))
	}
	o := headerSize
	for _, l := range r.Layers {
		le.PutUint32(buf[o:], math.Float32bits(l.Z))
		le.PutUint32(buf[o+4:], uint32(l.First))
		le.PutUint32(buf[o+8:], uint32(l.Count))
		le.PutUint32(buf[o+12:], math.Float32bits(l.T0))
		o += layerSize
	}
	for _, s := range r.Segments {
		for i, v := range [...]float32{s.X0, s.Y0, s.Z0, s.X1, s.Y1, s.Z1, s.T0, s.Dur, s.Width} {
			le.PutUint32(buf[o+4*i:], math.Float32bits(v))
		}
		buf[o+36] = byte(s.Kind)
		buf[o+37] = s.Tool
		o += segmentSize
	}
	return buf, nil
}

// Decode is the inverse of Encode. E and F are not part of the wire form (the front end
// needs only geometry, time and width), so they come back zero.
func Decode(b []byte) (*SliceResult, error) {
	if len(b) < headerSize || string(b[:4]) != magic {
		return nil, errors.New("not a toolpath buffer")
	}
	le := binary.LittleEndian
	if v := le.Uint32(b[4:]); v != version {
		return nil, fmt.Errorf("unsupported toolpath version %d", v)
	}
	nl, ns := int(le.Uint32(b[8:])), int(le.Uint32(b[12:]))
	if ns > MaxSegments || nl > ns+1 || len(b) != headerSize+nl*layerSize+ns*segmentSize {
		return nil, errors.New("toolpath buffer has an inconsistent size")
	}
	f32 := func(o int) float32 { return math.Float32frombits(le.Uint32(b[o:])) }
	r := &SliceResult{
		Duration: float64(f32(16)), FilamentMM: float64(f32(20)), FilamentG: float64(f32(24)),
		Supports: le.Uint32(b[28:])&1 != 0, Preview: le.Uint32(b[28:])&2 != 0,
		Layers: make([]Layer, nl), Segments: make([]Segment, ns),
	}
	for i := range r.BBox {
		r.BBox[i] = f32(32 + 4*i)
	}
	o := headerSize
	for i := range r.Layers {
		r.Layers[i] = Layer{Z: f32(o), First: int(le.Uint32(b[o+4:])), Count: int(le.Uint32(b[o+8:])), T0: f32(o + 12)}
		o += layerSize
	}
	for i := range r.Segments {
		k := Kind(b[o+36])
		if k >= kindCount {
			return nil, fmt.Errorf("segment %d has invalid kind %d", i, k)
		}
		r.Segments[i] = Segment{
			X0: f32(o), Y0: f32(o + 4), Z0: f32(o + 8), X1: f32(o + 12), Y1: f32(o + 16), Z1: f32(o + 20),
			T0: f32(o + 24), Dur: f32(o + 28), Width: f32(o + 32), Kind: k, Tool: b[o+37],
		}
		o += segmentSize
	}
	return r, r.Validate()
}
