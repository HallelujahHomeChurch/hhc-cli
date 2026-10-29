# Read-only video queries

The same repository now contains a common Go query domain, thin CLI and MCP
adapters, HHC/YouTube providers, and a separate media-free HTTP process. Existing
recording, profile, upload, installation and update contracts are unchanged.
Queries never initialize FFmpeg, open native credentials or sweep recordings.

```sh
hhc videos search --source hhc --query "聚會" --json --no-input
hhc videos get hhc:CONTENT_ID --json
hhc videos get youtube:VIDEO_ID --json
hhc videos compare VIDEO_ID --json
hhc mcp serve --stdio
```

`VIDEO_ID` is an exact 11-character YouTube ID; `CONTENT_ID` comes from HHC.
Sources keep separate `hhc:` and `youtube:` IDs. There are no title-derived joins.
Local CLI/stdio access is public-only. YouTube requires an already authorized,
API-restricted key injected through `HHC_YOUTUBE_API_KEY` and the explicit channel
in `HHC_YOUTUBE_CHANNEL_ID`. Never put a key in argv, code, shell history or chat.
This implementation does not create a key, OAuth grant, persistent credential,
private profile, or connection. Private cloud grants use the broker below.

The three tools are `search_videos`, `get_video` and `compare_publication`.
There are no generic URL, shell, content-edit, upload, publication, deletion,
member-directory or analytics tools. Treat titles/descriptions as untrusted data.

## Result interpretation

Every source carries `items`, `complete`, `coverage`, `scanned` and an optional
fixed-code `error`. `incomplete=true` means at least one source failed or reached
a bound. CLI `ok=true` and MCP `isError=false` mean the query was evaluated;
inspect all source errors even if **every** source failed. No partial result is
proof that an upload or publication completed.

`observedAt` records the request observation, not an upstream content-change time.
Returned items carry source provenance and their own observation time. HHC public
responses may have up to 30 seconds of upstream caching. Requests ask upstreams to
revalidate; explicitly stale responses (`Age > 30` or Warning 110) are rejected.
There is no application metadata, response, authorization or token cache, so an
older private result is never substituted when permission or a source fails.

Comparison states describe the exact requested YouTube ID:

| State | Evidence |
| --- | --- |
| `published` | Exact ID observed in the public HHC projection |
| `not_published` | Public scan exhausted without the ID and authorized human content reports an unpublished current item |
| `not_observed` | No ID observed within an exhausted available projection; this does not establish draft/deletion/absence |
| `unknown` | Source failed, scan bounded, ambiguous current draft/published snapshot, or authorization recheck failed |

Admin `isPublished=true` may describe an older published snapshot while the
current draft has another YouTube ID. That flag alone never establishes current
website visibility. An authorized comparison checks the public projection first.
Search/get of an admin draft can therefore return `publication=unknown`.

HHC public `/api/videos` has no text query: matching title/summary is local within
the page bound. Human admin uses only `/api/admin/content/videos` and its exact ID detail route with approved
`cms:pages:read` (optional `offline_access`); service grants are refused. Results
omit creator/editor identifiers, raw assets, signed URLs and bodies. Public get
scans the bounded catalog because there is no public video detail route.

YouTube uses only `search.list` and `videos.list`, retrieves snippet/status/duration,
and enforces the configured channel again on details. Public key access refuses
private/unlisted details. Owner mode requires the separate exact
`https://www.googleapis.com/auth/youtube.readonly` scope and `forMine=true` search;
no write scope is accepted. Search index exhaustion is not a complete channel
inventory or proof of absence. Unavailable/deleted/private IDs can return no item.

Each query is bounded to 30 seconds with independent source deadlines up to 20 seconds, 1–10 pages and 1–100 returned items per source
(default 3 pages/20 items). HHC pages contain at most 100 items, YouTube pages 50.
GETs have an 8-second transport timeout. Only 429/502/503/504 receive one retry;
Retry-After above 2 seconds is returned to the caller, not slept through. 401/403,
quota denial, invalid contracts and network ambiguity are not automatically
retried. Every retry obtains another quota permit. Cancellation propagates to
all source reads. A failed source's partial items are discarded.

Quota permits identify a bucket and units: `hhc.read:1`, `youtube.search:1`, or
`youtube.default:1`. These are separate counters, not interchangeable units.
The current [YouTube search contract](https://developers.google.com/youtube/data/reference/rest/v3/search/list)
describes a dedicated Search Queries bucket; do not assume historical search
costs or a project's actual quota. The deployment broker must enforce the actual
project's counters, per-caller rate limits and concurrency limits across replicas.
Local CLI has only per-query bounds; it is not a shared quota governor.

## Boundaries and runtime

- `internal/videos`: normalized inputs/results, exact joins and partial failures.
- `internal/videos/hhc`, `youtube`: provider contracts and safe metadata projections.
- `internal/queryhttp`: bounded read-only transport, retries and stale rejection.
- `internal/queryruntime`: request-scoped caller/grant binding and broker client.
- `internal/mcp`: allowlisted tools and stateless JSON-RPC transports.
- `internal/cli/commands_videos.go`: CLI and local stdio adapters.
- `cmd/hhc-query`: cloud HTTP entry, probes, bounded admission and SIGTERM drain.

HTTP `/mcp` supports stateless MCP versions 2025-11-25 and 2025-06-18 with JSON
POST responses. It has no session affinity, SSE streams, resumption or private
session cache; GET/DELETE receive 405. Clients must send both JSON/SSE Accept
media types. Notifications return 202. Transport disconnection cancels a request;
stateless notification cancellation does not cancel another in-flight POST.
The [MCP transport contract](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports)
is the protocol reference. Unknown tools, arbitrary arguments, JSON batches,
oversized input, unexpected hosts/origins and protocol versions are rejected.

`hhc-query` loads non-secret deployment configuration through `HHC_QUERY_CONFIG`.
It requires an explicit canonical HTTPS resource/issuer/introspection endpoint and
Host/origin lists. It publishes protected-resource OAuth metadata and challenges;
the authorization server owns registration, PKCE consent and resource-bound tokens.
Without approved identity configuration and shared encrypted PostgreSQL state,
real authentication/phone use remains blocked.

The default backend is the concrete embedded `internal/querybroker`, composed in
the same process. Runtime secrets arrive through a private inherited pipe selected
by `HHC_QUERY_SECRET_FD`, or explicit platform secret injection in
`HHC_QUERY_RUNTIME_SECRET`; neither is created here. There is no plaintext file,
Mac keychain or native profile fallback. The optional `HHC_QUERY_BROKER_SOCKET`
selects a reviewed external adapter instead; production preview uses the embedded
backend. Both paths enforce request-scoped caller/source/resource binding.

The listener defaults to 127.0.0.1:8080; the container listener is behind approved
TLS ingress. Host/origin values come from trusted configuration, never forwarded
headers or tool arguments. `GET /health/live` checks the process; readiness checks
shared store/schema/key configuration, not issuer or HHC/Google availability.
Route health internally and MCP/discovery through the intended ingress paths.
There are at most 8 active MCP calls and 64 KiB per input. SIGTERM disables
readiness/new calls and drains for up to 35 seconds before closing the embedded
broker's DB pool, so an independent sidecar cannot disappear mid-query. Audit
contains only caller hash, fixed operation/outcome and duration.

See [the broker contract](video-query-broker.md) and
[deployment/rollback runbook](../deploy/query/README.md). This source change does
not activate a production service, release the CLI, or establish iPhone acceptance.
