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

<!-- BEGIN HHC DELIVERY V1 -->
## HHC delivery coordination

Before retained implementation, use `.agents/skills/hhc-delivery/SKILL.md` and read the [pinned shared workflow](https://github.com/HallelujahHomeChurch/hhc-planning/blob/9329942c91cc4d8d93104c666e2752024ee4e319/workflow/v1/WORKFLOW.md).
Reuse/create the owning-repository engineering issue, link its private canonical Feature and Project, verify current head/dependencies, and record coordinator acknowledgement plus run ID/environment/branch/HEAD. Read-only discovery needs no engineering ticket.
Assignees/comments/timeouts are not locks or takeover authority. Separate CI, merge, publication, deployment, flags and live/device acceptance; keep accepted Feature baselines visible and track improvements separately. Closed duplicates/cancellations are not delivery.
Progress authority: [private HHC all-product Project](https://github.com/orgs/HallelujahHomeChurch/projects/1). Read its current item/status and the canonical Feature before implementation; report specific access blockers instead of guessing or using the old PENDING bootstrap. Do not expand OAuth permissions implicitly. External requests require Ray triage. Keep private/member/note content and raw sensitive evidence out of public repositories.
Preserve repository-specific rules. This adoption uses draft PRs and does not authorize engineering merge, deployment, permission changes or new full-suite CI.
<!-- END HHC DELIVERY V1 -->
