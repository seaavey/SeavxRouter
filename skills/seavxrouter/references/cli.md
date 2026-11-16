# CLI reference

```text
seavxrouter [flags]              run the HTTP server
seavxrouter login [provider]     authenticate and store credentials
seavxrouter check [provider...]  verify credentials and list models
seavxrouter tui [flags]          terminal usage dashboard
```

`check` with no arguments verifies every configured provider with a 45-second
timeout per provider. `tui` discovers a running server through
`~/.seavx/server.url` (falling back to `http://localhost:20128`) for the SSE log
feed.

## Server flags

| Flag | Default | Description |
| --- | --- | --- |
| `-listen` | `127.0.0.1:20128` | Listen address. Keep it on loopback unless protected. |
| `-auth-file` | `~/.seavx/auth.jsonc` | Credentials file. |
| `-api-key` | empty | Require this bearer token on `/v1/*`. Empty disables auth. |
| `-default-model` | empty | Model used when a request omits `model`. |
| `-usage-db` | `~/.seavx/usage.db` | SQLite usage database; `off` disables tracking. |
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
FreeBSD (amd64, arm64), published with `checksums.txt`.

Build from source:

```bash
git clone https://github.com/seaavey/SeavxRouter.git
cd SeavxRouter
go build -o seavxrouter .
```
