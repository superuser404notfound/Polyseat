#!/usr/bin/env bash
# Can a capture client ask a wlroots compositor for HDR, and does it get it?
#
# No seat, no container, no Sunshine. This builds wlroots at the tip of merge
# request 5443 - fpoisot's scene-based output capture, which closes work item
# 4108 - twice, once with the patch in patches/ and once without, and runs
# capture-probe.c against each.
#
# The probe is a headless compositor with one output, a scene holding a rect of
# mid grey, and a capture client that takes one frame. Mid grey on purpose:
# black and white survive almost any transfer function unchanged, so either
# would hide the difference this looks for. The primaries conversion leaves a
# neutral alone, so what the number shows is the transfer function and nothing
# else.
#
# Three answers are possible for that pixel in a ten bit buffer, and they are
# far apart:
#
#     512   the value was passed through untouched
#     219   linear light
#     437   BT.2020 with the ST2084 PQ transfer function
#
# The unpatched build is not decoration. It has to refuse, because a probe that
# passed against both would be measuring nothing.
#
# Needs meson, ninja, a C compiler, wayland-scanner, git and curl. It fetches
# wayland-protocols and the Vulkan headers into a prefix of its own rather than
# asking anybody to install them, because both are missing on at least one
# machine here. The Vulkan renderer is not optional: it is the only one in
# wlroots whose features.output_color_transform is true, so gles2 and pixman
# would carry the ten bits and convert nothing.
set -uo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PATCH="$HERE/patches/wlroots-capture-image-description.patch"
PROBE="$HERE/capture-probe.c"
MR="${WLROOTS_MR:-5443}"
WP_TAG="${WAYLAND_PROTOCOLS_TAG:-1.49}"
VK_TAG="${VULKAN_HEADERS_TAG:-v1.4.357}"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

step "Tools"
missing=""
for tool in cc pkg-config curl git tar patch wayland-scanner; do
    command -v "$tool" >/dev/null || missing="$missing $tool"
done
for tool in meson ninja; do
    command -v "$tool" >/dev/null || missing="$missing $tool"
done
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

WORK=${KEEP_WORK:-$(mktemp -d)}
if [ -z "${KEEP_WORK:-}" ]; then
    trap 'rm -rf "$WORK"' EXIT
else
    mkdir -p "$WORK"
    warn "keeping the build in $WORK"
fi

PREFIX="$WORK/prefix"
export PKG_CONFIG_PATH="$PREFIX/share/pkgconfig"
export CFLAGS="-I$PREFIX/include"

step "Build dependencies into a prefix of our own"
if ! pkg-config --exists wayland-protocols || [ -n "${FORCE_PREFIX:-}" ]; then
    curl -sfL "https://gitlab.freedesktop.org/wayland/wayland-protocols/-/archive/$WP_TAG/wayland-protocols-$WP_TAG.tar.gz" \
        -o "$WORK/wp.tar.gz" || { bad "could not fetch wayland-protocols $WP_TAG"; exit 1; }
    mkdir -p "$WORK/wp" && tar xz -C "$WORK/wp" --strip-components=1 -f "$WORK/wp.tar.gz"
    (cd "$WORK/wp" && meson setup build --prefix="$PREFIX" -Dtests=false && ninja -C build install) \
        >"$WORK/wp.log" 2>&1 || { bad "wayland-protocols did not build:"; tail -10 "$WORK/wp.log" | sed 's/^/    /'; exit 1; }
fi
ok "wayland-protocols $(pkg-config --modversion wayland-protocols)"

# wlroots' meson refuses outright when it cannot include vulkan/vulkan.h, and
# the loader's pkg-config file does not carry the headers on every
# distribution.
if [ ! -f /usr/include/vulkan/vulkan.h ]; then
    curl -sfL "https://github.com/KhronosGroup/Vulkan-Headers/archive/refs/tags/$VK_TAG.tar.gz" \
        -o "$WORK/vkh.tar.gz" || { bad "could not fetch Vulkan-Headers $VK_TAG"; exit 1; }
    mkdir -p "$WORK/vkh" && tar xz -C "$WORK/vkh" --strip-components=1 -f "$WORK/vkh.tar.gz"
    mkdir -p "$PREFIX/include"
    cp -r "$WORK/vkh/include/vulkan" "$WORK/vkh/include/vk_video" "$PREFIX/include/"
    ok "Vulkan headers $VK_TAG into the prefix"
else
    ok "Vulkan headers already present"
fi

step "wlroots at merge request $MR"
if [ ! -d "$WORK/wlroots" ]; then
    git clone -q --filter=blob:none https://gitlab.freedesktop.org/wlroots/wlroots.git "$WORK/wlroots" \
        || { bad "could not clone wlroots"; exit 1; }
    git -C "$WORK/wlroots" fetch -q origin "refs/merge-requests/$MR/head:mr" \
        || { bad "could not fetch merge request $MR"; exit 1; }
fi
git -C "$WORK/wlroots" checkout -q mr
ok "at $(git -C "$WORK/wlroots" rev-parse --short mr)"

# Generates the client half of the two protocols the probe speaks. The third is
# only there because ext-image-capture-source-v1 refers to it and the linker
# wants the symbol.
step "Protocol code for the probe"
mkdir -p "$WORK/probe"
for p in ext-image-capture-source/ext-image-capture-source-v1 \
         ext-image-copy-capture/ext-image-copy-capture-v1 \
         ext-foreign-toplevel-list/ext-foreign-toplevel-list-v1; do
    n=$(basename "$p")
    xml="$PREFIX/share/wayland-protocols/staging/$p.xml"
    [ -f "$xml" ] || xml="/usr/share/wayland-protocols/staging/$p.xml"
    wayland-scanner client-header "$xml" "$WORK/probe/$n-client-protocol.h" || exit 1
    wayland-scanner private-code  "$xml" "$WORK/probe/$n-protocol.c" || exit 1
done
ok "generated"

# Builds one tree and runs the probe against it. Prints the probe's output and
# returns its exit status: 0 means the capture came back as PQ.
build_and_probe() {
    local name=$1 apply=$2 dir="$WORK/$1"

    if [ ! -d "$dir" ]; then
        cp -r "$WORK/wlroots" "$dir" || return 2
        if [ "$apply" = yes ]; then
            (cd "$dir" && git apply "$PATCH") || { bad "the patch does not apply"; return 2; }
        fi
        # backends=[] leaves the headless backend, which is all this needs, and
        # avoids libliftoff. vulkan is the point: see the header of this file.
        (cd "$dir" && meson setup build "-Dbackends=[]" -Dexamples=false \
            -Dxwayland=disabled -Drenderers=gles2,vulkan) >"$dir/setup.log" 2>&1 \
            || { bad "meson setup failed for $name:"; tail -12 "$dir/setup.log" | sed 's/^/    /'; return 2; }
        (cd "$dir" && ninja -C build) >"$dir/build.log" 2>&1 \
            || { bad "build failed for $name:"; grep -m3 -A6 -i "error" "$dir/build.log" | sed 's/^/    /'; return 2; }
    fi

    local lib
    lib=$(basename "$(ls "$dir"/build/libwlroots-*.so 2>/dev/null | head -1)" 2>/dev/null)
    [ -n "$lib" ] || { bad "no libwlroots-*.so in the $name build"; return 2; }

    local -a cflags libs
    read -r -a cflags <<< "$(pkg-config --cflags wayland-server wayland-client pixman-1 libdrm)"
    read -r -a libs <<< "$(pkg-config --libs wayland-server wayland-client)"

    cc -g -O1 -DWLR_USE_UNSTABLE -o "$dir/probe" "$PROBE" \
        "$WORK/probe/ext-image-capture-source-v1-protocol.c" \
        "$WORK/probe/ext-image-copy-capture-v1-protocol.c" \
        "$WORK/probe/ext-foreign-toplevel-list-v1-protocol.c" \
        -I "$WORK/probe" -I "$dir/include" -I "$dir/build/include" -I "$PREFIX/include" \
        "${cflags[@]}" -L "$dir/build" "-l:$lib" "${libs[@]}" -lm \
        2>"$dir/probe.log" \
        || { bad "the probe does not compile against $name:"; sed 's/^/    /' "$dir/probe.log" | head -12; return 2; }

    # XB30 rather than XR30, and that is a property of the card rather than of
    # the protocol: an NVIDIA RTX 4080 here lists both through EGL and then
    # refuses XR30 at gbm_bo_create.
    PROBE_HDR=1 PROBE_FORMAT="${PROBE_FORMAT:-XB30}" WLR_RENDERER=vulkan \
        LD_LIBRARY_PATH="$dir/build" timeout 120 "$dir/probe" 2>/dev/null | sed 's/^/    /'
    return "${PIPESTATUS[0]}"
}

step "Upstream, unpatched - it has to refuse"
# The patch adds the only way to ask, so an unpatched tree cannot even build
# the probe. That is the refusal, and it is checked rather than assumed.
if build_and_probe plain no >/dev/null 2>&1; then
    bad "the probe built and passed against an unpatched wlroots"
    warn "  either upstream has implemented this, in which case the patch is obsolete,"
    warn "  or the probe is not measuring what it claims to"
    exit 1
fi
ok "upstream has no way to ask, so the probe measures something"

step "Patched - the capture has to come back as PQ"
build_and_probe patched yes
case $? in
    0) ok "a capture client asked for BT.2020 PQ and got it, from an SDR output" ;;
    1) bad "the capture came back, but not as PQ"; exit 1 ;;
    *) bad "could not build or run the patched tree"; exit 1 ;;
esac

step "Result"
ok "after merge request 5443, the only thing missing is the asking"
