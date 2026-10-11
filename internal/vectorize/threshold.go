package vectorize

import "math"

// Mask is a binary image: true = foreground.
type Mask struct {
	W, H int
	Pix  []bool
}

func newMask(w, h int) *Mask { return &Mask{W: w, H: h, Pix: make([]bool, w*h)} }

func (m *Mask) at(x, y int) bool {
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return false
	}
	return m.Pix[y*m.W+x]
}

// Count of foreground pixels.
func (m *Mask) Count() int {
	n := 0
	for _, v := range m.Pix {
		if v {
			n++
		}
	}
	return n
}

// Otsu returns the threshold that best separates the two modes of the histogram.
func Otsu(ink *Ink) int {
	var hist [256]int
	for _, v := range ink.Pix {
		hist[v]++
	}
	total := len(ink.Pix)
	var sum float64
	for i, c := range hist {
		sum += float64(i * c)
	}
	var wB, sumB, best float64
	thr := 128
	for t := 0; t < 256; t++ {
		wB += float64(hist[t])
		if wB == 0 {
			continue
		}
		wF := float64(total) - wB
		if wF == 0 {
			break
		}
		sumB += float64(t * hist[t])
		mB, mF := sumB/wB, (sum-sumB)/wF
		if between := wB * wF * (mB - mF) * (mB - mF); between > best {
			best, thr = between, t
		}
	}
	// pixels with ink > thr are foreground
	return thr
}

// ThresholdFixed marks pixels with ink >= t as foreground.
func ThresholdFixed(ink *Ink, t int) *Mask {
	m := newMask(ink.W, ink.H)
	for i, v := range ink.Pix {
		m.Pix[i] = int(v) >= t
	}
	return m
}

// ThresholdAdaptive marks a pixel foreground when it is darker than its neighbourhood mean (a
// square window of side `window`, via an integral image) by more than `offset`: it follows
// uneven lighting in a photographed drawing.
func ThresholdAdaptive(ink *Ink, window, offset int) *Mask {
	if window < 3 {
		window = 3
	}
	window |= 1
	w, h := ink.W, ink.H
	integral := make([]int64, (w+1)*(h+1))
	for y := 0; y < h; y++ {
		var row int64
		for x := 0; x < w; x++ {
			row += int64(ink.Pix[y*w+x])
			integral[(y+1)*(w+1)+x+1] = integral[y*(w+1)+x+1] + row
		}
	}
	m := newMask(w, h)
	r := window / 2
	for y := 0; y < h; y++ {
		y0, y1 := maxInt(0, y-r), minInt(h, y+r+1)
		for x := 0; x < w; x++ {
			x0, x1 := maxInt(0, x-r), minInt(w, x+r+1)
			area := int64((x1 - x0) * (y1 - y0))
			sum := integral[y1*(w+1)+x1] - integral[y0*(w+1)+x1] - integral[y1*(w+1)+x0] + integral[y0*(w+1)+x0]
			mean := float64(sum) / float64(area)
			m.Pix[y*w+x] = float64(ink.Pix[y*w+x]) > mean+float64(offset)
		}
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func abs(a float64) float64 { return math.Abs(a) }
