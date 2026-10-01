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

The `bundle` output includes binaries, license texts, exact upstream source
archives and the build recipe/configuration. Distribute these together, not
binary-only files. GPL notices do not establish patent clearance; no patent
license or jurisdiction-specific legal guarantee is asserted.

The Go CLI invokes FFmpeg as a separate process; it does not link the media
libraries. Media build CI must still verify both native platforms, inspect
external dependencies, run actual media/CLI tests and embed binary hashes
before a bundle is eligible for a separately signed application release.
Hardware encoders are not qualified merely because a build includes them.

Windows pins NVIDIA headers13.1.15.0, AMD AMF headers1.5.3 and Intel libvpl2.17.0,
each verified by SHA-256. The headers and static dispatcher enable the three
candidate backends; GPU drivers themselves are not bundled. Corresponding
sources/notices accompany the bundle. Older or absent drivers fail the actual
probe and use CPU; neither encoder-list detection nor a hosted CI build proves
hardware availability. See the [AMF build instructions](https://github.com/GPUOpen-LibrariesAndSDKs/AMF/wiki/Build-FFmpeg-with-AMF-Support),
[Intel libvpl source](https://github.com/intel/libvpl), and
[NVIDIA codec headers](https://github.com/FFmpeg/nv-codec-headers).

Primary references: [FFmpeg download/signatures](https://ffmpeg.org/download.html),
[FFmpeg license](https://ffmpeg.org/legal.html),
[x264 source](https://code.videolan.org/videolan/x264).
