# YouTube title, description and tags

`hhc videos edit` supports exactly these three metadata fields for one explicit
`youtube:VIDEO_ID` in the approved channel. It does not implement upload, deletion,
privacy, scheduling, publication or thumbnail operations. HHC queries remain
read-only. Existing recording upload/publish commands retain their separate
authorization contract.

## Scope and explicit execution

Google's [videos.update contract](https://developers.google.com/youtube/v3/docs/videos/update)
accepts `youtube.force-ssl` (also broader alternatives). This integration accepts
exactly `https://www.googleapis.com/auth/youtube.force-ssl` for metadata edits.
That Google scope can authorize more API actions than this CLI exposes. Explain
the actual Google consent screen and obtain approval for that scope; never call
it a metadata-only Google permission. The CLI's command surface restricts the
implemented actions. Readonly scope is insufficient to edit; no automatic scope
upgrade or new grant occurs.

All examples require an approved credential owner to attach the source-specific
short-lived credential descriptor through a private child-process stdin pipe.
Do not place credentials in shell commands, argv, files or chat.

```text
hhc videos edit youtube:abcdefghijk --title "New title" --dry-run --credentials-stdin --json --no-input
hhc videos edit youtube:abcdefghijk --title "New title" --apply --if-match ETAG_FROM_PREVIEW --plan-hash HASH_FROM_PREVIEW --credentials-stdin --json --no-input
```

The default is dry-run. `--apply` requires both exact preconditions from the
preview. Pass the same video ID and metadata intent. `--description ""` explicitly
clears the description; `--tags-json '[]'` explicitly clears tags. Omitted fields
are preserved. Treat examples as literal argv, not shell interpolation templates.
Title is 1–100 characters; description is at most 5000 UTF-8 bytes; tags follow
the 500-character aggregate limit including separators/space quoting. Google
remains authoritative on additional validation and quota.

## Preservation and concurrency

Updating a YouTube `part` can delete omitted mutable properties. The adapter first
reads the current snippet and approved channel, preserves title, description,
tags, required category and existing default language/audio language, and changes
only the explicitly requested fields. It omits output-only snippet properties.
An unknown snippet property or missing required category stops the edit instead
of risking a destructive replacement. Only `part=snippet` is sent; no other part
is included in the mutation.

The preview includes source-qualified ID, channel, ETag, changed fields and a
SHA-256 plan hash bound to the full candidate snippet and current ETag. Apply
re-reads current state and requires the same ETag and hash, then sends one PUT
with `If-Match`. YouTube documents ETags for preventing concurrent overwrites:
[ETag guidance](https://developers.google.com/youtube/v3/getting-started#etags).
A conflict requires a fresh preview and review of the new difference. Do not
retry with a new ETag merely to force the old edit through.

After a successful PUT response the CLI independently reads the current snippet
and compares all preserved and changed writable fields. Report completion only
when exit 0, `ok=true`, `data.outcome=verified` and
`data.requestedActionSatisfied=true`, or `outcome=unchanged` confirms the exact
desired state already exists. A dry-run's `outcome=planned` is not a write.

## Uncertain outcomes and privacy

PUT is never retried automatically, including 429/5xx, redirects or timeout.
A lost, malformed or ambiguous response and a failed read-back yield
`mutation_uncertain`; a mismatching read-back yields `verification_failed`.
Neither means the write did not happen. Preserve the exact video ID, proposed
values and preconditions privately, and reconcile with read-only API reads. Do
not reapply automatically, invent a new operation or switch to public access.

Dry-run differences contain potentially private titles/descriptions/tags. Keep
them within the authorized conversation and destination, not public issues or
routine logs. No production write, real OAuth grant or private acceptance is
established by the synthetic fixture tests in this repository.
