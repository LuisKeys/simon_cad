// export.cpp — STEP export/import and STL export.
#include <mutex>

#include <BRepMesh_IncrementalMesh.hxx>
#include <Message.hxx>
#include <Message_Messenger.hxx>
#include <Message_PrinterOStream.hxx>
#include <IFSelect_ReturnStatus.hxx>
#include <STEPControl_Reader.hxx>
#include <STEPControl_Writer.hxx>
#include <StlAPI_Writer.hxx>

#include "internal.h"

using namespace simon;

// OCCT's STEP reader/writer print transfer statistics to stdout, which would corrupt
// machine-readable CLI output. Failures are reported through statuses instead.
static void silence_occt_messages() {
  static std::once_flag once;
  std::call_once(once, [] {
    Message::DefaultMessenger()->RemovePrinters(STANDARD_TYPE(Message_PrinterOStream));
  });
}

extern "C" {

occ_status occ_export_step(occ_handle h, const char* path) {
  return guarded([&] {
    if (!path) throw BridgeError{OCC_ERR_INVALID_ARG, "null path"};
    silence_occt_messages();
    STEPControl_Writer w;
    if (w.Transfer(require_shape(h), STEPControl_AsIs) != IFSelect_RetDone) {
      throw BridgeError{OCC_ERR_KERNEL, "STEP transfer failed"};
    }
    if (w.Write(path) != IFSelect_RetDone) {
      throw BridgeError{OCC_ERR_IO, std::string("cannot write STEP file: ") + path};
    }
  });
}

occ_status occ_export_stl(occ_handle h, const char* path, double deflection, int32_t binary) {
  return guarded([&] {
    if (!path) throw BridgeError{OCC_ERR_INVALID_ARG, "null path"};
    if (!(deflection > 0)) throw BridgeError{OCC_ERR_INVALID_ARG, "deflection must be positive"};
    TopoDS_Shape s = require_shape(h);
    BRepMesh_IncrementalMesh mesh(s, deflection, false, 0.5, true);
    StlAPI_Writer w;
    w.ASCIIMode() = (binary == 0);
    if (!w.Write(s, path)) {
      throw BridgeError{OCC_ERR_IO, std::string("cannot write STL file: ") + path};
    }
  });
}

occ_status occ_import_step(const char* path, occ_handle* out) {
  return guarded([&] {
    require_out(out);
    if (!path) throw BridgeError{OCC_ERR_INVALID_ARG, "null path"};
    silence_occt_messages();
    STEPControl_Reader r;
    if (r.ReadFile(path) != IFSelect_RetDone) {
      throw BridgeError{OCC_ERR_IO, std::string("cannot read STEP file: ") + path};
    }
    if (r.TransferRoots() <= 0) throw BridgeError{OCC_ERR_KERNEL, "STEP contains no transferable roots"};
    *out = store_shape(r.OneShape());
  });
}

}  // extern "C"
