// primitives.cpp — box and cylinder.
#include <BRepPrimAPI_MakeBox.hxx>
#include <BRepBuilderAPI_MakeFace.hxx>
#include <BRepBuilderAPI_MakePolygon.hxx>
#include <BRepPrimAPI_MakeCone.hxx>
#include <BRepPrimAPI_MakeCylinder.hxx>
#include <BRepPrimAPI_MakePrism.hxx>
#include <gp_Pnt.hxx>
#include <gp_Vec.hxx>

#include "internal.h"

using namespace simon;

extern "C" {

occ_status occ_box(double dx, double dy, double dz, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(dx > 0 && dy > 0 && dz > 0)) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "box dimensions must be positive"};
    }
    BRepPrimAPI_MakeBox mk(dx, dy, dz);
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "box construction failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_cylinder(double radius, double height, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(radius > 0 && height > 0)) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "cylinder radius and height must be positive"};
    }
    BRepPrimAPI_MakeCylinder mk(radius, height);
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "cylinder construction failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_cone(double r1, double r2, double height, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!(r1 >= 0 && r2 >= 0 && r1 + r2 > 0 && height > 0)) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "cone radii must be non-negative (not both zero) and height positive"};
    }
    BRepPrimAPI_MakeCone mk(r1, r2, height);
    mk.Build();
    if (!mk.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "cone construction failed"};
    *out = store_shape(mk.Shape());
  });
}

occ_status occ_extrude_polygon(const double* xy, int32_t n, double height, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!xy || n < 3) throw BridgeError{OCC_ERR_INVALID_ARG, "polygon needs at least 3 points"};
    if (!(height > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "extrusion height must be positive"};
    BRepBuilderAPI_MakePolygon poly;
    for (int32_t i = 0; i < n; ++i) poly.Add(gp_Pnt(xy[2 * i], xy[2 * i + 1], 0.0));
    poly.Close();
    if (!poly.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "polygon wire construction failed"};
    BRepBuilderAPI_MakeFace face(poly.Wire(), true);
    if (!face.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "polygon is not planar/simple"};
    BRepPrimAPI_MakePrism prism(face.Face(), gp_Vec(0, 0, height));
    prism.Build();
    if (!prism.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "polygon extrusion failed"};
    *out = store_shape(prism.Shape());
  });
}

}  // extern "C"
