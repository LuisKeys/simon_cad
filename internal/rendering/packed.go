package rendering

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
)

// F32 and U32 are mesh buffers that travel as the base64 of their little-endian bytes
// instead of a JSON array of numbers. Mesh floats need 8-9 digits as text, so the packed
// form is roughly 35% smaller, encodes about 2.5x faster here, and the browser decodes it
// natively instead of scanning it number by number (frontend/mesh-codec.js; measured in
// Chrome: 11 ms down to 2.5 ms for a 120k-triangle body). Decoding also accepts a plain
// JSON array, so older recordings and hand-written fixtures still load.
type F32 []float32

// U32 is the index-buffer counterpart of F32.
type U32 []uint32

func (f F32) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 4*len(f))
	for i, v := range f {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
	}
	return quotedBase64(buf), nil
}

func (f *F32) UnmarshalJSON(data []byte) error {
	raw, arr, err := unpack(data)
	if err != nil {
		return err
	}
	if arr != nil {
		out := make(F32, len(arr))
		for i, v := range arr {
			out[i] = float32(v)
		}
		*f = out
		return nil
	}
	if len(raw)%4 != 0 {
		return fmt.Errorf("packed float32 buffer of %d bytes is not a multiple of 4", len(raw))
	}
	out := make(F32, len(raw)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	*f = out
	return nil
}

func (u U32) MarshalJSON() ([]byte, error) {
	buf := make([]byte, 4*len(u))
	for i, v := range u {
		binary.LittleEndian.PutUint32(buf[4*i:], v)
	}
	return quotedBase64(buf), nil
}

func (u *U32) UnmarshalJSON(data []byte) error {
	raw, arr, err := unpack(data)
	if err != nil {
		return err
	}
	if arr != nil {
		out := make(U32, len(arr))
		for i, v := range arr {
			if v < 0 || v > math.MaxUint32 || v != math.Trunc(v) {
				return fmt.Errorf("index %v is not a uint32", v)
			}
			out[i] = uint32(v)
		}
		*u = out
		return nil
	}
	if len(raw)%4 != 0 {
		return fmt.Errorf("packed uint32 buffer of %d bytes is not a multiple of 4", len(raw))
	}
	out := make(U32, len(raw)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(raw[4*i:])
	}
	*u = out
	return nil
}

// quotedBase64 is the JSON string of buf; base64 never needs escaping.
func quotedBase64(buf []byte) []byte {
	out := make([]byte, 0, base64.StdEncoding.EncodedLen(len(buf))+2)
	out = append(out, '"')
	out = base64.StdEncoding.AppendEncode(out, buf)
	return append(out, '"')
}

// unpack reads either form: the decoded bytes of a base64 string, or the numbers of an array
// (null yields an empty result).
func unpack(data []byte) (raw []byte, arr []float64, err error) {
	if string(data) == "null" {
		return nil, nil, nil
	}
	if len(data) > 0 && data[0] == '[' {
		arr = []float64{}
		return nil, arr, json.Unmarshal(data, &arr)
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil, fmt.Errorf("mesh buffer must be a base64 string or an array: %w", err)
	}
	raw, err = base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, nil, fmt.Errorf("mesh buffer is not valid base64: %w", err)
	}
	return raw, nil, nil
}
