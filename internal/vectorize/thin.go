package vectorize

import (
	"context"
	"math"
	"sort"
)

// Thin reduces the foreground to a one-pixel-wide skeleton (Zhang-Suen).
func Thin(ctx context.Context, m *Mask) ([]bool, error) {
	w, h := m.W, m.H
	img := make([]uint8, (w+2)*(h+2)) // padded so neighbours need no bounds checks
	W := w + 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if m.Pix[y*w+x] {
				img[(y+1)*W+x+1] = 1
			}
		}
	}
	var del []int
	for changed := true; changed; {
		changed = false
		for step := 0; step < 2; step++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			del = del[:0]
			for y := 1; y <= h; y++ {
				for x := 1; x <= w; x++ {
					i := y*W + x
					if img[i] == 0 {
						continue
					}
					p2, p3, p4, p5 := img[i-W], img[i-W+1], img[i+1], img[i+W+1]
					p6, p7, p8, p9 := img[i+W], img[i+W-1], img[i-1], img[i-W-1]
					b := int(p2 + p3 + p4 + p5 + p6 + p7 + p8 + p9)
					if b < 2 || b > 6 {
						continue
					}
					a := 0
					seq := [9]uint8{p2, p3, p4, p5, p6, p7, p8, p9, p2}
					for k := 0; k < 8; k++ {
						if seq[k] == 0 && seq[k+1] == 1 {
							a++
						}
					}
					if a != 1 {
						continue
					}
					if step == 0 {
						if p2*p4*p6 != 0 || p4*p6*p8 != 0 {
							continue
						}
					} else {
						if p2*p4*p8 != 0 || p2*p6*p8 != 0 {
							continue
						}
					}
					del = append(del, i)
				}
			}
			for _, i := range del {
				img[i] = 0
			}
			if len(del) > 0 {
				changed = true
			}
		}
	}
	// Zhang-Suen leaves redundant staircase pixels (a pixel whose neighbours already touch each
	// other): delete those until the skeleton is minimal 8-connected, so that only true
	// junctions have three or more arms.
	offs := [8][2]int{{0, -1}, {1, -1}, {1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}}
	for changed := true; changed; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		changed = false
		for y := 1; y <= h; y++ {
			for x := 1; x <= w; x++ {
				i := y*W + x
				if img[i] == 0 {
					continue
				}
				var on [8]int // indices into offs of the lit neighbours
				n := 0
				for k, o := range offs {
					if img[i+o[1]*W+o[0]] == 1 {
						on[n] = k
						n++
					}
				}
				if n < 2 {
					continue // an end point stays
				}
				// how many separate groups do the lit neighbours form (8-adjacent to each other)?
				var parent [8]int
				for a := 0; a < n; a++ {
					parent[a] = a
				}
				find := func(a int) int {
					for parent[a] != a {
						a = parent[a]
					}
					return a
				}
				for a := 0; a < n; a++ {
					for b := a + 1; b < n; b++ {
						dx, dy := offs[on[a]][0]-offs[on[b]][0], offs[on[a]][1]-offs[on[b]][1]
						if dx >= -1 && dx <= 1 && dy >= -1 && dy <= 1 {
							parent[find(a)] = find(b)
						}
					}
				}
				groups := 0
				for a := 0; a < n; a++ {
					if find(a) == a {
						groups++
					}
				}
				if groups == 1 { // the neighbours already touch: this pixel is redundant
					img[i] = 0
					changed = true
				}
			}
		}
	}
	out := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out[y*w+x] = img[(y+1)*W+x+1] == 1
		}
	}
	return out, nil
}

// EDT is the Euclidean distance from every foreground pixel to the nearest background pixel
// (Felzenszwalb & Huttenlocher), in pixels.
func EDT(m *Mask) []float64 {
	w, h := m.W, m.H
	const inf = 1e20
	f := make([]float64, w*h)
	for i, v := range m.Pix {
		if v {
			f[i] = inf
		}
	}
	n := maxInt(w, h)
	line := make([]float64, n)
	d := make([]float64, n)
	v := make([]int, n)
	z := make([]float64, n+1)
	transform := func(count int) { // squared distance transform of line[0:count] into d
		k := 0
		v[0] = 0
		z[0], z[1] = -inf, inf
		for q := 1; q < count; q++ {
			var s float64
			for {
				p := v[k]
				s = ((line[q] + float64(q*q)) - (line[p] + float64(p*p))) / float64(2*q-2*p)
				if s <= z[k] {
					k--
					continue
				}
				break
			}
			k++
			v[k] = q
			z[k] = s
			z[k+1] = inf
		}
		k = 0
		for q := 0; q < count; q++ {
			for z[k+1] < float64(q) {
				k++
			}
			p := v[k]
			d[q] = float64((q-p)*(q-p)) + line[p]
		}
	}
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			line[y] = f[y*w+x]
		}
		transform(h)
		for y := 0; y < h; y++ {
			f[y*w+x] = d[y]
		}
	}
	for y := 0; y < h; y++ {
		copy(line[:w], f[y*w:(y+1)*w])
		transform(w)
		copy(f[y*w:(y+1)*w], d[:w])
	}
	far := math.Hypot(float64(w), float64(h))
	for i := range f {
		if f[i] >= inf/2 {
			f[i] = far // no background at all
		} else {
			f[i] = math.Sqrt(f[i])
		}
	}
	return f
}

// Chain is one stretch of skeleton between two nodes (junctions or end points).
type Chain struct {
	Pts    []Pt      // pixel centres, in order
	Width  []float64 // stroke width in pixels at every point
	Closed bool
}

type skelNode struct {
	pos   Pt
	edges []int
}

// SkeletonChains traces the skeleton into chains, removes spurs shorter than spur pixels and
// merges chains through junctions left with only two branches. dist is EDT(mask) for widths.
func SkeletonChains(ctx context.Context, skel []bool, w, h int, dist []float64, spur float64) ([]Chain, error) {
	idx := func(x, y int) int { return y*w + x }
	deg := make([]int, w*h)
	nbrs := func(x, y int, f func(nx, ny int)) {
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				nx, ny := x+dx, y+dy
				if nx >= 0 && ny >= 0 && nx < w && ny < h && skel[idx(nx, ny)] {
					f(nx, ny)
				}
			}
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if skel[idx(x, y)] {
				c := 0
				nbrs(x, y, func(int, int) { c++ })
				deg[idx(x, y)] = c
			}
		}
	}
	// Junctions: clusters of pixels with three or more neighbours that have three or more
	// separate arms leaving them. A staircase corner of a plain line also has pixels with three
	// neighbours, but only two arms, so it is not a junction.
	junction := make([]bool, w*h)
	inCluster := make([]int32, w*h)
	var clusters [][]int
	for i := range skel {
		if !skel[i] || deg[i] < 3 || inCluster[i] != 0 {
			continue
		}
		id := int32(len(clusters) + 1)
		cl := []int{i}
		inCluster[i] = id
		for k := 0; k < len(cl); k++ {
			nbrs(cl[k]%w, cl[k]/w, func(nx, ny int) {
				j := idx(nx, ny)
				if deg[j] >= 3 && inCluster[j] == 0 {
					inCluster[j] = id
					cl = append(cl, j)
				}
			})
		}
		clusters = append(clusters, cl)
	}
	for ci, cl := range clusters {
		id := int32(ci + 1)
		ext := map[int]bool{}
		for _, p := range cl {
			nbrs(p%w, p/w, func(nx, ny int) {
				if j := idx(nx, ny); inCluster[j] != id {
					ext[j] = true
				}
			})
		}
		// group the arm pixels by adjacency
		seen := map[int]bool{}
		groups := 0
		for p := range ext {
			if seen[p] {
				continue
			}
			groups++
			stack := []int{p}
			seen[p] = true
			for len(stack) > 0 {
				c := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				for q := range ext {
					if !seen[q] && abs(float64(c%w-q%w)) <= 1 && abs(float64(c/w-q/w)) <= 1 {
						seen[q] = true
						stack = append(stack, q)
					}
				}
			}
		}
		if groups >= 3 {
			for _, p := range cl {
				junction[p] = true
			}
		}
	}
	// cluster adjacent junction pixels into nodes; end points are nodes of their own
	nodeOf := make([]int32, w*h)
	for i := range nodeOf {
		nodeOf[i] = -1
	}
	var nodes []*skelNode
	newNode := func(px []int) int {
		var sx, sy float64
		for _, p := range px {
			sx += float64(p%w) + 0.5
			sy += float64(p/w) + 0.5
		}
		n := &skelNode{pos: Pt{sx / float64(len(px)), sy / float64(len(px))}}
		nodes = append(nodes, n)
		for _, p := range px {
			nodeOf[p] = int32(len(nodes) - 1)
		}
		return len(nodes) - 1
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := idx(x, y)
			if !skel[i] || nodeOf[i] >= 0 {
				continue
			}
			switch {
			case junction[i]:
				cluster := []int{i}
				nodeOf[i] = -2
				for k := 0; k < len(cluster); k++ {
					cx, cy := cluster[k]%w, cluster[k]/w
					nbrs(cx, cy, func(nx, ny int) {
						j := idx(nx, ny)
						if junction[j] && nodeOf[j] == -1 {
							nodeOf[j] = -2
							cluster = append(cluster, j)
						}
					})
				}
				newNode(cluster)
			case deg[i] == 1:
				newNode([]int{i})
			}
		}
	}
	type rawChain struct {
		a, b int
		pix  []int
	}
	var chains []rawChain
	visited := make([]bool, w*h)
	walk := func(startNode int, first int) {
		pix := []int{first}
		visited[first] = true
		prev, cur := -1, first
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			var next []int
			nbrs(cur%w, cur/w, func(nx, ny int) {
				j := idx(nx, ny)
				if j != prev {
					next = append(next, j)
				}
			})
			// stop at a node pixel (not the one we came from)
			end := -1
			for _, j := range next {
				if nodeOf[j] >= 0 && int(nodeOf[j]) != startNode {
					end = int(nodeOf[j])
				} else if nodeOf[j] >= 0 && len(pix) > 2 {
					end = int(nodeOf[j])
				}
			}
			if end >= 0 {
				chains = append(chains, rawChain{startNode, end, pix})
				return
			}
			step := -1
			for _, j := range next {
				if nodeOf[j] < 0 && !visited[j] {
					step = j
					break
				}
			}
			if step < 0 {
				chains = append(chains, rawChain{startNode, -1, pix})
				return
			}
			visited[step] = true
			pix = append(pix, step)
			prev, cur = cur, step
		}
	}
	for ni, n := range nodes {
		_ = n
		for i := range nodeOf {
			if int(nodeOf[i]) != ni {
				continue
			}
			nbrs(i%w, i/w, func(nx, ny int) {
				j := idx(nx, ny)
				if nodeOf[j] < 0 && !visited[j] {
					walk(ni, j)
				}
			})
		}
	}
	// cycles with no node at all
	for i := range skel {
		if skel[i] && nodeOf[i] < 0 && !visited[i] {
			pix := []int{i}
			visited[i] = true
			cur := i
			for {
				step := -1
				nbrs(cur%w, cur/w, func(nx, ny int) {
					j := idx(nx, ny)
					if step < 0 && !visited[j] && nodeOf[j] < 0 {
						step = j
					}
				})
				if step < 0 {
					break
				}
				visited[step] = true
				pix = append(pix, step)
				cur = step
			}
			chains = append(chains, rawChain{-1, -1, pix})
		}
	}
	// graph bookkeeping
	type ch struct {
		a, b int
		pts  []Pt
		wid  []float64
		dead bool
	}
	var cs []*ch
	for _, rc := range chains {
		c := &ch{a: rc.a, b: rc.b}
		if rc.a >= 0 {
			c.pts = append(c.pts, nodes[rc.a].pos)
			c.wid = append(c.wid, math.Max(1, 2*dist[rc.pix[0]]-1))
		}
		for _, p := range rc.pix {
			c.pts = append(c.pts, Pt{float64(p%w) + 0.5, float64(p/w) + 0.5})
			c.wid = append(c.wid, math.Max(1, 2*dist[p]-1))
		}
		if rc.b >= 0 {
			c.pts = append(c.pts, nodes[rc.b].pos)
			c.wid = append(c.wid, c.wid[len(c.wid)-1])
		}
		cs = append(cs, c)
	}
	for i, c := range cs {
		if c.a >= 0 {
			nodes[c.a].edges = append(nodes[c.a].edges, i)
		}
		if c.b >= 0 && c.b != c.a {
			nodes[c.b].edges = append(nodes[c.b].edges, i)
		}
	}
	chainLen := func(c *ch) float64 {
		l := 0.0
		for i := 1; i < len(c.pts); i++ {
			l += pdist(c.pts[i-1], c.pts[i])
		}
		return l
	}
	live := func(n *skelNode) []int {
		var out []int
		for _, e := range n.edges {
			if !cs[e].dead {
				out = append(out, e)
			}
		}
		return out
	}
	// prune spurs: a short chain with a free end attached to a junction
	for changed := true; changed; {
		changed = false
		for i, c := range cs {
			if c.dead || c.a < 0 || c.b < 0 && false {
				continue
			}
			freeEnd := c.b < 0 || (c.b >= 0 && len(live(nodes[c.b])) == 1)
			freeStart := c.a < 0 || len(live(nodes[c.a])) == 1
			attached := (c.a >= 0 && len(live(nodes[c.a])) >= 3) || (c.b >= 0 && len(live(nodes[c.b])) >= 3)
			if c.a != c.b && attached && (freeEnd || freeStart) && chainLen(c) < spur {
				cs[i].dead = true
				changed = true
			}
		}
	}
	// merge through junctions that now have exactly two branches
	for changed := true; changed; {
		changed = false
		for ni, n := range nodes {
			l := live(n)
			if len(l) != 2 || l[0] == l[1] {
				continue
			}
			a, b := cs[l[0]], cs[l[1]]
			// orient so a ends at node ni and b starts there
			if a.b != ni {
				for i, j := 0, len(a.pts)-1; i < j; i, j = i+1, j-1 {
					a.pts[i], a.pts[j] = a.pts[j], a.pts[i]
					a.wid[i], a.wid[j] = a.wid[j], a.wid[i]
				}
				a.a, a.b = a.b, a.a
			}
			if b.a != ni {
				for i, j := 0, len(b.pts)-1; i < j; i, j = i+1, j-1 {
					b.pts[i], b.pts[j] = b.pts[j], b.pts[i]
					b.wid[i], b.wid[j] = b.wid[j], b.wid[i]
				}
				b.a, b.b = b.b, b.a
			}
			a.pts = append(a.pts, b.pts[1:]...)
			a.wid = append(a.wid, b.wid[1:]...)
			a.b = b.b
			b.dead = true
			if b.b >= 0 {
				for k, e := range nodes[b.b].edges {
					if cs[e] == b {
						nodes[b.b].edges[k] = l[0]
					}
				}
			}
			n.edges = nil
			changed = true
			break
		}
	}
	var out []Chain
	for _, c := range cs {
		if c.dead || len(c.pts) < 2 {
			continue
		}
		closed := c.a >= 0 && c.a == c.b
		if c.a < 0 && c.b < 0 && len(c.pts) > 3 && pdist(c.pts[0], c.pts[len(c.pts)-1]) < 2 {
			closed = true
		}
		out = append(out, Chain{Pts: c.pts, Width: c.wid, Closed: closed})
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Pts) > len(out[j].Pts) })
	return out, nil
}
