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
/* Sphere centered at origin. */
occ_status occ_sphere(double radius, occ_handle* out);
/* Torus centered at origin, axis +Z; major = ring radius, minor = tube radius (0 < minor < major). */
occ_status occ_torus(double major_radius, double minor_radius, occ_handle* out);
/* Prism of a closed planar polygon in the XY plane (xy = n interleaved x,y pairs, n >= 3),
 * extruded along +Z by height. */
occ_status occ_extrude_polygon(const double* xy, int32_t n, double height, occ_handle* out);

/* Sketch geometry. A segment is a 2D curve in the sketch plane (z=0):
 *   kind 0 line   a = x1,y1,x2,y2
 *   kind 1 arc    a = x1,y1, xm,ym (a point on the arc), x2,y2
 *   kind 2 circle a = cx,cy,r      (a full circle is a loop on its own)
 * Loop 0 is the outer boundary, the following loops are holes; segments of a loop must chain
 * end to start. The face is returned in the XY plane with a +Z normal. */
typedef struct { int32_t kind; double a[6]; } occ_seg2d;
occ_status occ_make_face(const occ_seg2d* segs, const int32_t* loop_sizes, int32_t n_loops, occ_handle* out);
/* Sweeps any shape along the vector (dx,dy,dz); a face becomes a solid. */
occ_status occ_prism(occ_handle s, double dx, double dy, double dz, occ_handle* out);
/* Moves a shape built in the local XY/+Z frame onto the frame (origin o, normal z, x direction x). */
occ_status occ_place(occ_handle s, const double o[3], const double z[3], const double x[3], occ_handle* out);
/* Placement frame of a planar face (index as in occ_tessellate): out = origin(3), outward
 * normal(3), x direction(3) = longest straight edge, sign-normalised. */
occ_status occ_face_frame(occ_handle s, int32_t face_index, double out[9]);

/* Booleans (inputs are not consumed) */
occ_status occ_boolean_union(occ_handle a, occ_handle b, occ_handle* out);
occ_status occ_boolean_subtract(occ_handle a, occ_handle b, occ_handle* out);
occ_status occ_boolean_intersect(occ_handle a, occ_handle b, occ_handle* out);

/* Edge treatments: all edges of the shape. */
occ_status occ_fillet_all_edges(occ_handle s, double radius, occ_handle* out);
occ_status occ_chamfer_all_edges(occ_handle s, double distance, occ_handle* out);

/* Edge treatments on chosen edges. Indices are 0-based positions in TopExp::MapShapes edge
 * order, i.e. the edge indices occ_tessellate reports. */
occ_status occ_fillet_edges(occ_handle s, const int32_t* edges, int32_t n, double radius, occ_handle* out);
occ_status occ_chamfer_edges(occ_handle s, const int32_t* edges, int32_t n, double distance, occ_handle* out);

/* Transforms */
occ_status occ_translate(occ_handle s, double dx, double dy, double dz, occ_handle* out);
occ_status occ_rotate(occ_handle s, double ox, double oy, double oz,
                      double ax, double ay, double az, double angle, occ_handle* out);

/* Mirror across the plane through (ox,oy,oz) with normal (nx,ny,nz). */
occ_status occ_mirror(occ_handle s, double ox, double oy, double oz,
                      double nx, double ny, double nz, occ_handle* out);

/* Scale about (ox,oy,oz) by (sx,sy,sz), all > 0. Equal factors keep analytic surfaces;
 * unequal ones convert them to B-splines. */
occ_status occ_scale(occ_handle s, double ox, double oy, double oz, double sx, double sy, double sz, occ_handle* out);

/* Face-level edits. face_index is as in occ_tessellate.
 *  occ_face_get            copy of the face (e.g. to sweep it with occ_prism)
 *  occ_face_inset          the planar face's region shrunk by distance from every boundary
 *  occ_face_imprint_inset  the shape with that inset outline imprinted (the face is split in two)
 *  occ_offset_face         the shape with that face moved by distance along its outward normal
 *                          (negative = inward); adjacent faces are extended or trimmed */
/* Merges coplanar / co-cylindrical neighbouring faces and collinear edges (face indices change). */
occ_status occ_unify_faces(occ_handle s, occ_handle* out);
occ_status occ_face_get(occ_handle s, int32_t face_index, occ_handle* out);
occ_status occ_face_inset(occ_handle s, int32_t face_index, double distance, occ_handle* out);
occ_status occ_face_imprint_inset(occ_handle s, int32_t face_index, double distance, occ_handle* out);
occ_status occ_offset_face(occ_handle s, int32_t face_index, double distance, occ_handle* out);

/* Queries */
occ_status occ_is_valid(occ_handle s, int32_t* valid);
occ_status occ_mass_props(occ_handle s, occ_props* out);

/* Export / import */
occ_status occ_export_step(occ_handle s, const char* path);
/* Binary STL if binary != 0, otherwise ASCII. Meshes with the given linear deflection. */
occ_status occ_export_stl(occ_handle s, const char* path, double deflection, int32_t binary);
occ_status occ_import_step(const char* path, occ_handle* out);


/* Tessellation for rendering and picking. A mesh carries the topology identity of what it
 * draws: faces, edges and vertices are numbered in the deterministic order of
 * TopExp::MapShapes, so index N means the same entity on every rebuild of the same shape.
 * Triangles are grouped by face (face i owns [tri_start, tri_start+tri_count)). */
typedef struct occ_mesh occ_mesh;

enum { OCC_SURF_OTHER = 0, OCC_SURF_PLANE = 1, OCC_SURF_CYLINDER = 2, OCC_SURF_CONE = 3,
       OCC_SURF_SPHERE = 4, OCC_SURF_TORUS = 5 };
enum { OCC_CURVE_OTHER = 0, OCC_CURVE_LINE = 1, OCC_CURVE_CIRCLE = 2 };

typedef struct {
  int32_t surface;                 /* OCC_SURF_* */
  double nx, ny, nz;               /* outward normal (plane) or axis direction (cylinder/cone) */
  double cx, cy, cz;               /* centroid */
  double area;
  double radius;                   /* cylinder/sphere/cone-base radius, else 0 */
  double min_x, min_y, min_z, max_x, max_y, max_z;
  int32_t tri_start, tri_count;
} occ_face_info;

typedef struct {
  int32_t curve;                   /* OCC_CURVE_* */
  double length;
  double sx, sy, sz, ex, ey, ez;   /* start / end point */
  double min_x, min_y, min_z, max_x, max_y, max_z;
  int32_t pt_start, pt_count;      /* polyline in the edge point buffer */
} occ_edge_info;

typedef struct {
  int32_t vertices, triangles, faces, edges, edge_points, topo_vertices;
} occ_mesh_counts;

/* Meshes the shape at the given linear deflection. Free with occ_mesh_free. */
occ_status occ_tessellate(occ_handle s, double deflection, occ_mesh** out);
/* As occ_tessellate with an explicit angular deflection (radians; occ_tessellate uses 0.5). */
occ_status occ_tessellate_ex(occ_handle s, double deflection, double angular, occ_mesh** out);
occ_status occ_mesh_get_counts(const occ_mesh* m, occ_mesh_counts* out);
/* Copies into caller buffers sized from occ_mesh_get_counts: positions/normals 3*vertices
 * floats, indices 3*triangles, faces, edges, edge_pts 3*edge_points floats,
 * vertex_pts 3*topo_vertices floats. Any output may be NULL to skip it. */
occ_status occ_mesh_copy(const occ_mesh* m, float* positions, float* normals, uint32_t* indices,
                         occ_face_info* faces, occ_edge_info* edges, float* edge_pts, float* vertex_pts);
void occ_mesh_free(occ_mesh* m);
int64_t occ_live_mesh_count(void);

#ifdef __cplusplus
}
#endif

#endif /* SIMONCAD_OCC_BRIDGE_H */
