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
