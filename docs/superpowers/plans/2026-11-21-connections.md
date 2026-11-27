# Connections Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use executing-plans or subagent-driven-development to implement task-by-task. Steps use checkbox syntax for tracking.

**Goal:** A configured-provider directory with account details and verified model add/delete management.

**Architecture:** Existing provider and model queries feed directory and detail sibling routes. Shared pure selectors compute account counts and model ownership. Avoid nesting detail under the current page without an Outlet.

**Tech Stack:** React, TypeScript, TanStack Router/Query, existing shadcn UI.

**Spec:** docs/superpowers/specs/2026-11-21-connections-design.md

## Global Constraints
- No quota UI; future `/qouta` is separate.
- Active means `status === 'ready'`; only positive active counts get green text.
- Configured providers only, including custom providers and unavailable accounts.
- Preserve current uncommitted request-table and pagination edits.
- Do not commit, change credentials, or restart production.

## Review Focus
- Unknown provider deep links show not-found rather than unrelated data.
- Missing optional fields and null accounts do not crash.
- Model ownership uses owned_by, including custom providers and slash-containing model IDs.
- Provider and model errors remain independently visible.
- Clipboard failure is reported instead of claiming a copy succeeded.

## Task 1: Directory and shared selection
Files: replace frontend/src/routes/_app.connections.tsx with an Outlet-free sibling route arrangement (rename directory to `_app.connections.index.tsx` if no parent route is kept); create frontend/src/features/connections/provider-data.ts; inspect app-shell breadcrumbs before editing.
- [ ] Add pure selectors for total/ready/unavailable counts and exact owned_by model selection, consuming ProviderQuota and Model from lib/api.ts.
- [ ] Exercise selectors against ready/unavailable/null accounts and custom provider models; verify no foreign models leak.
- [ ] Implement searchable directory with fully clickable accessible Links, account counts, positive active count green, loading/error/empty states.
- [ ] Regenerate routeTree via the existing router plugin/generator; verify index route path remains `/connections`.

## Task 2: Provider detail
Files: create frontend/src/routes/_app.connections.$providerId.tsx (or equivalent sibling trailing-underscore convention to avoid accidental nesting); create frontend/src/features/connections/provider-detail.tsx only if splitting keeps the route focused; update app-shell breadcrumb if required.
- [ ] Resolve provider by exact param and render loading/error/not-found states before detail.
- [ ] Render summary counts and account list with identity fallbacks, raw readable status, optional tier/expiry. No quota values.
- [ ] Render exact provider-owned models, search, model-query errors, empty state, and per-model copy button with success/error feedback using existing toast conventions.
- [ ] Verify direct navigation, back link, keyboard link activation, and mobile overflow.

## Task 3: Verified model management
Files to inspect first: internal/httpapi/hidden.go, internal/httpapi/dashboard.go, internal/providers/upstream/custom.go, internal/providers/upstream/upstream.go, internal/multi/multi.go, main.go. Resolve catalog and routing extension points before choosing new backend interfaces.
- [ ] Add regression tests for strict probe success, quota/auth rejection, malformed HTTP 200, timeout, and upstream HTTP status preservation. Do not reuse ProbeKey's quota-success policy.
- [ ] Implement authenticated model add/delete operations using configured provider credentials only; never accept a browser-supplied upstream URL or expose credentials. Validate exact provider/model identity and use bounded deadlines.
- [ ] Persist manual additions and disabled IDs atomically; test reload/restart persistence, concurrent changes, and add/delete of slash-containing IDs. Failed probes must leave storage untouched.
- [ ] Reuse or extend the existing hiddenBackend kill-switch semantics: deletion removes discovery and HasModel eligibility. Verify chat requests fail with model_not_found and other providers are unaffected. Restoring a deleted model requires a fresh successful probe.
- [ ] Connect detail-page Add model dialog and inline Delete confirmation; show pending state, sanitized upstream error/status or transport cause. Invalidate relevant catalog queries only after confirmed success.

## Task 4: Verification
- [ ] Run `npm run build` from frontend and fix introduced type/build failures.
- [ ] Browser-test directory search, positive/zero active counts, provider navigation/deep links/not-found, null/empty fields, model search and clipboard behavior. Use intercepted API fixtures or isolated auth state, never production secrets.
- [ ] Recheck git diff to ensure request pagination edits remain and no generated credentials or scratch files were added.
- [ ] Report verification and leave changes uncommitted.
