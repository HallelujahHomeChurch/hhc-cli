# Google authorization and the cloud runtime boundary

The desired interaction is a supported browser sign-in and consent, followed by
authorized CLI access to the selected channel. `internal/videoauth` implements
Google authorization-code handling, S256 PKCE, state, expiry, refresh, channel
verification and explicit revocation. It does not create a Google client,
callback listener, persistent credential store or runtime integration.

## Protocol components

Configure an already approved, separate Google client with the actual client
type, exact registered redirect URI and approved channel. Do not reuse HHC
Account credentials. Select exactly `youtube.readonly` for reads or explicitly
`youtube.force-ssl` for the implemented metadata edits; the latter Google scope
is broader than those three CLI fields. Other scopes/combinations are rejected.

`GoogleOAuth.Begin` creates a 10-minute in-memory session with cryptographic state
and an S256 challenge. Deliver its `AuthorizationURL` only to the consenting user
through the approved browser integration. `Complete` takes the callback directly
from the approved receiver, checks the exact callback/client/state and consumes
the code session once before exchange. It never requests pasted codes/tokens,
uses a deprecated OOB redirect, or retries an ambiguous code exchange. Google
endpoints are fixed HTTPS; redirects are refused. Returned grant scope/expiry
and `channels.list(mine=true)` must match the explicitly configured channel.

The approved credential owner must keep that session alive while the callback
arrives and securely commit the returned grant before issuing access tokens to
CLI child processes. The session cannot be serialized to resume a second CLI
invocation. A desktop loopback callback is supported only for a browser on that
same desktop; an iPhone's loopback address does not reach a cloud executor. A
web flow requires an already supported registered HTTPS receiver. This code
starts neither kind of receiver and does not expose a working `auth youtube
login` command without that integration.

`GoogleRefresh` accepts only the exact previously approved scope, preserves
replacement credentials and refuses retries/redirects or escalation. Its owner
must serialize refresh and securely commit replacement tokens; after an
uncertain exchange it must fence the old grant and obtain fresh consent rather
than replaying it. `GoogleOAuth.Revoke` performs one explicit Google revocation
request; the owner must disable use of the grant first and clear stored state
appropriately. Permission to develop these methods is not permission to call
them against a real account.

## Existing cloud executor

An agent skill supplies command instructions. The executor separately supplies
command execution, browser/callback integration, grant ownership and injection.
The CLI accepts only short-lived source-specific access descriptors via private
stdin; refresh tokens and authorization codes are never accepted as query/edit
arguments or emitted in result JSON.

The currently inspected task has callable cloud execution but no configured
Google OAuth client, callback receiver, YouTube grant or credential owner. Native
recording credentials on Linux fail closed; a Mac keychain is not a cloud route.
Therefore protocol fixtures and public CLI smoke tests do not establish the
natural iPhone OAuth experience or Mac-offline private access.

The [cloud environment guide](https://learn.chatgpt.com/docs/environments/cloud-environments#configure-environment-variables-and-network-secrets)
documents direct variables, domain-scoped HTTP secrets and Personal vault.
These facilities can hold already provisioned values; the documentation does
not establish a Google consent callback, automatic refresh-token replacement or
a supported YouTube OAuth connection. Network secrets substitute placeholders
for approved HTTPS destinations rather than exposing raw values to a program.
Do not turn a placeholder or manual token copy into a claimed OAuth integration.

Before live use, the user/owner must select an existing supported OAuth
connection that can receive Google's approved redirect, securely own the grant
across cloud tasks, handle refresh/revocation and inject short-lived credentials
into the approved child process. Confirm its actual callback/client identity,
scope and channel. If no such connection is available, report that exact missing
platform capability and obtain separately scoped authorization for any setup;
do not create infrastructure, grants or secrets as an implicit repository change.

The first live acceptance is browser consent through that supported connection,
private query on the approved channel, then (only when explicitly authorized)
preview/apply/read-back of a chosen metadata change. Verify the actual iPhone
workflow with the Mac offline. Do not promise future executor availability from
the current ephemeral task.
