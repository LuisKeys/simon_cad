/*
 * occ_bridge.h — stable C ABI between Simon CAD (Go) and OpenCascade.
 *
 * Rules:
 *  - No OCCT type crosses this boundary. Shapes are opaque uint64 handles.
 *  - Every function returns an occ_status; on failure occ_last_error() holds a message
 *    (thread-local). C++ exceptions never escape.
 *  - Handles are owned by the caller and must be released with occ_release().
 *  - All lengths are millimetres, angles are radians.
 */
#ifndef SIMONCAD_OCC_BRIDGE_H
#define SIMONCAD_OCC_BRIDGE_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef uint64_t occ_handle; /* 0 is the invalid handle */
typedef int32_t occ_status;  /* 0 = OK, non-zero = failure */

enum {
  OCC_OK = 0,
  OCC_ERR_INVALID_ARG = 1,
  OCC_ERR_INVALID_HANDLE = 2,
  OCC_ERR_KERNEL = 3,  /* OpenCascade reported a failure */
  OCC_ERR_IO = 4,
  OCC_ERR_UNKNOWN = 5
};

typedef struct {
  double volume;
  double area;
  double cx, cy, cz;               /* center of mass */
  double min_x, min_y, min_z;      /* bounding box */
  double max_x, max_y, max_z;
  int32_t solid_count, face_count, edge_count;
} occ_props;

/* Diagnostics */
const char* occ_last_error(void);
int64_t occ_live_handle_count(void);
const char* occ_version(void);

/* Handle lifetime */
occ_status occ_release(occ_handle h);

/* Primitives */
occ_status occ_box(double dx, double dy, double dz, occ_handle* out);
/* Cylinder with base circle centered at origin, axis +Z. */
occ_status occ_cylinder(double radius, double height, occ_handle* out);

/* Cone frustum, base circle (radius r1) centered at origin, axis +Z, top radius r2 at z=height. */
occ_status occ_cone(double r1, double r2, double height, occ_handle* out);
/* Prism of a closed planar polygon in the XY plane (xy = n interleaved x,y pairs, n >= 3),
 * extruded along +Z by height. */
occ_status occ_extrude_polygon(const double* xy, int32_t n, double height, occ_handle* out);

/* Booleans (inputs are not consumed) */
occ_status occ_boolean_union(occ_handle a, occ_handle b, occ_handle* out);
occ_status occ_boolean_subtract(occ_handle a, occ_handle b, occ_handle* out);

/* Edge treatments: all edges of the shape. */
occ_status occ_fillet_all_edges(occ_handle s, double radius, occ_handle* out);
occ_status occ_chamfer_all_edges(occ_handle s, double distance, occ_handle* out);

/* Transforms */
occ_status occ_translate(occ_handle s, double dx, double dy, double dz, occ_handle* out);
occ_status occ_rotate(occ_handle s, double ox, double oy, double oz,
                      double ax, double ay, double az, double angle, occ_handle* out);

/* Mirror across the plane through (ox,oy,oz) with normal (nx,ny,nz). */
occ_status occ_mirror(occ_handle s, double ox, double oy, double oz,
                      double nx, double ny, double nz, occ_handle* out);

/* Queries */
occ_status occ_is_valid(occ_handle s, int32_t* valid);
occ_status occ_mass_props(occ_handle s, occ_props* out);

/* Export / import */
occ_status occ_export_step(occ_handle s, const char* path);
/* Binary STL if binary != 0, otherwise ASCII. Meshes with the given linear deflection. */
occ_status occ_export_stl(occ_handle s, const char* path, double deflection, int32_t binary);
occ_status occ_import_step(const char* path, occ_handle* out);

#ifdef __cplusplus
}
#endif

#endif /* SIMONCAD_OCC_BRIDGE_H */
