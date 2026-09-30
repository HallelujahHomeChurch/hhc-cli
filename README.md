# HHC CLI

Recording preparation and authenticated publishing for HHC.

Implementation is in progress. This producer checkpoint contains the versioned
HLS inventory/digest contract, independent source/package budgets and source
rendition planning; it does not yet ship an executable or media bundle.

The approved target is Windows amd64 and macOS arm64, bundled FFmpeg, local HLS
preparation, human/service login, resumable direct upload, explicit publication
and signed manual updates. Original recordings are never deleted by the CLI.

## Verification

```sh
go test -race ./... -count=1
go vet ./...
```

The Asset producer golden digest is pinned in tests. Cross-compilation checks
are not native Windows/macOS media, credential or update acceptance.
