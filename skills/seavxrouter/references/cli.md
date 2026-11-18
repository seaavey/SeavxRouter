# CLI reference

```text
seavxrouter [flags]              run the HTTP server
seavxrouter login [provider]     authenticate and store credentials
seavxrouter check [provider...]  verify credentials and list models
seavxrouter tui [flags]          terminal usage dashboard
seavxrouter key <action>         manage tenant API keys
seavxrouter provider <action>    manage custom OpenAI-compatible providers
seavxrouter combo <action>       manage cross-provider model combos
seavxrouter service <action>     run as a systemd user service (Linux)
```

`check` with no arguments verifies every configured provider with a 45-second
timeout per provider. `tui` discovers a running server through
`~/.seavx/server.url` (falling back to `http://localhost:20128`) for the SSE log
feed.

`key` actions run against the local key database, not a running server:

```bash
seavxrouter key create <name> [-budget USD] [-limit-token N] [-keys-db path]
seavxrouter key list [-keys-db path]
seavxrouter key revoke <id> [-keys-db path]
```

`create` prints the plaintext key once. `-budget` and `-limit-token` set monthly
caps; `0` (the default) means unlimited. Revoking is soft; the row stays in
`list` with status `revoked`.

`provider` actions manage custom OpenAI-compatible endpoints in the same local
credentials file as `login`:

```bash
seavxrouter provider add <name> -base-url URL [-api-key KEY] [-model ID] [-auth-file path]
seavxrouter provider list [-auth-file path]
seavxrouter provider remove <name> [-auth-file path]
```

`add` requires a lowercase provider name (`a-z`, `0-9`, `_`, `-`; built-in
names are rejected) and an `http(s)` base URL. The key is always validated
before saving: an empty `/models` catalog is refused, and a tiny
`chat/completions` request must succeed on at least one catalog model. A
401/403 rejects the key outright; a 402/429 quota response registers it with a
`warning:` so the account can be topped up. When `-api-key` is omitted it
prompts on stdin. `-model` pins the smoke test to one model ID, which matters
when the catalog's first chat models are outside a free plan (for example
`-model gpt-6-luna` on a free-tier key). Models are exposed as
`<name>/<model>`. Restart a running server to activate a newly added or removed
provider; extra keys added to an existing provider are picked up live.

`combo` actions manage ordered fallback lists across providers. They live in
`combos.jsonc` next to the credentials file:

```bash
seavxrouter combo add <name> -models ID,ID,... [-auth-file path] [-combos path]
seavxrouter combo list [-auth-file path] [-combos path]
seavxrouter combo remove <name> [-auth-file path] [-combos path]
```

Members are full model IDs (`groq/openai/gpt-oss-120b`,
`antigravity/gemini-3.8-flash`) tried left to right; bare IDs also resolve.
Request the combo as `combo/<name>`. Combo names use the same lowercase rule as
provider names, and a member cannot be another `combo/` ID. A quota, auth, or
transient failure before the first streamed delta moves to the next member; a
non-retryable error or a partially streamed answer stops the request. Usage and
logs record the member that actually answered. A running server re-reads the
file when its mtime changes, so no restart is needed.

## Run in the background (Linux)

```bash
seavxrouter service install [server flags...]
seavxrouter service start|stop|restart|status
seavxrouter service logs [journalctl flags...]
seavxrouter service uninstall
```

`install` writes `~/.config/systemd/user/seavxrouter.service` for the current
binary, forwards every flag after `install` verbatim to `ExecStart` (use `--`
first if you prefer), preserves the current directory as `WorkingDirectory`,
then runs `systemctl --user daemon-reload`, `enable`, and `restart`. The
restart makes a reinstall apply the new binary and flags even when the unit was
already active. The router starts at login and `Restart=on-failure` brings it
back after a crash. The unit file is
mode `0600` because it may contain `-api-key`; note that command-line secrets
are still visible in the process table to the same user. A user service stops
when you log out unless lingering is on: `loginctl enable-linger $USER` once.
Stop a manually started server that holds the same port before installing.
`status` runs `systemctl --user status --no-pager`; `logs` follows the journal
(last 100 lines); `uninstall` disables, stops, and removes the unit, but refuses
to delete it when the service cannot be stopped, so a running router is never
orphaned outside systemd's control.

## Server flags

| Flag | Default | Description |
| --- | --- | --- |
| `-listen` | `127.0.0.1:20128` | Listen address. Keep it on loopback unless protected. |
| `-auth-file` | `~/.seavx/auth.jsonc` | Credentials file. |
| `-combos` | next to `-auth-file` | JSONC combo table; defaults to `combos.jsonc` beside the credentials file. |
| `-api-key` | empty | Require this bearer token on `/v1/*`. Empty disables auth. Also the master key for key management. |
| `-default-model` | empty | Model used when a request omits `model`. |
| `-usage-db` | `~/.seavx/usage.db` | SQLite usage database; `off` disables tracking. |
| `-keys-db` | `~/.seavx/keys.db` | SQLite tenant API keys and budgets; `off` disables. |
| `-compress` | `on` | Compress large tool results (`on`/`off`). |
| `-compress-min-bytes` | `2000` | Minimum tool-result size before compression. |
| `-pricing` | empty | JSON price overrides in USD per 1M tokens. |
| `-api-base` | Antigravity API | Antigravity API base URL. |

## TUI flags

| Flag | Default | Description |
| --- | --- | --- |
| `-once` | `false` | Print one snapshot and exit. |
| `-group-by` | `day` | `day`, `hour`, `provider`, `model`, `user`, or `endpoint`. |
| `-refresh` | `2s` | Refresh interval. |
| `-server` | auto | Server URL for the SSE live-log feed; `off` disables it. |
| `-usage-db` | `~/.seavx/usage.db` | Database to read. |

Keys: `q`/`Ctrl-C` quit, `j`/`k` or arrows move, `Enter` opens detail, `Esc`
goes back, `r` reloads, `p` pauses, `1`-`6` switch grouping (day, provider,
model, user, endpoint, hour).

## Install a release

```bash
curl -fsSL https://raw.githubusercontent.com/seaavey/SeavxRouter/main/install.sh | sh
```

Pin a version or directory:

```bash
curl -fsSL https://raw.githubusercontent.com/seaavey/SeavxRouter/main/install.sh \
  | sh -s -- --version v0.1.1 --dir "$HOME/bin"
```

Releases are static binaries (`CGO_ENABLED=0`) for Linux/Termux (amd64, arm64,
armv7, 386, riscv64), macOS (amd64, arm64), Windows (amd64, arm64, 386), and
FreeBSD (amd64, arm64), published with `checksums.txt`. `seavxrouter service`
targets systemd user managers only; on other platforms use your process
supervisor.

Build from source:

```bash
git clone https://github.com/seaavey/SeavxRouter.git
cd SeavxRouter
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o seavxrouter .
```
