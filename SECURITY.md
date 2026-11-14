# Security Policy

## Supported versions

SeavxRouter is experimental. Security fixes are applied to the `main` branch
only; there are no long-lived release branches. Build from the latest commit.

## Reporting a vulnerability

**Do not open a public issue for security problems.** Public reports can expose
other users before a fix is available.

Please use GitHub's private vulnerability reporting:

1. Open the repository on GitHub.
2. Go to **Security** > **Report a vulnerability**.
3. Describe the issue with enough detail to reproduce it.

If private reporting is unavailable, contact the maintainer privately through
GitHub (`@seaavey`) and ask for a secure channel.

Include, when possible:

- A description of the issue and its impact
- Steps to reproduce or a minimal proof of concept
- Affected endpoints, flags, or provider adapters
- The version or commit hash you tested
- Any suggested mitigation

You will receive an acknowledgement as soon as possible. Please give the
maintainer reasonable time to investigate and ship a fix before public
disclosure.

## Never include credentials

Do not attach real tokens, refresh tokens, cookies, session identifiers, or
`auth.jsonc` contents to a report. Redact values and use placeholders. If you
believe a credential was exposed, revoke it with the provider first.

## Security model and known considerations

SeavxRouter is a local, single-user gateway. Understanding the model helps you
deploy it safely:

- **No authentication by default.** The server binds to `127.0.0.1` and accepts
  every request unless `-api-key` is set. Anyone who can reach the port can
  spend your provider quota. Never expose an instance without an API key.
- **Plaintext credential storage.** `~/.seavx/auth.jsonc` is written with mode
  `0600` and holds tokens in plaintext so they can be refreshed. Protect the
  host and the file; do not sync it to untrusted storage.
- **Local request logs.** The most recent 1,000 requests are stored in
  `~/.seavx/usage.db`, including response bodies (request bodies are not
  persisted). Treat the database as sensitive. Streaming responses are not
  captured as response bodies.
- **TLS is not terminated by SeavxRouter.** For remote access, put it behind a
  reverse proxy that terminates TLS and forwards to loopback.
- **Provider trust.** Credentials are sent only to the provider selected for a
  request, but the upstream provider's own security and retention practices
  apply to your prompts.

## Scope

In scope:

- Authentication or authorization bypass on `/v1/*`
- Credential leakage through logs, API responses, error messages, or files with
  weak permissions
- Request smuggling, SSRF, path traversal, or command injection in the router
- Denial of service caused by malformed requests that a normal client could not
  trigger
- Supply-chain issues in direct dependencies

Out of scope:

- Account bans, rate limits, or provider-side enforcement
- Vulnerabilities in third-party providers or their APIs
- Misconfiguration that leaves the server exposed without an API key
- Issues that require an already-compromised host or credentials

## Dependency security

Dependencies are kept intentionally small. Run `go vuln check` (or
`govulncheck ./...`) and `go test ./...` before releases, and report known
vulnerable dependency versions through the same private channel.

## License

This policy supplements the warranty and liability terms of the
[MIT License](LICENSE). See also [DISCLAIMER.md](DISCLAIMER.md).
