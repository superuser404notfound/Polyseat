#!/usr/bin/env bash
# Kill gate four: a Sunshine that believes the output when it says it is in HDR.
#
# Sunshine decides the stream's colourspace in colorspace_from_client_config():
#
#     if (config.dynamicRange > 0 && hdr_display) -> BT.2020 PQ
#     else                                        -> Rec.601/709/2020 SDR
#
# and `hdr_display` is `display->is_hdr()`. That method is virtual with a `false`
# default in platform/common.h, and it is overridden in exactly two places:
# kmsgrab, which reads the DRM connector's HDR_OUTPUT_METADATA property, and the
# PipeWire path, which reads the SPA colorimetry. wlgrab overrides neither, so a
# client that asks for HDR over screencopy gets a 10 bit encode of Rec.709 and
# Moonlight is correctly told HDR is off.
#
# The patch gives wlgrab the same answer from the same place the compositor
# already publishes it: colour-management-v1, which hands out the output's whole
# image description and does not care whether the output has a cable behind it.
#
# This is the long step. Sunshine is a large C++ project, and on NVIDIA it wants
# the CUDA toolkit, which is several gigabytes on its own. Half an hour is
# normal, longer on a slow disk.
set -uo pipefail
source "$(dirname "$0")/lib.sh"

PATCH="$HERE/patches/sunshine-wlgrab-hdr.patch"
SRC=/root/m8/sunshine
DROPIN=/home/$PLAYER/.config/systemd/user/polyseat-sunshine.service.d/50-hdr.conf
RUNNER=$PREFIX/bin/polyseat-m8-sunshine-run

[ -f "$PATCH" ] || { bad "missing $PATCH"; exit 1; }

step "Which encoder this seat needs"
if inseat nvidia-smi -L >/dev/null 2>&1; then
    VENDOR=nvidia
    ok "NVIDIA, so the build needs CUDA"
else
    VENDOR=amd
    ok "no NVIDIA, building for VA-API"
fi

: "${M8_CUDA:=auto}"
USE_CUDA=false
if [ "$VENDOR" = nvidia ] && [ "$M8_CUDA" != 0 ]; then
    USE_CUDA=true
fi

if [ "$VENDOR" = nvidia ] && [ "$USE_CUDA" = false ]; then
    bad "M8_CUDA=0 on an NVIDIA seat would build a Sunshine with no NVENC at all"
    warn "  that is worse than the packaged one, so this stops here"
    exit 1
fi

step "Build dependencies"
# From LizardByte's own Arch PKGBUILD, so that what is built here is the package
# the seat already runs, plus the patch.
deps="base-devel cmake git make nodejs npm python-jinja shaderc
      appstream appstream-glib desktop-file-utils
      avahi curl gtk3 libayatana-appindicator libcap libdrm libevdev libmfx
      libpulse libva libx11 libxcb libxfixes libxrandr libxtst miniupnpc
      numactl openssl opus qt6-base qt6-svg vulkan-icd-loader
      wayland wayland-protocols"
[ "$USE_CUDA" = true ] && deps="$deps cuda"

# The graphics driver is not a package in this container, it is a set of files
# libnvidia-container mirrors in from the host. pacman does not know that, so
# anything that resolves a dependency on it tries to install a real package over
# those files and the transaction dies on a file conflict:
#
#     opencl-nvidia: /usr/lib/libnvidia-opencl.so.1 exists in filesystem
#
# provision.go carries the same four flags for the same reason and says they
# belong on every pacman call that can resolve dependencies, not only the one
# that installs Steam. opencl-nvidia is the fifth and is specific to this step:
# Arch's cuda depends on it by name rather than through the virtual
# opencl-driver, so assuming the virtual one is not enough.
flags=""
if [ "$VENDOR" = nvidia ]; then
    flags="--assume-installed opengl-driver
           --assume-installed vulkan-driver
           --assume-installed lib32-opengl-driver
           --assume-installed lib32-vulkan-driver
           --assume-installed opencl-nvidia"
fi

# shellcheck disable=SC2086
inseat pacman -S --needed --noconfirm $flags $deps || { bad "pacman failed"; exit 1; }
ok "toolchain in place"

step "Does the system wayland-protocols carry colour management"
# The patch generates bindings for staging/color-management. Sunshine bundles
# its own wayland-protocols as a submodule, and that copy can predate the
# protocol; the symptom would be a cmake error about a missing xml with nothing
# to say which one. So the system copy is used and checked first.
wpdir=$(insh "pkg-config --variable=pkgdatadir wayland-protocols 2>/dev/null")
if [ -n "$wpdir" ] && insh "test -f $wpdir/staging/color-management/color-management-v1.xml"; then
    ok "$wpdir/staging/color-management/color-management-v1.xml"
else
    bad "the seat's wayland-protocols has no staging/color-management"
    warn "  update wayland-protocols in the seat; the protocol went stable upstream in 1.41"
    exit 1
fi

step "Source at $SUNSHINE_COMMIT"
insh "rm -rf $SRC && mkdir -p $(dirname $SRC)"
inseat git clone https://github.com/LizardByte/Sunshine.git "$SRC" \
    || { bad "clone failed"; exit 1; }
insh "cd $SRC && git checkout --quiet $SUNSHINE_COMMIT" || { bad "no such commit"; exit 1; }
insh "cd $SRC && git submodule update --init --recursive --depth 1" \
    || { bad "submodules failed"; exit 1; }
ok "checked out"

step "Applying the patch"
"${INCUS[@]}" file push "$PATCH" "$CT/root/m8/sunshine-wlgrab-hdr.patch" >/dev/null
if insh "cd $SRC && patch -p1 --dry-run < /root/m8/sunshine-wlgrab-hdr.patch"; then
    insh "cd $SRC && patch -p1 < /root/m8/sunshine-wlgrab-hdr.patch" >/dev/null
    ok "patched"
else
    bad "the patch does not apply to $SUNSHINE_COMMIT"
    exit 1
fi

step "Building (this is the long one)"
# BUILD_WERROR is off on purpose. Upstream builds with warnings as errors, which
# is right for upstream and wrong here: a patched tree that stops on a warning
# tells us nothing about whether the idea works.
#
# The prefix is /usr/local so the packaged Sunshine at /usr/bin/sunshine stays
# exactly where it is and this step is undone by deleting a drop-in.
cuda_opts="-DSUNSHINE_ENABLE_CUDA=OFF -DCUDA_FAIL_ON_MISSING=OFF"
cuda_env=""
if [ "$USE_CUDA" = true ]; then
    cuda_opts="-DSUNSHINE_ENABLE_CUDA=ON"

    # Which compiler nvcc is allowed to call, and it is not the default one.
    # nvcc refuses a gcc newer than the toolkit knows about, and Arch's cuda
    # says which one it wants by depending on a versioned gcc: today cuda 13.3
    # pulls gcc15 while the seat's cc is gcc 16. Read from the dependency
    # rather than pinned here, which is what LizardByte's own PKGBUILD does,
    # so this does not go stale on the next toolkit.
    gccver=$(insh "LC_ALL=C pacman -Si cuda 2>/dev/null | grep -Pom1 '^Depends On\s*:.*\bgcc\K[0-9]+\b'")
    if [ -n "$gccver" ] && insh "test -x /usr/bin/g++-$gccver"; then
        ok "nvcc will use g++-$gccver, as cuda asks"
        cuda_env="CUDA_PATH=/opt/cuda NVCC_CCBIN=/usr/bin/g++-$gccver"
    else
        warn "cuda names no versioned gcc, or g++-$gccver is missing; using the default g++"
        warn "  if nvcc stops with \"unsupported GNU version\", this is why"
        cuda_env="CUDA_PATH=/opt/cuda NVCC_CCBIN=/usr/bin/g++"
    fi
fi

insh "cd $SRC && env $cuda_env cmake -S . -B build -Wno-dev \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_INSTALL_PREFIX=$PREFIX \
    -DBUILD_DOCS=OFF -DBUILD_TESTS=OFF -DBUILD_WERROR=OFF \
    -DSUNSHINE_SYSTEM_WAYLAND_PROTOCOLS=ON \
    -DSUNSHINE_ENABLE_WAYLAND=ON \
    -DSUNSHINE_EXECUTABLE_PATH=$PREFIX/bin/sunshine \
    $cuda_opts" || { bad "cmake configure failed"; exit 1; }

insh "cd $SRC && cmake --build build -j\$(nproc)" || { bad "build failed"; exit 1; }
insh "cd $SRC && cmake --install build" || { bad "install failed"; exit 1; }
ok "installed into $PREFIX"

step "A runner for the patched binary"
# Derived from the daemon's own runner with one substitution, rather than
# written fresh: the waiting it does is load bearing, see M1, and a second copy
# that drifts from it would fail in the same nasty way.
insh "sed 's#exec /usr/bin/sunshine#exec $PREFIX/bin/sunshine#' \
      $PREFIX/bin/polyseat-sunshine-run > $RUNNER && chmod 755 $RUNNER"
if insh "grep -q '$PREFIX/bin/sunshine' $RUNNER"; then
    ok "wrote $RUNNER"
else
    bad "the substitution did not take - has polyseat-sunshine-run changed?"
    exit 1
fi

step "Pointing the unit at it"
insh "cat > $DROPIN <<'CONF'
# Polyseat spike M8: the patched Sunshine.
# Written by spike/m8-hdr/40-sunshine.sh. Deleting this file puts the seat back
# on the packaged Sunshine at /usr/bin/sunshine.
[Service]
ExecStart=
ExecStart=$RUNNER
CONF"
insh "chown $PLAYER:$PLAYER $DROPIN"
ok "wrote $DROPIN"

as_player systemctl --user daemon-reload
as_player systemctl --user restart polyseat-sunshine.service

if wait_active polyseat-sunshine.service; then
    ok "the patched Sunshine is running"
else
    bad "it did not start"
    as_player journalctl --user -u polyseat-sunshine.service --no-pager -n 60 | sed 's/^/    /'
    exit 1
fi

ok "gate four passed, continue with 50-verify.sh"
