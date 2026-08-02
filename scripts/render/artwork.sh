#!/bin/bash
# Render the committed brand artwork - the README banner and the GitHub social
# preview - from the one geometry defined here:
#
#   docs/assets/prem-down_banner.svg                  - README header, 1600x400
#   docs/assets/prem-down_social_preview_1280x640.png - GitHub social preview,
#                                                       exported at 2x
#                                                       (2560x1280)
#
# Both compositions are the logo plus a wordmark. The wordmark uses Mannin Bold,
# converted to outlines for the banner.
#
# Developer tool, not part of the build: the outputs are committed, so this only
# needs re-running when the artwork changes.
#
# Usage: artwork.sh [banner|social|all]   (default: all)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

TARGET="${1:-all}"
case "$TARGET" in
banner | social | all) ;;
*)
  echo "usage: $0 [banner|social|all]" >&2
  exit 2
  ;;
esac

BANNER_SVG="docs/assets/prem-down_banner.svg"
SOCIAL_PNG="docs/assets/prem-down_social_preview_1280x640.png"

# Brand constants, shared by both compositions.
BG="#000033"
FG="#9999FF"
# The sprocket columns are drawn at 40% so the arrow reads as the subject.
SPROCKET_OPACITY="0.4"
WORDMARK_FONT="Mannin"
WORDMARK_WEIGHT="bold"

need() {
  command -v "$1" >/dev/null || {
    echo "ERROR: $1 not found ($2)" >&2
    exit 1
  }
}

need rsvg-convert "brew install librsvg"
need fc-list "brew install fontconfig"
[ "$TARGET" = "social" ] || need inkscape "brew install --cask inkscape"

# The wordmark cannot be reconstructed without the typeface, so fail loudly
# rather than silently substituting a fallback face. grep -c rather than -q: -q
# exits on the first match, and the resulting SIGPIPE would fail the pipeline
# under `set -o pipefail`.
if [ "$(fc-list 2>/dev/null | grep -ci "Mannin-Bold" || true)" -eq 0 ]; then
  echo "ERROR: Mannin Bold not installed - the wordmark would render in a substitute face." >&2
  echo "       Install Mannin-Bold.otf into ~/Library/Fonts and re-run." >&2
  exit 1
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# sprockets emits one perforation column: six rounded rects on the 15-unit pitch
# of the master tile, at the given x.
sprockets() {
  local x="$1" y
  for y in 10 25 40 55 70 85; do
    printf '      <rect x="%s" y="%s" width="6" height="8" rx="1"/>\n' "$x" "$y"
  done
}

# mark emits the icon artwork - both sprocket columns and the arrow - inside a
# group placed by the caller's translate/scale, in the master's 100-unit space.
mark() {
  local transform="$1"
  cat <<EOF
  <g transform="$transform">
    <g fill="$FG" opacity="$SPROCKET_OPACITY">
$(sprockets 10)
    </g>

    <g fill="$FG" opacity="$SPROCKET_OPACITY">
$(sprockets 84)
    </g>

    <path d="M 50 22 L 50 68" stroke="$FG" stroke-width="8" stroke-linecap="round"/>

    <path d="M 32 50 L 50 72 L 68 50" stroke="$FG" stroke-width="8" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
  </g>
EOF
}

# ------------------------------ banner ---------------------------------------
# 1600x400, mark on the left, "prem-down" set on one line beside it. The text
# origin puts the wordmark's bounding box at 720.652,145.712 - matching the
# committed artwork - at font-size 113.
if [ "$TARGET" = "banner" ] || [ "$TARGET" = "all" ]; then
  echo "rendering $BANNER_SVG"
  cat >"$TMP/banner.svg" <<EOF
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1600 400" width="1600" height="400">
  <rect width="1600" height="400" fill="$BG"/>

  <!-- original mark, no wordmark -->
$(mark "translate(223.45 -6) scale(4)")

  <!-- wordmark: "prem-down" -->
  <text x="712.29" y="233.4" fill="$FG"
        font-family="$WORDMARK_FONT" font-weight="$WORDMARK_WEIGHT" font-size="113">prem-down</text>
</svg>
EOF
  # Outline the text so the committed SVG carries no font dependency.
  inkscape --export-text-to-path --export-plain-svg \
    --export-filename="$TMP/banner_out.svg" "$TMP/banner.svg" >/dev/null 2>&1
  mv "$TMP/banner_out.svg" "$BANNER_SVG"
fi

# ------------------------------- social preview ------------------------------
# 1280x640, mark on the left, wordmark stacked as "prem" / "down" with both
# lines' bounding boxes flush at x 736 and the block balancing the mark's
# 167.6 left margin. Exported at 2x for a crisp preview card.
if [ "$TARGET" = "social" ] || [ "$TARGET" = "all" ]; then
  echo "rendering $SOCIAL_PNG"
  cat >"$TMP/social.svg" <<EOF
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1280 640" width="1280" height="640">
  <rect width="1280" height="640" fill="$BG"/>

  <!-- original mark, no wordmark -->
$(mark "translate(119.6 80) scale(4.8)")

  <!-- wordmark: "prem" over "down", optically flush left -->
  <g fill="$FG" font-family="$WORDMARK_FONT" font-weight="$WORDMARK_WEIGHT" font-size="135">
    <text x="726.010" y="280.860">prem</text>
    <text x="729.520" y="425.260">down</text>
  </g>
</svg>
EOF
  rsvg-convert -w 2560 -h 1280 "$TMP/social.svg" -o "$SOCIAL_PNG"
fi

echo "done"
