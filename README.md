# HHC CLI

Recording preparation and authenticated publishing for HHC.

Implementation is in progress. This producer checkpoint contains the versioned
HLS inventory/digest contract, independent source/package budgets and source
rendition planning, bounded ffprobe metadata parsing and the fixed CPU encode
arguments, measured-bandwidth master playlist builder and bounded streaming
package inventory assembly, with actual 65-second dual-rendition tests.
Local probe/encode processes use bounded output and native process-tree
cancellation (Windows Job Objects and macOS process groups), not shell/PATH
execution. The actual fixture probes its source through this runner before
planning and uses it for both encoded renditions.
Source handles deny concurrent writes/deletion on Windows; macOS uses a native
copy-on-write snapshot and refuses unsupported filesystems rather than copying
the entire source. Native source checks run on both supported OS families;
they are not FFmpeg bundle or GPU acceptance.
The service-token transport uses Account HTTPS/client-secret-basic, bounded
responses and exact recording scopes, refuses redirects/refresh/cookies, and
redacts bearer output. The human transport uses PKCE S256, a bounded one-shot
loopback callback and a system-browser launcher; `NoInput` never starts it.
Human renewal makes one noninteractive refresh request, requires the current
issued scope, rejects scope escalation and returns the rotated credential even
when only `offline_access` remains. Profiles hold an OS lock and save a rotation
fence before exchange, then save the new credential before returning its token.
A crash or lost response requires login instead of replaying a consumed token.
Human revocation uses the existing Account native revoke endpoint once, with
no redirect, browser or credential echo. Profile logout clears local credentials
even when remote revocation cannot be confirmed and reports that failure.
Credentials are one atomic native item per lowercase profile, held in Windows
Credential Manager or macOS Keychain, with no plaintext fallback or GUI prompt.
macOS requires a cgo-enabled native build linked to Apple's frameworks. Native
store tests use disposable fixture entries; real agent logon contexts and
credential access across signed application updates still need acceptance.
CLI commands are not yet integrated; this is not a usable unattended uploader.
Inventory assembly rejects missing/extra files, symlinks and size violations;
it does not establish encoded-media readiness. It does not yet ship an
executable, verified FFmpeg bundle or complete preparation/upload workflow.

The approved target is Windows amd64 and macOS arm64, bundled FFmpeg, local HLS
preparation, human/service login, resumable direct upload, explicit publication
and signed manual updates. Original recordings are never deleted by the CLI.

## Verification

```sh
go test -race ./... -count=1
go vet ./...
```

Actual media tests use explicitly supplied absolute test-tool paths:

```sh
HHC_TEST_FFMPEG=/absolute/path/to/ffmpeg HHC_TEST_FFPROBE=/absolute/path/to/ffprobe HHC_REQUIRE_MEDIA_TESTS=1 go test -race ./... -count=1
```

Those tools are fixtures, not the production bundle. CI requires actual media
tests; cross-compilation does not run them on the target OS.

The Asset producer golden digest is pinned in tests. Cross-compilation checks
are not native Windows/macOS media, credential or update acceptance.
