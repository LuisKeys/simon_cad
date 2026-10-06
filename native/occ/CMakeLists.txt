cmake_minimum_required(VERSION 3.16)
project(simon_occ CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

# OCCT prefix is passed by the Makefile: -DOCCT_PREFIX=<repo>/third_party/occt-install
find_package(OpenCASCADE REQUIRED PATHS "${OCCT_PREFIX}" NO_DEFAULT_PATH)

add_library(simon_occ STATIC
  bridge.cpp primitives.cpp booleans.cpp fillets.cpp export.cpp mesh.cpp sketch.cpp)
target_include_directories(simon_occ PUBLIC include PRIVATE . ${OpenCASCADE_INCLUDE_DIR})

# Standalone C++ test of the C ABI (leak/validity checks, runs under valgrind/ASan).
add_executable(bridge_test tests/bridge_test.cpp)
target_include_directories(bridge_test PRIVATE include)
target_link_directories(bridge_test PRIVATE ${OpenCASCADE_LIBRARY_DIR})
target_link_libraries(bridge_test PRIVATE simon_occ
  TKDESTEP TKDESTL TKMesh TKBO TKBool TKFillet TKOffset TKTopAlgo TKPrim TKGeomAlgo
  TKGeomBase TKBRep TKG3d TKG2d TKMath TKXSBase TKShHealing TKernel)
set_target_properties(bridge_test PROPERTIES INSTALL_RPATH "${OpenCASCADE_LIBRARY_DIR}" BUILD_WITH_INSTALL_RPATH ON)
