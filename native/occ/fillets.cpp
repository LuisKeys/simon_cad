// fillets.cpp — fillet and chamfer applied to every edge.
#include <BRepFilletAPI_MakeChamfer.hxx>
#include <BRepFilletAPI_MakeFillet.hxx>
#include <TopExp.hxx>
#include <TopTools_IndexedMapOfShape.hxx>
#include <TopoDS.hxx>

#include "internal.h"

using namespace simon;

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

}  // extern "C"
