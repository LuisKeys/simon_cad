ROOT      := $(CURDIR)
GO        := $(shell command -v go || echo $(ROOT)/third_party/go/bin/go)
OCCT      := $(ROOT)/third_party/occt-install
NATIVE    := $(ROOT)/out/native
export PATH := $(ROOT)/third_party/go/bin:$(PATH)
export LD_LIBRARY_PATH := $(OCCT)/lib:$(LD_LIBRARY_PATH)

# Go's build cache does not notice changes to the native static library, so stamp its
# checksum into CGO_CFLAGS: a rebuilt libsimon_occ.a then forces cgo packages to relink.
STAMP = $(shell shasum $(NATIVE)/libsimon_occ.a 2>/dev/null | cut -c1-12)
export CGO_CFLAGS = -DSIMON_NATIVE_STAMP=$(STAMP)

.PHONY: occt native build build-go test test-go test-js leakcheck bracket eval demo docs ui e2e

occt:
	bash scripts/build-occt.sh

native:
	cmake -S native/occ -B $(NATIVE) -DOCCT_PREFIX=$(OCCT) -DCMAKE_BUILD_TYPE=Release
	cmake --build $(NATIVE) -j$$(sysctl -n hw.ncpu 2>/dev/null || nproc)

build: native
	$(MAKE) --no-print-directory build-go

build-go:
	$(GO) build -o out/simoncad-cli ./cmd/simoncad-cli
	$(GO) build -o out/simoncad ./cmd/simoncad

test: native
	$(NATIVE)/bridge_test
	$(MAKE) --no-print-directory test-go
	$(MAKE) --no-print-directory test-js

test-go:
	$(GO) vet ./cmd/... ./internal/...
	$(GO) test ./...

# Recorded agent evals (no network); add `--live` via the CLI for the nightly run.
eval: build
	./out/simoncad-cli eval

demo: build
	bash scripts/demo_m1.sh

# Valgrind over the C++ ABI test: no leaks, no invalid accesses.
leakcheck: native
	valgrind --leak-check=full --errors-for-leak-kinds=definite --error-exitcode=1 $(NATIVE)/bridge_test

bracket: build
	./out/simoncad-cli bracket --out out/

# Regenerate docs/tools.md from the live tool registry.
docs: build
	python3 scripts/gen-tool-docs.py ./out/simoncad-cli

# Run the desktop UI (serves on a loopback port and opens the browser).
ui: build
	./out/simoncad $(ARGS)

# Browser end-to-end flows: M2 (select face -> prompt -> preview -> commit -> undo), image to
# CAD, materials, body/face modeling (align, duplicate, scale, group, extrude, inset) and visual
# closed-loop refinement, Single/Quad views, interactive dimensions, units & grid, primary/fallback models, interactive move/rotate, shortcuts panel, paths, image tracing, connection loss and recovery. Needs Chrome.
e2e: build
	node e2e/m2.mjs
	node e2e/image.mjs
	node e2e/materials.mjs
	node e2e/modeling.mjs
	node e2e/components.mjs
	node e2e/viewports.mjs
	node e2e/dimensions.mjs
	node e2e/workspace.mjs
	node e2e/rendering.mjs
	node e2e/visual.mjs
	node e2e/models.mjs
	node e2e/transform.mjs
	node e2e/shortcuts.mjs
	node e2e/printsim.mjs
	node e2e/paths.mjs
	node e2e/trace.mjs
	node e2e/resilience.mjs

# Pure frontend helpers (confidence bands, SVG geometry, ambiguity edits); needs Node >= 18.
test-js:
	node --test frontend/
