#!/usr/bin/env bash
# Builds a pinned OpenCascade (OCCT) into third_party/occt-install (project-local, no sudo).
set -euo pipefail
OCCT_TAG="${OCCT_TAG:-V7_8_1}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/third_party/occt-src"
BUILD="$ROOT/third_party/occt-build"
PREFIX="$ROOT/third_party/occt-install"

if [ -f "$PREFIX/.built-$OCCT_TAG" ]; then
  echo "OCCT $OCCT_TAG already built"
  exit 0
fi

mkdir -p "$ROOT/third_party"
if [ ! -d "$SRC" ]; then
  curl -sSfL "https://github.com/Open-Cascade-SAS/OCCT/archive/refs/tags/$OCCT_TAG.tar.gz" -o "$ROOT/third_party/occt.tgz"
  mkdir -p "$SRC"
  tar -xzf "$ROOT/third_party/occt.tgz" -C "$SRC" --strip-components=1
fi

cmake -S "$SRC" -B "$BUILD" -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$PREFIX" \
  -DBUILD_LIBRARY_TYPE=Shared -DBUILD_MODULE_Draw=OFF -DBUILD_MODULE_Visualization=OFF \
  -DBUILD_MODULE_ApplicationFramework=OFF -DBUILD_MODULE_DETools=OFF \
  -DUSE_FREETYPE=OFF -DUSE_TK=OFF -DUSE_FREEIMAGE=OFF -DUSE_RAPIDJSON=OFF -DUSE_TBB=OFF \
  -DBUILD_DOC_Overview=OFF -DBUILD_SAMPLES_QT=OFF -DBUILD_Inspector=OFF
cmake --build "$BUILD" -j"$(nproc)"
cmake --install "$BUILD"
touch "$PREFIX/.built-$OCCT_TAG"
