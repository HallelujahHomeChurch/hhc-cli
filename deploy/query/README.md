# Video query deployment preview and rollback

This package is a review preview. Nothing here provisions infrastructure, creates
credentials, grants Google access, changes backend scopes, starts production
load, or installs a ChatGPT connection. Deploy only reviewed, merged immutable
source/artifacts under the repository's delivery rules. Keep draft PRs unmerged
until their required checks/review gates pass.

## Decisions required before implementation can become a live service

Confirm the existing hosting subscription/environment/region, permitted monthly
cost, ingress hostname and supported MCP/OAuth client connection. Prefer suitable
existing managed capacity if it can run two independently serving instances with
private networking and the required shared identity coordinator. Do not infer
that the Kubernetes preview selects Kubernetes or authorizes a new cluster.

Select the caller issuer/audience, policy owner and concrete broker/secret manager
adapter. Confirm HHC human-only content access and the exact YouTube channel ID;
choose public metadata only, or separately approve owner private/unlisted metadata
with youtube.readonly. Existing CLI recording credentials/Google sessions are
not an unattended cloud identity. Do not enable service principals or change human
route restrictions to make this work. New credentials, OAuth grants, persistent
access and paid resources require action-time approval.

The broker contract is in [docs/video-query-broker.md](../../docs/video-query-broker.md).
Its production adapter/refresh coordinator is not supplied by this patch. No
production identity, target or cost estimate has yet been verified. Until these
choices are made, the deployment action is **pending**, not attempted or denied.

## Build and validate

```sh
go test -race ./... -count=1
go vet ./...
CGO_ENABLED=0 go build -buildvcs=false -trimpath -o hhc-query ./cmd/hhc-query
docker build -f deploy/query/Dockerfile --build-arg VERSION=MERGED_COMMIT_SHA -t hhc-query:review .
```

The image contains only a static Go binary and public root CAs; it includes no
FFmpeg, native credentials or shell. In a managed proxy build, mount the combined
public CA bundle as a BuildKit `build_ca` secret. Keep proxy/TLS validation; never
bake session proxy trust, credential values or private configuration into layers.
Pin reviewed builder/runtime provenance for release, scan the built image and use
its registry digest. The example tag and `dev` version are not release identities.
No new CI workflow is introduced; existing tests compile the command, and native
CLI/platform checks retain their separate acceptance boundaries.

## Preview and rolling deployment

Render `kubernetes.yaml.tmpl` only after the target is approved. Replace namespace,
query/broker **image digests**, actual accepted Host headers, ingress and broker
workload identity. Add the target's network policy (HHC/Google and shared broker
dependencies only), TLS, OAuth resource metadata/discovery/client registration,
resource requests/limits for the broker, and any approved browser origins. The
broker socket must be created with its reviewed UID/GID/mode, not world-writable.
Do not log headers or the broker response. Review the rendered plan/diff and
monthly cost before apply; validate against the actual cluster's schemas. The
preview is not provider-validated and has not been applied.

Minimum topology: two independently serving query instances across distinct
failure domains where supported; zero-unavailable rolling update, readiness
routing, disruption budget, and SIGTERM drain (45-second platform grace for the
35-second application shutdown). A one-node/two-process setup does not establish
host-failure tolerance. Multiple broker adapters need an HA shared grant/quota
store and coordinated refresh; a single shared refresh/identity/secret-manager
outage can make both replicas unready. HHC or Google outages/quota denial produce
partial/unknown source results, not fabricated availability. No numerical SLO is
promised without measurement.

Observe process/readiness, request/error/partial rates, latency, broker failures,
quota consumption and denied policy. Fixed audit records contain caller hash,
operation/outcome and duration only. Keep query bodies, tokens, video metadata
and signed URLs out of application/ingress diagnostics. Health probes exercise
only the process/broker and do not establish real upstream query acceptance.

Before routing normal traffic, verify: unauthorized and cross-caller/channel
requests fail; token revocation clears results; stale private/publication data is
never served; exhausted quota is bounded; concurrent refresh is fenced across
replicas; broker/store outage behavior is correct; kill/drain one instance while
the other serves; then roll a revision without dropping accepted in-flight reads.
Use bounded fixture/individual read-only queries, not a production load test.

## Rollback and final acceptance

Retain previous image digests and the prior reviewed deployment configuration.
On elevated errors/authorization problems, withdraw the new revision from
readiness and route to the previous verified revision; drain active requests,
restore prior digest/config, and verify authorization and a safe read. This
service has no data migration or content-write rollback. Credential revocation
belongs to its issuer/broker and is separate from application rollback. A token
rotation must never be reversed by restoring old secret material.

After the phone's MCP connection is authenticated through the approved identity
flow, switch off the personal Mac, ask from the iPhone for a known video, read its
details and compare its website publication state. Repeat with a denied caller,
a missing/bounded result and one unavailable source; confirm honest unknown/partial
results. Record account/channel and timestamps privately with sanitized evidence.
A passing curl, local Linux process or CI run does not pass this device acceptance.

Report separately: code/tests, CI, review/merge, immutable artifact publication,
provider preview/apply, deployment/probes, identity/channel activation and actual
Mac-offline iPhone acceptance. Unperformed stages stay explicitly pending.
