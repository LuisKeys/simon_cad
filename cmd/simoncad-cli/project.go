package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/accelone/simoncad/internal/app"
	"github.com/accelone/simoncad/internal/commands"
	"github.com/accelone/simoncad/internal/history"
	"github.com/accelone/simoncad/internal/tools"
)

func openProject(path string) (*app.App, error) {
	if path == "" {
		return nil, usageError{"project path required"}
	}
	return app.Open(path, time.Now)
}

func policyFrom(preview, commit bool, def app.Policy) (app.Policy, error) {
	switch {
	case preview && commit:
		return 0, usageError{"--preview and --commit are mutually exclusive"}
	case preview:
		return app.PolicyPreview, nil
	case commit:
		return app.PolicyCommit, nil
	}
	return def, nil
}

func ensureSelection(a *app.App, sel []string) error {
	if err := a.SetSelection(sel); err != nil {
		return fmt.Errorf("--select: %w", err)
	}
	return nil
}

func csSummary(cs *commands.ChangeSet) map[string]any {
	return map[string]any{
		"id": cs.ID, "state": cs.State, "intent": cs.Intent, "explanation": cs.Explanation,
		"affected": cs.Affected, "warnings": cs.Warnings, "validation": cs.Validation,
		"expected_effects": cs.Effects, "commands": cs.Commands,
	}
}

func describeOutcome(o app.Outcome) string {
	var sb strings.Builder
	switch {
	case !o.OK:
		fmt.Fprintf(&sb, "✗ %s rejected: %s\n", o.Tool, o.Error)
	case o.ChangeSet == nil:
		b, _ := json.MarshalIndent(o.Data, "", "  ")
		fmt.Fprintf(&sb, "✓ %s\n%s\n", o.Tool, b)
	case o.Committed:
		fmt.Fprintf(&sb, "✓ %s → %s committed: %s\n", o.Tool, o.ChangeSet.ID, o.ChangeSet.Explanation)
	default:
		fmt.Fprintf(&sb, "… %s → %s PROPOSED (not applied): %s\n  approve with: simoncad-cli commit <project> %s\n", o.Tool, o.ChangeSet.ID, o.ChangeSet.Explanation, o.ChangeSet.ID)
	}
	if o.ChangeSet != nil {
		for _, w := range o.ChangeSet.Warnings {
			fmt.Fprintf(&sb, "  warning: %s\n", w)
		}
	}
	return sb.String()
}

func outcomeJSON(o app.Outcome) map[string]any {
	m := map[string]any{"tool": o.Tool, "tool_call_id": o.ToolCallID, "accepted": o.OK, "committed": o.Committed}
	if o.Error != "" {
		m["error"] = o.Error
	}
	if o.Data != nil {
		m["result"] = o.Data
	}
	if o.ChangeSet != nil {
		m["changeset"] = csSummary(o.ChangeSet)
	}
	return m
}

func checkpointAfter(a *app.App, label string) *history.Checkpoint {
	return a.Checkpoint(label, false)
}

func runTool(args []string) error {
	fs := newFlags("tool")
	preview := fs.Bool("preview", false, "propose only; do not commit")
	commit := fs.Bool("commit", false, "commit even multi-command ChangeSets")
	label := fs.String("label", "", "checkpoint label")
	var sel multiFlag
	fs.Var(&sel, "select", "selection context selector (repeatable)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 || len(pos) > 3 {
		return usageError{`usage: tool <project> <tool> '<json>'`}
	}
	pol, err := policyFrom(*preview, *commit, app.PolicyAuto)
	if err != nil {
		return err
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	if err := ensureSelection(a, sel); err != nil {
		return err
	}
	raw := "{}"
	if len(pos) == 3 {
		raw = pos[2]
	}
	o := a.ApplyTool(pos[1], []byte(raw), pol)
	var cp *history.Checkpoint
	if o.Committed {
		l := *label
		if l == "" {
			l = o.ChangeSet.Intent
		}
		cp = checkpointAfter(a, l)
	}
	if err := a.Save(pos[0]); err != nil {
		return err
	}
	m := outcomeJSON(o)
	if cp != nil {
		m["checkpoint"] = cp.ID
	}
	if !o.OK && !jsonOut {
		fmt.Print(describeOutcome(o))
		return fmt.Errorf("tool call rejected")
	}
	if !o.OK {
		m["ok"] = false
		b, _ := json.MarshalIndent(m, "", "  ")
		fmt.Println(string(b))
		os.Exit(1)
	}
	emit(m, describeOutcome(o))
	return nil
}

func runTools(args []string) error {
	fs := newFlags("tools")
	if _, err := parseInterspersed(fs, args); err != nil {
		return err
	}
	reg := tools.Default()
	var list []map[string]any
	var sb strings.Builder
	for _, n := range reg.Names() {
		t, _ := reg.Get(n)
		list = append(list, map[string]any{"name": t.Name, "capability": t.Capability, "mutating": t.Mutating(), "description": t.Description, "schema": t.Schema})
		kind := "query"
		if t.Mutating() {
			kind = "edit "
		}
		first := t.Description
		if i := strings.Index(first, ". "); i > 0 {
			first = first[:i+1]
		}
		fmt.Fprintf(&sb, "%-18s %s [%s] %s\n", t.Name, kind, t.Capability, first)
	}
	emit(map[string]any{"tools": list}, sb.String())
	return nil
}

func runBuild(args []string) error {
	fs := newFlags("build")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"usage: build <project>"}
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	m, err := a.Model()
	if err != nil {
		return err
	}
	var sb strings.Builder
	var feats []map[string]any
	for _, id := range m.Order {
		r := m.Nodes[id]
		n := a.Graph.Get(id)
		mark := map[string]string{"ok": "✓", "failed": "✗", "suppressed": "–"}[r.Status]
		fmt.Fprintf(&sb, "%s %-16s %-14s %s\n", mark, id, n.Type, r.Status)
		for _, e := range r.Errors {
			fmt.Fprintf(&sb, "    %s\n", e)
		}
		f := map[string]any{"id": id, "type": n.Type, "status": r.Status}
		if len(r.Errors) > 0 {
			f["errors"] = r.Errors
		}
		feats = append(feats, f)
	}
	bodies := []map[string]any{}
	for _, id := range m.Leaves {
		p := m.Nodes[id].Body.Props
		bodies = append(bodies, map[string]any{"feature": id, "volume_mm3": p.Volume, "area_mm2": p.Area,
			"bbox_min": []float64{p.MinX, p.MinY, p.MinZ}, "bbox_max": []float64{p.MaxX, p.MaxY, p.MaxZ}})
		fmt.Fprintf(&sb, "body %s: volume %.3f mm³, bbox %.2f x %.2f x %.2f mm\n", id, p.Volume, p.MaxX-p.MinX, p.MaxY-p.MinY, p.MaxZ-p.MinZ)
	}
	failed := m.Failed()
	res := map[string]any{"features": feats, "bodies": bodies, "failed": failed, "model_hash": a.Graph.Hash(),
		"cache": map[string]int{"hits": a.Engine.Stats.Hits, "misses": a.Engine.Stats.Misses}, "occt_version": a.K.Version()}
	emit(res, sb.String())
	if len(failed) > 0 {
		return fmt.Errorf("%d feature(s) failed", len(failed))
	}
	return nil
}

func runExport(args []string) error {
	fs := newFlags("export")
	format := fs.String("format", "step", "step or stl")
	out := fs.String("out", "", "output file (default: <project>.<format>)")
	defl := fs.Float64("deflection", 0.05, "STL mesh deflection (mm)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || (*format != "step" && *format != "stl") {
		return usageError{"usage: export <project> --format step|stl [--out FILE]"}
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	path := *out
	if path == "" {
		path = strings.TrimSuffix(pos[0], filepath.Ext(pos[0])) + "." + *format
	}
	argMap := map[string]any{"path": path}
	if *format == "stl" {
		argMap["deflection_mm"] = *defl
	}
	body, _ := json.Marshal(argMap)
	res, err := a.Reg.Invoke(app.EnvFor(a), "export_"+*format, body)
	if err != nil {
		return err
	}
	data := res.Data.(map[string]any)
	human := fmt.Sprintf("✓ wrote %s (%v bytes, volume %.3f mm³)", path, data["bytes"], data["volume_mm3"])
	if r, ok := data["reimport_volume_rel_error"]; ok {
		human += fmt.Sprintf("\n  STEP re-import volume error: %.2e", r)
	}
	if w, ok := data["watertight"]; ok {
		human += fmt.Sprintf("\n  STL: %v triangles, watertight=%v", data["triangles"], w)
	}
	emit(data, human)
	return nil
}

func runHistory(args []string) error {
	fs := newFlags("history")
	compare := fs.String("compare", "", "two checkpoint refs: A,B")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"usage: history <project> [--compare A,B]"}
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	if *compare != "" {
		parts := strings.Split(*compare, ",")
		if len(parts) != 2 {
			return usageError{"--compare needs two comma-separated checkpoint refs"}
		}
		c1, err := a.Hist.Find(strings.TrimSpace(parts[0]))
		if err != nil {
			return err
		}
		c2, err := a.Hist.Find(strings.TrimSpace(parts[1]))
		if err != nil {
			return err
		}
		d := history.Compare(c1.Snapshot, c2.Snapshot)
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s (%q) → %s (%q)\n", c1.ID, c1.Label, c2.ID, c2.Label)
		for _, id := range d.Added {
			fmt.Fprintf(&sb, "  + %s\n", id)
		}
		for _, id := range d.Removed {
			fmt.Fprintf(&sb, "  - %s\n", id)
		}
		for _, c := range d.Changed {
			fmt.Fprintf(&sb, "  ~ %s.%s: %v → %v\n", c.Feature, c.Name, c.From, c.To)
		}
		if d.Empty() {
			sb.WriteString("  (no differences)\n")
		}
		emit(map[string]any{"from": c1.ID, "to": c2.ID, "diff": d}, sb.String())
		return nil
	}
	var sb strings.Builder
	cps := a.Hist.Recent(len(a.Hist.Checkpoints))
	for _, c := range cps {
		fmt.Fprintf(&sb, "%s  %s  [%s] %s  (changesets: %s)\n", c.ID, c.Time, c.Actor, c.Label, strings.Join(c.ChangeSets, ","))
	}
	if len(cps) == 0 {
		sb.WriteString("no checkpoints yet\n")
	}
	var sets []map[string]any
	for _, cs := range a.Mgr.Log {
		sets = append(sets, map[string]any{"id": cs.ID, "state": cs.State, "intent": cs.Intent, "tool_call": cs.Provenance.ToolCallID, "created_by": cs.Provenance.CreatedBy})
		if cs.State == commands.StateProposed {
			fmt.Fprintf(&sb, "PENDING %s: %s\n", cs.ID, cs.Intent)
		}
	}
	emit(map[string]any{"checkpoints": cps, "changesets": sets, "prompts": a.AH.Prompts, "tool_calls": len(a.AH.ToolCalls)}, sb.String())
	return nil
}

func runRestore(args []string) error {
	fs := newFlags("restore")
	preview := fs.Bool("preview", false, "propose only")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError{"usage: restore <project> <checkpoint>"}
	}
	pol, _ := policyFrom(*preview, !*preview, app.PolicyCommit)
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	o := a.Restore(pos[1], pol)
	if !o.OK {
		return fmt.Errorf("%s", o.Error)
	}
	var cp *history.Checkpoint
	if o.Committed {
		cp = checkpointAfter(a, "Restored "+pos[1])
	}
	if err := a.Save(pos[0]); err != nil {
		return err
	}
	m := outcomeJSON(o)
	if cp != nil {
		m["checkpoint"] = cp.ID
	}
	emit(m, describeOutcome(o))
	return nil
}

func runCommit(args []string) error { return decide(args, "commit") }
func runReject(args []string) error { return decide(args, "reject") }

func decide(args []string, verb string) error {
	fs := newFlags(verb)
	label := fs.String("label", "", "checkpoint label (commit)")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usageError{fmt.Sprintf("usage: %s <project> <changeset>", verb)}
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	var human string
	res := map[string]any{"changeset": pos[1]}
	if verb == "commit" {
		cs, err := a.Commit(pos[1])
		if err != nil {
			return err
		}
		l := *label
		if l == "" {
			l = cs.Intent
		}
		if cp := a.Checkpoint(l, false); cp != nil {
			res["checkpoint"] = cp.ID
		}
		human = fmt.Sprintf("✓ committed %s: %s", cs.ID, cs.Explanation)
	} else {
		if err := a.Reject(pos[1]); err != nil {
			return err
		}
		human = "✓ rejected " + pos[1]
	}
	if err := a.Save(pos[0]); err != nil {
		return err
	}
	emit(res, human)
	return nil
}

func runUndo(args []string) error {
	fs := newFlags("undo")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"usage: undo <project>"}
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	cs, err := a.Undo()
	if err != nil {
		return err
	}
	if err := a.Save(pos[0]); err != nil {
		return err
	}
	emit(map[string]any{"undone": cs.ID, "intent": cs.Intent}, fmt.Sprintf("✓ undid %s: %s", cs.ID, cs.Intent))
	return nil
}

func runReplay(args []string) error {
	fs := newFlags("replay")
	showEvents := fs.Bool("events", false, "print the recorded event timeline")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usageError{"usage: replay <project> [--events]"}
	}
	a, err := openProject(pos[0])
	if err != nil {
		return err
	}
	defer a.Close()
	g, match, n, err := a.Replay()
	if err != nil {
		return err
	}
	var sb strings.Builder
	counts := map[string]int{}
	for _, e := range a.Bus.Events() {
		counts[e.Type]++
		if *showEvents {
			fmt.Fprintf(&sb, "%4d %s %-22s %s\n", e.Seq, e.Time, e.Type, string(e.Data))
		}
	}
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)
	fmt.Fprintf(&sb, "replayed %d committed ChangeSet(s) onto an empty model → %d feature(s); matches saved model: %v\n", n, len(g.Nodes), match)
	for _, t := range types {
		fmt.Fprintf(&sb, "  %-22s %d\n", t, counts[t])
	}
	res := map[string]any{"changesets_replayed": n, "features": len(g.Nodes), "matches": match, "replayed_hash": g.Hash(), "saved_hash": a.Graph.Hash(), "event_counts": counts}
	if *showEvents {
		res["events"] = a.Bus.Events()
	}
	emit(res, sb.String())
	if !match {
		return fmt.Errorf("replay does not reproduce the saved model")
	}
	return nil
}
