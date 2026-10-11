package tools

import (
	"fmt"
	"math"
	"strings"

	"github.com/accelone/simoncad/internal/geometry/features"
	"github.com/accelone/simoncad/internal/recipe"
)

func impressionTools() []*Tool {
	return []*Tool{
		{
			Name: "stamp_impression",
			Description: "Preview what a stamp prints. Returns two SVG drawings: `relief_svg` is the stamp seen from its relief side (the physical stamp), `impression_svg` is the mark it leaves in clay " +
				"(the relief mirrored left-right). With mirror=true the impression reads like the source drawing; with mirror=false it is reversed. The preview follows the stored recipe, so it is always in sync.",
			Capability: capGeometry,
			Schema:     object(props{"stamp": text("Stamp id (or its recipe id)")}, "stamp"),
			Query:      queryStampImpression,
		},
	}
}

type pt2 = [2]float64

// bezierD formats cubic segments (4 points each) as SVG path data, mirrored about x=0 when flip.
func bezierD(segs [][4]pt2, flip bool) string {
	var b strings.Builder
	f := func(p pt2) string {
		x := p[0]
		if flip {
			x = -x
		}
		return fmt.Sprintf("%.3f %.3f", x, p[1])
	}
	for i, s := range segs {
		if i == 0 {
			b.WriteString("M" + f(s[0]))
		}
		b.WriteString(fmt.Sprintf("C%s %s %s", f(s[1]), f(s[2]), f(s[3])))
	}
	b.WriteString("Z")
	return b.String()
}

func segsOfEntity(e map[string]any) ([][4]pt2, bool) {
	raw, ok := e["segments"].([]any)
	if !ok {
		return nil, false
	}
	var out [][4]pt2
	for _, s := range raw {
		quad, ok := s.([]any)
		if !ok || len(quad) != 4 {
			return nil, false
		}
		var q [4]pt2
		for k := range quad {
			p, ok := quad[k].([]any)
			if !ok || len(p) != 2 {
				return nil, false
			}
			x, ok1 := p[0].(float64)
			y, ok2 := p[1].(float64)
			if !ok1 || !ok2 {
				return nil, false
			}
			q[k] = pt2{x, y}
		}
		out = append(out, q)
	}
	return out, true
}

func queryStampImpression(env Env, a Args) (any, error) {
	rn, err := findRecipeNode(env, a.Str("stamp"))
	if err != nil {
		return nil, err
	}
	st, _, err := recipe.ParseStamp(rn.Parameters)
	if err != nil {
		return nil, features.Coded(features.CodeRecipeInvalid, "", "%v", err)
	}
	id := recipeBaseID(rn)
	W, H := st.Base.Width, st.Base.Height
	if st.Base.Shape == "circle" {
		W, H = st.Base.Diameter, st.Base.Diameter
	}
	// the relief as built (seen from its relief side): collect its drawing commands
	var built []string // path elements, un-mirrored
	var impr []string  // the same elements mirrored
	elem := func(d string, fill bool, width float64) string {
		if fill {
			return fmt.Sprintf(`<path d="%s" fill="#111" fill-rule="evenodd"/>`, d)
		}
		return fmt.Sprintf(`<path d="%s" fill="none" stroke="#111" stroke-width="%.3f" stroke-linecap="round" stroke-linejoin="round"/>`, d, width)
	}
	designID := st.Design.Source.Sketch
	if designID == "" {
		designID = id + "_design"
	}
	{
		sk := env.Graph().Get(designID)
		if sk == nil || sk.Type != "sketch" {
			return nil, fmt.Errorf("the stamp's design sketch %q is gone", designID)
		}
		ents := map[string][][4]pt2{}
		list, _ := normalizeJSON(sk.Parameters["entities"]).([]any)
		for _, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if segs, ok := segsOfEntity(m); ok {
				id, _ := m["id"].(string)
				ents[id] = segs
			}
		}
		regions, _ := normalizeJSON(sk.Parameters["regions"]).([]any)
		if len(regions) == 0 { // every closed entity is its own region
			for id := range ents {
				regions = append(regions, map[string]any{"id": id, "outer": []any{id}})
			}
		}
		for _, r := range regions {
			rm, _ := r.(map[string]any)
			var db, di strings.Builder
			addLoop := func(ids []any) {
				for _, x := range ids {
					if segs := ents[fmt.Sprint(x)]; segs != nil {
						db.WriteString(bezierD(segs, st.Mirror)) // as built: mirrored when the relief is
						di.WriteString(bezierD(segs, !st.Mirror))
					}
				}
			}
			outer, _ := rm["outer"].([]any)
			addLoop(outer)
			if holes, ok := rm["holes"].([]any); ok {
				for _, h := range holes {
					hl, _ := h.([]any)
					addLoop(hl)
				}
			}
			built = append(built, elem(db.String(), true, 0))
			impr = append(impr, elem(di.String(), true, 0))
		}
	}
	if len(built) == 0 {
		return nil, fmt.Errorf("the stamp has no relief to preview")
	}
	margin := math.Max(W, H) * 0.06
	svg := func(body []string, plate string) string {
		return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="%.3f %.3f %.3f %.3f" width="%.1f" height="%.1f"><g transform="scale(1 -1)">%s%s</g></svg>`,
			-W/2-margin, -H/2-margin, W+2*margin, H+2*margin, (W+2*margin)*4, (H+2*margin)*4, plate, strings.Join(body, ""))
	}
	plate := fmt.Sprintf(`<rect x="%.3f" y="%.3f" width="%.3f" height="%.3f" fill="#f4efe6" stroke="#8a7f6c" stroke-width="0.4"/>`, -W/2, -H/2, W, H)
	if st.Base.Shape == "circle" {
		plate = fmt.Sprintf(`<circle cx="0" cy="0" r="%.3f" fill="#f4efe6" stroke="#8a7f6c" stroke-width="0.4"/>`, W/2)
	}
	note := "The impression is the relief mirrored left-right, so it reads like the source drawing."
	if !st.Mirror {
		note = "mirror is off: the impression is the source drawing reversed (text reads backwards). Use update_recipe with {\"mirror\": true} for lettering."
	}
	return map[string]any{
		"stamp": id, "mirrored": st.Mirror, "impression_reads_like_source": st.Mirror, "note": note,
		"size_mm":        []float64{W, H},
		"relief_svg":     svg(built, plate),
		"impression_svg": svg(impr, plate),
	}, nil
}
