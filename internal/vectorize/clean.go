package vectorize

import "context"

// label returns a component label per pixel (0 = other value) for pixels equal to `want`, with
// the given connectivity (8 or 4), and the area and border-contact of every component.
type components struct {
	label   []int32
	area    []int
	touches []bool // touches the image border
}

func label(ctx context.Context, m *Mask, want bool, conn8 bool) (*components, error) {
	w, h := m.W, m.H
	c := &components{label: make([]int32, w*h), area: []int{0}, touches: []bool{false}}
	stack := make([]int32, 0, 1024)
	next := int32(1)
	for start := 0; start < w*h; start++ {
		if m.Pix[start] != want || c.label[start] != 0 {
			continue
		}
		if start&0xffff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		id := next
		next++
		c.area = append(c.area, 0)
		c.touches = append(c.touches, false)
		stack = append(stack[:0], int32(start))
		c.label[start] = id
		for len(stack) > 0 {
			p := int(stack[len(stack)-1])
			stack = stack[:len(stack)-1]
			x, y := p%w, p/w
			c.area[id]++
			if x == 0 || y == 0 || x == w-1 || y == h-1 {
				c.touches[id] = true
			}
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if (dx == 0 && dy == 0) || (!conn8 && dx != 0 && dy != 0) {
						continue
					}
					nx, ny := x+dx, y+dy
					if nx < 0 || ny < 0 || nx >= w || ny >= h {
						continue
					}
					q := ny*w + nx
					if m.Pix[q] == want && c.label[q] == 0 {
						c.label[q] = id
						stack = append(stack, int32(q))
					}
				}
			}
		}
	}
	return c, nil
}

// Clean removes foreground specks smaller than minArea pixels and fills background holes
// smaller than minHole pixels that are enclosed by foreground (noise in a filled shape). It
// returns how many of each it removed.
func Clean(ctx context.Context, m *Mask, minArea, minHole int) (specks, holes int, err error) {
	if minArea > 1 {
		c, err := label(ctx, m, true, true)
		if err != nil {
			return 0, 0, err
		}
		for i, l := range c.label {
			if l != 0 && c.area[l] < minArea {
				m.Pix[i] = false
			}
		}
		for id := 1; id < len(c.area); id++ {
			if c.area[id] < minArea {
				specks++
			}
		}
	}
	if minHole > 1 {
		c, err := label(ctx, m, false, false)
		if err != nil {
			return specks, 0, err
		}
		for i, l := range c.label {
			if l != 0 && !c.touches[l] && c.area[l] < minHole {
				m.Pix[i] = true
			}
		}
		for id := 1; id < len(c.area); id++ {
			if !c.touches[id] && c.area[id] < minHole {
				holes++
			}
		}
	}
	return specks, holes, nil
}

// Dilate / Erode with a 3x3 square, for closing small gaps in strokes (Close = dilate, erode).
func morph(m *Mask, grow bool) *Mask {
	out := newMask(m.W, m.H)
	for y := 0; y < m.H; y++ {
		for x := 0; x < m.W; x++ {
			all, any := true, false
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					v := m.at(x+dx, y+dy)
					all = all && v
					any = any || v
				}
			}
			if grow {
				out.Pix[y*m.W+x] = any
			} else {
				out.Pix[y*m.W+x] = all
			}
		}
	}
	return out
}

// Close fills one-pixel gaps (dilate then erode).
func Close(m *Mask) *Mask { return morph(morph(m, true), false) }
