// booleans.cpp — union, subtraction and intersection.
#include <BRepAlgoAPI_Common.hxx>
#include <BRepAlgoAPI_Cut.hxx>
#include <BRepAlgoAPI_Fuse.hxx>

#include "internal.h"

using namespace simon;

extern "C" {

occ_status occ_boolean_union(occ_handle a, occ_handle b, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    BRepAlgoAPI_Fuse op(require_shape(a), require_shape(b));
    if (!op.IsDone() || op.HasErrors()) throw BridgeError{OCC_ERR_KERNEL, "boolean union failed"};
    *out = store_shape(op.Shape());
  });
}

occ_status occ_boolean_subtract(occ_handle a, occ_handle b, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    BRepAlgoAPI_Cut op(require_shape(a), require_shape(b));
    if (!op.IsDone() || op.HasErrors()) {
      throw BridgeError{OCC_ERR_KERNEL, "boolean subtraction failed"};
    }
    *out = store_shape(op.Shape());
  });
}

occ_status occ_boolean_intersect(occ_handle a, occ_handle b, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    BRepAlgoAPI_Common op(require_shape(a), require_shape(b));
    if (!op.IsDone() || op.HasErrors()) {
      throw BridgeError{OCC_ERR_KERNEL, "boolean intersection failed"};
    }
    *out = store_shape(op.Shape());
  });
}

}  // extern "C"
