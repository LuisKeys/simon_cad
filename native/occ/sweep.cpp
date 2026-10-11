// sweep.cpp — sweeps a profile through caller-placed sections (smooth loft), with analytic
// ellipse sections for round profiles, hollow profiles, ring (closed) paths and pointed ends.
#include <algorithm>
#include <cmath>
#include <vector>

#include <BRepAlgoAPI_Cut.hxx>
#include <BRepBuilderAPI_MakeEdge.hxx>
#include <BRepBuilderAPI_MakeFace.hxx>
#include <BRepBuilderAPI_MakePolygon.hxx>
#include <BRepBuilderAPI_MakeSolid.hxx>
#include <BRepBuilderAPI_MakeVertex.hxx>
#include <BRepBuilderAPI_MakeWire.hxx>
#include <BRepBuilderAPI_Sewing.hxx>
#include <BRepGProp.hxx>
#include <BRep_Builder.hxx>
#include <BRepOffsetAPI_ThruSections.hxx>
#include <GProp_GProps.hxx>
#include <Geom_Ellipse.hxx>
#include <ShapeFix_Solid.hxx>
#include <TopAbs_ShapeEnum.hxx>
#include <TopExp_Explorer.hxx>
#include <TopoDS.hxx>
#include <TopoDS_Compound.hxx>
#include <TopoDS_Shell.hxx>
#include <TopoDS_Solid.hxx>
#include <TopoDS_Wire.hxx>
#include <gp_Ax2.hxx>
#include <gp_Dir.hxx>
#include <gp_Pnt.hxx>
#include <gp_Vec.hxx>

#include "internal.h"

using namespace simon;

namespace {

constexpr double kPi = 3.14159265358979323846;

struct Contour {
  occ_contour hdr;
  const double* xy;  // polygon points: kind 0 one shared loop (2*n doubles), kind 2 one loop per section
};

// pts is the polygon this section uses (kind 0/2); unused for ellipses.
TopoDS_Wire section_wire(const Contour& c, const occ_section& s, const double* pts) {
  gp_Pnt O(s.o[0], s.o[1], s.o[2]);
  gp_Vec X(s.x[0], s.x[1], s.x[2]), Y(s.y[0], s.y[1], s.y[2]);
  if (c.hdr.kind == 1) {
    double rx = c.hdr.rx * s.sx, ry = c.hdr.ry * s.sy;
    if (!(rx > 1e-9) || !(ry > 1e-9)) throw BridgeError{OCC_ERR_INVALID_ARG, "section scale collapses the profile"};
    gp_Dir xd(X), yd(Y);
    gp_Dir zd = xd.Crossed(yd);
    // Every ellipse must start on +X and run toward +Y so consecutive sections stay aligned
    // whichever radius is larger.
    Handle(Geom_Ellipse) el;
    BRepBuilderAPI_MakeEdge me;
    if (rx >= ry) {
      el = new Geom_Ellipse(gp_Ax2(O, zd, xd), rx, ry);
      me = BRepBuilderAPI_MakeEdge(el, 0.0, 2 * kPi);
    } else {
      el = new Geom_Ellipse(gp_Ax2(O, zd, yd), ry, rx);  // major along Y, local Y axis = -X
      me = BRepBuilderAPI_MakeEdge(el, -kPi / 2, 3 * kPi / 2);
    }
    if (!me.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "ellipse section failed"};
    return BRepBuilderAPI_MakeWire(me.Edge());
  }
  BRepBuilderAPI_MakePolygon poly;
  for (int32_t i = 0; i < c.hdr.n; i++) {
    double x = pts[2 * i] * s.sx, y = pts[2 * i + 1] * s.sy;
    poly.Add(O.Translated(X * x + Y * y));
  }
  poly.Close();
  if (!poly.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "polygon section failed"};
  return poly.Wire();
}

TopoDS_Shape positive_solid(TopoDS_Shape s) {
  GProp_GProps g;
  BRepGProp::VolumeProperties(s, g);
  if (g.Mass() < 0) s.Reverse();
  return s;
}

// Longest run of sections lofted in one go. OCCT's smooth loft interpolates all its sections
// with one surface; with many sections (or unevenly spaced ones) that approximation can return a
// "valid" solid that is simply wrong. Short runs that share their boundary section are well
// conditioned, and joined (sewn) they follow the same sections. The tangent kink at a joint is
// far below the sampling tolerance.
constexpr int kChunk = 10;  // keep equal to sweep.SectionsPerRun (internal/geometry/sweep/plan.go)

// Adds the lateral faces through wires W[lo..hi] to the sewing, in runs of at most kChunk wires.
// v0 / v1 optionally close the first / last run at an apex vertex.
void add_lateral(BRepBuilderAPI_Sewing& sew, const std::vector<TopoDS_Wire>& W, int lo, int hi, const double* v0,
                 const double* v1, bool ruled) {
  const int step = ruled ? hi - lo : kChunk - 1;  // ruled bands are exact: one run
  for (int a = lo; a < hi; a += step) {
    const int b = std::min(a + step, hi);
    BRepOffsetAPI_ThruSections gen(false, ruled, 1e-6);
    gen.CheckCompatibility(false);
    if (a == lo && v0) gen.AddVertex(BRepBuilderAPI_MakeVertex(gp_Pnt(v0[0], v0[1], v0[2])));
    for (int i = a; i <= b; i++) gen.AddWire(W[i]);
    if (b == hi && v1) gen.AddVertex(BRepBuilderAPI_MakeVertex(gp_Pnt(v1[0], v1[1], v1[2])));
    gen.Build();
    if (!gen.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "loft through the sweep sections failed"};
    sew.Add(gen.Shape());
  }
}

TopoDS_Shape sewn_solid(BRepBuilderAPI_Sewing& sew, const char* what) {
  sew.Perform();
  TopoDS_Shape sewn = sew.SewedShape();
  TopoDS_Shell shell;
  TopExp_Explorer ex(sewn, TopAbs_SHELL);
  if (ex.More()) {
    shell = TopoDS::Shell(ex.Current());
  } else {  // a single doubly-periodic face is returned bare
    BRep_Builder bb;
    bb.MakeShell(shell);
    for (TopExp_Explorer f(sewn, TopAbs_FACE); f.More(); f.Next()) bb.Add(shell, f.Current());
  }
  BRepBuilderAPI_MakeSolid ms(shell);
  if (!ms.IsDone()) throw BridgeError{OCC_ERR_KERNEL, std::string(what) + " did not close into a solid"};
  ShapeFix_Solid fix(ms.Solid());
  fix.Perform();
  return positive_solid(fix.Solid());
}

// One contour through all its sections: the body (sections cs .. n-1-ce), an optional cap at each
// end (the rings 0..cs and n-1-ce..n-1 running to the apex) and flat faces where there is none.
// A closed sweep is a ring: the last run returns to the first section.
TopoDS_Shape build_solid(const Contour& c, const std::vector<occ_section>& secs, const std::vector<const double*>& pts,
                         bool closed, int cs, int ce, const double* a0, const double* a1, bool body_ruled,
                         bool cs_ruled, bool ce_ruled) {
  const int n = (int)secs.size();
  std::vector<TopoDS_Wire> W;
  W.reserve(n + 1);
  for (int i = 0; i < n; i++) W.push_back(section_wire(c, secs[i], pts[i]));
  BRepBuilderAPI_Sewing sew(1e-6);
  if (closed) {
    W.push_back(W.front());
    add_lateral(sew, W, 0, n, nullptr, nullptr, body_ruled);
    return sewn_solid(sew, "closed sweep");
  }
  if (n - cs - ce < 2) throw BridgeError{OCC_ERR_INVALID_ARG, "sweep body needs at least 2 sections besides its caps"};
  auto flat = [&](int i) {
    BRepBuilderAPI_MakeFace mf(W[i], /*OnlyPlane=*/true);
    if (!mf.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "flat end cap failed"};
    sew.Add(mf.Face());
  };
  add_lateral(sew, W, cs, n - 1 - ce, nullptr, nullptr, body_ruled);
  if (cs > 0) add_lateral(sew, W, 0, cs, a0, nullptr, cs_ruled); else flat(0);
  if (ce > 0) add_lateral(sew, W, n - 1 - ce, n - 1, nullptr, a1, ce_ruled); else flat(n - 1);
  return sewn_solid(sew, "sweep");
}

// A sweep that is only a cap: one section running to an apex (a cone when ruled).
TopoDS_Shape apex_solid(const Contour& c, const occ_section& s, const double* pts, const double* a0, const double* a1,
                        bool ruled) {
  BRepOffsetAPI_ThruSections gen(true, ruled, 1e-6);
  gen.CheckCompatibility(false);
  if (a0) gen.AddVertex(BRepBuilderAPI_MakeVertex(gp_Pnt(a0[0], a0[1], a0[2])));
  gen.AddWire(section_wire(c, s, pts));
  if (a1) gen.AddVertex(BRepBuilderAPI_MakeVertex(gp_Pnt(a1[0], a1[1], a1[2])));
  gen.Build();
  if (!gen.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "cap through the sweep section failed"};
  return positive_solid(gen.Shape());
}

// Returns the single solid of s, or s itself when there are several.
TopoDS_Shape single_solid(const TopoDS_Shape& s) {
  TopoDS_Shape first;
  int n = 0;
  for (TopExp_Explorer ex(s, TopAbs_SOLID); ex.More(); ex.Next()) {
    if (n++ == 0) first = ex.Current();
  }
  return n == 1 ? first : s;
}

}  // namespace

occ_status occ_sweep(const occ_section* secs, int32_t n_secs, const occ_contour* contours, int32_t n_contours,
                     const double* xy, int32_t closed, const double* apex_start, const double* apex_end,
                     int32_t ruled, int32_t cap_start_n, int32_t cap_end_n, int32_t cap_start_ruled,
                     int32_t cap_end_ruled, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    // An end cap is a sweep of one or more sections that finishes at a point.
    int32_t min_secs = (apex_start || apex_end) ? 1 : 2;
    if (!secs || n_secs < min_secs) throw BridgeError{OCC_ERR_INVALID_ARG, "sweep needs at least 2 sections (1 with an apex)"};
    if (closed && n_secs < 3) throw BridgeError{OCC_ERR_INVALID_ARG, "a closed sweep needs at least 3 sections"};
    if (!contours || n_contours < 1) throw BridgeError{OCC_ERR_INVALID_ARG, "sweep needs a profile contour"};
    std::vector<Contour> cs;
    const double* cursor = xy;
    for (int32_t i = 0; i < n_contours; i++) {
      Contour c{contours[i], cursor};
      if (c.hdr.kind == 0 || c.hdr.kind == 2) {
        if (c.hdr.n < 3 || !cursor) throw BridgeError{OCC_ERR_INVALID_ARG, "polygon contour needs at least 3 points"};
        cursor += 2 * c.hdr.n * (c.hdr.kind == 2 ? n_secs : 1);
      } else if (c.hdr.kind != 1) {
        throw BridgeError{OCC_ERR_INVALID_ARG, "unknown contour kind"};
      }
      cs.push_back(c);
    }
    if (cs.front().hdr.hole) throw BridgeError{OCC_ERR_INVALID_ARG, "the first contour must be the outer boundary"};
    bool holes = false;
    for (const auto& c : cs) holes = holes || c.hdr.hole;
    if (holes && (closed == 0) && (apex_start || apex_end))
      throw BridgeError{OCC_ERR_INVALID_ARG, "hollow profiles support flat end caps only"};

    std::vector<occ_section> base(secs, secs + n_secs);
    // Polygon pointers per section: shared loop (kind 0), own loop (kind 2), none (ellipse).
    auto points_of = [&](const Contour& c) {
      std::vector<const double*> v(base.size(), nullptr);
      for (size_t i = 0; i < v.size(); i++) {
        if (c.hdr.kind == 0) v[i] = c.xy;
        if (c.hdr.kind == 2) v[i] = c.xy + 2 * (size_t)c.hdr.n * i;
      }
      return v;
    };
    TopoDS_Shape result;
    if (!closed && (cap_start_n > 0 || cap_end_n > 0)) {
      if (holes) throw BridgeError{OCC_ERR_INVALID_ARG, "hollow profiles support flat end caps only"};
      if ((cap_start_n > 0 && !apex_start) || (cap_end_n > 0 && !apex_end))
        throw BridgeError{OCC_ERR_INVALID_ARG, "a cap needs its apex point"};
    }
    if (!closed && n_secs == 1) {  // a lone cap section
      result = apex_solid(cs.front(), base[0], points_of(cs.front())[0], apex_start, apex_end, ruled != 0);
    } else if (!closed && n_secs >= 2 && (apex_start || apex_end) && cap_start_n == 0 && cap_end_n == 0) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "an apex needs cap_start_n / cap_end_n (or a single section)"};
    } else {
      result = build_solid(cs.front(), base, points_of(cs.front()), closed != 0, closed ? 0 : cap_start_n,
                           closed ? 0 : cap_end_n, apex_start, apex_end, ruled != 0, cap_start_ruled != 0, cap_end_ruled != 0);
    }

    for (size_t i = 1; i < cs.size(); i++) {
      if (!cs[i].hdr.hole) throw BridgeError{OCC_ERR_INVALID_ARG, "only one outer contour per sweep"};
      // Open sweeps: extend the hole past both end planes so the cut opens the end caps cleanly.
      std::vector<occ_section> ext = base;
      if (!closed) {
        const double e = 1.0;
        auto shifted = [&](const occ_section& s, double d) {
          gp_Vec x(s.x[0], s.x[1], s.x[2]), y(s.y[0], s.y[1], s.y[2]);
          gp_Vec t = x.Crossed(y);
          t.Normalize();
          occ_section r = s;
          for (int k = 0; k < 3; k++) r.o[k] = s.o[k] + d * t.Coord(k + 1);
          return r;
        };
        ext.insert(ext.begin(), shifted(base.front(), -e));
        ext.push_back(shifted(base.back(), e));
      }
      std::vector<const double*> hp = points_of(cs[i]);
      if (!closed) {
        hp.insert(hp.begin(), hp.front());
        hp.push_back(hp.back());
      }
      TopoDS_Shape hole = build_solid(cs[i], ext, hp, closed != 0, 0, 0, nullptr, nullptr, false, false, false);
      BRepAlgoAPI_Cut cut(result, hole);
      if (!cut.IsDone() || cut.HasErrors()) throw BridgeError{OCC_ERR_KERNEL, "cutting the hollow of the sweep failed"};
      result = single_solid(cut.Shape());
    }
    *out = store_shape(result);
  });
}
