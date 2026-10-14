// booleans.cpp — union, subtraction and intersection.
#include <sstream>
#include <string>

#include <BRepAlgoAPI_BuilderAlgo.hxx>
#include <BRepAlgoAPI_Common.hxx>
#include <BRepAlgoAPI_Cut.hxx>
#include <BRepAlgoAPI_Fuse.hxx>

#include "internal.h"

using namespace simon;

// The message of a failed boolean: what failed, plus the alerts OCCT recorded (for example
// BOPAlgo_AlertSelfInterferingShape or BOPAlgo_AlertTooFewArguments). The agent repairs a model
// from these messages, so "boolean union failed" alone gives it nothing to act on.
static std::string failure(const char* what, const BRepAlgoAPI_BuilderAlgo& op) {
  std::ostringstream os;
  op.DumpErrors(os);
  std::string detail = os.str();
  // one line, single spaces: the report is multi-line and indented
  std::string out;
  bool space = false;
  for (char c : detail) {
    if (c == '\n' || c == '\r' || c == '\t' || c == ' ') { space = !out.empty(); continue; }
    if (space) out += ' ';
    space = false;
    out += c;
  }
  return out.empty() ? std::string(what) : std::string(what) + ": " + out;
}

extern "C" {

occ_status occ_boolean_union(occ_handle a, occ_handle b, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    BRepAlgoAPI_Fuse op(require_shape(a), require_shape(b));
    if (!op.IsDone() || op.HasErrors()) throw BridgeError{OCC_ERR_KERNEL, failure("boolean union failed", op)};
    *out = store_shape(op.Shape());
  });
}

occ_status occ_boolean_subtract(occ_handle a, occ_handle b, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    BRepAlgoAPI_Cut op(require_shape(a), require_shape(b));
    if (!op.IsDone() || op.HasErrors()) {
      throw BridgeError{OCC_ERR_KERNEL, failure("boolean subtraction failed", op)};
    }
    *out = store_shape(op.Shape());
  });
}

occ_status occ_boolean_intersect(occ_handle a, occ_handle b, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    BRepAlgoAPI_Common op(require_shape(a), require_shape(b));
    if (!op.IsDone() || op.HasErrors()) {
      throw BridgeError{OCC_ERR_KERNEL, failure("boolean intersection failed", op)};
    }
    *out = store_shape(op.Shape());
  });
}

}  // extern "C"
