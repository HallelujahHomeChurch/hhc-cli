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
