// import.cpp — B-Rep from a validated triangle mesh.
//
// The mesh arrives welded (vertices shared by index) and already checked by the Go side to be one
// closed, consistently oriented manifold. Every vertex and every undirected edge is created once
// and shared by the faces around it, so the shell is closed by construction and needs no sewing.
// Coplanar triangles are then merged into the faces a designer would recognise, which keeps the
// face count (and so selection, fillets and booleans) manageable.
#include <cmath>
#include <map>
#include <string>
#include <utility>
#include <vector>

#include <BRepBuilderAPI_MakeEdge.hxx>
#include <BRepBuilderAPI_MakeFace.hxx>
#include <BRepBuilderAPI_MakeSolid.hxx>
#include <BRepBuilderAPI_MakeVertex.hxx>
#include <BRepCheck_Analyzer.hxx>
#include <BRepGProp.hxx>
#include <BRep_Builder.hxx>
#include <GProp_GProps.hxx>
#include <ShapeUpgrade_UnifySameDomain.hxx>
#include <TopAbs_ShapeEnum.hxx>
#include <gp_Dir.hxx>
#include <gp_Pln.hxx>
#include <gp_Pnt.hxx>
#include <gp_Vec.hxx>
#include <TopExp_Explorer.hxx>
#include <TopoDS.hxx>
#include <TopoDS_Edge.hxx>
#include <TopoDS_Face.hxx>
#include <TopoDS_Shell.hxx>
#include <TopoDS_Solid.hxx>
#include <TopoDS_Vertex.hxx>
#include <TopoDS_Wire.hxx>

#include "internal.h"

using namespace simon;

extern "C" {

occ_status occ_import_mesh(const double* pos, int32_t nverts, const uint32_t* idx, int32_t ntris, double expected_volume,
                           occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!pos || !idx || nverts < 4 || ntris < 4) throw BridgeError{OCC_ERR_INVALID_ARG, "mesh needs at least 4 vertices and triangles"};
    if (!(expected_volume > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "mesh volume must be positive"};

    BRep_Builder bb;
    std::vector<TopoDS_Vertex> verts(nverts);
    for (int32_t i = 0; i < nverts; i++) verts[i] = BRepBuilderAPI_MakeVertex(gp_Pnt(pos[3 * i], pos[3 * i + 1], pos[3 * i + 2]));

    std::map<std::pair<uint32_t, uint32_t>, TopoDS_Edge> edges;  // keyed (min,max), built min -> max
    auto edge_of = [&](uint32_t a, uint32_t b) {
      const bool fwd = a < b;
      const auto key = fwd ? std::make_pair(a, b) : std::make_pair(b, a);
      auto it = edges.find(key);
      if (it == edges.end()) {
        BRepBuilderAPI_MakeEdge me(verts[key.first], verts[key.second]);
        if (!me.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "mesh has an edge too short for the kernel"};
        it = edges.emplace(key, me.Edge()).first;
      }
      return fwd ? it->second : TopoDS::Edge(it->second.Reversed());
    };

    // Neighbouring triangles with the same normal: the only case where merging faces can help.
    std::vector<gp_Dir> normals;
    normals.reserve(ntris);
    bool coplanar_neighbours = false;
    {
      std::map<std::pair<uint32_t, uint32_t>, int32_t> first;
      for (int32_t t = 0; t < ntris && idx[3 * t + 2] < (uint32_t)nverts; t++) {
        const uint32_t v[3] = {idx[3 * t], idx[3 * t + 1], idx[3 * t + 2]};
        if (v[0] >= (uint32_t)nverts || v[1] >= (uint32_t)nverts) break;
        auto P = [&](uint32_t i) { return gp_Pnt(pos[3 * i], pos[3 * i + 1], pos[3 * i + 2]); };
        const gp_Vec n = gp_Vec(P(v[0]), P(v[1])).Crossed(gp_Vec(P(v[0]), P(v[2])));
        normals.push_back(n.Magnitude() > 1e-12 ? gp_Dir(n) : gp_Dir(0, 0, 1));
        for (int j = 0; j < 3 && !coplanar_neighbours; j++) {
          const uint32_t a = v[j], b = v[(j + 1) % 3];
          const auto key = a < b ? std::make_pair(a, b) : std::make_pair(b, a);
          auto it = first.find(key);
          if (it == first.end()) first.emplace(key, t);
          else if (normals[it->second].Dot(normals[t]) > 1.0 - 1e-10) coplanar_neighbours = true;
        }
      }
    }

    TopoDS_Shell shell;
    bb.MakeShell(shell);
    for (int32_t t = 0; t < ntris; t++) {
      const uint32_t a = idx[3 * t], b = idx[3 * t + 1], c = idx[3 * t + 2];
      if (a >= (uint32_t)nverts || b >= (uint32_t)nverts || c >= (uint32_t)nverts) {
        throw BridgeError{OCC_ERR_INVALID_ARG, "mesh index out of range"};
      }
      TopoDS_Wire w;
      bb.MakeWire(w);
      bb.Add(w, edge_of(a, b));
      bb.Add(w, edge_of(b, c));
      bb.Add(w, edge_of(c, a));
      w.Closed(true);
      // The plane's normal follows the triangle's winding, so every face keeps the orientation
      // the mesh gave it (a plane found from the wire alone may face either way).
      const gp_Pnt pa(pos[3 * a], pos[3 * a + 1], pos[3 * a + 2]), pb(pos[3 * b], pos[3 * b + 1], pos[3 * b + 2]),
          pc(pos[3 * c], pos[3 * c + 1], pos[3 * c + 2]);
      const gp_Vec normal = gp_Vec(pa, pb).Crossed(gp_Vec(pa, pc));
      if (normal.Magnitude() < 1e-12) throw BridgeError{OCC_ERR_KERNEL, "a triangle of the mesh has no area"};
      BRepBuilderAPI_MakeFace mf(gp_Pln(pa, gp_Dir(normal)), w);
      if (!mf.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "a triangle of the mesh could not become a face"};
      bb.Add(shell, mf.Face());
    }
    shell.Closed(true);

    BRepBuilderAPI_MakeSolid ms(shell);
    if (!ms.IsDone()) throw BridgeError{OCC_ERR_KERNEL, "the mesh did not close into a solid"};
    const TopoDS_Shape raw = ms.Solid();

    // Volume of a candidate, made positive; a candidate is acceptable when it is a single valid
    // solid whose volume matches the mesh's.
    // The raw sewing is built valid by construction (and the caller validates every body again), so
    // only the merged candidate pays for a full validity check.
    auto accept = [&](TopoDS_Shape& cand, bool full_check) {
      TopExp_Explorer found(cand, TopAbs_SOLID);
      if (!found.More()) return false;
      cand = found.Current();
      GProp_GProps g;
      BRepGProp::VolumeProperties(cand, g);
      if (g.Mass() < 0) {
        cand.Reverse();
        BRepGProp::VolumeProperties(cand, g);
      }
      return std::fabs(g.Mass() - expected_volume) <= 1e-4 * expected_volume &&
             (!full_check || BRepCheck_Analyzer(cand, Standard_True).IsValid());
    };

    // Merge coplanar triangles into faces. On a mesh of a curved surface (nothing is coplanar) the
    // merge can only hurt, so a result that is not as good as the raw sewing is dropped.
    TopoDS_Shape solid = raw;
    if (coplanar_neighbours) {
      ShapeUpgrade_UnifySameDomain unify(raw, Standard_True, Standard_True, Standard_False);
      unify.SetLinearTolerance(1e-5);
      unify.SetAngularTolerance(1e-6);
      unify.Build();
      solid = unify.Shape();
    }
    if (!coplanar_neighbours || !accept(solid, true)) {
      solid = raw;
      if (!accept(solid, false)) {
        throw BridgeError{OCC_ERR_KERNEL, "the converted mesh is not a solid of the mesh's volume"};
      }
    }
    *out = store_shape(solid);
  });
}

}  // extern "C"
