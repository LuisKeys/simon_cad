// internal.h — C++-only helpers shared by the bridge translation units.
#pragma once

#include <cstdint>
#include <string>

#include <TopoDS_Shape.hxx>

#include "occ_bridge.h"

namespace simon {

void set_error(const std::string& msg);

// Handle table: owns shapes behind opaque ids.
occ_handle store_shape(const TopoDS_Shape& s);
bool lookup_shape(occ_handle h, TopoDS_Shape& out);

// Runs fn(), translating every exception into an occ_status + thread-local message.
template <typename Fn>
occ_status guarded(Fn&& fn);

occ_status guarded_impl(void (*invoke)(void*), void* ctx);

template <typename Fn>
occ_status guarded(Fn&& fn) {
  auto thunk = [](void* c) { (*static_cast<Fn*>(c))(); };
  return guarded_impl(thunk, &fn);
}

// Resolves a handle or throws a status-carrying exception (caught by guarded()).
TopoDS_Shape require_shape(occ_handle h);
void require_out(void* p);

struct BridgeError {
  occ_status status;
  std::string message;
};

}  // namespace simon
