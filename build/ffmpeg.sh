#!/usr/bin/env bash
set -euo pipefail

# Build separate GPL media executables; do not link FFmpeg/x264 into the Go CLI.
# The pinned archives, notices and this recipe accompany every media bundle.
media_build=${1:?absolute new build directory required}
case "$media_build" in /*) ;; *) echo 'absolute build directory required' >&2; exit 2;; esac
test ! -e "$media_build"
mkdir -p "$media_build"
media_build=$(cd "$media_build" && pwd)
media_recipe=$(cd "$(dirname "$0")" && pwd)/ffmpeg.sh
case "$(uname -s)" in
  Darwin)
    test "$(uname -m)" = arm64
    media_suffix=''
    media_cflags='-mmacosx-version-min=12.0'
    media_ldflags='-mmacosx-version-min=12.0'
    media_platform=(--enable-videotoolbox --enable-pthreads)
    ;;
  MINGW*|MSYS*)
    test "$(uname -m)" = x86_64
    media_suffix='.exe'
    media_cflags="-I$media_build/prefix/include"
    media_ldflags='-static'
    media_platform=(--target-os=mingw32 --arch=x86_64 --enable-w32threads --disable-pthreads --enable-ffnvcodec --enable-nvenc --enable-amf --enable-libvpl)
    ;;
  *) echo 'supported builders: macOS arm64 or Windows MSYS2 x64' >&2; exit 2;;
esac

cd "$media_build"
mkdir gpg
# The keyring contains only the public release key. NTFS runner ACLs do not
# support POSIX chmod here; retain their inherited permissions on Windows.
if test "$(uname -s)" = Darwin; then chmod 700 gpg; fi
mkdir sources prefix bundle
mkdir bundle/ffmpeg bundle/licenses bundle/source
fetch() { curl --fail --location --retry 2 --max-time 120 --proto '=https' --tlsv1.2 --output "$1" "$2"; }
verify_hash() {
  if command -v sha256sum >/dev/null; then printf '%s  %s\n' "$1" "$2" | sha256sum -c -;
  else printf '%s  %s\n' "$1" "$2" | shasum -a 256 -c -; fi
}
fetch_verified() {
  fetch "$1" "$3" || return 1
  if verify_hash "$2" "$1"; then return 0; fi
  # Some upstream edge responses are HTTP 200 HTML rather than archive bytes.
  # Try the official download variant once; the original digest still gates tar.
  printf 'Primary source checksum failed; trying official download variant.\n' >&2
  fetch "$1" "$4" || return 1
  verify_hash "$2" "$1"
}

fetch sources/ffmpeg.tar.xz https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz
verify_hash 7138d28c96d9d3e3af4ee3d8cad72741f8ffb40da90c1112235dea3ecd3178a3 sources/ffmpeg.tar.xz
fetch sources/ffmpeg.tar.xz.asc https://ffmpeg.org/releases/ffmpeg-8.1.3.tar.xz.asc
fetch sources/ffmpeg-devel.asc https://ffmpeg.org/ffmpeg-devel.asc
gpg --homedir "$media_build/gpg" --batch --import sources/ffmpeg-devel.asc
gpg --homedir "$media_build/gpg" --batch --status-fd 1 --verify sources/ffmpeg.tar.xz.asc sources/ffmpeg.tar.xz |
  grep -F '[GNUPG:] VALIDSIG FCF986EA15E6E293A5644F10B4322F04D67658D8 '
fetch_verified sources/x264.tar.bz2 6eeb82934e69fd51e043bd8c5b0d152839638d1ce7aa4eea65a3fedcf83ff224 \
  https://code.videolan.org/videolan/x264/-/archive/b35605ace3ddf7c1a5d67a2eb553f034aef41d55/x264-b35605ace3ddf7c1a5d67a2eb553f034aef41d55.tar.bz2 \
  'https://code.videolan.org/videolan/x264/-/archive/b35605ace3ddf7c1a5d67a2eb553f034aef41d55/x264-b35605ace3ddf7c1a5d67a2eb553f034aef41d55.tar.bz2?inline=false'
tar -xf sources/ffmpeg.tar.xz
tar -xf sources/x264.tar.bz2

if test "$media_suffix" = .exe; then
  fetch sources/nv-codec-headers.tar.gz https://github.com/FFmpeg/nv-codec-headers/releases/download/n13.1.15.0/nv-codec-headers-13.1.15.0.tar.gz
  verify_hash 52532ceade3d5c1af62624986f13cf01b63c910576b08c0c278756c5e4b41ad0 sources/nv-codec-headers.tar.gz
  fetch sources/amf-headers.tar.gz https://github.com/GPUOpen-LibrariesAndSDKs/AMF/releases/download/v1.5.3/AMF-headers-v1.5.3.tar.gz
  verify_hash 570c8f6593b8c24a6e47a0a9c59b216a7198138e660ba4011590ae913343d5c3 sources/amf-headers.tar.gz
  fetch sources/libvpl.tar.gz https://codeload.github.com/intel/libvpl/tar.gz/refs/tags/v2.17.0
  verify_hash 4de3e2faf1e8307fb282e4a43f443191810f6a6b0a484fffa7995ba1c814c6ec sources/libvpl.tar.gz
  tar -xf sources/nv-codec-headers.tar.gz
  tar -xf sources/amf-headers.tar.gz
  tar -xf sources/libvpl.tar.gz
  # Upstream libvpl PR198: MinGW has the real bounded CRT functions; do not
  # activate ancient MSVC replacement macros that break current Windows headers.
  patch -d libvpl-2.17.0 -p1 < "$(dirname "$media_recipe")/libvpl-mingw.patch"
  make -C nv-codec-headers-13.1.15.0 PREFIX="$media_build/prefix" install
  cp -R amf-headers-v1.5.3/AMF "$media_build/prefix/include/"
  cmake -G Ninja -S libvpl-2.17.0 -B vpl-build \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$media_build/prefix" \
    -DVPL_PKGCONFIG_PRIVATE_LIBS=-lstdc++ \
    -DCMAKE_INSTALL_LIBDIR=lib -DBUILD_SHARED_LIBS=OFF -DBUILD_TESTS=OFF \
    -DBUILD_EXAMPLES=OFF -DINSTALL_EXAMPLES=OFF -DBUILD_EXPERIMENTAL=OFF
  cmake --build vpl-build --parallel 3
  cmake --install vpl-build
  cp sources/nv-codec-headers.tar.gz sources/amf-headers.tar.gz sources/libvpl.tar.gz bundle/source/
  cp "$(dirname "$media_recipe")/libvpl-mingw.patch" bundle/source/
  cp nv-codec-headers-13.1.15.0/include/ffnvcodec/nvEncodeAPI.h bundle/licenses/NVIDIA-header-notice.h
  cp amf-headers-v1.5.3/AMF/core/Version.h bundle/licenses/AMF-header-notice.h
  cp libvpl-2.17.0/LICENSE bundle/licenses/libvpl-LICENSE.txt
  cp libvpl-2.17.0/third-party-programs.txt bundle/licenses/libvpl-third-party-programs.txt
fi

cd x264-b35605ace3ddf7c1a5d67a2eb553f034aef41d55
./configure --prefix="$media_build/prefix" --enable-static --enable-pic --disable-cli --disable-opencl --bit-depth=8 --extra-cflags="$media_cflags" --extra-ldflags="$media_ldflags"
make -j 3
make install-lib-static
cd ../ffmpeg-8.1.3
PKG_CONFIG_PATH="$media_build/prefix/lib/pkgconfig" ./configure \
  --prefix="$media_build/prefix" --disable-autodetect --enable-gpl --enable-libx264 \
  --disable-shared --enable-static --disable-network --disable-ffplay --disable-doc \
  --disable-debug --extra-cflags="$media_cflags" \
  --extra-ldflags="$media_ldflags" --pkg-config-flags=--static "${media_platform[@]}"
make -j 3 ffmpeg"$media_suffix" ffprobe"$media_suffix"
cp ffmpeg"$media_suffix" ffprobe"$media_suffix" "$media_build/bundle/ffmpeg/"
cp COPYING.GPLv2 "$media_build/bundle/licenses/FFmpeg-GPLv2.txt"
cp config.h config_components.h ffbuild/config.mak "$media_build/bundle/source/"
cd "$media_build"
cp x264-b35605ace3ddf7c1a5d67a2eb553f034aef41d55/COPYING bundle/licenses/x264-GPLv2.txt
cp x264-b35605ace3ddf7c1a5d67a2eb553f034aef41d55/config.mak bundle/source/x264-config.mak
cp sources/ffmpeg.tar.xz sources/ffmpeg.tar.xz.asc sources/ffmpeg-devel.asc sources/x264.tar.bz2 bundle/source/
cp "$media_recipe" bundle/source/build-ffmpeg.sh
bundle/ffmpeg/ffmpeg"$media_suffix" -version
bundle/ffmpeg/ffprobe"$media_suffix" -version
