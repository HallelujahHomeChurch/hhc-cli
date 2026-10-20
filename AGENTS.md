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
