# Video queries and limited YouTube metadata editing

The `hhc videos` domain shares bounded Go query logic across thin CLI commands,
separate HHC/YouTube providers and a bounded read-only query transport. It does not start
a server, database, media tool or recording cleanup. Existing recording command
paths, profiles, journals and JSON schema v1 remain compatible.

## Commands and results

```text
hhc videos search --query 聚會 --source hhc --limit 10 --max-pages 3 --json --no-input
hhc videos get youtube:abcdefghijk --credentials-stdin --json --no-input
hhc videos compare abcdefghijk --credentials-stdin --json --no-input
```

The last two examples require an authorized private pipe as stdin; never put
credentials in a command line, shell literal, chat message or file redirection.
`--source` accepts `all`, `hhc`, `youtube`; each source returns at most 100 items
and scans at most 10 pages. The default is 20 items/3 pages. `--timeout` is at
most 30s, including credential input; each source has a 20s maximum.

With `--json`, stdout is the existing schema-v1 envelope. `ok=true` means the
query was evaluated; inspect `data.incomplete` and every source's `complete`,
`coverage` and `error`. A source failure can preserve other source results.
Missing credentials are not evidence that private content does not exist.
Authentication errors require renewed authorization, not a public-only retry.

IDs are source-qualified (`hhc:ID`, `youtube:VIDEO_ID`). Comparison joins only
exact `youtubeVideoId`, never title similarity. Every item includes provenance
and observed time. `published` means a current public HHC observation;
`not_published` requires an authorized human content observation.
`not_observed` is a bounded observation, not proof of deletion/draft/absence;
`unknown` covers failed/incomplete/ambiguous observations. Search is not a full
channel inventory. Private YouTube search uses authorized `forMine=true`; the
provider checks every result against the explicitly approved channel.

For browserless Google login with URL/code and a useful process-only session,
see [device sign-in](google-oauth.md). It needs no callback or persistent token
store for that session. For explicit YouTube title/description/tag changes, see
[metadata editing](youtube-metadata.md). For browser consent and secure cloud
grant ownership, see [Google OAuth](google-oauth.md).

## Private credentials

`--credentials-stdin` consumes one strict JSON object, at most 16 KiB, from an
actual private pipe owned by the invoking executor. Regular files, terminals,
unknown/duplicate fields, null descriptors, expired credentials and trailing
JSON are rejected. Omit an unused source descriptor. These are field names,
not a credential file template to save:

| Object | Required fields |
| --- | --- |
| `youtube` | `accessToken`, `scope`, `channelId`, `expiresAt` |
| `hhc` | `accessToken`, `scope`, `principalType`, `expiresAt` |

`expiresAt` is RFC3339 and must be in the future within 24 hours. YouTube scope
is exactly `https://www.googleapis.com/auth/youtube.readonly` or the explicitly
consented `https://www.googleapis.com/auth/youtube.force-ssl` or
`https://www.googleapis.com/auth/youtube`; channelId must
be the approved `UC...` channel. HHC requires `principalType=human` and
`cms:pages:read` (optionally `offline_access`), not a recording-service token.
The descriptor is a trusted executor assertion; Google/HHC remain authoritative
for actual token validity, ownership, audience, permission and revocation.
The CLI rechecks descriptor expiry before each upstream attempt. It does not
verify human identity by trusting a user-supplied label or bypass server policy.

Credentials are process-local, source-specific, never included in result JSON,
logs or journals. Native recordings profiles and personal keychains are not
opened. Private HHC requests use the existing supported human-only content
route; website publication comparison still checks the public catalog first.
No new Account scopes, routes, clients or infrastructure are provisioned.

For optional public YouTube queries, an approved executor may inject
`HHC_YOUTUBE_CHANNEL_ID` and `HHC_YOUTUBE_API_KEY`; those variables do not grant
private access. A private YouTube descriptor overrides that public configuration
without falling back after OAuth denial.

`internal/videoauth` also retains a reusable Google refresh adapter.
It exchanges only with Google's fixed HTTPS token endpoint, refuses redirects,
never retries ambiguous refreshes, and returns any rotated credential to its
owner. Standalone `hhc videos` does not refresh injected credentials. The device
process-only session serializes refresh, replaces tokens only in memory and
fences uncertain exchanges. A separately approved durable owner would also
need secure persistence/replacement and recovery without old-token replay. This repository does
not add a persistent store, OAuth client registration or consent grant.

## On-demand agent execution

Use the repository [HHC skill](../skills/hhc/SKILL.md) and its query reference.
The skill supplies instructions and argv examples; an existing authorized
executor must separately run the CLI, inject short-lived credentials through
a private child-process pipe, and protect private results. Do not build a
shell command containing credentials. A private pipe transports credentials;
it does not acquire or persist them.

A cloud executor can run independently of a personal Mac when it is actually
available and callable from the current conversation. An ephemeral workspace,
passing unit test or successful public request establishes neither durable
credentials nor future availability. Device sign-in can directly authorize private access for that process after
approved client setup and consent. Cross-task grant reuse still needs a supported
durable owner; it is not established by a workspace or an injected access token.
Linux native recording credential storage still fails closed; never add a
plaintext fallback. No new always-on compute is required by these CLI commands.
Existing executor usage, API quota and platform charges depend on that platform.

## Reliability and validation

The transport permits bounded GETs to composed provider origins, refuses
redirects, limits bodies to 2 MiB and hides raw upstream errors/URLs. It retries
429/502/503/504 at most once with a bounded Retry-After. It does not retry
401/403, malformed responses or refresh exchanges. Explicit stale responses
are rejected; there is no metadata/private-result cache. Cancellation reaches
HTTP requests. Pagination, source failures and channel isolation use fixtures.
Google's project quota remains authoritative; per-invocation bounds are not a
global counter across executors. No background monitor or load test is started.

Report source tests, CI, installed release, credential provisioning and real
iPhone/Mac-offline private acceptance separately. Fixtures use synthetic
credentials and do not establish live consent or deployed capability.
