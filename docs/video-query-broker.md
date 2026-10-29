# Embedded identity and shared grant coordination

The production implementation now lives in `internal/querybroker` and is composed
inside `cmd/hhc-query`. Each query process embeds its broker; there is no required
broker sidecar or separate shutdown dependency. Independent replicas share only
the approved PostgreSQL authority/grant/quota schema and OAuth authority. It does
not depend on a personal computer, OS keychain or recording profile.

The optional existing Unix-socket client remains an explicit external-adapter
mode; it is not used by the embedded deployment preview. Incoming caller bearers
are request-local and are sent only to the selected authorization authority/broker,
never HHC or Google. No metadata/token/authorization cache is shared between callers.

## Caller authority

Configure a canonical HTTPS `/mcp` resource, exact issuer, introspection endpoint,
required query scope and approved resource-client identity. The HTTP process
publishes RFC9728 protected-resource metadata and discovery-aware challenges.
The concrete RFC7662 client requires a fresh `active=true` response, exact issuer,
exact single resource audience, subject, expiry, Bearer type and required scope.
It uses bounded HTTPS/client-secret-basic, no redirects/retries or raw error bodies.
Issuer/introspection outage fails closed. No ID token or another API's access token
is accepted as the MCP resource's token. No caller write scope is requested.

A signed, short-lived binding pins the hashed issuer/subject, policy revision,
operation, caller-token hash and expiry. Each credential acquisition and upstream
permit rechecks issuer activity and shared policy. A known global caller/binding/
policy failure clears **all** sources and becomes HTTP 401/403/503; a source-only
grant/quota failure can preserve other authorized results. No bearer is serialized
in the binding or audit. Binding signatures are scoped to the canonical resource.

HHC Account's inspected current source has **no RFC7662 introspection endpoint**,
uses the `hhc-api` audience and limits native CLI refresh to recording scopes.
This implementation does not invent an Account endpoint, register a client,
broaden scopes or reuse an admin browser session. Choose an already available
compatible issuer, or obtain separate Account-owner approval for the required
query resource/client/introspection contract. Until then, real phone authentication
is an explicit blocker despite passing fixtures.

## Encrypted shared state and refresh

Apply [migration 001](../deploy/query/migrations/001_query_broker.sql) only through
a reviewed owner deployment. Runtime never auto-migrates, seeds policies, imports
credentials or grants itself access. [Role preview](../deploy/query/permissions.sql.tmpl)
keeps policies/quota limits read-only and gives only EXECUTE on the narrowly owned
`lock_policy` read-lock function. That function has a fixed trusted search_path,
qualified relations and no PUBLIC execution. Owner writes follow policy → grant
→ quota ordering. Permit transactions hold authority SHARE locks through quota
commit; a concurrent revoke cannot commit and then be bypassed by a stale permit
blocked on quota. A final fresh authorization decision still fences response data.

AES-256-GCM encrypts access/refresh/client secrets. AAD pins environment/resource,
grant identity, owner, source/resource, kind/scope, generation/state and expiry.
Stored timestamp values are normalized to PostgreSQL microseconds before sealing.
An injected 32-byte master key is checked against the schema's resource-scoped
fingerprint; wrong-environment/key imports fail before writing. Plaintext credential
files, home credential stores and libpq credential-file fallbacks are refused.
Production connections require explicit password-bearing injected DSN, verify-full
TLS/system roots, bounded pools/timeouts and `synchronous_commit=on`.

For expiry, one replica commits a generation CAS into `rotating` **before** OAuth.
The network exchange holds no SQL transaction/connection lock. Other replicas use
bounded backoff/jitter and read the shared outcome. A lost/failed exchange remains
`uncertain`; a dead worker remains fenced, and lease expiry never replays a token.
Successful replacement is encrypted and committed before access is returned.
Revocation can prevent publication of a just-refreshed credential. Production
HA storage must preserve acknowledged fences during failover; this has not been
established for any selected provider/target yet.

Keyed refresh-token identities bind provider/client/token across grant IDs.
Tombstones survive grant deletion and uncertainty. Import cannot retry the same
old token under a new ID. Publication permits an existing identity only for the
immediately current non-rotating token; a historical R1→R2→R1 token is refused.
Never truncate this registry or restore old state to recover access. Key rotation
needs a separately reviewed re-encryption/identity-tombstone plan; `bind-key` cannot
overwrite an existing fingerprint. A DB restore/acknowledged-write-loss incident
requires withdrawing service and issuer-side revocation/reauthorization for
potentially consumed grants, not automatic replay from an old snapshot.

Google owner refresh is implemented for exact `youtube.readonly`, using Google's
fixed HTTPS token endpoint with one exchange and no retry/scope escalation.
Public API keys need no OAuth refresh; their approved stored expiry and global
project quota still apply. Human HHC metadata can use an approved expiring human
read grant, but unattended `cms:pages:read` renewal is **not established** by the
existing recordings-only native client. Public HHC is the usable first scope;
private/draft HHC requires the separate approved human identity contract.

## Shared quota and bounded resource use

Shared caller minute counters and source/project quota counters commit atomically;
a global denial does not charge a caller. Missing limits or wrong resource/bucket
fail closed. Google Search and other API buckets remain distinct. YouTube daily
limits reset at Pacific midnight, including 23/25-hour DST days; HHC/caller fixed
windows use their configured UTC period. Configure actual project limits, not
historical quota assumptions. No quota is charged for final authorization checks.

Each serving instance has at most 4 PostgreSQL connections and 8 active MCP calls.
Two instances plus a one-instance rollout surge can consume 12 runtime connections;
owner/migration/operations reserve is additional. Polling is capped with jitter,
not an unbounded refresh storm. Quota rows have expiry metadata; approved owner
maintenance may remove expired usage rows. Refresh identity tombstones are retained.
Actual DB capacity, write load, issuer request volume and costs need target evidence.

## Explicit owner provisioning

`cmd/hhc-query-admin` provides `bind-key`, `set-policy`, `set-quota`, `import-grant`
and `revoke-caller`. It creates no provider credential or consent and is not an MCP
tool or part of startup. Executing it against a real environment requires separate
action-time authorization and the approved owner role. Secret-bearing grant input
must be a private inherited pipe; TTYs and regular files are refused. All JSON is
validated before writes, and diagnostics contain fixed codes only. Policy changes
use expected revisions; revocation disables caller policy and owned private grants.
Public project keys are not revoked for unrelated callers.

See the [deployment checklist](../deploy/query/README.md) for key injection, owner
sequence, remaining identity/hosting choices and rollback. PostgreSQL tests use a
disposable local/CI fixture with synthetic credentials; they prove SQL effects,
coordination and isolation, not real issuer consent or cloud failover acceptance.
