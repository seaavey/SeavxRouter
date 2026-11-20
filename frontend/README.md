# SeavxRouter web dashboard

Start the Go router with a dedicated SQLite file for dashboard login:

```sh
./seavxrouter -listen 127.0.0.1:20128 -dashboard-db "$HOME/.seavx/dashboard.db"
```

In another terminal, use Node.js 24 to start Vite:

```sh
cd frontend
npm ci
npm run dev
```

Open http://127.0.0.1:5173 on the router host. On the first run, sign in with `seavx12345` and the setup token in `$HOME/.seavx/dashboard.db.setup-token`. If you set a custom database path, look for the same path with `.setup-token` appended. Go logs the token file path at startup. Set a new password of at least 12 characters to finish setup and consume the token. To rotate the password later, select **Change password** in the dashboard footer and enter your current password. The router stores the bcrypt hash in SQLite. Dashboard sessions are valid for 12 hours, and signing in again replaces the previous session. For a walkthrough with sample data, open http://127.0.0.1:5173/?demo=1 or choose **View demo**.

Vite proxies `/dashboard/api` and `/healthz` to the router. Set `SEAVX_ROUTER_ORIGIN` in `frontend/.env.local` to override the upstream URL. Usage and request logs require the SQLite usage ledger. Tenant key management requires the key store and the router's `-api-key` flag.

Build with `npm run build` and try the result with `npm run preview`. Finish first-time setup over localhost before exposing the dashboard. For deployment, serve `dist/` over HTTPS and forward `/dashboard/api/*` and `/healthz` to Go on the same origin with the original Host header. Inference clients continue using `/v1` with their existing authentication.

Overview and the first Requests page subscribe to `/dashboard/api/logging/stream`. Finished requests refresh the latest logs and usage totals in batches. Older request pages stay stable while you browse. Configure the reverse proxy to stream SSE without response buffering or a short read timeout. Login retries are throttled per connecting IP. Clients behind the same reverse proxy share that proxy’s throttle because the router ignores forwarded client IP headers.

## CI and releases

The **frontend** CI job installs locked dependencies with Node.js 24, type-checks
the TypeScript code, and builds the Vite app. Download the
`seavxrouter-dashboard` artifact from a successful run to inspect the static build.

Pushing a release tag such as `v1.2.3` also builds the frontend. The GitHub
Release includes `seavxrouter-dashboard.tar.gz` and its SHA-256 checksum in
`checksums.txt`. Extract the archive into your web server's document root and
use the same-origin proxy configuration described above.
