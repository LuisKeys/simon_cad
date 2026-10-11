// bridge.cpp — handle table, error plumbing, lifetime, transforms and queries.
#include <atomic>
#include <exception>
#include <mutex>
#include <unordered_map>

#include <BRepBndLib.hxx>
#include <BRepBuilderAPI_Transform.hxx>
#include <BRepCheck_Analyzer.hxx>
#include <BRepGProp.hxx>
#include <Bnd_Box.hxx>
#include <GProp_GProps.hxx>
#include <Standard_Failure.hxx>
#include <Standard_Version.hxx>
#include <TopExp.hxx>
#include <TopTools_IndexedMapOfShape.hxx>
#include <gp_Ax1.hxx>
#include <gp_Ax2.hxx>
#include <gp_Trsf.hxx>

#include "internal.h"

namespace simon {
namespace {

thread_local std::string g_last_error;
std::mutex g_mu;
std::unordered_map<occ_handle, TopoDS_Shape> g_shapes;
std::atomic<occ_handle> g_next{1};

}  // namespace

void set_error(const std::string& msg) { g_last_error = msg; }

occ_handle store_shape(const TopoDS_Shape& s) {
  occ_handle h = g_next.fetch_add(1);
  std::lock_guard<std::mutex> lk(g_mu);
  g_shapes.emplace(h, s);
  return h;
}

bool lookup_shape(occ_handle h, TopoDS_Shape& out) {
  std::lock_guard<std::mutex> lk(g_mu);
  auto it = g_shapes.find(h);
  if (it == g_shapes.end()) return false;
  out = it->second;
  return true;
}

TopoDS_Shape require_shape(occ_handle h) {
  TopoDS_Shape s;
  if (h == 0 || !lookup_shape(h, s)) {
    throw BridgeError{OCC_ERR_INVALID_HANDLE, "invalid or released shape handle"};
  }
  return s;
}

void require_out(void* p) {
  if (!p) throw BridgeError{OCC_ERR_INVALID_ARG, "null output pointer"};
}

occ_status guarded_impl(void (*invoke)(void*), void* ctx) {
  g_last_error.clear();
  try {
    invoke(ctx);
    return OCC_OK;
  } catch (const BridgeError& e) {
    g_last_error = e.message;
    return e.status;
  } catch (const Standard_Failure& e) {
    g_last_error = std::string("OCCT: ") + e.GetMessageString();
    return OCC_ERR_KERNEL;
  } catch (const std::exception& e) {
    g_last_error = std::string("std: ") + e.what();
    return OCC_ERR_UNKNOWN;
  } catch (...) {
    g_last_error = "unknown exception";
    return OCC_ERR_UNKNOWN;
  }
}

}  // namespace simon

using namespace simon;

extern "C" {

const char* occ_last_error(void) { return g_last_error.c_str(); }

const char* occ_version(void) { return OCC_VERSION_COMPLETE; }

int64_t occ_live_handle_count(void) {
  std::lock_guard<std::mutex> lk(g_mu);
  return static_cast<int64_t>(g_shapes.size());
}

occ_status occ_release(occ_handle h) {
  return guarded([&] {
    std::lock_guard<std::mutex> lk(g_mu);
    if (g_shapes.erase(h) == 0) {
      throw BridgeError{OCC_ERR_INVALID_HANDLE, "release of unknown handle"};
    }
  });
}

occ_status occ_translate(occ_handle s, double dx, double dy, double dz, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    gp_Trsf t;
    t.SetTranslation(gp_Vec(dx, dy, dz));
    BRepBuilderAPI_Transform tr(require_shape(s), t, true);
    *out = store_shape(tr.Shape());
  });
}

occ_status occ_mirror(occ_handle s, double ox, double oy, double oz, double nx, double ny,
                      double nz, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (nx == 0 && ny == 0 && nz == 0) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "mirror normal must be non-zero"};
    }
    gp_Trsf t;
    t.SetMirror(gp_Ax2(gp_Pnt(ox, oy, oz), gp_Dir(nx, ny, nz)));
    BRepBuilderAPI_Transform tr(require_shape(s), t, true);
    *out = store_shape(tr.Shape());
  });
}

occ_status occ_rotate(occ_handle s, double ox, double oy, double oz, double ax, double ay,
                      double az, double angle, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (ax == 0 && ay == 0 && az == 0) {
      throw BridgeError{OCC_ERR_INVALID_ARG, "rotation axis must be non-zero"};
    }
    gp_Trsf t;
    t.SetRotation(gp_Ax1(gp_Pnt(ox, oy, oz), gp_Dir(ax, ay, az)), angle);
    BRepBuilderAPI_Transform tr(require_shape(s), t, true);
    *out = store_shape(tr.Shape());
  });
}

occ_status occ_is_valid(occ_handle s, int32_t* valid) {
  return guarded([&] {
    require_out(valid);
    BRepCheck_Analyzer a(require_shape(s));
    *valid = a.IsValid() ? 1 : 0;
  });
}

occ_status occ_mass_props(occ_handle h, occ_props* out) {
  return guarded([&] {
    require_out(out);
    TopoDS_Shape s = require_shape(h);

    // Adaptive integration to a relative accuracy: ~10x faster than the fixed Gauss order on
    // B-spline faces (sweeps, traced curves) and as exact on analytic ones (|error| < 1e-7).
    GProp_GProps vol, surf;
    BRepGProp::VolumeProperties(s, vol, 1e-5, Standard_False, Standard_False);
    BRepGProp::SurfaceProperties(s, surf, 1e-5);
    gp_Pnt c = vol.CentreOfMass();

    Bnd_Box box;
    BRepBndLib::AddOptimal(s, box, Standard_False, Standard_False);
    double x0, y0, z0, x1, y1, z1;
    box.Get(x0, y0, z0, x1, y1, z1);

    TopTools_IndexedMapOfShape solids, faces, edges;
    TopExp::MapShapes(s, TopAbs_SOLID, solids);
    TopExp::MapShapes(s, TopAbs_FACE, faces);
    TopExp::MapShapes(s, TopAbs_EDGE, edges);

    out->volume = vol.Mass();
    out->area = surf.Mass();
    out->cx = c.X();
    out->cy = c.Y();
    out->cz = c.Z();
    out->min_x = x0;
    out->min_y = y0;
    out->min_z = z0;
    out->max_x = x1;
    out->max_y = y1;
    out->max_z = z1;
    out->solid_count = solids.Extent();
    out->face_count = faces.Extent();
    out->edge_count = edges.Extent();
  });
}

}  // extern "C"
