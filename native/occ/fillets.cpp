// fillets.cpp — fillet and chamfer on every edge or on chosen edges.
#include <string>

#include <BRepFilletAPI_MakeChamfer.hxx>
#include <BRepFilletAPI_MakeFillet.hxx>
#include <TopExp.hxx>
#include <TopTools_IndexedMapOfShape.hxx>
#include <TopoDS.hxx>

#include "internal.h"

using namespace simon;

namespace {

// Resolves 0-based edge indices (TopExp::MapShapes order) against the shape.
template <typename Maker>
void add_edges(Maker& mk, const TopoDS_Shape& s, const int32_t* idx, int32_t n, double size) {
  if (!idx || n <= 0) throw BridgeError{OCC_ERR_INVALID_ARG, "no edges given"};
  TopTools_IndexedMapOfShape edges;
  TopExp::MapShapes(s, TopAbs_EDGE, edges);
  for (int32_t i = 0; i < n; ++i) {
    if (idx[i] < 0 || idx[i] >= edges.Extent()) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "edge index " + std::to_string(idx[i]) + " is out of range (0.." +
                                                 std::to_string(edges.Extent() - 1) + ")"};
    }
    mk.Add(size, TopoDS::Edge(edges(idx[i] + 1)));
  }
}

}  // namespace

extern "C" {

occ_status occ_fillet_all_edges(occ_handle h, double radius, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(radius > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "fillet radius must be positive"};
    TopoDS_Shape s = require_shape(h);
    BRepFilletAPI_MakeFillet mk(s);
    TopTools_IndexedMapOfShape edges;
    TopExp::MapShapes(s, TopAbs_EDGE, edges);
    for (int i = 1; i <= edges.Extent(); ++i) mk.Add(radius, TopoDS::Edge(edges(i)));
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "fillet failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_chamfer_all_edges(occ_handle h, double distance, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(distance > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "chamfer distance must be positive"};
    TopoDS_Shape s = require_shape(h);
    BRepFilletAPI_MakeChamfer mk(s);
    TopTools_IndexedMapOfShape edges;
    TopExp::MapShapes(s, TopAbs_EDGE, edges);
    for (int i = 1; i <= edges.Extent(); ++i) mk.Add(distance, TopoDS::Edge(edges(i)));
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "chamfer failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_fillet_edges(occ_handle h, const int32_t* edges, int32_t n, double radius, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(radius > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "fillet radius must be positive"};
    TopoDS_Shape s = require_shape(h);
    BRepFilletAPI_MakeFillet mk(s);
    add_edges(mk, s, edges, n, radius);
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "fillet failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_chamfer_edges(occ_handle h, const int32_t* edges, int32_t n, double distance, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(distance > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "chamfer distance must be positive"};
    TopoDS_Shape s = require_shape(h);
    BRepFilletAPI_MakeChamfer mk(s);
    add_edges(mk, s, edges, n, distance);
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "chamfer failed"};
    *out = store_shape(mk.Shape());
  });
}

}  // extern "C"
