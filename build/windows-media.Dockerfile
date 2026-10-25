# Fixed base; installed package versions are recorded in the cache fingerprint
# and corresponding-source bundle. Repository updates deliberately miss cache.
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    gcc libc6-dev gcc-mingw-w64-x86-64-win32 g++-mingw-w64-x86-64-win32 \
    binutils-mingw-w64-x86-64 nasm pkg-config cmake ninja-build make \
    curl ca-certificates gnupg patch bzip2 xz-utils unzip \
    && rm -rf /var/lib/apt/lists/*
ENV MEDIA_TARGET=windows-amd64
