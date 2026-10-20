# HHC CLI

- Work in an isolated worktree on a task branch from origin/main; never commit to main.
- Preserve unrelated changes. PR checks must pass before merge; release only merged immutable artifacts.
- Use idiomatic Go and the standard library first. Keep wire contracts aligned with Account, Gateway, CMS and Asset owners.
- Original recordings are read-only. Only operation-owned generated files may be cleaned; never delete user output or source files.
- Never persist or print credentials, bearer tokens or presigned URLs in journals/logs. OS credential storage must fail closed.
- This repository is public. No private infrastructure configuration, source recordings or secrets belong here.
- Run `go test -race ./... -count=1`, `go vet ./...` and both Windows amd64/macOS arm64 compilation checks.
- Cross-compilation is not native media, credential, GPU or update acceptance. Record those verification states separately.
- Production infrastructure/provider changes require a reviewed preview and explicit approval.

## Architecture and local verification

- Native Go command-line client, not a web service. `cmd/hhc` is the command entry; launcher/release commands are separate packaging/updater tools. Command domains currently cover account access, recordings and installation/updates.
- `internal/cli` owns command/output contracts, `internal/api` transports, `internal/auth` OS credentials, `internal/media` bounded probe/encode, `internal/recordings` remote workflow, `internal/operation` resumable journals and `internal/update` managed updates. Follow [docs/command-architecture.md](docs/command-architecture.md) before adding a domain.
- Read [README.md](README.md), [go.mod](go.mod) and [.github/workflows/ci.yml](.github/workflows/ci.yml); build with `go build ./cmd/hhc`. Local help/build needs no running server; authenticated workflows depend on Account/Gateway/CMS/Asset contracts and must not be invoked as a smoke test without authorization.
- Run `go test -race ./... -count=1`, `go vet ./...`, and the Windows amd64/macOS arm64 compile checks in CI. Media fixtures need ffmpeg/ffprobe and the CI's required-media settings. Native credential/media tests run on supported Windows/macOS runners; Linux or cross-compilation cannot establish device/GPU/update acceptance.
- Preserve original recordings, bounded subprocesses, operation-owned cleanup, wire/journal compatibility and credential redaction. Upload, publish, update installation and production activation are distinct actions; this repo is outside the initial Cloud checkout.

The commands above are source-defined entrypoints, not a record of successful execution. Report installation, local tests, Linux startup, CI and live acceptance separately.

## Multi-repository Codex tasks

- First read the checked-out `hhc-planning/AGENTS.md` and
  `hhc-planning/docs/cloud-environments.md` at the planning revision recorded in
  the task. Sibling repositories do not inherit those instructions automatically.
- This repository owns its setup and verification: read its README, runtime
  manifests and CI before choosing commands. Report missing dependencies or
  unverified Linux startup instead of claiming a successful run.
- Preserve unrelated edits and active writer ownership. Use an isolated task
  branch and follow the task's explicit review, merge and deployment authorization.
- Pure human documentation uses lightweight validation. Source, workflow, runtime
  configuration, executable skills and mixed changes retain full verification.
  Record read-only inspection, test execution, CI and live acceptance separately.
