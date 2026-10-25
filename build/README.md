# Media bundle build

Run `bash build/ffmpeg.sh /absolute/new/build-directory` on macOS arm64 or
Windows x64 MSYS2/MinGW64 with a C toolchain, make, pkg-config, nasm, curl,
tar and GnuPG. The destination must not exist; failed builds remain inspectable.
Windows also requires C++, CMake and Ninja for the static Intel dispatcher.

The recipe pins FFmpeg 8.1.3 and x264 stable commit
`b35605ace3ddf7c1a5d67a2eb553f034aef41d55`, verifies archive SHA-256 and the
FFmpeg release signature/fingerprint, and disables automatic external-library
detection and network input. x264 is statically linked into the separate
FFmpeg programs. `--enable-nonfree` is never used. macOS may link Apple system
frameworks; Windows must not depend on a separately installed MinGW runtime.

The primary x264 archive and official `?inline=false` variant remain preferred.
If both are unavailable or invalid, the recipe downloads the fixed v1.0.6 macOS
release bundle, verifies its pinned outer SHA-256, and extracts only
`source/x264.tar.bz2` to stdout. The unchanged source SHA-256 still gates
extraction/build. No prior executables run, source/version changes, challenge
solving or unverified mirror is allowed; failed verification stops the build.

The `bundle` output includes binaries, license texts, exact upstream source
archives and the build recipe/configuration. Distribute these together, not
binary-only files. GPL notices do not establish patent clearance; no patent
license or jurisdiction-specific legal guarantee is asserted.

The Go CLI invokes FFmpeg as a separate process; it does not link the media
libraries. Media build CI must still verify both native platforms, inspect
external dependencies, run actual media/CLI tests and embed binary hashes
before a bundle is eligible for a separately signed application release.
Hardware encoders are not qualified merely because a build includes them.

Windows pins NVIDIA headers13.1.15.0, the original AMD AMF archive and Intel libvpl2.17.0,
each verified by SHA-256. The headers and static dispatcher enable the three
candidate backends; GPU drivers themselves are not bundled. Corresponding
sources/notices accompany the bundle. Older or absent drivers fail the actual
probe and use CPU; neither encoder-list detection nor a hosted CI build proves
hardware availability. See the [AMF build instructions](https://github.com/GPUOpen-LibrariesAndSDKs/AMF/wiki/Build-FFmpeg-with-AMF-Support),
[Intel libvpl source](https://github.com/intel/libvpl), and
[NVIDIA codec headers](https://github.com/FFmpeg/nv-codec-headers).
AMF replaced its `v1.5.3` release archive on 2026-10-16. We retain the original
archive shipped in CLI v1.1.4 (whose Version.h reports 1.5.2), verifying both the
fixed release ZIP and the inner source digest. Only the source member is read;
no old executable is extracted or run. Updating these headers is a separate
media compatibility change.
The included one-line [libvpl MinGW compatibility patch](https://github.com/intel/libvpl/pull/198)
restricts legacy MSVC macros to MSVC; MinGW retains its bounded CRT functions.
The patch accompanies the corresponding source and build recipe.

Primary references: [FFmpeg download/signatures](https://ffmpeg.org/download.html),
[FFmpeg license](https://ffmpeg.org/legal.html),
[x264 source](https://code.videolan.org/videolan/x264).

## CI reuse

Native media bundles are cached by the complete recipe/patch/workflow/helper
identity, OS/architecture, runner image and installed compiler/build-tool versions.
There are no prefix restore keys. Before executing a restored tool, CI verifies
its input key and SHA-256 inventory, including corresponding sources and licenses;
missing, additional, changed or linked files fail the job. This detects corruption;
cache authenticity relies on GitHub's branch/ref isolation, not on the manifest
being a signature. PR caches cannot populate the default-branch or release scope.

Changes under `build/` or to the media workflow warm the default-branch cache
once merged. This adds native runner work on those main pushes; it never publishes
a release. Tags can reuse matching main-cache tools, but still build the current
CLI/version and run native dependency, media and archive verification. Native
checks also run on cache hits. Installed toolchain or runner-image changes cause
a full rebuild; cache eviction and cold builds remain possible. Compilation uses
available CPUs capped at four without changing enabled codecs or libraries.

PR workflows cancel superseded runs. Tag release and main cache-warming runs are
not cancelled. Measure all workflows/attempts for a SHA and runner time by platform;
a warm cache comparison does not establish the cold-build latency or dollar cost.
See [GitHub cache isolation](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching#restrictions-for-accessing-a-cache).
