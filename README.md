# HHC CLI

Recording preparation and authenticated publishing for HHC.

Implementation is in progress. This producer checkpoint contains the versioned
HLS inventory/digest contract, independent source/package budgets and source
rendition planning, bounded ffprobe metadata parsing and the fixed CPU encode
arguments, measured-bandwidth master playlist builder and bounded streaming
package inventory assembly, with actual 65-second dual-rendition tests at 2 and
30 fps. Bounded init+fragment probes now measure codecs and verify packet/A/V
continuity, playlist closure and rendition start alignment. Sparse video limits
x264 lookahead and disables B-frames below 20 fps to preserve fragment alignment;
normal 24–30 fps retains medium's 40-frame lookahead and three B-frames.
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
The development executable now supports `version` and `auth login/status/logout`,
including noninteractive service login through `--secret-stdin`, hidden terminal
input, safe JSON results and exit codes. Profiles bind renewal to the issuer's
principal, client and credential IDs. JSON human login never opens a browser.
This is not yet a usable unattended recording uploader.
`recordings get ID` queries the authenticated CMS recording projection, with
one bounded same-principal renewal on 401 and no redirect or response-body echo.
It reports metadata only; a successful query does not establish HLS readiness.
The recording transport verifies each local object's hash and size before one
bounded, credential-isolated presigned PUT. It rejects redirects and unsafe
targets; PUT acceptance is never reported as package readiness. Signed targets
are redacted from formatting and JSON output.
Operation journals now pin intent and observed remote identifiers under native
locks, use platform-specific flushed replacement, and refuse unknown schemas.
Resume must still query the server: local journals are not proof of readiness.
Prepared HLS directories can now use `recordings upload DIRECTORY --title TITLE
--profile NAME --operation-id UUID [--publish]` and `recordings resume UUID
--profile NAME`. Both accept `--json --no-input --timeout 4h`. The journal pins
identity, content digest and publication precondition; server paging determines
missing objects, with at most three isolated PUTs and 100 signed objects per batch.
The command waits for remote ready, preserves user-owned packages and never
interprets 202 as completion. Interrupted operations keep their original key.
`recordings publish ID --profile NAME --operation-id UUID` uses the same durable
publication path. Transient control calls have three attempts with bounded backoff
and Retry-After; ambiguous completion first queries server status. A rejected
upload URL is re-signed only once. Permission and state conflicts never retry.
The CPU preparation pipeline now holds a native stable source, preflights disk
space, monitors generated-byte/free-space budgets, measures actual fragments and
atomically finalizes a new package without overwriting existing output. Local
tests verify source preservation and snapshot/scratch cleanup. Native CI now also
runs actual CPU media fixtures on Windows and macOS; these are not release bundles.
`recordings upload FILE --prepare` now connects the fixed CPU pipeline to the
same upload operation, verifies the embedded tool manifest before execution,
and pins the stable source fingerprint before encoding. Its private generated
tree is removed only after remote ready, including when publication then fails.
Ready resume queries the server before touching source, local media or FFmpeg;
it retains a small result receipt and does not re-encode or re-upload. A failed
cleanup returns nonzero; `cleanupBytesRemaining` is null when unknown and zero
only after successful managed cleanup. Original sources and user packages remain.
Standalone `recordings prepare FILE --output DIRECTORY --operation-id UUID`
requires no login and keeps the explicit output. Its tracked scratch directory
lives on the output volume; finalization never replaces an existing directory.
`recordings resume UUID` also resumes local preparation without a profile,
rehashing a saved output rather than encoding again after a finalization crash.
Recording commands also sweep owned, unlocked temporary workspaces after 24 hours
of inactivity or the saved upload-session expiry. Journals/receipts, active work,
originals and explicit outputs are retained. Cleanup failures are reported on
stderr. `HHC_CLI_OPERATIONS_DIR` may point to an absolute directory on a larger
disk; it changes only operation storage, never native credentials/profile locks.
Tests set it to a private temporary directory so maintenance cannot sweep real
user operations. Keep it unchanged when resuming an operation.
The native command fixture builds a real CLI with embedded fixture-tool hashes,
then exercises prepare and resume without changing its Chinese-path source.
The distributed FFmpeg builds, automatic hardware qualification,
and final signed release integration are still being implemented;
these development commands are not a released end-to-end uploader.
Inventory assembly rejects missing/extra files, symlinks and size violations;
it does not establish encoded-media readiness. It does not yet ship a release,
verified FFmpeg bundle or complete preparation/upload workflow.

## Managed installation and explicit updates (development)

The implementation now supports `hhc install --directory ABSOLUTE_NEW_DIRECTORY`
and `hhc update [--check]`, with `--json --no-input` for automation. Neither needs
an account login. Signed application release artifacts and their trust key are
not provisioned yet; development builds fail closed with
`release_trust_unavailable` instead of downloading unsigned code.

From a verified release, choose a new user-writable directory whose parent exists
(for example `%LOCALAPPDATA%\HHC-CLI` on Windows or
`~/Library/Application Support/HHC-CLI` on macOS, expanded to an absolute path).
Install downloads the signed current stable bundle, verifies it, and creates:

```text
HHC-CLI/
  hhc[.exe]             stable launcher; invoke this entrypoint
  current.json         atomic current/previous version selection
  versions/<version>/  CLI, bundled tools, skill, licenses and sources
  skills/hhc/SKILL.md   stable skill router; use in place
```

Installation never overwrites an existing directory, changes PATH, elevates
privileges, or moves account credentials or operation journals. Configure PATH
explicitly if desired; a portable binary's `update` reports
`managed_install_required`. Agent-owned copies of the versioned skill are not
silently replaced. Keep the stable router in place, or explicitly copy a matching
versioned skill into your agent's skill directory.

Update verifies an Ed25519-signed manifest and SHA-256 before extracting or
executing the new version, checks its tool bundle offline, and switches the
version pointer only after success. Active managed commands make updates return
`operation_busy`. Old bundles remain for safe recovery; this consumes one bundle
per installed version. No background update, forced termination or source cleanup
is performed. Release-manifest signatures are not Windows Authenticode or Apple
Developer ID/notarization; those platform-signing guarantees are not claimed.

The approved target is Windows amd64 and macOS arm64, bundled FFmpeg, local HLS
preparation, human/service login, resumable direct upload, explicit publication
and signed manual updates. Original recordings are never deleted by the CLI.

## Maintainer release workflow

`Release` accepts stable `vMAJOR.MINOR.PATCH` tags only when their commit is
already included in `origin/main`. It reuses the native media-bundle workflow,
runs actual encoding tests, builds both native CLIs and launchers, embeds the
tool hashes and public release key, and round-trips each archive through the
updater's bounded extractor. Bundles include FFmpeg/x264 corresponding source,
build configuration and license notices, Go/dependency notices, and matching
agent skill files. PR artifacts use version `0.0.0` and are not public releases.

Maintainers must provision the repository variable `HHC_RELEASE_PUBLIC_KEY`
(64 lowercase hexadecimal characters) and Actions secret
`HHC_RELEASE_SIGNING_KEY` (base64-encoded Ed25519 32-byte seed) before tagging.
Only the publish job receives the private key. The signer verifies the pair,
signs exact `release.json` bytes and creates `SHA256SUMS`; GitHub publication
never replaces existing assets. Missing keys stop release, without unsigned
fallback. No release tag or signing credentials have been provisioned yet.

The first downloaded CLI must come from the trusted repository release; the
embedded-key update mechanism does not independently establish initial trust.
Windows SmartScreen and macOS Gatekeeper prompts may remain because these are
not Authenticode-signed or Apple-notarized application distributions.

## Verification

Development commands (the new Account producer contract must be released before
real authentication can work):

```sh
go run ./cmd/hhc version --json
go run ./cmd/hhc auth login --profile personal
go run ./cmd/hhc auth login --service-principal --client-id CLIENT_ID --profile uploader
go run ./cmd/hhc auth status --profile uploader --json
go run ./cmd/hhc auth logout --profile uploader --json
```

For explicit provisioning, supply one credential line through a private stdin
pipe and add `--secret-stdin --no-input --json`; never place secrets in arguments,
shell history or agent prompts. Use `--scope` with space-separated recording
scopes for a principal with fewer than read/write/publish grants. Service logout
clears only this local profile, not the organization's credential.

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
