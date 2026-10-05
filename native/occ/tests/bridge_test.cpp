// bridge_test.cpp — exercises the C ABI directly: validity, volumes, errors and leak counting.
#include <cmath>
#include <cstdio>
#include <cstdlib>

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
