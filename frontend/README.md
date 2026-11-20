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

Open http://127.0.0.1:5173 on the router host. Sign in with `seavx12345` on the first run, then set a new password of at least 12 characters before using the dashboard. The router stores the bcrypt hash in SQLite. Dashboard sessions are valid for 12 hours, and signing in again replaces the previous session. For a walkthrough with sample data, open http://127.0.0.1:5173/?demo=1 or choose **View demo**.

Vite proxies `/dashboard/api` and `/healthz` to the router. Set `SEAVX_ROUTER_ORIGIN` in `frontend/.env.local` to override the upstream URL. Usage and request logs require the SQLite usage ledger. Tenant key management requires the key store and the router's `-api-key` flag.

Build with `npm run build` and try the result with `npm run preview`. Finish first-time setup over localhost before exposing the dashboard. For deployment, serve `dist/` over HTTPS and forward `/dashboard/api/*` and `/healthz` to Go on the same origin with the original Host header. Inference clients continue using `/v1` with their existing authentication.

Overview and the first Requests page subscribe to `/dashboard/api/logging/stream`. Finished requests refresh the latest logs and usage totals in batches. Older request pages stay stable while you browse. Configure the reverse proxy to stream SSE without response buffering or a short read timeout.
