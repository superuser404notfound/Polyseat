#!/usr/bin/env bash
# Patch 1, measured rather than read, without a seat.
#
# It builds wlroots 0.20.2 twice, once patched and once as upstream ships it,
# and runs headless-hdr-probe.c against each. The probe creates a real headless
# output and asks it the two questions sway's output_supports_hdr() asks, then
# hands it an HDR output state and checks that it is accepted.
#
# The unpatched build is not decoration. A probe that passes against both would
# be measuring nothing at all, so this fails if the plain build does not refuse.
# Upstream must say no and the patched one must say yes.
#
# Needs meson, ninja, a C compiler and wlroots' own build dependencies. Skips
# rather than fails when they are not there, because a missing toolchain is not
# a broken patch. Two builds, so allow a few minutes.
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PATCH="$HERE/patches/wlroots-headless-hdr.patch"
PROBE="$HERE/headless-hdr-probe.c"
TAG="${WLROOTS_TAG:-0.20.2}"
URL="https://gitlab.freedesktop.org/wlroots/wlroots/-/archive/$TAG/wlroots-$TAG.tar.gz"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

step "Tools"
missing=""
for tool in meson ninja cc pkg-config curl tar patch; do
    command -v "$tool" >/dev/null || missing="$missing $tool"
done
# wayland-protocols is a build dependency of wlroots and is not something this
# script should install behind anybody's back.
pkg-config --exists wayland-protocols 2>/dev/null || missing="$missing wayland-protocols"
if [ -n "$missing" ]; then
    warn "skipping, not installed:$missing"
    warn "  meson and ninja install cleanly into a venv if the distribution has neither:"
    warn "    python3 -m venv /tmp/mv && /tmp/mv/bin/pip install meson ninja"
    warn "    PATH=/tmp/mv/bin:\$PATH $0"
    exit 0
fi
ok "meson $(meson --version), ninja $(ninja --version)"

[ -f "$PATCH" ] || { bad "missing $PATCH"; exit 1; }
[ -f "$PROBE" ] || { bad "missing $PROBE"; exit 1; }

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# Builds one tree and runs the probe against it.
#   build_and_probe <name> <apply the patch: yes|no>
# Prints the probe's output and returns the probe's own exit status, so that
# 0 means the output does HDR and 1 means it does not.
build_and_probe() {
    local name=$1 apply=$2 dir="$WORK/$1"

    mkdir -p "$dir"
    tar xz -C "$dir" --strip-components=1 -f "$WORK/wlroots.tar.gz" || return 2

    if [ "$apply" = yes ]; then
        (cd "$dir" && patch -p1 --quiet < "$PATCH") || { bad "the patch does not apply to $TAG"; return 2; }
    fi

    # backends=[] leaves only the headless backend, which is the one being
    # patched and the only one this needs. It also avoids libliftoff, which the
    # DRM backend wants and which many machines do not have.
    (cd "$dir" && meson setup build "-Dbackends=[]" -Dexamples=false) >"$dir/setup.log" 2>&1 \
        || { bad "meson setup failed for $name:"; tail -15 "$dir/setup.log" | sed 's/^/    /'; return 2; }
    (cd "$dir" && ninja -C build) >"$dir/build.log" 2>&1 \
        || { bad "build failed for $name:"; grep -A6 -m2 -i "error" "$dir/build.log" | sed 's/^/    /'; return 2; }

    # Read into arrays rather than left to word splitting. The linter objects
    # to the unquoted form and is right to: pkg-config emits several flags and
    # they have to arrive as several arguments rather than one.
    local -a cflags libs
    read -r -a cflags <<< "$(pkg-config --cflags wayland-server pixman-1 libdrm)"
    read -r -a libs <<< "$(pkg-config --libs wayland-server)"

    # The library's name is read out of the build rather than written down.
    # wlroots carries its version in the soname, so master builds
    # libwlroots-0.21.so while the released tag builds 0.20 - and this script is
    # most useful precisely when it is pointed at master, to check the patch
    # against what it would actually merge into.
    local lib
    lib=$(basename "$(ls "$dir"/build/libwlroots-*.so 2>/dev/null | head -1)" 2>/dev/null)
    if [ -z "$lib" ]; then
        bad "no libwlroots-*.so in the $name build"
        return 2
    fi

    cc -DWLR_USE_UNSTABLE -o "$dir/probe" "$PROBE" \
        -I "$dir/include" -I "$dir/build/include" "${cflags[@]}" \
        -L "$dir/build" "-l:$lib" "${libs[@]}" 2>"$dir/probe.log" \
        || { bad "the probe does not compile against $name ($lib):"; sed 's/^/    /' "$dir/probe.log"; return 2; }

    LD_LIBRARY_PATH="$dir/build" WLR_LOG=silent "$dir/probe" 2>/dev/null | sed 's/^/    /'
    return "${PIPESTATUS[0]}"
}

step "Source at $TAG"
curl -sfL "$URL" -o "$WORK/wlroots.tar.gz" || { bad "could not fetch wlroots $TAG"; exit 1; }
ok "fetched"

step "Upstream, unpatched - it has to refuse"
build_and_probe plain no
case $? in
    1) ok "upstream says no, so the probe measures something" ;;
    0) bad "an unpatched wlroots already does HDR on a headless output"
       warn "  either upstream has fixed this, in which case the patch is obsolete,"
       warn "  or the probe is not measuring what it claims to"
       exit 1 ;;
    *) bad "could not build or run against upstream"; exit 1 ;;
esac

step "Patched - it has to accept"
build_and_probe patched yes
case $? in
    0) ok "the patched headless output does HDR" ;;
    1) bad "the patch did not change the answer"; exit 1 ;;
    *) bad "could not build or run against the patched tree"; exit 1 ;;
esac

step "Result"
ok "patch 1 does what it claims, on a real headless output, with no seat involved"
warn "sway's third condition, a renderer that can do output colour transforms,"
warn "is not checked here: that is Vulkan on the machine's own driver, and only"
warn "10-vulkan.sh can answer it"
