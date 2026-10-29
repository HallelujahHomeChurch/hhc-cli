# Video query deployment checklist and rollback

The repository contains the query service **and concrete embedded broker**:
RFC7662 caller verification, encrypted PostgreSQL grants, durable refresh fences,
refresh-identity tombstones and shared quota. No separate broker service is needed.
This package remains a review preview: no production identity, database, host,
credential, consent, infrastructure or phone connection has been activated.

## Smallest decisions needed

| Choice | Benefit and cost dependency |
| --- | --- |
| Existing managed container-app capacity + approved HA PostgreSQL | Preferred if already available: two always-on query instances, one image/process each, existing TLS and secret references. Incremental cost depends on CPU/memory reservation, minimum replicas, DB HA/capacity/storage and issuer usage. No new cluster is implied. |
| An already operated Kubernetes cluster + approved HA PostgreSQL | The included template gives two replicas, readiness/drain and zero-unavailable rolling strategy. Cost depends on available node/zone capacity, surge and shared DB. Do not create a cluster solely from this preview. |

Exact subscription/environment/region, monthly ceiling, public hostname and
existing capacity are still unknown. No numerical price or SLO is claimed.
Choose the approved OAuth authority separately: use an already available issuer
that supports resource-bound access tokens, PKCE/client registration and fresh
RFC7662 introspection, or obtain separate Account-owner approval for a new query
resource/client/introspection contract. The inspected HHC Account does not yet
supply that contract. Do not substitute its `hhc-api` token or admin session.

For the first useful scope, public HHC + an explicitly selected YouTube channel
with an API-restricted project key avoids a Google owner grant. Private/unlisted
YouTube needs separately approved youtube.readonly consent/client credentials.
Private/draft HHC needs an approved human cms:pages:read flow; the recordings-only
native refresh client cannot supply unattended renewal. Never enable service
principals or broaden backend scopes to bypass those boundaries.

## Build and review gates

- [ ] Review required checks on the exact PR head; keep the PR draft until its
      review/acceptance boundary is ready. No automatic approval/merge.
- [ ] Run Go race/vet, real disposable PostgreSQL coordination tests and relevant
      existing CPU media/native checks; distinguish compile from device acceptance.
- [ ] Run govulncheck with the patched toolchain. Go 1.26.10 and x/text 0.41.0
      address the reachable findings detected while adding the DB adapter.
- [ ] Build from reviewed merged source once, scan/publish its immutable digest,
      and retain matching previous image/config. PR images are not releases.

```sh
go test -race ./... -count=1
go vet ./...
# Add an explicitly disposable local DB for the shared-state tests:
# HHC_QUERY_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:PORT/hhc_query_fixture?sslmode=disable
# HHC_QUERY_REQUIRE_DATABASE_TESTS=1
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o hhc-query ./cmd/hhc-query
go build -buildvcs=false -o hhc-query-admin ./cmd/hhc-query-admin
docker build -f deploy/query/Dockerfile --build-arg VERSION=MERGED_COMMIT_SHA -t hhc-query:review .
```

The pinned builder produces a static, non-root/read-only image with public CAs
and dependency licenses; there is no shell/media/native credential store. For a
managed build proxy, mount a combined public CA bundle through BuildKit `build_ca`;
never bake session proxy trust or credential values into image layers.
CI adds one ephemeral PostgreSQL service to the existing Linux contracts job and
requires its tests, without a new workflow or native matrix. Its runner/cold-cache
resource cost remains measurable separately; do not raise the Actions budget.

## Identity, state and owner setup — action-time approval required

- [ ] Confirm the caller resource's exact HTTPS `/mcp` URL, issuer, actual
      introspection endpoint/client, required read scope and supported client
      registration/PKCE flow. The issuer must return active, issuer, subject,
      exact resource audience, expiry, scope and Bearer type. Verify revocation
      against that real authority before accepting private metadata.
- [ ] Review the concrete provider plan/diff and cost. Confirm at least two
      independently serving instances/failure domains and rollout surge capacity.
- [ ] Approve any new role, secret, OAuth consent, persistent access or paid
      infrastructure immediately before that action. Nothing here creates them.
- [ ] Review/apply migration 001 as the approved non-login schema owner. Apply
      the rendered permissions preview using actual approved roles. Runtime has
      no policy/quota-limit write or grant INSERT/DELETE; only EXECUTE on the
      narrow lock_policy read-lock function and required state/quota operations.
- [ ] Verify PostgreSQL acknowledged-write durability, including synchronous
      standby/failover behavior and no split-brain. Production connections force
      synchronous_commit=on, but that alone does not certify an HA provider.
- [ ] Budget 4 connections/instance: two instances plus one surge can use 12;
      reserve additional owner/migration/operations capacity. No new app cache
      or Redis service is required.
- [ ] Inject the approved master key, password-bearing verify-full PostgreSQL
      DSN and introspection client secret. Use the platform's encrypted secret
      reference in HHC_QUERY_RUNTIME_SECRET, or a private inherited pipe selected
      by HHC_QUERY_SECRET_FD. Its JSON fields are databaseUrl, masterKey (base64
      of 32 approved random bytes), introspectionSecret. Never put values in argv,
      chat, a ConfigMap, .env, regular credential file or repository.
- [ ] Set HHC_QUERY_CONFIG to the **non-secret** config JSON. The example uses
      nonexistent .invalid authorities and must be replaced with reviewed values.
- [ ] Use the separately approved owner utility to bind the initial vault,
      configure hashed caller policy/project limits and import already authorized
      grants. bind-key refuses replacement; policy changes use expected revisions.
      Secret imports require private pipe stdin and never print credentials.
      Do not run these operations as part of ordinary query startup.
- [ ] Set YouTube daily limits from the actual project's distinct Search/other
      buckets; resets use Pacific calendar midnight, including DST. Owner changes
      cannot silently change a bucket's existing period.

The owner executable is `cmd/hhc-query-admin`, with explicit `--operation` values
`bind-key`, `set-policy`, `set-quota`, `import-grant`, `revoke-caller`. Policy JSON
contains a hashed caller ID, source modes/resources/grant/quota IDs and rate limit;
source grants bind the same caller (or an explicitly allowed public project key).
This executable stores approved state; it does not create provider credentials
or implement an unapproved consent flow. Use the schema-owner role only for
provisioning, not serving. Its input and all errors are redacted/fixed-code.

## Rollout and real acceptance

- [ ] Render/validate the chosen provider's configuration against its actual API.
      The Kubernetes template is not a provider selection or applied deployment.
      It references an existing approved secret; it does not create one.
- [ ] Route only MCP and OAuth discovery publicly through approved TLS ingress;
      keep health paths internal. Set exact Host/origin lists, private DB access,
      issuer/Google/HHC egress and shared quota. Do not forward spoofable internal
      identity/Dapr headers. Authorization metadata comes from trusted config.
- [ ] Verify process and schema/key readiness; readiness does not prove issuer,
      HHC/Google availability or actual consent. Monitor request outcomes,
      caller/issuer/store failures, quota and latency with redacted fields only.
- [ ] Test unauthorized/cross-caller/channel/scope reads, immediate revocation,
      source-only outage, global-authority outage, stale status, quota denial,
      cancellation and concurrent refresh across **actual** replicas.
- [ ] Kill/drain one instance while another serves; roll a revision with active
      reads. Embedded broker/DB pool remain alive until the 35-second drain ends;
      platform termination grace is at least 45 seconds. No production load test.
- [ ] Authenticate the approved iPhone MCP client, turn the personal Mac off,
      find a known video, retrieve details and compare exact website status.
      Repeat a denied/bounded/unavailable-source case. Record sanitized evidence
      separately. A curl, fixture issuer, local Docker or CI success does not pass
      this acceptance.

## Rollback and recovery

Withdraw the failing revision from readiness, route to the previous verified
image/config and drain accepted reads. Verify authorization and a bounded source
read after restoring the previous compatible revision. No HHC/YouTube content
rollback is needed because queries never mutate content.

Do not restore old grant rows/keys or truncate refresh_identity as application
rollback. Once refresh may have consumed a token, rotating/uncertain state and
identity tombstones remain fenced. Recovery needs **new issuer-authorized
credentials**, not the same old secret under a new grant ID. A storage rollback,
acknowledged-write loss or key rotation needs a reviewed revocation/reauthorization
and identity-preservation plan; normal release rollback must leave authority/grant
state intact. Owner maintenance may delete expired quota usage, never refresh
identity tombstones. Actual provider failover/restore acceptance remains pending.

Report code/tests, CI, review/merge, immutable publication, provider preview/apply,
deployment/probes, identity/channel activation and Mac-offline iPhone acceptance
as separate facts. Pending stages must remain explicit.
