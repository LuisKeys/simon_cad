// sketch.cpp — planar faces from 2D line/arc/circle loops, prisms, placement and face frames.
#include <cmath>
#include <vector>

#include <BRepAdaptor_Curve.hxx>
#include <BRepAdaptor_Surface.hxx>
#include <BRepBuilderAPI_MakeEdge.hxx>
#include <BRepBuilderAPI_MakeFace.hxx>
#include <BRepBuilderAPI_MakeWire.hxx>
#include <BRepBuilderAPI_Transform.hxx>
#include <BRepCheck_Analyzer.hxx>
#include <BRepClass_FaceClassifier.hxx>
#include <BRepGProp.hxx>
#include <BRep_Tool.hxx>
#include <BRepPrimAPI_MakePrism.hxx>
#include <GC_MakeArcOfCircle.hxx>
#include <GC_MakeCircle.hxx>
#include <Geom_BezierCurve.hxx>
#include <Geom_Circle.hxx>
#include <TColgp_Array1OfPnt.hxx>
#include <GeomAbs_CurveType.hxx>
#include <GeomAbs_SurfaceType.hxx>
#include <ShapeFix_Face.hxx>
#include <ShapeFix_Wire.hxx>
#include <TopAbs_ShapeEnum.hxx>
#include <TopExp.hxx>
#include <TopExp_Explorer.hxx>
#include <TopTools_IndexedMapOfShape.hxx>
#include <TopoDS.hxx>
#include <TopoDS_Edge.hxx>
#include <TopoDS_Face.hxx>
#include <TopoDS_Wire.hxx>
#include <GProp_GProps.hxx>
#include <gp_Ax2.hxx>
#include <gp_Ax3.hxx>
#include <gp_Circ.hxx>
#include <gp_Dir.hxx>
#include <gp_Pln.hxx>
#include <gp_Pnt.hxx>
#include <gp_Pnt2d.hxx>
#include <gp_Trsf.hxx>
#include <gp_Vec.hxx>

#include "internal.h"

using namespace simon;

namespace {

TopoDS_Wire make_loop(const occ_seg2d* segs, int32_t n) {
  BRepBuilderAPI_MakeWire mw;
  for (int32_t i = 0; i < n; i++) {
    const double* a = segs[i].a;
    TopoDS_Edge e;
    switch (segs[i].kind) {
      case 0: {
        gp_Pnt p1(a[0], a[1], 0), p2(a[2], a[3], 0);
        if (p1.Distance(p2) < 1e-9) throw BridgeError{OCC_ERR_INVALID_ARG, "zero-length line segment"};
        e = BRepBuilderAPI_MakeEdge(p1, p2);
        break;
      }
      case 1: {
        GC_MakeArcOfCircle arc(gp_Pnt(a[0], a[1], 0), gp_Pnt(a[2], a[3], 0), gp_Pnt(a[4], a[5], 0));
        if (!arc.IsDone()) throw BridgeError{OCC_ERR_INVALID_ARG, "arc points are collinear or coincident"};
        e = BRepBuilderAPI_MakeEdge(arc.Value());
        break;
      }
      case 2: {
        if (!(a[2] > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "circle radius must be positive"};
        e = BRepBuilderAPI_MakeEdge(gp_Circ(gp_Ax2(gp_Pnt(a[0], a[1], 0), gp_Dir(0, 0, 1)), a[2]));
        break;
      }
      case 3: {
        TColgp_Array1OfPnt poles(1, 4);
        for (int k = 0; k < 4; k++) poles.SetValue(k + 1, gp_Pnt(a[2 * k], a[2 * k + 1], 0));
        if (poles(1).Distance(poles(4)) < 1e-9 && poles(1).Distance(poles(2)) < 1e-9)
          throw BridgeError{OCC_ERR_INVALID_ARG, "degenerate bezier segment"};
        e = BRepBuilderAPI_MakeEdge(new Geom_BezierCurve(poles));
        break;
      }
      default: throw BridgeError{OCC_ERR_INVALID_ARG, "unknown segment kind"};
    }
    mw.Add(e);
    if (!mw.IsDone()) throw BridgeError{OCC_ERR_INVALID_ARG, "loop is not connected (segments must chain end to start)"};
  }
  if (!mw.IsDone()) throw BridgeError{OCC_ERR_INVALID_ARG, "loop construction failed"};
  TopoDS_Wire w = mw.Wire();
  if (!w.Closed()) throw BridgeError{OCC_ERR_INVALID_ARG, "loop is not closed"};
  return w;
}

}  // namespace

extern "C" {

occ_status occ_make_face(const occ_seg2d* segs, const int32_t* loop_sizes, int32_t n_loops, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!segs || !loop_sizes || n_loops < 1) throw BridgeError{OCC_ERR_INVALID_ARG, "sketch needs at least one loop"};
    int32_t off = 0;
    TopoDS_Wire outer;
    std::vector<TopoDS_Wire> holes;
    for (int32_t l = 0; l < n_loops; l++) {
      if (loop_sizes[l] < 1) throw BridgeError{OCC_ERR_INVALID_ARG, "empty loop"};
      TopoDS_Wire w = make_loop(segs + off, loop_sizes[l]);
      off += loop_sizes[l];
      if (l == 0) outer = w; else holes.push_back(w);
    }
    BRepBuilderAPI_MakeFace mf(gp_Pln(), outer);
    if (!mf.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "outer loop does not bound a planar face"};
    TopoDS_Face face = mf.Face();
    // Orient the outer loop counter-clockwise (+Z normal); holes get the opposite sense.
    ShapeFix_Face fix(face);
    fix.FixOrientation();
    face = fix.Face();
    const TopoDS_Face outerFace = face;
    for (auto& h : holes) {
      BRepBuilderAPI_MakeFace probe(gp_Pln(), h);
      if (!probe.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "hole loop does not bound a planar face"};
      // A hole must lie strictly inside the outer loop.
      TopExp_Explorer vx(h, TopAbs_VERTEX);
      if (!vx.More()) throw BridgeError{OCC_ERR_INVALID_ARG, "hole loop has no vertices"};
      gp_Pnt hp = BRep_Tool::Pnt(TopoDS::Vertex(vx.Current()));
      BRepClass_FaceClassifier cls(outerFace, gp_Pnt2d(hp.X(), hp.Y()), 1e-7);
      if (cls.State() != TopAbs_IN) throw BridgeError{OCC_ERR_INVALID_ARG, "hole loop is not inside the outer loop"};
      BRepBuilderAPI_MakeFace mfh(face);
      mfh.Add(h);
      face = mfh.Face();
    }
    ShapeFix_Face fix2(face);
    fix2.FixOrientation();
    fix2.Perform();
    face = fix2.Face();
    BRepCheck_Analyzer ana(face);
    if (!ana.IsValid()) throw BridgeError{OCC_ERR_INVALID_ARG, "sketch region is invalid (self-intersecting or hole outside outer loop)"};
    *out = store_shape(face);
  });
}

occ_status occ_prism(occ_handle s, double dx, double dy, double dz, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    TopoDS_Shape sh = require_shape(s);
    if (gp_Vec(dx, dy, dz).Magnitude() < 1e-9) throw BridgeError{OCC_ERR_INVALID_ARG, "prism vector is zero"};
    BRepPrimAPI_MakePrism mk(sh, gp_Vec(dx, dy, dz));
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "prism construction failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_place(occ_handle s, const double o[3], const double z[3], const double x[3], occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!o || !z || !x) throw BridgeError{OCC_ERR_INVALID_ARG, "placement frame is null"};
    TopoDS_Shape sh = require_shape(s);
    gp_Vec zv(z[0], z[1], z[2]), xv(x[0], x[1], x[2]);
    if (zv.Magnitude() < 1e-9 || xv.Magnitude() < 1e-9 || zv.CrossMagnitude(xv) < 1e-9)
      throw BridgeError{OCC_ERR_INVALID_ARG, "placement axes are degenerate or parallel"};
    gp_Ax3 target(gp_Pnt(o[0], o[1], o[2]), gp_Dir(zv), gp_Dir(xv));
    gp_Trsf t;
    t.SetTransformation(target);  // global -> target-local coordinates
    t.Invert();                   // target-local (our sketch) -> global
    BRepBuilderAPI_Transform tr(sh, t, true);
    if (!tr.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "placement failed"};
    *out = store_shape(tr.Shape());
  });
}

/* out = origin(3), outward normal(3), x direction(3). The x direction is the longest straight
 * edge of the face (lowest edge index on ties), sign fixed so its first non-zero component is
 * positive and projected into the face plane. */
occ_status occ_face_frame(occ_handle s, int32_t face_index, double out[9]) {
  return guarded([&] {
    require_out(out);
    TopoDS_Shape sh = require_shape(s);
    TopTools_IndexedMapOfShape faces;
    TopExp::MapShapes(sh, TopAbs_FACE, faces);
    if (face_index < 0 || face_index >= faces.Extent())
      throw BridgeError{OCC_ERR_INVALID_ARG, "face index out of range"};
    TopoDS_Face f = TopoDS::Face(faces(face_index + 1));
    BRepAdaptor_Surface surf(f);
    if (surf.GetType() != GeomAbs_Plane) throw BridgeError{OCC_ERR_INVALID_ARG, "face is not planar"};
    gp_Dir n = surf.Plane().Axis().Direction();
    if (f.Orientation() == TopAbs_REVERSED) n.Reverse();

    TopTools_IndexedMapOfShape edges;
    TopExp::MapShapes(f, TopAbs_EDGE, edges);
    double best = -1;
    gp_Vec bestDir;
    for (int i = 1; i <= edges.Extent(); i++) {
      TopoDS_Edge e = TopoDS::Edge(edges(i));
      BRepAdaptor_Curve c(e);
      if (c.GetType() != GeomAbs_Line) continue;
      gp_Vec d(c.Value(c.FirstParameter()), c.Value(c.LastParameter()));
      double len = d.Magnitude();
      if (len > best + 1e-9) { best = len; bestDir = d; }
    }
    gp_Vec nv(n);
    gp_Vec xdir;
    if (best > 0) {
      xdir = bestDir - nv * bestDir.Dot(nv);
    }
    if (best <= 0 || xdir.Magnitude() < 1e-9) {
      // No straight edge (e.g. a disc): pick a deterministic axis most perpendicular to n.
      gp_Vec ref = std::fabs(n.X()) < 0.9 ? gp_Vec(1, 0, 0) : gp_Vec(0, 1, 0);
      xdir = ref - nv * ref.Dot(nv);
    }
    xdir.Normalize();
    const double comp[3] = {xdir.X(), xdir.Y(), xdir.Z()};
    for (int i = 0; i < 3; i++) {
      if (std::fabs(comp[i]) > 1e-9) {
        if (comp[i] < 0) xdir.Reverse();
        break;
      }
    }
    GProp_GProps gp;
    BRepGProp::SurfaceProperties(f, gp);
    gp_Pnt c = gp.CentreOfMass();
    out[0] = c.X(); out[1] = c.Y(); out[2] = c.Z();
    out[3] = n.X(); out[4] = n.Y(); out[5] = n.Z();
    out[6] = xdir.X(); out[7] = xdir.Y(); out[8] = xdir.Z();
  });
}

}  // extern "C"
