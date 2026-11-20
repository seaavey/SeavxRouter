# SeavxRouter web dashboard

Run the Go router on `127.0.0.1:20128`, then start the dashboard with Node.js 24:

```sh
cd frontend
npm ci
npm run dev
```

Open http://127.0.0.1:5173. If the router uses `-api-key`, open **Connection** and enter its admin key. The dashboard keeps the key in memory for the current page session. For a walkthrough with sample data, open http://127.0.0.1:5173/?demo=1 or choose **View demo**.

Vite proxies `/v1` and `/healthz` to the router. Set `SEAVX_ROUTER_ORIGIN` in `frontend/.env.local` to override the upstream URL. Usage and request logs require the router's SQLite usage ledger. Tenant key management requires the key store and admin authentication.

Build with `npm run build` and try the result with `npm run preview`. To deploy, serve `dist/` behind an HTTPS reverse proxy and route `/v1/*` and `/healthz` on the same origin to the Go router. Keep the router admin key server-configured and enter it through the dashboard's connection dialog.
