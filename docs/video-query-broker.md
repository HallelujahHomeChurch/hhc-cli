# Query identity and credential broker contract

This is the interface for a **deployment-supplied** broker. The CLI repository
provides a fail-closed client and fixtures; it does not yet provide the real broker,
Google consent UI, HHC identity bridge, secret store or distributed refresh DB.
Choose/review that implementation together with the hosting target before deploy.

The broker is trusted code on an isolated Unix socket with filesystem permissions
restricted to its query process. Use a shared volume within each instance; never
expose the socket protocol as a public HTTP endpoint. Query pods cannot reach
another pod's socket. The broker must itself authenticate to shared storage with
the approved workload identity; mounting the socket grants security authority.

Requests are JSON POSTs to these paths. Responses must be HTTP 200 JSON within
16 KiB, with the exact fields below. Unknown fields fail closed. 401/403/429 and
service failures return fixed safe failures; bodies are never relayed to callers.

`/v1/ready`, body `{}`, response `{"ready":true}`. True means the broker's issuer
validation, caller policy, secret access and shared refresh/quota coordinator are
ready. It must not mean that a static credential happens to be loaded.

`/v1/authorize`, header `Authorization: Bearer <incoming caller token>`, body:

```json
{"audience":"hhc-query","tool":"search_videos"}
```

Response (example is synthetic, not a real binding):

```json
{
  "callerId":"fixture-caller",
  "bindingId":"opaque-short-lived-policy-binding",
  "hhc":{"enabled":true,"access":"public"},
  "youtube":{"enabled":true,"access":"public","channelId":"UCabcdefghijklmnopqrstuv"}
}
```

Validate signature, trusted issuer, intended resource/audience, expiry, revocation
and exact tool permission. Do not trust caller-supplied identity headers. `tool`
can also be `initialize`, `ping`, `tools/list`, `notifications/initialized`,
`notifications/cancelled`, or `unsupported` (deny unknown operations).
HHC access is `public` or `human`; YouTube access is `public` or `owner`.
Never broaden a disabled source. An opaque binding is bound to the verified
caller, specific source/resource, scope, policy version, tool and short expiry;
it must not be reusable after revocation. The client never accepts bindings from
MCP arguments or forwards an incoming token to HHC/Google.

`/v1/credential`, body:

```json
{"callerId":"fixture-caller","bindingId":"opaque-short-lived-policy-binding","source":"youtube","resource":"UCabcdefghijklmnopqrstuv"}
```

Response:

```json
{"callerId":"fixture-caller","bindingId":"opaque-short-lived-policy-binding","source":"youtube","resource":"UCabcdefghijklmnopqrstuv","kind":"api_key","scope":"","token":"synthetic-fixture-only","expiresAt":"2099-01-01T00:00:00Z"}
```

Never emit this response to logs/chat. Match all caller/binding/source/resource
fields; token expiry must allow at least 40 seconds when returned. HHC resource
is always `content:videos`, kind `human`, scope `cms:pages:read` (optional
`offline_access`). A service principal is never substituted. YouTube public uses
`api_key` with API/key restrictions; owner uses `oauth` with exactly
`https://www.googleapis.com/auth/youtube.readonly` and the explicitly approved
channel. No shared catch-all owner credential or broad write scope is permitted.

`/v1/permit`, body:

```json
{"callerId":"fixture-caller","bindingId":"opaque-short-lived-policy-binding","source":"youtube","resource":"UCabcdefghijklmnopqrstuv","bucket":"youtube.search","cost":1}
```

Response: `{"allowed":true}`. Atomically check the binding, revocation, resource,
scope and per-caller/project budgets **before each upstream attempt**, including
retries. Keep HHC reads, YouTube Search Queries and other YouTube quota buckets
separate. `bucket=authorization,cost=0` is a final authorization/revocation check;
it must not decrement quota, and a failed recheck discards that source's metadata.
Revocation cannot undo an already returned response; it fences subsequent reads
and responses still being evaluated.

Refresh access centrally, using a durable lock/CAS generation fence keyed by
issuer + caller/owner + source + resource + scopes + grant ID. Independent query
replicas must not consume the same rotating refresh token. Persist rotation intent
before exchange and replacement atomically before releasing a token; uncertain
exchange outcomes require reauthorization instead of replaying a consumed token.
Check revocation again after refresh. Do not implement plaintext fallback,
replica-local refresh locks, or persistent access through a personal computer.
Public project keys can be shared only under an explicit project/channel policy
and centrally coordinated quota; OAuth grants/private metadata remain caller-bound.

The fixture concurrency test demonstrates the client contract using a synthetic
shared coordinator. It does **not** verify a production distributed lock, issuer,
revocation system, secret manager, actual token rotation, or Google/HHC consent.
Those need integration and multi-replica failure tests after the target is chosen.
