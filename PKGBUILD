# Polyseat - Sunshine with event-driven wlr capture.
#
# LizardByte's own Arch recipe for the release named below, plus one patch. See
# README.md next to this file for what the patch does and why a seat needs it.
#
# Built with build.sh, which is the only supported way: it needs the CUDA
# toolkit, and a seat gets the finished package rather than this recipe.

_commit=63d35f702ee9e362e43263742981836ec0710384  # v2026.914.233613

pkgname=sunshine
pkgver=2026.914.233613
# One below the next upstream pkgrel, so the same release from LizardByte is
# older than this and the next one is newer.
pkgrel=1.1
pkgdesc="Self-hosted game stream host for Moonlight, with event-driven wlr capture for Polyseat seats"
arch=('x86_64')
url="https://github.com/superuser404notfound/Polyseat"
license=('GPL-3.0-only')
install=sunshine.install

depends=(
  'avahi'
  'curl'
  'gcc-libs'
  'gtk3'
  'hicolor-icon-theme'
  'libayatana-appindicator'
  'libcap'
  'libdrm'
  'libevdev'
  'libmfx'
  'libpipewire'
  'libpulse'
  'libva'
  'libx11'
  'libxcb'
  'libxfixes'
  'libxrandr'
  'libxtst'
  'miniupnpc'
  'numactl'
  'openssl'
  'opus'
  'qt6-base'
  'qt6-svg'
  'udev'
  'vulkan-icd-loader'
  'which'
)

makedepends=(
  'appstream'
  'appstream-glib'
  'cmake'
  'cuda'
  'desktop-file-utils'
  'gcc'
  'git'
  'make'
  'nodejs'
  'npm'
  'python-jinja'
  'shaderc'
)

optdepends=(
  'cuda: Nvidia GPU encoding support'
  'libva-mesa-driver: AMD GPU encoding support'
)

source=(
  "$pkgname::git+https://github.com/LizardByte/Sunshine.git#commit=${_commit}"
  'wlgrab-event-driven.patch'
)
sha256sums=(
  'SKIP'
  'SKIP'
)

prepare() {
    cd "$pkgname"
    git submodule update --recursive --init --depth 1
    patch -p1 < "$srcdir/wlgrab-event-driven.patch"
}

build() {
    export BRANCH="master"
    export BUILD_VERSION="$pkgver"
    export COMMIT="$_commit"

    export CFLAGS="${CFLAGS/-Werror=format-security/}"
    export CXXFLAGS="${CXXFLAGS/-Werror=format-security/}"

    export MAKEFLAGS="${MAKEFLAGS:--j4}"

    # Which compiler nvcc may call. It refuses one newer than the toolkit knows,
    # and Arch's cuda says which it wants by depending on a versioned gcc.
    local _cuda_gcc_version
    _cuda_gcc_version="$(LC_ALL=C pacman -Si cuda | grep -Pom1 '^Depends On\s*:.*\bgcc\K[0-9]+\b' || true)"
    export CUDA_PATH=/opt/cuda
    if [ -n "$_cuda_gcc_version" ] && [ -x "/usr/bin/g++-${_cuda_gcc_version}" ]; then
      export NVCC_CCBIN="/usr/bin/g++-${_cuda_gcc_version}"
    else
      export NVCC_CCBIN="/usr/bin/g++"
    fi

    # The publisher is Polyseat on purpose. This is not the binary LizardByte
    # ship, and somebody reading it off the web interface should not take a
    # problem with it to them.
    cmake \
      -S "$pkgname" \
      -B build \
      -Wno-dev \
      -D BUILD_DOCS=OFF \
      -D BUILD_TESTS=OFF \
      -D BUILD_WERROR=ON \
      -D CMAKE_INSTALL_PREFIX=/usr \
      -D SUNSHINE_EXECUTABLE_PATH=/usr/bin/sunshine \
      -D SUNSHINE_ASSETS_DIR="share/sunshine" \
      -D SUNSHINE_ENABLE_CUDA=ON \
      -D SUNSHINE_PUBLISHER_NAME='Polyseat' \
      -D SUNSHINE_PUBLISHER_WEBSITE='https://github.com/superuser404notfound/Polyseat' \
      -D SUNSHINE_PUBLISHER_ISSUE_URL='https://github.com/superuser404notfound/Polyseat/issues'

    cmake --build build
}

check() {
    cd "${srcdir}/build"
    ./sunshine --version
}

package() {
    DESTDIR="$pkgdir" cmake --install build
}
