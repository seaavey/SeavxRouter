# SeavxRouter Dashboard Progress

Pull request [#1 — Add Vite web dashboard](https://github.com/seaavey/SeavxRouter/pull/1)  
Branch `feat/web-dashboard` → `main`

## Included in the PR

- [x] Vite dashboard built with React, TypeScript, Tailwind CSS 4, Radix primitives styled with shadcn/ui patterns, and Motion.
- [x] Overview, Requests, Providers, Models, API Keys, Sign in, and first-time password setup. Demo mode is available at `/?demo=1`.
- [x] Authenticated SSE updates for Requests and Overview, batched every two seconds.
- [x] Admin login backed by SQLite and bcrypt. First-time setup over localhost requires changing the initial password. The session cookie also protects SSE and the dashboard API.
- [x] Vite build and Go authentication tests. A smoke test through the Vite proxy covered login, first-time setup, SSE delivery, and access revocation after logout.
- [x] Desktop demo review in Chromium, including the date range selector, request filtering, and the request details dialog.

## Remaining work for PR #1

Some backend and embedding changes were started locally before the workspace disconnected. Check for a recoverable local diff. Mark items complete once the changes are on the branch and their checks pass.

- [ ] Show all configured providers, including providers that do not report quotas. Explain when quota details are unavailable.
- [ ] Use stable cursor pagination for Requests. Add server-side search and account and tenant columns. Verify that incoming requests do not cause duplicates or omissions when paging through logs.
- [ ] Use the existing PATCH handler to edit USD budgets and token limits. Show usage since the start of the UTC month using the same data as Go's limit enforcement.
- [ ] Connect **TanStack Query** to the dashboard API. Batch query invalidation after SSE events and clear private cached data when the session ends.
- [ ] Use **TanStack Table** for Requests, Models, and API Keys. Keep Requests filtering and cursor pagination on the server.
- [ ] Use **TanStack Router** for page URLs, deep links, refresh, and Back/Forward navigation. Verify route fallback in Vite and the production binary.
- [ ] Use **TanStack Form and Zod** for sign-in, password setup, API key creation, and limit edits. Align input rules with Go validation, including password confirmation, length limits, and nonnegative amounts.
- [ ] Embed the Vite build in the Go binary. Verify that the release serves the dashboard and API on the same origin without a separate Node process.
- [ ] Add CI for a clean dependency install, frontend type checking and build, a Go build with embedded assets, and Playwright E2E.
- [ ] Use approximately 60% dark graphite, 30% dark gray, and 10% cyan. Use shadcn/ui components and keep transitions brief with reduced-motion support.

## Authentication fixes required before merge

- [ ] Require a random, single-use setup token alongside the initial password. Store the token in a local file with mode `0600`, consume it on successful setup, and verify that the default password alone cannot initialize the dashboard behind a proxy.
- [ ] Add password rotation after setup. Verify the current password, update its bcrypt hash, rotate the session, and verify that the old password and session are rejected.
- [ ] Replace the global login throttle with bounded per-client backoff. Trust forwarded client addresses only from explicitly trusted proxies. Test isolated backoff, successful login after the retry window, and proxy-header handling.
- [ ] Cover all three fixes in Go tests and the Playwright authentication flow.

## Pre-merge verification

| Viewport | What to check |
| --- | --- |
| Phone · 360 × 800 and 390 × 844 | Navigation, metric cards, table scrolling, dialogs, and forms with the on-screen keyboard open |
| Tablet · 768 × 1024 and 1024 × 768 | Provider grid and the Requests and API Keys tables in portrait and landscape |
| Laptop · 1366 × 768 | Visual hierarchy, request details, and the edit-limits form |
| Desktop · 1920 × 1080 | Content width, chart readability, and spacing between panels |

- [ ] Capture screenshots of Sign in, password setup, Overview, Requests, Providers, Models, and API Keys. Record the viewport and commit for each result.
- [ ] Run Playwright against the Go backend with a temporary SQLite database. Test first-time setup, password persistence after restart, signing in again, and logout.
- [ ] Test live request delivery via SSE, reconnect, search and pagination while new logs arrive, and empty states.
- [ ] Test creating, editing, and revoking API keys. Compare displayed usage with the backend calculation and verify that invalid input is rejected.
- [ ] Run Go tests and build the frontend. Open the dashboard directly from the Go binary to verify embedded assets, deep links, and SSE.

## Current status

The desktop demo was reviewed in Chromium. Page-by-page screenshots and responsive browser testing across viewports are pending. The TanStack migration, Form and Zod integration, embedded build, and final build checks have yet to be verified in the PR.
