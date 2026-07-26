# ChatInfra jmap

This module contains the `jmap` CLI and the `jmapd` agent bridge daemon.

`jmap` is a pure Go JMAP/Cyrus CLI for calendars, contacts, mail, scheduling, and appointments. `jmapd` is the long-running per-agent bridge that turns calendar VALARM fires, inbound mail, and shared-contact access into prompts for a local OpenCode agent.

## Public mirror

The public repository is <https://github.com/chatinfra/jmap.git>. Its root is a mirror of this monorepo's canonical `go/jmap/` subtree, so public checkouts contain `go.mod`, `cmd/jmap`, `cmd/jmapd`, `internal`, tests, the `spec/outputs/` schemas, and these docs directly at repository root.

`go/jmap` in the ChatInfra monorepo remains canonical. Maintainers import accepted public changes back into the monorepo first, then update the public mirror from the canonical subtree. The mirror sync rewrites published Go and Markdown module-path references so mirror checkouts use the public module path in examples such as `github.com/chatinfra/jmap/cmd/jmap`.

## Build and test

```sh
go test ./...
go build ./cmd/jmap
go build ./cmd/jmapd
```

The module currently declares Go 1.24 in `go.mod`. From a published mirror checkout, module-path installation uses the same public path shown after sync:

```sh
go install github.com/chatinfra/jmap/cmd/jmap@latest
```

## `jmap` CLI output

`jmap` help is deterministic terminal text. Successful command results are YAML documents, and failures are coded YAML error envelopes on stderr. `--trace` writes redacted HTTP diagnostics to stderr, with passwords and `Authorization` headers removed. `--json` is unsupported.

Run `jmap schemas` to list the supported structured YAML schema IDs and their paths under `spec/outputs/`.

Credentials come from flags or environment variables:

| Flag | Environment | Purpose |
| --- | --- | --- |
| `--url` | `JMAP_URL` | JMAP base URL |
| `--user` | `JMAP_USER` | JMAP account username |
| `--password` | `JMAP_PASSWORD` | JMAP password |
| `--timeout` | `JMAP_TIMEOUT` | Request timeout; `0` disables the client timeout (default `30s`) |
| `--trace` | `JMAP_TRACE` | Write redacted HTTP traces to stderr |
| `--state-root` | `JMAP_STATE_ROOT` | Appointment state root (default: user config dir) |
| `--dry-run` | `JMAP_DRY_RUN` | Preview mutating commands where supported |
| `--force` | `JMAP_FORCE` | Confirm destructive bulk commands such as `event delete-all` |

```sh
jmap check
jmap calendar list
jmap event create --title Demo --start 2026-01-01T10:00:00Z --duration 30m --calendar-id cal-1
jmap raw call Calendar/get --params '{"ids":null}' --capability urn:ietf:params:jmap:calendars
```

## `jmapd` bridge daemon

`cmd/jmapd` runs one long-lived bridge for one JMAP-attached agent. It watches the account's calendars and events, schedules local wall-clock VALARM fires, handles inbound mail and shared-contact access, and submits formatted prompts to the local OpenCode API. It never writes a response back to JMAP.

Required environment:

| Variable | Purpose |
| --- | --- |
| `JMAP_BASE_URL` or `JMAP_URL` | JMAP server base URL |
| `JMAP_USER` | JMAP account user identifier |
| `JMAP_PASS` or `JMAP_PASSWORD` | JMAP account password |
| `OPENCODE_BASE_URL` or `OPENCODE_URL` | OpenCode API base URL |
| `OPENCODE_PORT` | OpenCode API port when the base URL is unset |
| `OPENCODE_HOST` | Host used with `OPENCODE_PORT` (default `127.0.0.1`) |
| `OPENCODE_DIRECTORY` or `OPENCODE_DIR` | OpenCode working directory |
| `OPENCODE_AGENT` or `AGENT_ID` | OpenCode agent ID |
| `JMAPD_STATE_DIR` or `STATE_DIR` | Directory for `events.json`, `sessions.json`, `status.json` |
| `JMAP_POLL_INTERVAL` | Polling fallback interval (default `60s`) |
| `JMAP_ALARM_WINDOW` | VALARM expansion window (default `168h`) |
| `OPENCODE_PROMPT_TIMEOUT` | Prompt timeout (default: no timeout) |

`jmapd` writes its runtime state as three files under `JMAPD_STATE_DIR`: `events.json` (last calendar/event snapshot), `sessions.json` (calendar id to OpenCode session map, preserved across restarts), and `status.json` (the health contract read by ChatInfra: connection flag, push/polling state, last refresh, last VALARM fire, last completed prompt, latest error, session count, start time, and registered listener keys).

Prompt errors, stale-session recreation failures, timeouts, and missing assistant responses are log-only: `jmapd` records them in `status.json`, takes no JMAP-side action, and continues processing later VALARMs. Fatal JMAP connection errors exit non-zero so the supervising user-systemd unit restarts the daemon.

## OpenCode host layout

Source-backed OpenCode hosts use these stable paths. `jmap` and `jmapd` share one source checkout:

| Path | Purpose |
| ---- | ------- |
| `/data/opencode/src/jmap` | Editable source checkout cloned from the public mirror, shared by both tools |
| `/data/opencode/bin/jmap` | Stable CLI launcher used by ChatInfra operations |
| `/data/opencode/bin/jmapd` | Stable bridge-daemon launcher referenced by rendered systemd units |
| `/data/opencode/.cache/jmap` and `/data/opencode/.cache/jmapd` | Cached build output and source hash per tool |
| `/data/opencode/.cache/go-build` and `/data/opencode/.cache/go-mod` | OpenCode-owned Go build and module caches |

Each launcher rebuilds its command package (`./cmd/jmap` or `./cmd/jmapd`) when the source hash changes or the cached binary is missing, then execs the cached binary with the original arguments. ChatInfra-controlled operations run the launchers as the `opencode` user so editable source is not executed as root.

For local host edits:

```sh
sudo -u opencode git -C /data/opencode/src/jmap status
sudo -u opencode editor /data/opencode/src/jmap/internal/...
sudo -u opencode /data/opencode/bin/jmap --help
```

Installer and reconfigure flows preserve dirty `/data/opencode/src/jmap` checkouts and log a warning instead of resetting local work. Clean checkouts are fetched and fast-forwarded from the configured mirror. When the shared checkout moves, ChatInfra rebuilds the `jmapd` launcher once and restarts the reconciled bridge units; up-to-date or dirty-skipped checkouts do not restart running bridges.

## Contribution workflow

1. Fork <https://github.com/chatinfra/jmap.git>.
2. Clone your fork and create a topic branch.
3. Make changes, run `go test ./...`, and push the branch.
4. Open a pull request against the public mirror.

Accepted public changes are reviewed and imported into canonical `go/jmap` in the ChatInfra monorepo before the public mirror is synchronized again. See [CONTRIBUTING.md](./CONTRIBUTING.md) for details.
