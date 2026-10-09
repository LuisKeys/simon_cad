// Package gcode interprets slicer G-code into the normalized toolpath form. It is a
// simulator, not a controller: nothing here ever talks to hardware, and the input is
// treated as untrusted (size, line and segment limits, no shell, no file access).
//
// The interpreter tracks the machine state that changes what a move means: absolute vs
// relative XYZ (G90/G91), absolute vs relative extrusion (M82/M83), position offsets
// (G92), retraction debt, feed rate and the active tool. Commands it cannot interpret
// faithfully are either reported as diagnostics (harmless: temperatures, fans, …) or
// fail the parse (anything that could change geometry: inches, other planes, splines…).
package gcode

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/accelone/simoncad/internal/printsim/toolpath"
)

// Options bound the work done on untrusted input.
type Options struct {
	MaxBytes     int64   // default 512 MiB
	MaxSegments  int     // default toolpath.MaxSegments
	ArcTolerance float64 // chord error when discretizing G2/G3, mm; default 0.05
}

func (o *Options) defaults() {
	if o.MaxBytes <= 0 {
		o.MaxBytes = 512 << 20
	}
	if o.MaxSegments <= 0 || o.MaxSegments > toolpath.MaxSegments {
		o.MaxSegments = toolpath.MaxSegments
	}
	if o.ArcTolerance <= 0 {
		o.ArcTolerance = 0.05
	}
}

// Error is a parse failure tied to a line.
type Error struct {
	Line int
	Cmd  string
	Msg  string
}

func (e *Error) Error() string {
	if e.Cmd != "" {
		return fmt.Sprintf("g-code line %d (%s): %s", e.Line, e.Cmd, e.Msg)
	}
	return fmt.Sprintf("g-code line %d: %s", e.Line, e.Msg)
}

// coordLimit rejects absurd coordinates, which would only come from corrupt input.
const coordLimit = 1e5

// maxDiagnostics bounds the distinct diagnostic codes kept.
const maxDiagnostics = 200

// ignored are commands that do not affect the toolpath; they are counted as info.
var ignored = map[string]string{
	"G21": "units are millimetres", "G17": "XY plane", "G80": "cancel canned cycle",
	"G29": "bed levelling", "G30": "probe", "G31": "probe", "G32": "bed levelling", "G90.1": "absolute arc offsets",
	"G4": "dwell", "G12": "nozzle clean",
	"M0": "pause", "M1": "pause", "M17": "enable steppers", "M18": "disable steppers", "M84": "disable steppers",
	"M73": "progress", "M104": "set hotend temperature", "M105": "temperature report", "M106": "fan on",
	"M107": "fan off", "M109": "wait hotend temperature", "M115": "firmware info", "M117": "display message",
	"M118": "serial message", "M140": "set bed temperature", "M141": "set chamber temperature",
	"M190": "wait bed temperature", "M191": "wait chamber temperature", "M201": "max acceleration",
	"M202": "max travel acceleration", "M203": "max feedrate", "M204": "acceleration", "M205": "jerk",
	"M207": "firmware retraction setup", "M208": "firmware recover setup", "M220": "feedrate override",
	"M221": "flow override", "M400": "finish moves", "M486": "cancel object", "M900": "linear advance",
	"M862.1": "nozzle check", "M862.3": "model check", "M862.5": "gcode level check", "M862.6": "firmware check",
	"M555": "print area", "M572": "pressure advance", "M569": "driver mode",
	"M300": "beep", "M42": "set pin", "M80": "power on", "M81": "power off", "M92": "steps per unit",
	"M301": "pid", "M303": "pid autotune", "M304": "bed pid", "M500": "save settings", "M501": "load settings",
	"M502": "factory reset settings", "M503": "report settings",
}

// textCommands carry free text, so the rest of the line is not parsed as words.
var textCommands = map[string]bool{"M117": true, "M118": true, "M23": true, "M28": true, "M29": true, "M30": true, "M550": true}

// unsupported fail the parse: interpreting the file without them would misplace material.
var unsupported = map[string]string{
	"G20": "inch units are not supported (the toolpath would be 25.4× too small)",
	"G18": "only the XY arc plane is supported", "G19": "only the XY arc plane is supported",
	"G5": "spline moves are not supported", "G6": "direct stepper moves are not supported",
	"G53": "machine coordinate moves are not supported", "G54": "work coordinate systems are not supported",
	"G55": "work coordinate systems are not supported", "G56": "work coordinate systems are not supported",
	"G57": "work coordinate systems are not supported", "G58": "work coordinate systems are not supported",
	"G59": "work coordinate systems are not supported", "G38.2": "probing moves are not supported",
	"G38.3": "probing moves are not supported", "G91.1": "relative arc offsets are not supported",
	"G61": "path control modes are not supported", "G64": "path control modes are not supported",
}

var ignoredCodes = func() map[string]string {
	m := make(map[string]string, len(ignored))
	for k := range ignored {
		m[k] = "ignored_" + strings.ToLower(k)
	}
	return m
}()

func isIgnored(w word) bool {
	if w.letter != 'M' {
		return false
	}
	_, ok := ignored[cmdKey(w)]
	return ok
}

func ignoredCode(cmd string) string { return ignoredCodes[cmd] }

type typeInfo struct {
	kind toolpath.Kind
	ok   bool
}

func lookupType(name string) typeInfo {
	n := strings.ToLower(strings.TrimSpace(name))
	switch n {
	// PrusaSlicer / SuperSlicer
	case "perimeter", "wall-inner", "inner wall", "internal perimeter", "overhang perimeter":
		return typeInfo{toolpath.Perimeter, true}
	case "external perimeter", "wall-outer", "outer wall", "outer perimeter":
		return typeInfo{toolpath.ExternalPerimeter, true}
	case "internal infill", "fill", "infill", "sparse infill", "gap fill", "ironing":
		return typeInfo{toolpath.Infill, true}
	case "solid infill", "internal solid infill":
		return typeInfo{toolpath.SolidInfill, true}
	case "top solid infill", "skin", "top surface", "bottom surface", "bottom solid infill":
		return typeInfo{toolpath.TopBottom, true}
	case "bridge infill", "bridge", "internal bridge":
		return typeInfo{toolpath.Bridge, true}
	case "support material", "support material interface", "support", "support-interface", "support interface":
		return typeInfo{toolpath.Support, true}
	case "skirt/brim", "skirt", "brim":
		return typeInfo{toolpath.Skirt, true}
	case "custom", "wipe tower", "prime-tower", "prime tower":
		return typeInfo{toolpath.Other, true}
	}
	return typeInfo{toolpath.Other, false}
}

type word struct {
	letter byte
	val    float64
	raw    string
}

type state struct {
	x, y, z, e     float64 // physical XYZ, logical E
	offX, offY     float64 // G92 offsets: logical = physical - off
	offZ           float64
	absXYZ, absE   bool
	feed           float64 // mm/s, modal
	tool           uint8
	kind           toolpath.Kind
	retract        float64 // outstanding retraction, mm of filament
	homed          bool
	sawLayerMarker bool
	needLayer      bool
}

type parser struct {
	opt         Options
	st          state
	res         *toolpath.SliceResult
	diags       map[string]*toolpath.Diagnostic
	line        int
	words       []word          // reused across lines: parsing a million moves must not allocate per line
	cur         *toolpath.Layer // points into res.Layers; reassigned whenever a layer is appended
	curExtruded bool
}

// Parse reads G-code and returns the normalized toolpath. The result is not finalized:
// callers run SliceResult.Finalize with the machine values.
func Parse(ctx context.Context, r io.Reader, opt Options) (*toolpath.SliceResult, error) {
	opt.defaults()
	p := &parser{
		opt:   opt,
		st:    state{absXYZ: true, absE: true, kind: toolpath.Other},
		res:   &toolpath.SliceResult{},
		diags: map[string]*toolpath.Diagnostic{},
	}
	lr := &io.LimitedReader{R: r, N: opt.MaxBytes + 1}
	sc := bufio.NewScanner(lr)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		p.line++
		if p.line&0xfff == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if err := p.handle(sc.Text()); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		if err == bufio.ErrTooLong {
			return nil, &Error{Line: p.line + 1, Msg: "line longer than 1 MiB"}
		}
		return nil, err
	}
	if lr.N <= 0 {
		return nil, fmt.Errorf("g-code is larger than the %d MiB limit", opt.MaxBytes>>20)
	}
	// drop layers that hold no extrusion (e.g. a trailing end-of-print travel)
	p.res.Layers = trimLayers(p.res)
	p.res.Diagnostics = p.collect()
	if len(p.res.Segments) == 0 {
		return nil, &Error{Msg: "g-code contains no moves"}
	}
	return p.res, nil
}

// trimLayers folds layers that deposit nothing (a trailing park move, say) into the previous layer, so the
// layer list and slider only count real layers. Layers stay contiguous over the segments.
func trimLayers(r *toolpath.SliceResult) []toolpath.Layer {
	out := r.Layers[:0]
	for _, l := range r.Layers {
		extrudes := false
		for i := l.First; i < l.First+l.Count; i++ {
			if r.Segments[i].Kind.Extrudes() {
				extrudes = true
				break
			}
		}
		if !extrudes && len(out) > 0 {
			out[len(out)-1].Count += l.Count
			continue
		}
		out = append(out, l)
	}
	return out
}

func (p *parser) note(level toolpath.Level, code, msg string) {
	p.noteLazy(level, code, func() string { return msg })
}

// noteLazy counts a repeat of a diagnostic without building its message again.
func (p *parser) noteLazy(level toolpath.Level, code string, msg func() string) {
	if d, ok := p.diags[code]; ok {
		d.Count++
		return
	}
	if len(p.diags) >= maxDiagnostics { // a hostile file must not grow this without bound
		code, level = "more_notes", toolpath.LevelInfo
		if d, ok := p.diags[code]; ok {
			d.Count++
			return
		}
		msg = func() string { return "further distinct notes were omitted" }
	}
	p.diags[code] = &toolpath.Diagnostic{Level: level, Code: code, Message: msg(), Line: p.line, Count: 1}
}

func (p *parser) collect() []toolpath.Diagnostic {
	keys := make([]string, 0, len(p.diags))
	for k := range p.diags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]toolpath.Diagnostic, 0, len(keys))
	for _, lv := range []toolpath.Level{toolpath.LevelError, toolpath.LevelWarning, toolpath.LevelInfo} {
		for _, k := range keys {
			if d := p.diags[k]; d.Level == lv {
				out = append(out, *d)
			}
		}
	}
	return out
}

func (p *parser) fail(cmd, format string, a ...any) error {
	return &Error{Line: p.line, Cmd: cmd, Msg: fmt.Sprintf(format, a...)}
}

func (p *parser) handle(raw string) error {
	line := raw
	comment := ""
	if i := strings.IndexByte(line, ';'); i >= 0 {
		comment = line[i+1:]
		line = line[:i]
	}
	if comment != "" {
		p.comment(comment)
	}
	if i := strings.IndexByte(line, '*'); i >= 0 { // checksum
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	words, cmd, err := p.tokenize(line)
	if err != nil {
		return err
	}
	if cmd == "" {
		return nil
	}
	return p.exec(cmd, words)
}

// tokenize splits a line into the command key (e.g. "G1", "M862.1") and its parameter words.
func (p *parser) tokenize(line string) ([]word, string, error) {
	line = strings.ToUpper(line)
	i := 0
	words := p.words[:0]
	for i < len(line) {
		c := line[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		if c < 'A' || c > 'Z' {
			return nil, "", p.fail("", "unexpected character %q", string(c))
		}
		j := i + 1
		for j < len(line) && (line[j] == '+' || line[j] == '-' || line[j] == '.' || (line[j] >= '0' && line[j] <= '9')) {
			j++
		}
		w := word{letter: c, raw: line[i+1 : j]}
		if w.raw != "" {
			v, err := strconv.ParseFloat(w.raw, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, "", p.fail("", "bad number %q after %c", w.raw, c)
			}
			w.val = v
		}
		words = append(words, w)
		i = j
		if len(words) == 1 && (textCommands[cmdKey(words[0])] || isIgnored(words[0])) {
			break // free text or values we do not interpret (M862.3 P "MK3S", M115 U3.9.3)
		}
	}
	p.words = words
	if len(words) == 0 {
		return nil, "", nil
	}
	first := words[0]
	if first.letter == 'N' { // line number
		words = words[1:]
		if len(words) == 0 {
			return nil, "", nil
		}
		first = words[0]
	}
	switch first.letter {
	case 'G', 'M', 'T':
		if first.raw == "" {
			return nil, "", p.fail("", "%c without a number", first.letter)
		}
	default:
		return nil, "", p.fail("", "unsupported line starting with %c (modal axis words are not supported)", first.letter)
	}
	return words[1:], cmdKey(first), nil
}

// Command keys for the common cases are built once, so identifying a command costs no allocation.
var gKeys, mKeys [1000]string
var tKeys [256]string

func init() {
	for i := range gKeys {
		gKeys[i], mKeys[i] = "G"+strconv.Itoa(i), "M"+strconv.Itoa(i)
	}
	for i := range tKeys {
		tKeys[i] = "T" + strconv.Itoa(i)
	}
}

// cmdKey normalizes a command word: "G01" → "G1", "M862.1" stays.
func cmdKey(w word) string {
	if w.letter == 'N' {
		return ""
	}
	if strings.IndexByte(w.raw, '.') >= 0 {
		return string(w.letter) + strings.TrimLeft(w.raw, "0")
	}
	if n := int(w.val); n >= 0 && n < 1000 {
		switch w.letter {
		case 'G':
			return gKeys[n]
		case 'M':
			return mKeys[n]
		case 'T':
			if n < len(tKeys) {
				return tKeys[n]
			}
		}
	}
	return string(w.letter) + strconv.Itoa(int(w.val))
}

func (p *parser) comment(c string) {
	c = strings.TrimSpace(c)
	switch {
	case strings.HasPrefix(c, "TYPE:"):
		name := c[len("TYPE:"):]
		ti := lookupType(name)
		if !ti.ok {
			p.note(toolpath.LevelInfo, "unknown_type", "unrecognized feature type "+strconv.Quote(strings.TrimSpace(name))+" mapped to other")
		}
		p.st.kind = ti.kind
	case c == "LAYER_CHANGE":
		p.st.sawLayerMarker = true
		p.st.needLayer = true
	case strings.HasPrefix(c, "LAYER:"):
		// Cura numbers layers from 0 and uses negative numbers for the raft/prep area.
		if n, err := strconv.Atoi(strings.TrimSpace(c[len("LAYER:"):])); err == nil && n >= 0 {
			p.st.sawLayerMarker = true
			p.st.needLayer = true
		}
	case strings.HasPrefix(c, "estimated printing time (normal mode) ="):
		if d, ok := parseDuration(c[strings.IndexByte(c, '=')+1:]); ok {
			p.res.SlicerEstimate = d
		}
	case strings.HasPrefix(c, "generated by "):
		if p.res.Version == "" {
			p.res.Version = strings.TrimSpace(c[len("generated by "):])
		}
	}
}

// parseDuration reads PrusaSlicer's "1d 2h 3m 4s".
func parseDuration(s string) (float64, bool) {
	var total float64
	found := false
	for _, f := range strings.Fields(s) {
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseFloat(f[:len(f)-1], 64)
		if err != nil {
			continue
		}
		switch f[len(f)-1] {
		case 'd':
			total += n * 86400
		case 'h':
			total += n * 3600
		case 'm':
			total += n * 60
		case 's':
			total += n
		default:
			continue
		}
		found = true
	}
	return total, found
}

func get(words []word, letter byte) (float64, bool) {
	for _, w := range words {
		if w.letter == letter {
			if w.raw == "" {
				return 0, false
			}
			return w.val, true
		}
	}
	return 0, false
}

func (p *parser) exec(cmd string, words []word) error {
	s := &p.st
	switch cmd {
	case "G0", "G1":
		return p.linear(cmd, words)
	case "G2", "G3":
		return p.arc(cmd, words)
	case "G90":
		s.absXYZ = true
		// RepRap semantics: G90/G91 switch XYZ only; extrusion mode is M82/M83, which
		// PrusaSlicer and Cura always emit explicitly.
	case "G91":
		s.absXYZ = false
	case "M82":
		s.absE = true
	case "M83":
		s.absE = false
	case "G92":
		return p.setPosition(words)
	case "G28":
		p.home(words)
	case "G10", "G11":
		// Firmware retraction: the length lives in the firmware config (M207), unknown here.
		p.note(toolpath.LevelWarning, "firmware_retract", "firmware retraction (G10/G11) is used; the retraction length is unknown to the simulation and is not modelled")
	default:
		if cmd[0] == 'T' {
			n, err := strconv.Atoi(cmd[1:])
			if err != nil || n < 0 || n > 255 {
				return p.fail(cmd, "tool number out of range")
			}
			s.tool = uint8(n)
			return nil
		}
		if why, bad := unsupported[cmd]; bad {
			return p.fail(cmd, "%s", why)
		}
		if why, ok := ignored[cmd]; ok {
			p.noteLazy(toolpath.LevelInfo, ignoredCode(cmd), func() string { return cmd + " (" + why + ") does not affect the toolpath and was not simulated" })
			return nil
		}
		if cmd[0] == 'G' {
			// An unknown G command may move the machine; guessing would misplace material.
			return p.fail(cmd, "unsupported command")
		}
		p.note(toolpath.LevelWarning, "unknown_"+strings.ToLower(cmd), "unknown command "+cmd+" was ignored")
	}
	return nil
}

func (p *parser) home(words []word) {
	s := &p.st
	all := true
	for _, w := range words {
		if w.letter == 'X' || w.letter == 'Y' || w.letter == 'Z' {
			all = false
		}
	}
	// Homing repositions the machine without printing; the simulation only needs the new origin.
	if all || has(words, 'X') {
		s.x, s.offX = 0, 0
	}
	if all || has(words, 'Y') {
		s.y, s.offY = 0, 0
	}
	if all || has(words, 'Z') {
		s.z, s.offZ = 0, 0
	}
	s.homed = true
	p.note(toolpath.LevelInfo, "home", "G28 homing resets the nozzle position without a visible move")
}

func has(words []word, l byte) bool {
	for _, w := range words {
		if w.letter == l {
			return true
		}
	}
	return false
}

func (p *parser) setPosition(words []word) error {
	if err := p.numeric("G92", words, "XYZE"); err != nil {
		return err
	}
	s := &p.st
	none := true
	for _, w := range words {
		switch w.letter {
		case 'X', 'Y', 'Z', 'E':
			none = false
		}
	}
	if none { // G92 alone zeroes every axis
		s.offX, s.offY, s.offZ, s.e = s.x, s.y, s.z, 0
		return nil
	}
	if v, ok := get(words, 'X'); ok {
		s.offX = s.x - v
	}
	if v, ok := get(words, 'Y'); ok {
		s.offY = s.y - v
	}
	if v, ok := get(words, 'Z'); ok {
		s.offZ = s.z - v
	}
	if v, ok := get(words, 'E'); ok {
		s.e = v
	}
	return nil
}

func (p *parser) target(words []word) (x, y, z float64, err error) {
	s := &p.st
	x, y, z = s.x, s.y, s.z
	resolve := func(l byte, cur, off float64) (float64, error) {
		v, ok := get(words, l)
		if !ok {
			return cur, nil
		}
		if s.absXYZ {
			v += off
		} else {
			v += cur
		}
		if math.Abs(v) > coordLimit {
			return 0, p.fail("", "coordinate %c=%g out of range", l, v)
		}
		return v, nil
	}
	if x, err = resolve('X', s.x, s.offX); err != nil {
		return
	}
	if y, err = resolve('Y', s.y, s.offY); err != nil {
		return
	}
	z, err = resolve('Z', s.z, s.offZ)
	return
}

// eDelta returns the filament change requested by the words and advances the logical E.
func (p *parser) eDelta(words []word) float64 {
	s := &p.st
	v, ok := get(words, 'E')
	if !ok {
		return 0
	}
	var d float64
	if s.absE {
		d = v - s.e
		s.e = v
	} else {
		d = v
		s.e += v
	}
	return d
}

func (p *parser) setFeed(words []word) error {
	if f, ok := get(words, 'F'); ok {
		if f <= 0 || f > 1e6 {
			return p.fail("", "feed rate F%g out of range", f)
		}
		p.st.feed = f / 60
	}
	return nil
}

// numeric rejects a parameter that must carry a number but does not ("G1 X Y10"): treating it
// as absent would silently drop part of the move.
func (p *parser) numeric(cmd string, words []word, letters string) error {
	for _, w := range words {
		if w.raw == "" && strings.IndexByte(letters, w.letter) >= 0 {
			return p.fail(cmd, "parameter %c needs a value", w.letter)
		}
	}
	return nil
}

func (p *parser) linear(cmd string, words []word) error {
	if err := p.numeric(cmd, words, "XYZEF"); err != nil {
		return err
	}
	if err := p.setFeed(words); err != nil {
		return err
	}
	x, y, z, err := p.target(words)
	if err != nil {
		return err
	}
	return p.move(x, y, z, p.eDelta(words))
}

// move emits a segment from the current position to (x,y,z) with filament change e.
func (p *parser) move(x, y, z, e float64) error {
	s := &p.st
	dx, dy, dz := x-s.x, y-s.y, z-s.z
	length := math.Sqrt(dx*dx + dy*dy + dz*dz)
	if length < 1e-9 && e == 0 {
		return nil
	}
	// Filament that only refills a retracted nozzle must not widen the bead: split the
	// unretract off a move that also deposits.
	var refill float64
	if e > 0 && s.retract > 0 {
		refill = math.Min(e, s.retract)
		s.retract -= refill
	} else if e < 0 {
		s.retract += -e
	}
	deposit := e - refill
	if refill > 0 && deposit > 1e-9 && length > 1e-9 {
		if err := p.emit(toolpath.Segment{
			X0: float32(s.x), Y0: float32(s.y), Z0: float32(s.z), X1: float32(s.x), Y1: float32(s.y), Z1: float32(s.z),
			Kind: toolpath.Travel, E: float32(refill), F: float32(s.feed), Tool: s.tool,
		}); err != nil {
			return err
		}
		e = deposit
	}
	kind := toolpath.Travel
	if deposit > 1e-9 && length > 1e-9 {
		kind = s.kind
	}
	seg := toolpath.Segment{
		X0: float32(s.x), Y0: float32(s.y), Z0: float32(s.z), X1: float32(x), Y1: float32(y), Z1: float32(z),
		Kind: kind, E: float32(e), F: float32(s.feed), Tool: s.tool,
	}
	if err := p.emit(seg); err != nil {
		return err
	}
	s.x, s.y, s.z = x, y, z
	return nil
}

func (p *parser) openLayer() {
	p.res.Layers = append(p.res.Layers, toolpath.Layer{First: len(p.res.Segments)})
	p.cur = &p.res.Layers[len(p.res.Layers)-1]
	p.curExtruded = false
}

func (p *parser) emit(seg toolpath.Segment) error {
	if len(p.res.Segments) >= p.opt.MaxSegments {
		return p.fail("", "more than %d segments; the model is too detailed to simulate", p.opt.MaxSegments)
	}
	s := &p.st
	switch {
	case p.cur == nil:
		p.openLayer()
	case s.needLayer && p.curExtruded: // a layer marker; an open layer with no deposition yet is reused
		p.openLayer()
	case !s.sawLayerMarker && seg.Kind.Extrudes() && p.curExtruded && seg.Z1 > p.cur.Z+1e-4:
		p.openLayer() // no markers in the file: a deposition at a new height starts a layer
	}
	s.needLayer = false
	if seg.Kind.Extrudes() && !p.curExtruded {
		p.cur.Z = seg.Z1
		p.curExtruded = true
	}
	p.res.Segments = append(p.res.Segments, seg)
	p.cur.Count++
	return nil
}

func (p *parser) arc(cmd string, words []word) error {
	if err := p.numeric(cmd, words, "XYZEFIJR"); err != nil {
		return err
	}
	if err := p.setFeed(words); err != nil {
		return err
	}
	if has(words, 'R') {
		return p.fail(cmd, "radius-form arcs (R) are not supported; use I/J offsets")
	}
	s := &p.st
	i, hasI := get(words, 'I')
	j, hasJ := get(words, 'J')
	if !hasI && !hasJ {
		return p.fail(cmd, "arc without I/J centre offsets")
	}
	x1, y1, z1, err := p.target(words)
	if err != nil {
		return err
	}
	e := p.eDelta(words)
	x0, y0, z0 := s.x, s.y, s.z
	cx, cy := x0+i, y0+j
	r := math.Hypot(i, j)
	if r < 1e-6 {
		return p.fail(cmd, "arc radius is zero")
	}
	if d := math.Abs(math.Hypot(x1-cx, y1-cy) - r); d > 0.1 {
		p.note(toolpath.LevelWarning, "arc_radius_mismatch", "an arc whose end point is not on the circle defined by its start and centre was drawn to the end point anyway")
	}
	a0 := math.Atan2(y0-cy, x0-cx)
	a1 := math.Atan2(y1-cy, x1-cx)
	sweep := a1 - a0
	cw := cmd == "G2"
	if cw {
		if sweep >= 0 {
			sweep -= 2 * math.Pi
		}
	} else if sweep <= 0 {
		sweep += 2 * math.Pi
	}
	if math.Abs(x1-x0) < 1e-9 && math.Abs(y1-y0) < 1e-9 { // same start and end: a full circle
		if cw {
			sweep = -2 * math.Pi
		} else {
			sweep = 2 * math.Pi
		}
	}
	step := 2 * math.Acos(math.Max(0, 1-p.opt.ArcTolerance/r))
	n := 1
	if step > 1e-6 {
		n = int(math.Ceil(math.Abs(sweep) / step))
	}
	if n < 1 {
		n = 1
	}
	if n > 2000 {
		n = 2000
	}
	for k := 1; k <= n; k++ {
		f := float64(k) / float64(n)
		var px, py float64
		if k == n {
			px, py = x1, y1
		} else {
			a := a0 + sweep*f
			px, py = cx+r*math.Cos(a), cy+r*math.Sin(a)
		}
		if err := p.move(px, py, z0+(z1-z0)*f, e/float64(n)); err != nil {
			return err
		}
	}
	return nil
}
