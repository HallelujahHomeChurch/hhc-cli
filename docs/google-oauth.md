# Google device sign-in on a browserless CLI

`hhc auth youtube-device` uses Google's Device Authorization Flow: display the
verification URL/user code, let the user sign in on a phone, and poll for tokens
directly in the CLI. No browser automation, callback server, Mac, pasted token or
plaintext token file is needed. Client/grant creation and real consent still
require action-time user approval; fixture tests perform none.

## Client and scope

Use an existing OAuth client properly registered as **TVs and Limited Input
devices**, with YouTube Data API enabled. Google must accept the genuinely
browserless environment. Do not impersonate hardware or substitute a web/desktop
client. The executor must securely inject the existing client secret using the
fixed direct variable `HHC_YOUTUBE_DEVICE_CLIENT_SECRET`; client ID and approved
channel ID are nonsecret flags. The CLI reads only that binding and removes it
from child inheritance. Never put its value or user tokens in argv/history/chat.

The [device-flow restricted scope list](https://developers.google.com/identity/protocols/oauth2/limited-input-device#allowedscopes)
permits `youtube.readonly` and `youtube`, not `youtube.force-ssl`:

| Choice | Exact Google scope | CLI surface |
| --- | --- | --- |
| `--access read` (default) | `https://www.googleapis.com/auth/youtube.readonly` | Public/private queries |
| `--access edit` | `https://www.googleapis.com/auth/youtube` | Queries plus explicit title/description/tag edits |

The broader youtube scope supports [videos.update](https://developers.google.com/youtube/data/reference/rest/v3/videos/update).
Google authorizes more than this CLI exposes; disclose the real scope before
consent. No YouTube upload/delete/privacy/publish command is provided. Existing
injected/code-flow grants may also use exact force-ssl; device login never asks
for it. HHC credentials and permissions remain separate.

## One operation or one process session

After approval to start sign-in, run one requested command:

```text
hhc auth youtube-device --client-id DEVICE_CLIENT_ID --channel-id UCabcdefghijklmnopqrstuv --access read --json -- videos search --source youtube --query 聚會 --limit 10
```

The URL and user code appear on stderr. Present those exact values only to the
consenting user; keep the same process alive while they finish Google sign-in.
Only after scope/channel validation does the CLI run the selected command. Its
stdout is one ordinary schema-v1 command result, without credentials.

For several operations, retain the same process:

```text
hhc auth youtube-device --client-id DEVICE_CLIENT_ID --channel-id UCabcdefghijklmnopqrstuv --access edit --session --json
```

After consent, stdout first emits authenticated metadata marked
`persistence=process_only`. Send newline-delimited argv arrays to that process:

```jsonl
["videos","search","--source","youtube","--query","聚會","--limit","10"]
["videos","edit","youtube:abcdefghijk","--title","New title","--dry-run"]
["videos","edit","youtube:abcdefghijk","--title","New title","--apply","--if-match","ACTUAL_ETAG","--plan-hash","ACTUAL_64_CHARACTER_HASH"]
["exit"]
```

Use the actual preview values and apply only explicitly authorized changes.
Read every schema-v1 result line; final session exit alone does not prove every
command succeeded. Only video search/get/compare/edit argv are accepted, never
shell/recording/credential commands or arbitrary upstream URLs. An uncertain
write blocks subsequent applies in this session while permitting read-only
reconciliation. Do not restart a session to bypass that fence.

Login wait defaults to 15m (maximum30m). Session defaults to1h (maximum4h and100
commands); each video operation keeps its30s bound. EOF, exit, cancellation or
timeout discards memory credentials. Exit does not silently revoke Google consent.

## Lifecycle and existing cloud runtime

Polling waits the returned interval before every request; authorization_pending
continues and slow_down permanently adds5s. Expiry/denial/cancellation and
ambiguous exchanges stop without replay. Endpoints are fixed HTTPS, redirects
refused, responses bounded and the approved owned channel verified. Access,
refresh and private device codes are never shown.

The process-only owner serializes refresh, removes the old usable grant before
exchange, and accepts replacement only for the same client/channel/scope. Failed
or uncertain refresh permanently fences that owner. Process death loses all
state; there is no saved old credential to reload. No user token is persisted
in a file, environment variable, native recording profile or personal keychain.

**Cross-task reuse is not implemented.** A new process/task needs new device
sign-in unless a separately approved durable owner adapter is provided. An
ephemeral workspace is not a persistence or availability guarantee. Durable
storage is not a prerequisite for useful single-process device login.

The [cloud environment guide](https://learn.chatgpt.com/docs/environments/cloud-environments#configure-environment-variables-and-network-secrets)
documents direct variables/personal values for programs that read values. Use
that supported UI to configure the client secret after approval. HTTP network
secrets provide proxy placeholders; do not assume raw delivery or form encoding.
Static client configuration is separate from storing a user's OAuth grant.
The currently inspected environment has execution but no approved real device
client/grant configured; live Google acceptance and iPhone private operations
remain unverified. No callback integration is needed for device sign-in.

Authorization-code/PKCE and explicit revoke components remain available for
separately approved integrations. Google revocation can affect the user's project
grant across clients/scopes and may take time to propagate; disable local use
first and obtain explicit approval. It is not automatic session cleanup. No
new infrastructure, Account or PostgreSQL changes are required.
