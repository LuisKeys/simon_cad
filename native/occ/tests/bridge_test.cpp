// bridge_test.cpp — exercises the C ABI directly: validity, volumes, errors and leak counting.
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <vector>

#include "occ_bridge.h"

static int failures = 0;
#define CHECK(cond)                                                      \
  do {                                                                   \
    if (!(cond)) {                                                       \
      std::fprintf(stderr, "FAIL %s:%d: %s (%s)\n", __FILE__, __LINE__, #cond, occ_last_error()); \
      ++failures;                                                        \
    }                                                                    \
  } while (0)

int main() {
  occ_handle box = 0, cyl = 0, cut = 0, fil = 0;
  CHECK(occ_box(80, 60, 4, &box) == OCC_OK);
  CHECK(occ_cylinder(1.6, 10, &cyl) == OCC_OK);
  CHECK(occ_boolean_subtract(box, cyl, &cut) == OCC_OK);

  occ_props p;
  CHECK(occ_mass_props(cut, &p) == OCC_OK);
  // The cylinder is centred on the box corner, so a quarter of it pierces the plate.
  double expected = 80.0 * 60 * 4 - 0.25 * M_PI * 1.6 * 1.6 * 4;
  CHECK(std::fabs(p.volume - expected) / expected < 1e-6);

  int32_t valid = 0;
  CHECK(occ_is_valid(cut, &valid) == OCC_OK && valid == 1);
  CHECK(occ_fillet_all_edges(box, 1.0, &fil) == OCC_OK);

  // Error paths must not crash and must report.
  occ_handle bad = 0;
  CHECK(occ_box(-1, 1, 1, &bad) == OCC_ERR_INVALID_ARG);
  CHECK(occ_release(9999) == OCC_ERR_INVALID_HANDLE);

  CHECK(occ_release(box) == OCC_OK);
  CHECK(occ_release(cyl) == OCC_OK);
  CHECK(occ_release(cut) == OCC_OK);
  CHECK(occ_release(fil) == OCC_OK);
  CHECK(occ_live_handle_count() == 0);

  // Phase 1 additions: cone, polygon prism, mirror.
  {
    occ_handle cone = 0, prism = 0, mir = 0;
    occ_props q;
    CHECK(occ_cone(2, 0, 3, &cone) == OCC_OK);
    CHECK(occ_mass_props(cone, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - M_PI * 4 * 3 / 3.0) < 1e-6);
    const double tri[] = {0, 0, 10, 0, 0, 10};
    CHECK(occ_extrude_polygon(tri, 3, 2, &prism) == OCC_OK);
    CHECK(occ_mass_props(prism, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 100.0) < 1e-6);
    CHECK(occ_mirror(prism, 0, 0, 0, 1, 0, 0, &mir) == OCC_OK);
    CHECK(occ_mass_props(mir, &q) == OCC_OK);
    CHECK(q.max_x <= 1e-9 && std::fabs(q.min_x + 10) < 1e-6);
    CHECK(occ_extrude_polygon(tri, 2, 2, &prism) == OCC_ERR_INVALID_ARG);
    occ_release(cone); occ_release(prism); occ_release(mir);
    CHECK(occ_live_handle_count() == 0);
  }

  // Sketch: rectangle with a circular hole, prism volume, placement, face frame.
  {
    occ_seg2d segs[5] = {};
    segs[0].kind = 0; segs[0].a[0] = 0;  segs[0].a[1] = 0;  segs[0].a[2] = 10; segs[0].a[3] = 0;
    segs[1].kind = 0; segs[1].a[0] = 10; segs[1].a[1] = 0;  segs[1].a[2] = 10; segs[1].a[3] = 10;
    segs[2].kind = 0; segs[2].a[0] = 10; segs[2].a[1] = 10; segs[2].a[2] = 0;  segs[2].a[3] = 10;
    segs[3].kind = 0; segs[3].a[0] = 0;  segs[3].a[1] = 10; segs[3].a[2] = 0;  segs[3].a[3] = 0;
    segs[4].kind = 2; segs[4].a[0] = 5;  segs[4].a[1] = 5;  segs[4].a[2] = 2;
    const int32_t loops[2] = {4, 1};
    occ_handle face = 0, solid = 0, placed = 0;
    occ_props q;
    CHECK(occ_make_face(segs, loops, 2, &face) == OCC_OK);
    CHECK(occ_mass_props(face, &q) == OCC_OK);
    CHECK(std::fabs(q.area - (100 - M_PI * 4)) < 1e-6);
    CHECK(occ_prism(face, 0, 0, 3, &solid) == OCC_OK);
    CHECK(occ_mass_props(solid, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 3 * (100 - M_PI * 4)) < 1e-5);
    const double o[3] = {0, 0, 0}, z[3] = {0, 1, 0}, x[3] = {1, 0, 0};
    CHECK(occ_place(solid, o, z, x, &placed) == OCC_OK);
    CHECK(occ_mass_props(placed, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 3 * (100 - M_PI * 4)) < 1e-5);
    double fr[9];
    occ_handle box3 = 0;
    CHECK(occ_box(30, 10, 5, &box3) == OCC_OK);
    int planar = 0;
    for (int i = 0; i < 6; i++) {
      if (occ_face_frame(box3, i, fr) == OCC_OK) planar++;
    }
    CHECK(planar == 6);
    CHECK(occ_face_frame(box3, 6, fr) == OCC_ERR_INVALID_ARG);
    CHECK(occ_make_face(segs, loops, 0, &face) == OCC_ERR_INVALID_ARG);
    occ_release(face); occ_release(solid); occ_release(placed); occ_release(box3);
    CHECK(occ_live_handle_count() == 0);
  }

  // Tessellation: a box has 6 planar faces, 12 edges, 8 vertices; winding agrees with normals.
  {
    occ_handle box2 = 0;
    occ_mesh* m = nullptr;
    CHECK(occ_box(10, 20, 30, &box2) == OCC_OK);
    CHECK(occ_tessellate(box2, 0.1, &m) == OCC_OK);
    occ_mesh_counts c;
    CHECK(occ_mesh_get_counts(m, &c) == OCC_OK);
    CHECK(c.faces == 6 && c.edges == 12 && c.topo_vertices == 8 && c.triangles == 12);
    std::vector<float> pos(3 * c.vertices), nrm(3 * c.vertices);
    std::vector<uint32_t> idx(3 * c.triangles);
    std::vector<occ_face_info> faces(c.faces);
    CHECK(occ_mesh_copy(m, pos.data(), nrm.data(), idx.data(), faces.data(), nullptr, nullptr, nullptr) == OCC_OK);
    double area = 0;
    for (auto& f : faces) {
      CHECK(f.surface == OCC_SURF_PLANE && f.tri_count == 2);
      area += f.area;
      for (int t = f.tri_start; t < f.tri_start + f.tri_count; ++t) {
        const float* a = &pos[3 * idx[3 * t]];
        const float* b = &pos[3 * idx[3 * t + 1]];
        const float* d = &pos[3 * idx[3 * t + 2]];
        double u[3] = {b[0] - a[0], b[1] - a[1], b[2] - a[2]}, v[3] = {d[0] - a[0], d[1] - a[1], d[2] - a[2]};
        double cr[3] = {u[1] * v[2] - u[2] * v[1], u[2] * v[0] - u[0] * v[2], u[0] * v[1] - u[1] * v[0]};
        CHECK(cr[0] * f.nx + cr[1] * f.ny + cr[2] * f.nz > 0);  // CCW seen from outside
      }
    }
    CHECK(std::fabs(area - 2 * (200 + 300 + 600)) < 1e-6);
    CHECK(occ_live_mesh_count() == 1);
    occ_mesh_free(m);
    CHECK(occ_live_mesh_count() == 0);
    CHECK(occ_tessellate(box2, -1, &m) == OCC_ERR_INVALID_ARG);
    occ_release(box2);
    CHECK(occ_live_handle_count() == 0);
  }

  // Selective edges: filleting one edge of a box removes exactly the corner material.
  {
    occ_handle bx = 0, f1 = 0, bad = 0;
    occ_props q0, q1;
    CHECK(occ_box(10, 20, 30, &bx) == OCC_OK);
    CHECK(occ_mass_props(bx, &q0) == OCC_OK);
    const int32_t one[] = {0};
    CHECK(occ_fillet_edges(bx, one, 1, 2.0, &f1) == OCC_OK);
    CHECK(occ_mass_props(f1, &q1) == OCC_OK);
    CHECK(q1.volume < q0.volume && q1.face_count == q0.face_count + 1);
    const int32_t oob[] = {99};
    CHECK(occ_fillet_edges(bx, oob, 1, 1.0, &bad) == OCC_ERR_INVALID_ARG);
    CHECK(occ_chamfer_edges(bx, nullptr, 0, 1.0, &bad) == OCC_ERR_INVALID_ARG);
    occ_release(bx); occ_release(f1);
    CHECK(occ_live_handle_count() == 0);
  }

  // Scale and face edits on a 10 x 20 x 30 box.
  {
    occ_handle bx = 0, sc = 0, nu = 0, face = 0, pr = 0, ins = 0, imp = 0, off = 0, offin = 0, bad = 0;
    occ_props q;
    CHECK(occ_box(10, 20, 30, &bx) == OCC_OK);
    CHECK(occ_scale(bx, 5, 10, 15, 2, 2, 2, &sc) == OCC_OK);
    CHECK(occ_mass_props(sc, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 6000 * 8) < 1e-6 && std::fabs(q.min_x + 5) < 1e-9 && std::fabs(q.max_z - 45) < 1e-9);
    CHECK(occ_scale(bx, 0, 0, 0, 1, 2, 0.5, &nu) == OCC_OK);
    CHECK(occ_mass_props(nu, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 6000) < 1e-6 && std::fabs(q.max_y - 40) < 1e-6 && std::fabs(q.max_z - 15) < 1e-6);
    CHECK(occ_scale(bx, 0, 0, 0, 1, 0, 1, &bad) == OCC_ERR_INVALID_ARG);

    // Find the +Z face.
    int top = -1;
    for (int i = 0; i < 6; i++) {
      double fr[9];
      if (occ_face_frame(bx, i, fr) == OCC_OK && fr[5] > 0.5) top = i;
    }
    CHECK(top >= 0);
    CHECK(occ_face_get(bx, top, &face) == OCC_OK);
    CHECK(occ_prism(face, 0, 0, 5, &pr) == OCC_OK);
    CHECK(occ_mass_props(pr, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 1000) < 1e-6 && std::fabs(q.min_z - 30) < 1e-9);

    CHECK(occ_face_inset(bx, top, 2, &ins) == OCC_OK);
    CHECK(occ_mass_props(ins, &q) == OCC_OK);
    CHECK(std::fabs(q.area - 6 * 16) < 1e-6 && std::fabs(q.min_z - 30) < 1e-9);
    CHECK(occ_face_inset(bx, top, 6, &bad) != OCC_OK);  // 10 mm wide face: nothing left

    CHECK(occ_face_imprint_inset(bx, top, 2, &imp) == OCC_OK);
    CHECK(occ_mass_props(imp, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 6000) < 1e-6 && q.face_count == 7 && q.solid_count == 1);

    CHECK(occ_offset_face(bx, top, 5, &off) == OCC_OK);
    CHECK(occ_mass_props(off, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 7000) < 1e-6 && std::fabs(q.max_z - 35) < 1e-6);
    CHECK(occ_offset_face(bx, top, -10, &offin) == OCC_OK);
    CHECK(occ_mass_props(offin, &q) == OCC_OK);
    CHECK(std::fabs(q.volume - 4000) < 1e-6);
    CHECK(occ_offset_face(bx, top, 0, &bad) == OCC_ERR_INVALID_ARG);
    CHECK(occ_face_get(bx, 6, &bad) == OCC_ERR_INVALID_ARG);
    for (occ_handle h : {bx, sc, nu, face, pr, ins, imp, off, offin}) occ_release(h);
    CHECK(occ_live_handle_count() == 0);
  }

  // Churn: handles must return to zero.
  for (int i = 0; i < 2000; ++i) {
    occ_handle a = 0, b = 0, c = 0;
    occ_box(10, 10, 10, &a);
    occ_cylinder(2, 12, &b);
    occ_boolean_subtract(a, b, &c);
    occ_release(a);
    occ_release(b);
    occ_release(c);
  }
  CHECK(occ_live_handle_count() == 0);

  std::printf(failures ? "bridge_test: FAILED (%d)\n" : "bridge_test: OK\n", failures);
  return failures ? 1 : 0;
}
