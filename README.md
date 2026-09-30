# HHC CLI

Recording preparation and authenticated publishing for HHC.

Implementation is in progress. This producer checkpoint contains the versioned
HLS inventory/digest contract, independent source/package budgets and source
rendition planning, bounded ffprobe metadata parsing and the fixed CPU encode
arguments, measured-bandwidth master playlist builder and bounded streaming
package inventory assembly, with actual 65-second dual-rendition tests.
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
