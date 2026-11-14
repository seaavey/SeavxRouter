# Contributing to SeavxRouter

Thanks for taking the time to contribute. This document covers how to set up the
project, the rules that keep it safe to use, and what a good pull request looks
like.

By participating you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Ground rules

1. **Never commit credentials.** No tokens, refresh tokens, API keys, cookies,
   session IDs, personal email addresses, or real `auth.jsonc` content — not in
   code, tests, fixtures, screenshots, commit messages, or issues.
2. **Do not paste secrets into issues or pull requests.** Redact tokens and use
   placeholders such as `gsk_REDACTED`. Security problems go through
   [SECURITY.md](SECURITY.md), not the public tracker.
3. **Keep changes focused.** A bug fix should not include unrelated refactors,
   formatting churn, or dependency upgrades.
4. **Respect provider terms.** Contributions whose purpose is to bypass
   billing, quotas, access controls, or rate limits will be rejected.

## Development setup

Requirements:

- Go 1.27 or newer
- Git

```bash
git clone https://github.com/seaavey/SeavxRouter.git
cd SeavxRouter
go build ./...
go test ./...
```

The project intentionally has no dotenv loader and no build-time code
generation. Configuration is done with CLI flags; credentials live in
`~/.seavx/auth.jsonc` and are created with `seavxrouter login`.

## Before you open a pull request

Run all of these locally and make sure they pass:

```bash
gofmt -l .          # must print nothing
go vet ./...
go build ./...
go test ./...
```

If your change affects streaming, tool calls, or provider translation, also
exercise the relevant endpoint against a mock upstream (see the `httptest`-based
tests in `internal/completions` and `internal/providers`).

## Code guidelines

- Prefer the standard library. Add a dependency only when it clearly earns its
  place.
- Keep packages small and focused; provider-specific behavior belongs in that
  provider's package.
- Wrap errors with context using `%w`, and use the shared error helpers in
  `internal/backend` so quota and auth failures stay detectable.
- Write table-driven tests. Use `httptest.Server` for upstream behavior; never
  call a real provider from a test.
- Use fake credentials in tests (`gsk_test`, `vbk_test`, `pt-test`). They must
  be obviously invalid and short.
- Do not add environment-variable indirection for configuration unless it is
  unavoidable; flags are the supported interface.
- Comments should explain non-obvious constraints, not restate the code.

## Adding a provider

1. Create `internal/providers/<provider>/` with an engine that implements
   `backend.BackendAPI` and, when the provider exposes it, `backend.Checker`.
2. Implement `Login` and store credentials through `internal/auth.AuthStore`.
3. Register the provider in `main.go` (login switch, backend wiring,
   `providerNames`, `groupAuths`) and in `internal/multi/multi.go` for routing.
4. Add the model prefix (`<provider>/<model>`) to the routing docs in
   `README.md`.
5. Add tests with a mock upstream covering catalog fetch, chat, streaming, and
   usage reporting.
6. Confirm the credential type in `internal/auth/bearerOnlyProviders` if the
   credential never refreshes.

## Commit messages

Use the imperative mood and a short subject line:

```text
Add Groq streaming usage passthrough
Fix quota rotation when the last account fails
Document hidden-model blocklist
```

Explain the *why* in the body when it is not obvious. Keep one logical change
per commit.

## Pull request checklist

- [ ] `gofmt`, `go vet`, `go build`, and `go test ./...` pass.
- [ ] New behavior is covered by tests.
- [ ] Documentation updated when flags, endpoints, or providers change.
- [ ] No credentials, personal data, or generated binaries included.
- [ ] The PR description explains the problem and the approach.

## CI and releases

Every push to `main` and every pull request runs `gofmt`, `go vet`, the test
suite with the race detector, and a cross-build for Linux, macOS, and Windows.
Pushing a tag that matches `v*` builds release binaries for amd64 and arm64,
generates SHA-256 checksums, and publishes a GitHub Release.

## License

By contributing, you agree that your contributions are licensed under the
[MIT License](LICENSE).
