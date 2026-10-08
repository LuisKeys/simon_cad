package vision

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
	"testing"
)

func mustProposal(t *testing.T, raw string) *Proposal {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	p, err := Decode(v)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A 400x300 px drawing of a rectangular plate (320x200 px) with two holes of 12 px radius.
const plateJSON = `{
 "image_id":"img_x","image_size":[400,300],"confidence":0.86,
 "entities":[
  {"id":"e1","kind":"rectangle","x":50,"y":50,"w":320,"h":200,"confidence":0.95},
  {"id":"e2","kind":"circle","cx":130,"cy":150,"r":12,"confidence":0.9},
  {"id":"e3","kind":"circle","cx":290,"cy":150,"r":12,"confidence":0.9}],
 "profiles":[{"id":"p1","outer":["e1"],"holes":[["e2"],["e3"]]}],
 "relationships":[{"kind":"equal","entities":["e2","e3"],"confidence":0.9}],
 "dimensions":[
  {"id":"overall_width","kind":"length","binds":"e1.w","source":"missing","confidence":0},
  {"id":"thickness","kind":"depth","source":"missing","confidence":0}]
}`

func setDim(p *Proposal, id string, v float64, source string, conf float64) {
	d := p.dimension(id)
	d.Value, d.Source, d.Confidence = &v, source, conf
}

func TestPlateNeedsOnlyWidthAndThickness(t *testing.T) {
	p := mustProposal(t, plateJSON)
	r := Validate(p, nil)
	if len(r.Errors) != 0 {
		t.Fatalf("errors: %v", r.Errors)
	}
	if r.Ready {
		t.Fatal("must not be ready without scale and depth")
	}
	if len(r.Missing) != 2 {
		t.Fatalf("exactly two questions expected (width, thickness), got %+v", r.Missing)
	}
	got := map[string]bool{}
	for _, m := range r.Missing {
		got[m.Dimension] = true
	}
	if !got["overall_width"] || !got["thickness"] {
		t.Fatalf("missing %+v", r.Missing)
	}
	if _, err := ToSketch(p, r.Scale); err == nil {
		t.Fatal("converting without a scale must fail")
	}

	setDim(p, "overall_width", 80, "user", 1)
	setDim(p, "thickness", 5, "user", 1)
	r = Validate(p, nil)
	if !r.Ready || r.Scale == nil || math.Abs(r.Scale.MMPerPx-0.25) > 1e-9 {
		t.Fatalf("ready=%v scale=%+v report=%+v", r.Ready, r.Scale, r)
	}
	sk, err := ToSketch(p, r.Scale)
	if err != nil {
		t.Fatal(err)
	}
	ents := sk.Params["entities"].([]any)
	rect := ents[0].(map[string]any)
	if rect["w"] != "@dim_overall_width" || sk.Params["dim_overall_width"] != 80.0 {
		t.Fatalf("width must be a parameter: %v / %v", rect["w"], sk.Params["dim_overall_width"])
	}
	if rect["h"] != 50.0 || rect["x"] != 0.0 || rect["y"] != 0.0 {
		t.Fatalf("rectangle in mm wrong: %v", rect)
	}
	c := ents[1].(map[string]any)
	if c["r"] != 3.0 || c["cx"] != 20.0 || c["cy"] != 25.0 {
		t.Fatalf("hole in mm wrong (y must flip): %v", c)
	}
	if len(sk.Constraints) != 1 || sk.Constraints[0].Kind != "equal" {
		t.Fatalf("constraints %+v", sk.Constraints)
	}
	regs := sk.Params["regions"].([]any)[0].(map[string]any)
	if len(regs["holes"].([]any)) != 2 {
		t.Fatalf("holes not preserved: %v", regs)
	}
}

func TestPixelSizeAloneNeverScales(t *testing.T) {
	p := mustProposal(t, plateJSON)
	p.Dimensions = nil
	r := Validate(p, nil)
	if r.Scale != nil || r.Ready {
		t.Fatalf("no dimension means no scale: %+v", r)
	}
	found := false
	for _, m := range r.Missing {
		found = found || m.Dimension == "scale"
	}
	if !found {
		t.Fatalf("expected a single scale question, got %+v", r.Missing)
	}
}

func TestLowConfidenceDimensionNeedsConfirmation(t *testing.T) {
	p := mustProposal(t, plateJSON)
	setDim(p, "overall_width", 80, "handwritten", 0.5)
	setDim(p, "thickness", 5, "handwritten", 0.9)
	r := Validate(p, nil)
	if r.Ready || len(r.Unconfirmed) != 1 || r.Unconfirmed[0] != "overall_width" {
		t.Fatalf("report %+v", r)
	}
	if r.Scale != nil {
		t.Fatal("an unconfirmed dimension must not fix the scale")
	}
	// incorrectly read: the user corrects it to 40 mm
	v := 40.0
	if err := ConfirmDimension(p, "overall_width", &v); err != nil {
		t.Fatal(err)
	}
	r = Validate(p, nil)
	if !r.Ready || math.Abs(r.Scale.MMPerPx-40.0/320) > 1e-9 {
		t.Fatalf("after correction: %+v", r)
	}
	sk, _ := ToSketch(p, r.Scale)
	if sk.Params["dim_overall_width"] != 40.0 {
		t.Fatalf("param %v", sk.Params["dim_overall_width"])
	}
	if err := ConfirmDimension(p, "nope", nil); err == nil {
		t.Fatal("unknown dimension must fail")
	}
}

func TestDimensionDisagreementWarns(t *testing.T) {
	p := mustProposal(t, plateJSON)
	setDim(p, "overall_width", 80, "user", 1)
	p.Dimensions = append(p.Dimensions, Dimension{ID: "hole_d", Kind: "diameter", Binds: "e2.diameter", Source: "user", Confidence: 1})
	v := 9.0 // implies 0.375 mm/px against 0.25
	p.Dimensions[len(p.Dimensions)-1].Value = &v
	setDim(p, "thickness", 5, "user", 1)
	r := Validate(p, nil)
	if len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "differ") {
		t.Fatalf("warnings %v", r.Warnings)
	}
}

func TestExplicitScale(t *testing.T) {
	p := mustProposal(t, plateJSON)
	setDim(p, "thickness", 5, "user", 1)
	r := Validate(p, &Scale{MMPerPx: 0.5, Source: "explicit"})
	if r.Scale == nil || r.Scale.MMPerPx != 0.5 {
		t.Fatalf("scale %+v", r.Scale)
	}
	for _, m := range r.Missing {
		if m.Dimension == "scale" || m.Dimension == "overall_width" {
			t.Fatalf("with an explicit scale nothing about width is missing: %+v", r.Missing)
		}
	}
}

func TestIrregularProfileGapsSnapAndLargeGapFails(t *testing.T) {
	// a hand-drawn quarter-disc-ish shape: two lines and an arc, endpoints off by 2-3 px
	raw := `{"image_id":"i","image_size":[400,300],"confidence":0.7,
	 "entities":[
	  {"id":"l1","kind":"line","p1":[50,250],"p2":[250,251],"confidence":0.8},
	  {"id":"a1","kind":"arc","p1":[252,249],"mid":[200,100],"p2":[53,52],"confidence":0.7},
	  {"id":"l2","kind":"line","p1":[51,50],"p2":[49,248],"confidence":0.8}],
	 "profiles":[{"id":"p1","outer":["l1","a1","l2"]}],
	 "dimensions":[{"id":"w","kind":"length","binds":"l1.length","value":50,"source":"handwritten","confidence":0.9},
	               {"id":"t","kind":"depth","value":3,"source":"user","confidence":1}]}`
	p := mustProposal(t, raw)
	r := Validate(p, nil)
	if len(r.Errors) != 0 || !r.Ready {
		t.Fatalf("report %+v", r)
	}
	sk, err := ToSketch(p, r.Scale)
	if err != nil {
		t.Fatal(err)
	}
	ents := sk.Params["entities"].([]any)
	byID := map[string]map[string]any{}
	for _, e := range ents {
		m := e.(map[string]any)
		byID[m["id"].(string)] = m
	}
	end := func(e map[string]any, k string) string { b, _ := json.Marshal(e[k]); return string(b) }
	if end(byID["l1"], "p2") != end(byID["a1"], "p1") || end(byID["a1"], "p2") != end(byID["l2"], "p1") ||
		end(byID["l2"], "p2") != end(byID["l1"], "p1") {
		t.Fatalf("joints must be welded exactly: %v", byID)
	}

	// a gap far above 1.5% of the diagonal is a real error, not something to snap
	p.entity("l2")["p2"] = []any{49.0, 150.0}
	r = Validate(p, nil)
	if r.Ready || len(r.Errors) == 0 || !strings.Contains(strings.Join(r.Errors, ";"), "not closed") {
		t.Fatalf("open loop must be an error: %+v", r)
	}
}

func TestStructuralErrors(t *testing.T) {
	cases := map[string]func(p *Proposal){
		"hole outside":      func(p *Proposal) { p.entity("e2")["cx"] = 500.0 },
		"unknown entity":    func(p *Proposal) { p.Profiles[0].Holes[0] = []string{"zz"} },
		"missing field":     func(p *Proposal) { delete(p.entity("e1"), "w") },
		"bad kind":          func(p *Proposal) { p.entity("e1")["kind"] = "blob" },
		"duplicate entity":  func(p *Proposal) { p.entity("e3")["id"] = "e2" },
		"bad bind":          func(p *Proposal) { p.Dimensions[0].Binds = "e2.w" },
		"unknown bind":      func(p *Proposal) { p.Dimensions[0].Binds = "q9.w" },
		"zero depth":        func(p *Proposal) { v := 0.0; p.Dimensions[1].Value = &v },
		"no profiles":       func(p *Proposal) { p.Profiles = nil },
		"bad relationship":  func(p *Proposal) { p.Relationships[0].Kind = "zigzag" },
		"bad plane":         func(p *Proposal) { p.Placement.Plane = "QQ" },
		"hole on boundary":  func(p *Proposal) { p.entity("e2")["cx"] = 50.0 },
		"bad image size":    func(p *Proposal) { p.ImageSize = [2]float64{0, 0} },
		"circle bad radius": func(p *Proposal) { p.entity("e2")["r"] = -3.0 },
	}
	for name, mutate := range cases {
		p := mustProposal(t, plateJSON)
		mutate(p)
		r := Validate(p, nil)
		if len(r.Errors) == 0 || r.Ready {
			t.Errorf("%s: expected errors, got %+v", name, r)
		}
	}
	// bow-tie polygon crosses itself
	p := mustProposal(t, `{"image_id":"i","image_size":[100,100],"confidence":1,
	 "entities":[{"id":"b","kind":"polygon","points":[[0,0],[100,100],[100,0],[0,100]],"confidence":1}],
	 "profiles":[{"id":"p","outer":["b"]}]}`)
	if r := Validate(p, nil); len(r.Errors) == 0 || !strings.Contains(r.Errors[0], "crosses itself") {
		t.Fatalf("bow-tie: %+v", r.Errors)
	}
}

const twoRects = `{"image_id":"i","image_size":[400,300],"confidence":0.9,
 "entities":[
  {"id":"a","kind":"rectangle","x":50,"y":50,"w":200,"h":100,"confidence":0.9},
  {"id":"b","kind":"rectangle","x":150,"y":80,"w":200,"h":100,"confidence":0.9}],
 "profiles":[{"id":"pa","outer":["a"]},{"id":"pb","outer":["b"]}],
 "dimensions":[{"id":"w","kind":"length","binds":"a.w","value":100,"source":"user","confidence":1},
               {"id":"t","kind":"depth","value":4,"source":"user","confidence":1}]}`

func TestOverlappingProfilesAreAmbiguousUntilOperationsSayOtherwise(t *testing.T) {
	p := mustProposal(t, twoRects)
	r := Validate(p, nil)
	if r.Ready || len(r.Ambiguous) != 1 || !strings.Contains(r.Ambiguous[0].Question, "Join") {
		t.Fatalf("report %+v", r)
	}
	for _, kind := range []string{"union", "subtract", "intersect"} {
		q := p.Clone()
		q.Operations = []Operation{
			{ID: "o1", Kind: "extrude", Profile: "pa", DepthDim: "t"},
			{ID: "o2", Kind: "extrude", Profile: "pb", DepthDim: "t"},
			{ID: "o3", Kind: kind, A: "o1", B: "o2"},
		}
		r := Validate(q, nil)
		if !r.Ready || len(r.Ambiguous) != 0 {
			t.Fatalf("%s: %+v", kind, r)
		}
	}
	// disjoint profiles are not ambiguous
	q := p.Clone()
	q.entity("b")["x"] = 300.0
	if r := Validate(q, nil); len(r.Ambiguous) != 0 {
		t.Fatalf("disjoint profiles must not be ambiguous: %+v", r.Ambiguous)
	}
}

func TestOperationOrderingRules(t *testing.T) {
	base := mustProposal(t, twoRects)
	bad := map[string][]Operation{
		"boolean on profile": {{ID: "o1", Kind: "union", A: "pa", B: "pb"}},
		"forward reference":  {{ID: "o1", Kind: "union", A: "o2", B: "o3"}, {ID: "o2", Kind: "extrude", Profile: "pa", DepthDim: "t"}},
		"consumed twice": {
			{ID: "o1", Kind: "extrude", Profile: "pa", DepthDim: "t"},
			{ID: "o2", Kind: "extrude", Profile: "pb", DepthDim: "t"},
			{ID: "o3", Kind: "union", A: "o1", B: "o2"},
			{ID: "o4", Kind: "subtract", A: "o1", B: "o2"}},
		"self combine":    {{ID: "o1", Kind: "extrude", Profile: "pa", DepthDim: "t"}, {ID: "o2", Kind: "union", A: "o1", B: "o1"}},
		"cut no target":   {{ID: "o1", Kind: "cut", Profile: "pa", DepthDim: "t"}},
		"unknown profile": {{ID: "o1", Kind: "extrude", Profile: "nope", DepthDim: "t"}},
		"bad depth dim":   {{ID: "o1", Kind: "extrude", Profile: "pa", DepthDim: "w"}},
		"duplicate id":    {{ID: "o1", Kind: "extrude", Profile: "pa", DepthDim: "t"}, {ID: "o1", Kind: "extrude", Profile: "pb", DepthDim: "t"}},
		"through+depth":   {{ID: "o1", Kind: "extrude", Profile: "pa", DepthDim: "t"}, {ID: "o2", Kind: "cut", Profile: "pb", Target: "o1", Through: true, DepthDim: "t"}},
	}
	for name, ops := range bad {
		p := base.Clone()
		p.Operations = ops
		if r := Validate(p, nil); len(r.Errors) == 0 || r.Ready {
			t.Errorf("%s: expected errors, got %+v", name, r)
		}
	}
	// cut through an existing body is fine
	p := base.Clone()
	p.Operations = []Operation{{ID: "o1", Kind: "cut", Profile: "pa", Target: "feature:plate", Through: true}}
	if r := Validate(p, nil); !r.Ready {
		t.Fatalf("cut through feature: %+v", r)
	}
	// an extrude without a depth asks for one
	p.Operations = []Operation{{ID: "o1", Kind: "extrude", Profile: "pa"}}
	if r := Validate(p, nil); r.Ready || len(r.Missing) == 0 {
		t.Fatalf("missing depth must be asked: %+v", r)
	}
}

func TestHorizontalVerticalSnap(t *testing.T) {
	raw := `{"image_id":"i","image_size":[400,300],"confidence":0.9,
	 "entities":[
	  {"id":"l1","kind":"line","p1":[50,250],"p2":[350,254],"confidence":0.9},
	  {"id":"l2","kind":"line","p1":[350,254],"p2":[352,50],"confidence":0.9},
	  {"id":"l3","kind":"line","p1":[352,50],"p2":[50,48],"confidence":0.9},
	  {"id":"l4","kind":"line","p1":[50,48],"p2":[50,250],"confidence":0.9}],
	 "profiles":[{"id":"p","outer":["l1","l2","l3","l4"]}],
	 "relationships":[{"kind":"horizontal","entities":["l1","l3"],"confidence":0.9},{"kind":"vertical","entities":["l2","l4"],"confidence":0.9}],
	 "dimensions":[{"id":"w","kind":"length","binds":"l1.length","value":100,"source":"user","confidence":1},
	               {"id":"t","kind":"depth","value":2,"source":"user","confidence":1}]}`
	p := mustProposal(t, raw)
	r := Validate(p, nil)
	if !r.Ready {
		t.Fatalf("%+v", r)
	}
	sk, err := ToSketch(p, r.Scale)
	if err != nil {
		t.Fatal(err)
	}
	var l1, l2 map[string]any
	for _, e := range sk.Params["entities"].([]any) {
		m := e.(map[string]any)
		switch m["id"] {
		case "l1":
			l1 = m
		case "l2":
			l2 = m
		}
	}
	p1, p2 := l1["p1"].([]any), l1["p2"].([]any)
	if p1[1] != p2[1] {
		t.Fatalf("l1 must be horizontal: %v %v", p1, p2)
	}
	q1, q2 := l2["p1"].([]any), l2["p2"].([]any)
	if q1[0] != q2[0] {
		t.Fatalf("l2 must be vertical: %v %v", q1, q2)
	}
	for _, c := range sk.Constraints {
		if c.Detail != "snapped" {
			t.Fatalf("constraint %+v", c)
		}
	}
}

func TestEditsStoreAndPersistence(t *testing.T) {
	st := NewStore()
	it := st.Add(mustProposal(t, plateJSON))
	if it.ID != "int_0001" || it.Revision != 1 {
		t.Fatalf("%+v", it)
	}
	// "That circle should be a hole": move e2 out of its own profile into p1's holes was already so;
	// here: correct a radius and drop a relationship
	got, err := st.Modify(it.ID, func(i *Interpretation) error {
		return ApplyEdits(i.Proposal, []Edit{
			{Op: "set_entity", ID: "e2", Value: map[string]any{"r": 14.0}},
			{Op: "remove_relationship", ID: "0"},
			{Op: "set_dimension", ID: "hole_d", Value: map[string]any{"kind": "diameter", "binds": "e2.diameter", "source": "missing"}},
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 2 || got.Proposal.entity("e2")["r"] != 14.0 || len(got.Proposal.Relationships) != 0 || got.Proposal.dimension("hole_d") == nil {
		t.Fatalf("edit not applied: %+v", got.Proposal)
	}
	// a failing edit batch leaves the stored interpretation untouched
	if _, err := st.Modify(it.ID, func(i *Interpretation) error {
		return ApplyEdits(i.Proposal, []Edit{{Op: "set_entity", ID: "e2", Value: map[string]any{"r": 99.0}}, {Op: "remove_entity", ID: "ghost"}})
	}); err == nil {
		t.Fatal("expected failure")
	}
	cur, _ := st.Get(it.ID)
	if cur.Revision != 2 || cur.Proposal.entity("e2")["r"] != 14.0 {
		t.Fatalf("failed edit corrupted the proposal: %+v", cur.Proposal.entity("e2"))
	}
	if _, err := st.Modify("int_9999", func(*Interpretation) error { return nil }); err == nil {
		t.Fatal("unknown interpretation must fail")
	}
	// the returned value is a copy
	cur.Proposal.entity("e2")["r"] = 1.0
	again, _ := st.Get(it.ID)
	if again.Proposal.entity("e2")["r"] != 14.0 {
		t.Fatal("store leaked a mutable reference")
	}

	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	st2 := NewStore()
	if err := json.Unmarshal(b, st2); err != nil {
		t.Fatal(err)
	}
	if st2.Latest() != it.ID || st2.Add(mustProposal(t, plateJSON)).ID != "int_0002" {
		t.Fatal("store did not round-trip")
	}
	for _, bad := range []Edit{{Op: "zap"}, {Op: "set_entity"}, {Op: "remove_dimension", ID: "x"}, {Op: "set_relationship", ID: "9"}} {
		if err := applyEdit(mustProposal(t, plateJSON), bad); err == nil {
			t.Errorf("edit %+v must fail", bad)
		}
	}
}

func TestSlotAndPolygonConvert(t *testing.T) {
	raw := `{"image_id":"i","image_size":[400,300],"confidence":0.9,
	 "entities":[
	  {"id":"s","kind":"slot","x1":100,"y1":150,"x2":300,"y2":150,"width":40,"confidence":0.9},
	  {"id":"g","kind":"polygon","points":[[10,10],[60,10],[35,50]],"confidence":0.9}],
	 "profiles":[{"id":"ps","outer":["s"]},{"id":"pg","outer":["g"]}],
	 "dimensions":[{"id":"sw","kind":"length","binds":"s.width","value":8,"source":"user","confidence":1},
	               {"id":"t","kind":"depth","value":1,"source":"user","confidence":1}]}`
	p := mustProposal(t, raw)
	r := Validate(p, nil)
	if !r.Ready || math.Abs(r.Scale.MMPerPx-0.2) > 1e-9 {
		t.Fatalf("%+v", r)
	}
	sk, err := ToSketch(p, r.Scale)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range sk.Params["entities"].([]any) {
		m := e.(map[string]any)
		if m["id"] == "s" && (m["width"] != "@dim_sw" || m["x2"].(float64)-m["x1"].(float64) != 40) {
			t.Fatalf("slot %v", m)
		}
	}
}

// ---- images --------------------------------------------------------------------------

func pngBytes(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.White)
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func webpHeader(chunk string, w, h int) []byte {
	b := make([]byte, 40)
	copy(b, "RIFF")
	copy(b[8:], "WEBP")
	copy(b[12:], chunk)
	switch chunk {
	case "VP8 ":
		copy(b[23:], []byte{0x9d, 0x01, 0x2a})
		binary.LittleEndian.PutUint16(b[26:], uint16(w))
		binary.LittleEndian.PutUint16(b[28:], uint16(h))
	case "VP8L":
		b[20] = 0x2f
		binary.LittleEndian.PutUint32(b[21:], uint32(w-1)|uint32(h-1)<<14)
	case "VP8X":
		w, h = w-1, h-1
		b[24], b[25], b[26] = byte(w), byte(w>>8), byte(w>>16)
		b[27], b[28], b[29] = byte(h), byte(h>>8), byte(h>>16)
	}
	return b
}

func TestImageSniffing(t *testing.T) {
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 30, 20)), nil)
	cases := []struct {
		name string
		data []byte
		mt   string
		w, h int
		bad  bool
	}{
		{"png", pngBytes(64, 48), "image/png", 64, 48, false},
		{"jpeg", jb.Bytes(), "image/jpeg", 30, 20, false},
		{"webp lossy", webpHeader("VP8 ", 640, 480), "image/webp", 640, 480, false},
		{"webp lossless", webpHeader("VP8L", 321, 123), "image/webp", 321, 123, false},
		{"webp extended", webpHeader("VP8X", 1000, 700), "image/webp", 1000, 700, false},
		{"gif", []byte("GIF89a....."), "", 0, 0, true},
		{"svg", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), "", 0, 0, true},
		{"empty", nil, "", 0, 0, true},
		{"truncated png", pngBytes(8, 8)[:20], "", 0, 0, true},
		{"huge webp", webpHeader("VP8X", 20000, 10), "", 0, 0, true},
	}
	for _, c := range cases {
		mt, w, h, err := Sniff(c.data)
		if c.bad {
			if err == nil {
				t.Errorf("%s: expected an error", c.name)
			}
			continue
		}
		if err != nil || mt != c.mt || w != c.w || h != c.h {
			t.Errorf("%s: got %s %dx%d %v", c.name, mt, w, h, err)
		}
	}
	s := NewImageStore()
	a, err := s.Put(pngBytes(10, 10))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Put(pngBytes(10, 10))
	if a != b || len(s.All()) != 1 {
		t.Fatal("identical uploads must dedupe")
	}
	if err := s.Retain(a.ID, true); err != nil || !a.Retained {
		t.Fatal("retain")
	}
	if err := s.Retain("img_nope", true); err == nil {
		t.Fatal("unknown image")
	}
	if _, err := s.Put(make([]byte, MaxImageBytes+1)); err == nil {
		t.Fatal("oversized image must be refused")
	}
}
