# HHC CLI command architecture

HHC is the church software CLI. Recordings are one domain, not the root
application. Keep the binary name `hhc`, existing command paths, profile store,
JSON schema, operation journals and release/update contracts compatible.

## Boundaries

- `cmd/hhc`: process entry, interrupt cancellation and native media supervisor.
- `internal/cli/run.go`: explicit command registry, one invocation's I/O and
  flags, installation lock, result envelope. No recording execution here.
- `commands_auth.go`: login/status/logout; reuse `internal/auth` and native
  credential storage. Existing default recording scopes are unchanged; future
  commands must request only their required approved scopes.
- `commands_recordings.go`: recording flags, permissions, journal and timeout
  orchestration. Recording cleanup belongs here, never in global startup.
- `commands_system.go`: install/update/version; no login requirement.
- `result.go` / `secrets.go`: fixed safe error messages and secret input. Never
  echo parser errors, arbitrary remote bodies, credentials or signed URLs.
- `internal/recordings`, `media`, `api`, `auth`, `update`: existing domain logic
  and transports; command files adapt user input rather than duplicating them.

## Adding a command group

1. Add `commands_<domain>.go` with a handler matching the existing groups.
   Parse only that group's flags with `c.flags()` and `c.parse()`; validate
   inputs before external effects. Finish through `c.finish(err)`.
2. Register the group in `commandGroups`. Root help is generated from that
   registry. Add group/subcommand help to `commandHelp`; extend help flag-value
   recognition when adding flags that consume values.
3. Use the existing context, profile storage and authenticated transport where
   appropriate. Acquire the installation lock for commands using the installed
   release; only install/update use their own exclusive update lifecycle.
   Do not initialize FFmpeg, sweep recording journals or require recording
   permissions for unrelated commands.
4. Keep business logic in the owning domain package. Durable operations must
   have their own explicit intent/recovery contract; do not reuse recording
   journals merely because they already exist.
5. Test help without external effects, flag isolation, JSON errors/redaction,
   cancellation and the command's critical behavior. Run repository tests,
   race detector, vet and both platform build checks.

This is a compile-time registry using Go's standard `flag` package. It does
not load plugins, broaden OAuth permissions, introduce a DI framework or add
commands for domains that do not exist yet.
