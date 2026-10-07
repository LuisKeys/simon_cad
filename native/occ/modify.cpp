// modify.cpp — scaling and face-level edits: face extraction, inset outlines, imprint and
// per-face offset.
#include <algorithm>
#include <cmath>
#include <vector>

#include <BRepAdaptor_Surface.hxx>
#include <BRepBuilderAPI_GTransform.hxx>
#include <BRepBuilderAPI_MakeFace.hxx>
#include <BRepBuilderAPI_MakeSolid.hxx>
#include <BRepBuilderAPI_Transform.hxx>
#include <BRepCheck_Analyzer.hxx>
#include <BRepFeat_SplitShape.hxx>
#include <BRepGProp.hxx>
#include <BRepOffsetAPI_MakeOffset.hxx>
#include <BRepOffset_MakeOffset.hxx>
#include <BRep_Tool.hxx>
#include <GProp_GProps.hxx>
#include <ShapeFix_Face.hxx>
#include <ShapeUpgrade_UnifySameDomain.hxx>
#include <TopExp.hxx>
#include <TopExp_Explorer.hxx>
#include <TopTools_IndexedMapOfShape.hxx>
#include <TopoDS.hxx>
#include <TopoDS_Face.hxx>
#include <TopoDS_Shell.hxx>
#include <TopoDS_Solid.hxx>
#include <TopoDS_Wire.hxx>
#include <gp_GTrsf.hxx>
#include <gp_Mat.hxx>
#include <gp_Pln.hxx>
#include <gp_Trsf.hxx>

#include "internal.h"

using namespace simon;

namespace {

TopoDS_Face face_at(const TopoDS_Shape& sh, int32_t index) {
  TopTools_IndexedMapOfShape faces;
  TopExp::MapShapes(sh, TopAbs_FACE, faces);
  if (index < 0 || index >= faces.Extent()) throw BridgeError{OCC_ERR_INVALID_ARG, "face index out of range"};
  return TopoDS::Face(faces(index + 1));
}

gp_Pln plane_of(const TopoDS_Face& f) {
  BRepAdaptor_Surface surf(f);
  if (surf.GetType() != GeomAbs_Plane) throw BridgeError{OCC_ERR_INVALID_ARG, "face is not planar"};
  return surf.Plane();
}

double face_area(const TopoDS_Face& f) {
  GProp_GProps g;
  BRepGProp::SurfaceProperties(f, g);
  return std::fabs(g.Mass());
}

// Wires of the face's boundary offset inward by d (> 0): the outer loop shrinks, holes grow.
std::vector<TopoDS_Wire> inset_wires(const TopoDS_Face& f, double d) {
  if (!(d > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "inset distance must be positive"};
  plane_of(f);
  BRepOffsetAPI_MakeOffset mo(f, GeomAbs_Intersection);
  mo.Perform(-d);
  if (!mo.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "inset failed: the distance is probably too large for this face"};
  std::vector<TopoDS_Wire> out;
  for (TopExp_Explorer ex(mo.Shape(), TopAbs_WIRE); ex.More(); ex.Next()) {
    TopoDS_Wire w = TopoDS::Wire(ex.Current());
    if (w.Closed() || BRep_Tool::IsClosed(w)) out.push_back(w);
  }
  if (out.empty()) throw BridgeError{OCC_ERR_KERNEL, "inset left nothing of the face: use a smaller distance"};
  return out;
}

// The inset region of a planar face as a new face (largest loop outside, the rest as holes).
TopoDS_Face inset_face(const TopoDS_Face& f, double d) {
  gp_Pln pln = plane_of(f);
  std::vector<TopoDS_Wire> wires = inset_wires(f, d);
  std::vector<std::pair<double, TopoDS_Wire>> sized;
  for (const TopoDS_Wire& w : wires) {
    BRepBuilderAPI_MakeFace mf(pln, w, Standard_True);
    if (!mf.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "inset outline is not a valid loop"};
    sized.emplace_back(face_area(mf.Face()), w);
  }
  std::stable_sort(sized.begin(), sized.end(), [](const auto& a, const auto& b) { return a.first > b.first; });
  BRepBuilderAPI_MakeFace mf(pln, sized[0].second, Standard_True);
  for (size_t i = 1; i < sized.size(); i++) mf.Add(sized[i].second);
  if (!mf.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "could not build the inset face"};
  ShapeFix_Face fix(mf.Face());
  fix.FixOrientation();
  fix.Perform();
  TopoDS_Face res = fix.Face();
  // Keep the original face's outward orientation so a prism along its normal behaves the same.
  if (f.Orientation() == TopAbs_REVERSED) res.Reverse();
  return res;
}

// A valid solid from an offset result: the result itself when it holds solids, else its closed
// shells closed into solids. Throws when nothing valid comes out.
TopoDS_Shape as_solid(const TopoDS_Shape& s) {
  TopExp_Explorer solids(s, TopAbs_SOLID);
  TopoDS_Shape out = s;
  if (!solids.More()) {
    BRepBuilderAPI_MakeSolid ms;
    int n = 0;
    for (TopExp_Explorer ex(s, TopAbs_SHELL); ex.More(); ex.Next()) {
      TopoDS_Shell sh = TopoDS::Shell(ex.Current());
      if (!BRep_Tool::IsClosed(sh)) continue;
      ms.Add(sh);
      n++;
    }
    if (n == 0) throw BridgeError{OCC_ERR_KERNEL, "face offset did not produce a closed solid"};
    out = ms.Solid();
  }
  if (!BRepCheck_Analyzer(out).IsValid()) throw BridgeError{OCC_ERR_KERNEL, "face offset produced invalid geometry"};
  GProp_GProps g;
  BRepGProp::VolumeProperties(out, g);
  if (!(g.Mass() > 0)) throw BridgeError{OCC_ERR_KERNEL, "face offset produced an inside-out solid"};
  return out;
}

}  // namespace

extern "C" {

occ_status occ_scale(occ_handle s, double ox, double oy, double oz, double sx, double sy, double sz, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    TopoDS_Shape sh = require_shape(s);
    for (double f : {sx, sy, sz}) {
      if (!(f > 1e-6) || !std::isfinite(f)) throw BridgeError{OCC_ERR_INVALID_ARG, "scale factors must be positive"};
    }
    if (std::fabs(sx - sy) < 1e-12 && std::fabs(sy - sz) < 1e-12) {
      gp_Trsf t;
      t.SetScale(gp_Pnt(ox, oy, oz), sx);
      BRepBuilderAPI_Transform op(sh, t, Standard_True);
      if (!op.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "scale failed"};
      *out = store_shape(op.Shape());
      return;
    }
    // Non-uniform: x' = o + S (x - o). Analytic surfaces become B-splines.
    gp_GTrsf g;
    g.SetVectorialPart(gp_Mat(sx, 0, 0, 0, sy, 0, 0, 0, sz));
    g.SetTranslationPart(gp_XYZ(ox * (1 - sx), oy * (1 - sy), oz * (1 - sz)));
    BRepBuilderAPI_GTransform op(sh, g, Standard_True);
    if (!op.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "non-uniform scale failed"};
    *out = store_shape(op.Shape());
  });
}

occ_status occ_unify_faces(occ_handle s, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    ShapeUpgrade_UnifySameDomain u(require_shape(s), Standard_True, Standard_True, Standard_False);
    u.Build();
    *out = store_shape(u.Shape());
  });
}

occ_status occ_face_get(occ_handle s, int32_t face_index, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    *out = store_shape(face_at(require_shape(s), face_index));
  });
}

occ_status occ_face_inset(occ_handle s, int32_t face_index, double distance, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    *out = store_shape(inset_face(face_at(require_shape(s), face_index), distance));
  });
}

occ_status occ_face_imprint_inset(occ_handle s, int32_t face_index, double distance, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    TopoDS_Shape sh = require_shape(s);
    TopoDS_Face f = face_at(sh, face_index);
    std::vector<TopoDS_Wire> wires = inset_wires(f, distance);
    BRepFeat_SplitShape split(sh);
    for (const TopoDS_Wire& w : wires) split.Add(w, f);
    split.Build();
    if (!split.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "could not imprint the inset outline on the face"};
    *out = store_shape(split.Shape());
  });
}

occ_status occ_offset_face(occ_handle s, int32_t face_index, double distance, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!std::isfinite(distance) || std::fabs(distance) < 1e-9) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "offset distance must be non-zero"};
    }
    TopoDS_Shape sh = require_shape(s);
    TopoDS_Face f = face_at(sh, face_index);
    BRepOffset_MakeOffset mo;
    mo.Initialize(sh, 0.0, 1e-7, BRepOffset_Skin, Standard_True, Standard_False, GeomAbs_Intersection);
    mo.SetOffsetOnFace(f, distance);
    mo.MakeOffsetShape();
    if (!mo.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "face offset failed (the adjacent faces cannot be extended that far)"};
    *out = store_shape(as_solid(mo.Shape()));
  });
}

}  // extern "C"
