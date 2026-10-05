// mesh.cpp — tessellation with face/edge/vertex identity, for the viewport and picking.
#include <atomic>
#include <cstring>
#include <vector>

#include <BRepAdaptor_Curve.hxx>
#include <BRepAdaptor_Surface.hxx>
#include <BRepBndLib.hxx>
#include <BRepGProp.hxx>
#include <BRepLib_ToolTriangulatedShape.hxx>
#include <BRepMesh_IncrementalMesh.hxx>
#include <BRep_Tool.hxx>
#include <Bnd_Box.hxx>
#include <GCPnts_QuasiUniformDeflection.hxx>
#include <GProp_GProps.hxx>
#include <Poly_Triangulation.hxx>
#include <TopExp.hxx>
#include <TopLoc_Location.hxx>
#include <TopTools_IndexedMapOfShape.hxx>
#include <TopoDS.hxx>
#include <TopoDS_Edge.hxx>
#include <TopoDS_Face.hxx>
#include <TopoDS_Vertex.hxx>
#include <gp_Cone.hxx>
#include <gp_Cylinder.hxx>
#include <gp_Pln.hxx>
#include <gp_Sphere.hxx>

#include "internal.h"

using namespace simon;

struct occ_mesh {
  std::vector<float> pos, nrm, edge_pts, vert_pts;
  std::vector<uint32_t> idx;
  std::vector<occ_face_info> faces;
  std::vector<occ_edge_info> edges;
};

static std::atomic<int64_t> g_live_meshes{0};

static void bounds(const TopoDS_Shape& s, double* mn, double* mx) {
  Bnd_Box b;
  BRepBndLib::AddOptimal(s, b, Standard_False, Standard_False);  // tight: no tolerance padding
  if (b.IsVoid()) { mn[0] = mn[1] = mn[2] = mx[0] = mx[1] = mx[2] = 0; return; }
  b.Get(mn[0], mn[1], mn[2], mx[0], mx[1], mx[2]);
}

static void add_face(occ_mesh& m, const TopoDS_Face& f) {
  occ_face_info fi;
  std::memset(&fi, 0, sizeof fi);
  fi.tri_start = (int32_t)(m.idx.size() / 3);
  {
    double mn[3], mx[3];
    bounds(f, mn, mx);
    fi.min_x = mn[0]; fi.min_y = mn[1]; fi.min_z = mn[2];
    fi.max_x = mx[0]; fi.max_y = mx[1]; fi.max_z = mx[2];
  }

  GProp_GProps gp;
  BRepGProp::SurfaceProperties(f, gp);
  fi.area = gp.Mass();
  gp_Pnt c = gp.CentreOfMass();
  fi.cx = c.X(); fi.cy = c.Y(); fi.cz = c.Z();

  BRepAdaptor_Surface surf(f);
  const bool reversed = f.Orientation() == TopAbs_REVERSED;
  switch (surf.GetType()) {
    case GeomAbs_Plane: {
      fi.surface = OCC_SURF_PLANE;
      gp_Dir n = surf.Plane().Axis().Direction();
      if (reversed) n.Reverse();
      fi.nx = n.X(); fi.ny = n.Y(); fi.nz = n.Z();
      break;
    }
    case GeomAbs_Cylinder: {
      fi.surface = OCC_SURF_CYLINDER;
      gp_Cylinder cy = surf.Cylinder();
      gp_Dir d = cy.Axis().Direction();
      fi.nx = d.X(); fi.ny = d.Y(); fi.nz = d.Z();
      fi.radius = cy.Radius();
      break;
    }
    case GeomAbs_Cone: {
      fi.surface = OCC_SURF_CONE;
      gp_Cone co = surf.Cone();
      gp_Dir d = co.Axis().Direction();
      fi.nx = d.X(); fi.ny = d.Y(); fi.nz = d.Z();
      fi.radius = co.RefRadius();
      break;
    }
    case GeomAbs_Sphere:
      fi.surface = OCC_SURF_SPHERE;
      fi.radius = surf.Sphere().Radius();
      break;
    case GeomAbs_Torus: fi.surface = OCC_SURF_TORUS; break;
    default: fi.surface = OCC_SURF_OTHER; break;
  }

  TopLoc_Location loc;
  Handle(Poly_Triangulation) tri = BRep_Tool::Triangulation(f, loc);
  if (!tri.IsNull()) {
    BRepLib_ToolTriangulatedShape::ComputeNormals(f, tri);
    const gp_Trsf tr = loc.Transformation();
    const uint32_t base = (uint32_t)(m.pos.size() / 3);
    for (int i = 1; i <= tri->NbNodes(); ++i) {
      gp_Pnt p = tri->Node(i).Transformed(tr);
      m.pos.push_back((float)p.X()); m.pos.push_back((float)p.Y()); m.pos.push_back((float)p.Z());
      gp_Dir n = tri->HasNormals() ? tri->Normal(i) : gp_Dir(0, 0, 1);
      n.Transform(tr);
      m.nrm.push_back((float)n.X()); m.nrm.push_back((float)n.Y()); m.nrm.push_back((float)n.Z());
    }
    for (int i = 1; i <= tri->NbTriangles(); ++i) {
      int a, b, c;
      tri->Triangle(i).Get(a, b, c);
      if (reversed) std::swap(b, c);
      m.idx.push_back(base + a - 1); m.idx.push_back(base + b - 1); m.idx.push_back(base + c - 1);
    }
  }
  fi.tri_count = (int32_t)(m.idx.size() / 3) - fi.tri_start;
  m.faces.push_back(fi);
}

static void add_edge(occ_mesh& m, const TopoDS_Edge& e, double deflection) {
  occ_edge_info ei;
  std::memset(&ei, 0, sizeof ei);
  ei.pt_start = (int32_t)(m.edge_pts.size() / 3);
  double mn[3], mx[3];
  bounds(e, mn, mx);
  ei.min_x = mn[0]; ei.min_y = mn[1]; ei.min_z = mn[2];
  ei.max_x = mx[0]; ei.max_y = mx[1]; ei.max_z = mx[2];

  GProp_GProps gp;
  BRepGProp::LinearProperties(e, gp);
  ei.length = gp.Mass();

  if (!BRep_Tool::Degenerated(e)) {
    BRepAdaptor_Curve c(e);
    switch (c.GetType()) {
      case GeomAbs_Line: ei.curve = OCC_CURVE_LINE; break;
      case GeomAbs_Circle: ei.curve = OCC_CURVE_CIRCLE; break;
      default: ei.curve = OCC_CURVE_OTHER; break;
    }
    gp_Pnt s = c.Value(c.FirstParameter()), t = c.Value(c.LastParameter());
    ei.sx = s.X(); ei.sy = s.Y(); ei.sz = s.Z();
    ei.ex = t.X(); ei.ey = t.Y(); ei.ez = t.Z();
    GCPnts_QuasiUniformDeflection d(c, deflection);
    if (d.IsDone()) {
      for (int i = 1; i <= d.NbPoints(); ++i) {
        gp_Pnt p = d.Value(i);
        m.edge_pts.push_back((float)p.X()); m.edge_pts.push_back((float)p.Y()); m.edge_pts.push_back((float)p.Z());
      }
    }
  }
  ei.pt_count = (int32_t)(m.edge_pts.size() / 3) - ei.pt_start;
  m.edges.push_back(ei);
}

extern "C" {

occ_status occ_tessellate(occ_handle h, double deflection, occ_mesh** out) {
  return guarded([&] {
    require_out(out);
    if (!(deflection > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "deflection must be positive"};
    TopoDS_Shape s = require_shape(h);
    BRepMesh_IncrementalMesh mesher(s, deflection, false, 0.5, false);
    auto* m = new occ_mesh();
    try {
      TopTools_IndexedMapOfShape faces, edges, verts;
      TopExp::MapShapes(s, TopAbs_FACE, faces);
      TopExp::MapShapes(s, TopAbs_EDGE, edges);
      TopExp::MapShapes(s, TopAbs_VERTEX, verts);
      for (int i = 1; i <= faces.Extent(); ++i) add_face(*m, TopoDS::Face(faces(i)));
      for (int i = 1; i <= edges.Extent(); ++i) add_edge(*m, TopoDS::Edge(edges(i)), deflection);
      for (int i = 1; i <= verts.Extent(); ++i) {
        gp_Pnt p = BRep_Tool::Pnt(TopoDS::Vertex(verts(i)));
        m->vert_pts.push_back((float)p.X()); m->vert_pts.push_back((float)p.Y()); m->vert_pts.push_back((float)p.Z());
      }
    } catch (...) {
      delete m;
      throw;
    }
    g_live_meshes.fetch_add(1);
    *out = m;
  });
}

occ_status occ_mesh_get_counts(const occ_mesh* m, occ_mesh_counts* out) {
  return guarded([&] {
    require_out(out);
    if (!m) throw BridgeError{OCC_ERR_INVALID_HANDLE, "null mesh"};
    out->vertices = (int32_t)(m->pos.size() / 3);
    out->triangles = (int32_t)(m->idx.size() / 3);
    out->faces = (int32_t)m->faces.size();
    out->edges = (int32_t)m->edges.size();
    out->edge_points = (int32_t)(m->edge_pts.size() / 3);
    out->topo_vertices = (int32_t)(m->vert_pts.size() / 3);
  });
}

occ_status occ_mesh_copy(const occ_mesh* m, float* positions, float* normals, uint32_t* indices,
                         occ_face_info* faces, occ_edge_info* edges, float* edge_pts, float* vertex_pts) {
  return guarded([&] {
    if (!m) throw BridgeError{OCC_ERR_INVALID_HANDLE, "null mesh"};
    auto cp = [](auto* dst, const auto& src) {
      if (dst && !src.empty()) std::memcpy(dst, src.data(), src.size() * sizeof(src[0]));
    };
    cp(positions, m->pos); cp(normals, m->nrm); cp(indices, m->idx);
    cp(faces, m->faces); cp(edges, m->edges); cp(edge_pts, m->edge_pts); cp(vertex_pts, m->vert_pts);
  });
}

void occ_mesh_free(occ_mesh* m) {
  if (!m) return;
  delete m;
  g_live_meshes.fetch_sub(1);
}

int64_t occ_live_mesh_count(void) { return g_live_meshes.load(); }

}  // extern "C"
