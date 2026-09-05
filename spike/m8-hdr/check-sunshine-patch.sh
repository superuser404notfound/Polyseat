#!/usr/bin/env bash
# The one part of this spike that can be checked without a seat.
#
# It applies patches/sunshine-wlgrab-hdr.patch to the upstream files it was
# written against, cuts out the two pieces of C++ it adds, and compiles them
# against the colour management protocol header generated from the real xml,
# with stand-ins for the Sunshine types they touch.
#
# What that proves: the listener tables name fields that exist, the callbacks
# have the signatures the protocol declares, and the unit conversions land on
# the right numbers. BT.2020's red primary is 0.708, 0.292; the protocol
# delivers it multiplied by a million and Moonlight wants it multiplied by fifty
# thousand, so 35400 and 14600 are the right answers and this checks for exactly
# those.
#
# What it does not prove: that Sunshine builds. Nothing short of 40-sunshine.sh
# proves that.
#
# Needs wayland-scanner, g++ and curl on whatever machine runs it. Not a seat.
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PATCH="$HERE/patches/sunshine-wlgrab-hdr.patch"
COMMIT="${SUNSHINE_COMMIT:-fa462d250bf19fb3ea7d6c9447023f4e61fa5053}"

# Both ends of the range a seat can actually present, pinned rather than taken
# from main. wayland-protocols 1.41 carries wp_color_manager_v1 at version 1 and
# 1.49 carries it at version 3, and the difference is not cosmetic: the newer
# one adds a `ready2` event, so the generated listener struct grows a member.
# The patch initialises those tables with designated initialisers precisely so
# that it compiles against both, and the only way to know it does is to compile
# it against both.
#
# This says nothing about which version a seat has. The real build generates its
# header from the seat's own wayland-protocols; these two are here so the check
# is reproducible and so the version spread is covered.
CM_TAGS="${CM_TAGS:-1.41 1.49}"
CM_XML_BASE="https://gitlab.freedesktop.org/wayland/wayland-protocols/-/raw"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

for tool in wayland-scanner g++ curl python3 patch; do
    command -v "$tool" >/dev/null || { bad "$tool is missing"; exit 1; }
done

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

step "Upstream sources at $COMMIT"
mkdir -p "$WORK/src/platform/linux" "$WORK/cmake/compile_definitions"
for f in src/platform/linux/wayland.h src/platform/linux/wayland.cpp \
         src/platform/linux/wlgrab.cpp cmake/compile_definitions/linux.cmake; do
    curl -sfL "https://raw.githubusercontent.com/LizardByte/Sunshine/$COMMIT/$f" -o "$WORK/$f" \
        || { bad "could not fetch $f"; exit 1; }
done
ok "four files"

step "Applying the patch"
if (cd "$WORK" && patch -p1 --quiet < "$PATCH"); then
    ok "applies cleanly"
else
    bad "does not apply - has the pinned commit moved?"
    exit 1
fi

step "The protocol headers, from the real xml"
for tag in $CM_TAGS; do
    curl -sfL "$CM_XML_BASE/$tag/staging/color-management/color-management-v1.xml" \
        -o "$WORK/cm-$tag.xml" || { bad "could not fetch the protocol at $tag"; exit 1; }
    mkdir -p "$WORK/proto-$tag"
    wayland-scanner client-header "$WORK/cm-$tag.xml" "$WORK/proto-$tag/color-management-v1.h" \
        || { bad "wayland-scanner failed on $tag"; exit 1; }
    ver=$(python3 -c "
import xml.etree.ElementTree as ET, sys
r = ET.parse('$WORK/cm-$tag.xml').getroot()
i = [x for x in r.findall('interface') if x.get('name') == 'wp_color_manager_v1'][0]
print(i.get('version'))")
    ok "wayland-protocols $tag, wp_color_manager_v1 version $ver"
done

step "Cutting the two pieces out of the patched file"
python3 - "$WORK" <<'PY'
import pathlib, sys
work = pathlib.Path(sys.argv[1])
s = (work / "src/platform/linux/wlgrab.cpp").read_text()

start = s.index("  /**\n   * @brief What colour an output")
end = s.index("  /**\n   * @brief Wayland screencopy capture backend")
(work / "block.inc").write_text(s[start:end])

start = s.index("    /**\n     * @brief Report whether the captured output is presenting in HDR.")
end = s.index("    platf::mem_type_e mem_type;")
(work / "methods.inc").write_text(s[start:end])
PY
[ -s "$WORK/block.inc" ] && [ -s "$WORK/methods.inc" ] || { bad "extraction failed"; exit 1; }
ok "the helper block and the two overrides"

step "Compiling the helper block"
cat > "$WORK/tu1.cpp" <<'CPP'
#include <algorithm>
#include <cstdint>
#include <iostream>
#include <string_view>
#include <unistd.h>
#include <wayland-client.h>
#include "color-management-v1.h"

using namespace std::literals;

namespace {
  struct log_sink {
    template<class T> log_sink &operator<<(const T &v) { (void) v; return *this; }
  };
  log_sink debug_sink;
}
#define BOOST_LOG(level) debug_sink

namespace wl {
  struct display_t {
    void roundtrip() {}
  };

#include "block.inc"

}  // namespace wl

int main() {
  wl::display_t d;
  auto c = wl::query_output_color(d, nullptr, nullptr);
  return c.known ? 1 : 0;
}
CPP
for tag in $CM_TAGS; do
    if g++ -std=c++23 -Wall -Werror -I"$WORK" -I"$WORK/proto-$tag" \
           -c "$WORK/tu1.cpp" -o "$WORK/tu1-$tag.o" 2>"$WORK/err1-$tag"; then
        ok "the listener tables match the protocol at $tag"
    else
        bad "it does not compile against wayland-protocols $tag:"
        sed 's/^/    /' "$WORK/err1-$tag"
        exit 1
    fi
done

step "Compiling and running the two overrides"
cat > "$WORK/tu2.cpp" <<'CPP'
#include <algorithm>
#include <cstdint>
#include <iostream>
#include <unistd.h>
#include <wayland-client.h>
#include "color-management-v1.h"

// From moonlight-common-c's Limelight.h, so the widths are the real ones.
typedef struct _SS_HDR_METADATA {
  struct { uint16_t x; uint16_t y; } displayPrimaries[3];
  struct { uint16_t x; uint16_t y; } whitePoint;
  uint16_t maxDisplayLuminance;
  uint16_t minDisplayLuminance;
  uint16_t maxContentLightLevel;
  uint16_t maxFrameAverageLightLevel;
  uint16_t maxFullFrameLuminance;
} SS_HDR_METADATA;

namespace platf {
  struct display_t {
    virtual ~display_t() = default;
    virtual bool is_hdr() { return false; }
    virtual bool get_hdr_metadata(SS_HDR_METADATA &metadata) { (void) metadata; return false; }
  };
}

namespace wl {
  struct output_color_t {
    bool known {false};
    std::uint32_t primaries {0};
    std::uint32_t transfer_function {0};
    std::int32_t target_primaries[8] {};
    std::uint32_t target_min_luminance {0};
    std::uint32_t target_max_luminance {0};
    std::uint32_t target_max_cll {0};
    std::uint32_t target_max_fall {0};
  };

  class wlr_t: public platf::display_t {
  public:
#include "methods.inc"
    output_color_t hdr_color;
  };
}

int main() {
  wl::wlr_t d;

  if (d.is_hdr()) {
    std::cerr << "an output nobody described reports HDR\n";
    return 1;
  }

  d.hdr_color.known = true;
  d.hdr_color.primaries = WP_COLOR_MANAGER_V1_PRIMARIES_BT2020;
  d.hdr_color.transfer_function = WP_COLOR_MANAGER_V1_TRANSFER_FUNCTION_ST2084_PQ;
  d.hdr_color.target_primaries[0] = 708000;  // BT.2020 red x, x 1000000
  d.hdr_color.target_primaries[1] = 292000;  // BT.2020 red y
  d.hdr_color.target_min_luminance = 50;     // 0.005 cd/m² x 10000
  d.hdr_color.target_max_luminance = 10000;  // cd/m²

  SS_HDR_METADATA m {};
  if (!d.is_hdr() || !d.get_hdr_metadata(m)) {
    std::cerr << "BT.2020 with PQ was not recognised as HDR\n";
    return 1;
  }

  if (m.displayPrimaries[0].x != 35400 || m.displayPrimaries[0].y != 14600) {
    std::cerr << "red primary came out as " << m.displayPrimaries[0].x
              << "," << m.displayPrimaries[0].y << " rather than 35400,14600\n";
    return 1;
  }

  if (m.minDisplayLuminance != 50 || m.maxDisplayLuminance != 10000) {
    std::cerr << "luminances came out as " << m.minDisplayLuminance
              << ".." << m.maxDisplayLuminance << "\n";
    return 1;
  }

  std::cout << "BT.2020 PQ recognised, red primary 35400,14600, luminance 50..10000\n";
  return 0;
}
CPP
for tag in $CM_TAGS; do
    if g++ -std=c++23 -Wall -Werror -I"$WORK" -I"$WORK/proto-$tag" \
           "$WORK/tu2.cpp" -o "$WORK/tu2-$tag" 2>"$WORK/err2-$tag"; then
        ok "compiles against $tag"
    else
        bad "it does not compile against wayland-protocols $tag:"
        sed 's/^/    /' "$WORK/err2-$tag"
        exit 1
    fi

    if out=$("$WORK/tu2-$tag"); then
        ok "$tag: $out"
    else
        bad "$tag: $out"
        exit 1
    fi
done

step "Result"
ok "the patch is internally consistent; whether Sunshine builds is 40-sunshine.sh"
