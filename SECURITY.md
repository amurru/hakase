# Security Policy

Hakase is a high-autonomy agent: it runs commands, edits files, and talks to
the network on the machine that hosts it. Security reports are taken
seriously and handled privately.

## Supported versions

| Version | Supported |
|---|---|
| latest release (see [Releases](https://github.com/amurru/hakase/releases)) | ✅ |
| older releases | ❌ — upgrade |

The project is pre-1.0 and moves fast; please test against the latest
`develop` branch before reporting.

## Reporting a vulnerability

**Please do not open a public issue for security problems.**

Use [GitHub's private vulnerability reporting](https://github.com/amurru/hakase/security/advisories/new)
for anything that could be exploited. Include:

- What you can make the agent do that it should not be able to do
- The config surface involved (sandbox mode, web auth, channels, MCP)
- Steps or a proof of concept; affected commit or release tag
- Your assessment of severity and impact

## Scope notes

Areas of particular interest:

- **Sandbox escape** — path confinement, bubblewrap namespaces, and the
  exec toolset's guardrails (`internal/sandbox/`)
- **Web auth** — JWT handling, cookies, rate limiting, file API boundaries
  (`internal/web/`, `internal/auth/`)
- **Untrusted-data handling** — tool output wrapping and injection routes
  from fetched content, MCP tool results, and channel messages
- **Secret handling** — redaction in `internal/util/redact.go` and the
  SkillOpt-Sleep harvest path

Out of scope: reports about markdown skill content doing prompt-injection on
a hostile LLM backend, and scanner findings without a working exploit path
(this codebase ships skill scripts with allowlist/confinement guards that
pattern-match common scanner rules — e.g. SSRF allowlists and
`realpath`/`commonpath` confinement — please verify the guard before filing).

## What to expect

Acknowledgment within a few days, a fix or mitigation assessment, and credit
in the release notes unless you prefer to remain anonymous.
