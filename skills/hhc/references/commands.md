# CLI contract (JSON schema v1)

These are argv arrays, not shell commands. Replace example values as individual
arguments; `$(whoami)` in a filename stays literal. Generate a new UUID only for
a new user intent; the UUID below is illustrative, not a shared default.

## Preflight

```json
["hhc", "version", "--json"]
```

```json
["hhc", "auth", "status", "--profile", "church-uploader", "--json", "--no-input"]
```

Status may perform one noninteractive token renewal. It never returns credentials.
Provisioning/login is separate: human `hhc auth login --profile NAME` opens a
browser only interactively. Service login uses `--service-principal --client-id`
with hidden terminal input or an explicitly provisioned private `--secret-stdin`
pipe; credentials do not belong in agent prompts, argv, or journal files.

## Prepare, upload, and optionally publish

```json
["hhc", "recordings", "upload", "C:\\錄影\\主日 $(whoami).mp4", "--prepare", "--title", "主日聚會", "--profile", "church-uploader", "--operation-id", "11111111-1111-4111-8111-111111111111", "--publish", "--json", "--no-input"]
```

Omit `--publish` for upload-only. macOS uses an absolute POSIX source path.
The default deadline is four hours for this invocation; it is not a completion
guarantee. The CLI owns generated HLS cleanup after server readiness. Source
limit is 50 GB; total HLS limit is 10 GB. Source files remain unchanged.

```json
["hhc", "recordings", "resume", "11111111-1111-4111-8111-111111111111", "--profile", "church-uploader", "--json", "--no-input"]
```

Resume uses the saved publish intent. Do not add `--publish` or a new title.
Keep the same Windows/macOS user and operation storage location. A timeout or
lost response does not authorize a new upload or deletion of the original.

## Inspect or publish an existing ready draft

```json
["hhc", "recordings", "get", "22222222-2222-4222-8222-222222222222", "--profile", "church-uploader", "--json", "--no-input"]
```

```json
["hhc", "recordings", "publish", "22222222-2222-4222-8222-222222222222", "--profile", "church-uploader", "--operation-id", "33333333-3333-4333-8333-333333333333", "--json", "--no-input"]
```

Standalone publish requires publication authorization. A later request to publish
an upload-only recording is a new publish intent, not a changed upload journal.

## Results and recovery

Stdout with `--json` is one object: `schemaVersion`, `ok`, `command`, `profile`,
`principal`, `data`, `error`, and when applicable `operationId`.
Unknown schema or malformed output means unconfirmed; never parse stderr as JSON.

| Observation | Action |
| --- | --- |
| Upload exit 0, `ok`, `data.requestedActionSatisfied`, `data.validationState=ready` | Upload verified; publication is a separate field. |
| Above plus `data.publicationState=published` | Requested upload and publication verified. |
| Standalone publish exit 0, `ok`, `data.requestedActionSatisfied`, `data.publication.outcome=published` | Publication verified. |
| `data.localCleanupState=complete` and `data.cleanupBytesRemaining=0` | Managed upload scratch removed; source retained. |
| `error.code=local_cleanup_failed` | Resume same operation; do not manually remove directories. |
| Exit 3 / authentication or credential store unavailable | Stop for explicit login/store access; no credential discovery. |
| Exit 4 / permission denied | Stop; no account switching, extra grants, or retry loop. |
| Conflict, session expired, package failed, source changed | Inspect/report; no automatic new operation. |
| Recording timeout/lost response | One same-operation resume reconciles remote state; no prior proof of remote completion is needed. |
| Recording operation busy | After the owning command finishes, one same-operation resume; never kill another job. |
| `state_changed` / historical publish receipt | Do not republish automatically; current state may reflect an administrator's action. |

Standalone prepare has `prepareState`/`outputPath`, not remote readiness. Keep its
explicit output. Exit 0 without the command-specific success fields is not enough
to claim requested work complete. A missing or null cleanup byte count is unknown,
not zero. Report only safe identifiers and selected state, not entire remote data.

Exit 0 paired with `validating` or `requestedActionSatisfied=false` contradicts
this command contract: report an unconfirmed result and reconcile once using
the same operation's resume. Do not convert it into success.

The development CLI does not yet expose `update`. Do not issue it from these
examples. Once a matching released skill documents the updater, a busy updater
must wait for active work and retry its own command, not `recordings resume`.
