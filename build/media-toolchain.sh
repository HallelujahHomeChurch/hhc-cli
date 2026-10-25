#!/usr/bin/env bash
set -euo pipefail
# Capture installed tools after setup; runner-image changes intentionally miss.
case "$(uname -s)" in
  Darwin)
    sw_vers -buildVersion
    xcrun --show-sdk-version
    clang --version
    brew list --versions pkgconf nasm gnupg
    ;;
  Linux)
    test "${MEDIA_TARGET:?explicit target required}" = windows-amd64
    uname -sm
    dpkg-query -W
    x86_64-w64-mingw32-gcc -v 2>&1
    x86_64-w64-mingw32-g++ -v 2>&1
    x86_64-w64-mingw32-ld --version
    x86_64-w64-mingw32-windres --version
    gcc --version
    cmake --version
    ;;
  MINGW*|MSYS*)
    gcc --version
    ld --version
    pacman -Q
    ;;
  *) echo 'unsupported native media builder' >&2; exit 2 ;;
esac
make --version
nasm -v
pkg-config --version
gpg --version
