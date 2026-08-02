#!/bin/bash
# Render the committed icon artwork, all of it from the one authored source,
# docs/assets/prem-down.svg:
#
#   packaging/windows/winres/icon*.png         - the raster sizes, and the input
#                                                to both artefacts below
#   internal/integrate/rsrc_windows_amd64.syso - Windows resource, so Explorer's
#                                                context-menu entry (and the
#                                                exe) carry the prem-down icon
#   internal/updates/prem-down.icns            - macOS dialog icon, so the
#                                                update prompts are visibly ours
#
# Developer tool, not part of the build: the outputs are committed (the .syso so
# GoReleaser needs no extra step, the .icns because go:embed cannot reach across
# directories), so this only needs re-running when the artwork changes - and
# then all of it wants regenerating together, or the platforms end up
# disagreeing.
#
# rsvg-convert reproduces the committed PNG files byte for byte, so editing the
# master SVG and re-running this is the whole workflow for an artwork change.
#
# The master carries a background tile for use as a standalone logo, which the
# rasters must not have; it is hidden here with a stylesheet.
#
# The .icns is built with iconutil where it exists, which is the path that
# produces the committed file, and with a Go tool everywhere else - so a Windows
# contributor who changes the artwork can still rebuild every artefact. The two
# encoders do not agree byte for byte, so a fallback-rendered .icns will show up
# as a diff on a Mac; regenerate there before committing if that matters.
#
# Usage: icons.sh [png|syso|icns|all]   (default: all)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

TARGET="${1:-all}"
case "$TARGET" in
png | syso | icns | all) ;;
*)
  echo "usage: $0 [png|syso|icns|all]" >&2
  exit 2
  ;;
esac

SRC="packaging/windows/winres/"

# The source artwork everything else descends from.
MASTER_SVG="docs/assets/prem-down.svg"

# The id of the background tile, hidden to get the transparent variant..
BACKDROP_ID="backdrop"

# The sizes winres.json lists, and the ones iconutil builds the .icns from.
PNG_SIZES="16 24 32 48 64 128 256"

# The size the master is rendered at for the non-macOS .icns path, which derives
# every entry from the one image it is given. 1024 is the largest icns entry, so
# nothing it writes has to be scaled up.
ICNS_MASTER_PX=1024

# render_svg draws the master at one size, with the background tile hidden, to
# the given path. Both the PNG set and the .icns fallback go through it, so
# neither can end up rendering the master differently from the other.
TMP_CSS=""
render_svg() {
  local size="$1" out="$2"
  if [ -z "$TMP_CSS" ]; then
    command -v rsvg-convert >/dev/null || {
      echo "ERROR: rsvg-convert not found (brew install librsvg)" >&2
      exit 1
    }
    # The plate is hidden by id, so an id that has been renamed away has to stop
    # the run.
    grep -q "id=\"$BACKDROP_ID\"" "$MASTER_SVG" || {
      echo "ERROR: no element with id=\"$BACKDROP_ID\" in $MASTER_SVG." >&2
      echo "       That id is what hides the backing plate for these rasters." >&2
      exit 1
    }
    TMP_CSS="$(mktemp -t premdown-icons)"
    # A stylesheet hides the plate without touching the file.
    printf '#%s { display: none }\n' "$BACKDROP_ID" >"$TMP_CSS"
  fi
  rsvg-convert --stylesheet "$TMP_CSS" -w "$size" -h "$size" "$MASTER_SVG" -o "$out"
}

cleanup() { [ -n "$TMP_CSS" ] && rm -f "$TMP_CSS"; }
trap cleanup EXIT

if [ "$TARGET" = "png" ] || [ "$TARGET" = "all" ]; then
  echo "rendering ${SRC}icon*.png from $MASTER_SVG"
  for size in $PNG_SIZES; do
    render_svg "$size" "${SRC}icon${size}.png"
  done
fi

if [ "$TARGET" = "png" ]; then
  echo "done"
  exit 0
fi

command -v go >/dev/null || {
  echo "ERROR: go not found" >&2
  exit 1
}

if [ "$TARGET" != "icns" ]; then
  echo "rendering internal/integrate/rsrc_windows_amd64.syso"
  # --arch amd64 only: the Windows binary publish.yml builds is amd64, and a
  # .syso for an architecture that is never built would just be dead weight.
  go run github.com/tc-hib/go-winres@latest make \
    --arch amd64 \
    --in "${SRC}winres.json" \
    --out internal/integrate/rsrc
fi

if [ "$TARGET" != "syso" ]; then
  echo "rendering internal/updates/prem-down.icns"
  if command -v iconutil >/dev/null; then
    # On macOS iconutil takes a .iconset directory whose filenames are the sizes
    # it should record; the @2x entries are the same artwork at double the
    # pixels for the retina variant of each size.
    TMP="$(mktemp -d)"
    trap 'cleanup; rm -rf "$TMP"' EXIT
    ICONSET="$TMP/prem-down.iconset"
    mkdir -p "$ICONSET"
    cp "${SRC}icon16.png" "$ICONSET/icon_16x16.png"
    cp "${SRC}icon32.png" "$ICONSET/icon_16x16@2x.png"
    cp "${SRC}icon32.png" "$ICONSET/icon_32x32.png"
    cp "${SRC}icon64.png" "$ICONSET/icon_32x32@2x.png"
    cp "${SRC}icon128.png" "$ICONSET/icon_128x128.png"
    cp "${SRC}icon256.png" "$ICONSET/icon_128x128@2x.png"
    cp "${SRC}icon256.png" "$ICONSET/icon_256x256.png"
    iconutil -c icns "$ICONSET" -o internal/updates/prem-down.icns
  else
    # On Windows icnsify takes a raster, so the master is rendered to one first
    # - at ICNS_MASTER_PX. The bytes differ from iconutil's.
    echo "  iconutil not found: using icnsify instead." >&2
    echo "  The result is valid but differs byte for byte from the macOS build." >&2
    TMP="$(mktemp -d)"
    trap 'cleanup; rm -rf "$TMP"' EXIT
    render_svg "$ICNS_MASTER_PX" "$TMP/master.png"
    go run github.com/jackmordaunt/icns/cmd/icnsify@latest \
      -i "$TMP/master.png" \
      -o internal/updates/prem-down.icns
  fi
fi

echo "done"
