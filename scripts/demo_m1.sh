#!/usr/bin/env bash
# M1 demo: natural language → agent → validated tool call → ChangeSet → CAD-IR → OCCT → STEP.
#
# With ANTHROPIC_API_KEY set this talks to the real model. Without it, it replays the
# recorded run in testdata/demo so the whole pipeline still executes offline.
set -euo pipefail
cd "$(dirname "$0")/.."
export LD_LIBRARY_PATH="$PWD/third_party/occt-install/lib:${LD_LIBRARY_PATH:-}"
CLI=./out/simoncad-cli
OUT=${1:-out/demo}
mkdir -p "$OUT"

EXTRA=()
if [[ -z "${ANTHROPIC_API_KEY:-}" ]]; then
  echo "(no ANTHROPIC_API_KEY: replaying testdata/demo/m1_create.recording.json)"
  EXTRA=(--recording testdata/demo/m1_create.recording.json)
fi

$CLI create "Create an 80 x 60 x 4 mm plate" --out "$OUT/p.scadproj" --force "${EXTRA[@]}"
$CLI export "$OUT/p.scadproj" --format step --out "$OUT/p.step"
$CLI history "$OUT/p.scadproj"
$CLI replay "$OUT/p.scadproj"
echo "agent-history trace: $OUT/p.scadproj/agent-history.json"
