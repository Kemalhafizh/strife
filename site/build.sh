#!/bin/sh
# Builds the website into site/public, the same way locally and in CI:
#
#   1. data/: the playground's presets run with the native engine, the latest
#      GitHub release and the release signing key (cmd/sitedata), and
#      generated/: example reports produced by the CLI;
#   2. assets/wasm/: the engine compiled to WebAssembly, then smoke-tested in
#      Node against the native presets;
#   3. the pages (Hugo), then checks that every link and anchor within the
#      site resolves and that no wording the roadmap rules out made it in.
#
# Environment: BASE_URL (default: hugo.toml's), HUGO (default: hugo),
# OFFLINE=1 to skip asking GitHub for the latest release.
set -eu
cd "$(dirname "$0")"

hugo=${HUGO:-hugo}
offline=""
[ "${OFFLINE:-}" = 1 ] && offline="-offline"

echo "==> data"
(cd wasm && go run ./cmd/sitedata -out ../data -root ../.. $offline)

echo "==> example reports"
# Real engine output, regenerated on every build: the timeline of the
# README's walkthrough, a 40-seed campaign, and a signed dossier with the
# bundle, key and history an auditor can try the verifier with.
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd .. && go build -o "$tmp/strife" ./cmd/strife)
s="$tmp/strife"
rm -rf generated
mkdir -p generated/demos
out=$(pwd)/generated/demos
# Run where a user would, with the CLI's default output directories, so the
# reproduction commands inside the reports read the same as on their machine.
(
  cd "$tmp"
  # shrink and campaign exit 1 when they find the violation they are shown for
  "$s" shrink --system raft-unsafe-fsync --faults kill,io-stall --seed 13 --out strife-out >/dev/null || [ $? -eq 1 ]
  cp strife-out/raft-unsafe-fsync-seed13-min/report.html "$out/timeline.html"
  "$s" campaign --system raft-lease --faults clock-skew,partition --seeds 1..40 --out strife-campaign >/dev/null || [ $? -eq 1 ]
  cp strife-campaign/campaign.html "$out/campaign.html"
  # with the report of each violating seed it links to
  for d in strife-campaign/*/; do
    [ -f "$d/report.html" ] || continue
    mkdir -p "$out/$(basename "$d")"
    cp "$d/report.html" "$out/$(basename "$d")/"
  done
  "$s" sim --system raft --seed 7 --out strife-out >/dev/null
  "$s" keygen --out example-auditor >/dev/null
  "$s" sign --report strife-out/raft-seed7/report.json --key example-auditor.key \
    --signer example-auditor --out bundle.json >/dev/null
  "$s" compliance --report strife-out/raft-seed7/report.json --bundle bundle.json \
    --key example-auditor.pub --frameworks dora,soc2,iso27001,bi-ojk --out strife-compliance >/dev/null
  cp strife-compliance/dossier.html "$out/dossier.html"
  cp bundle.json example-auditor.pub "$out/"
  cp strife-out/raft-seed7/history.jsonl "$out/history.jsonl"
)

echo "==> WebAssembly engine"
mkdir -p assets/wasm
(cd wasm && GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o ../assets/wasm/strife.wasm .)
gzip -9 -n -c assets/wasm/strife.wasm >assets/wasm/strife.wasm.gz
goroot=$(go env GOROOT)
for f in "$goroot/lib/wasm/wasm_exec.js" "$goroot/misc/wasm/wasm_exec.js"; do
  if [ -f "$f" ]; then
    cp "$f" assets/wasm/wasm_exec.js
    break
  fi
done
[ -f assets/wasm/wasm_exec.js ] || { echo "wasm_exec.js not found under $goroot" >&2; exit 1; }
node wasm/smoke.cjs assets/wasm/wasm_exec.js assets/wasm/strife.wasm data/presets.json static/samples

echo "==> pages"
rm -rf public
base=$(sed -n 's/^baseURL *= *"\(.*\)"/\1/p' hugo.toml)
[ -n "${BASE_URL:-}" ] && base=${BASE_URL%/}/
"$hugo" --gc --minify --baseURL "$base"

echo "==> links"
node scripts/check-links.cjs public "$base"

echo "==> wording"
scripts/check-wording.sh public ../docs/ROADMAP.md

if command -v shellcheck >/dev/null 2>&1; then
  shellcheck build.sh scripts/*.sh static/install.sh
fi
echo "site built in $(pwd)/public"
