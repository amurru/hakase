# Plan: Discord channel transport (DC)

Spec: [spec.md](spec.md) — tasks: [tasks.md](tasks.md) — issue
[#71](https://github.com/amurru/hakase/issues/71).

## Phase A — config + service seams (no behavior change)

A1. `internal/config`: `DiscordChannelConfig` (`enabled *bool`,
    `bot_token`, `allowed_user_ids []int64` with snowflake
    parse+range check, `pairing_code`) + `ApplyDefaults`/`Validate`/
    `EnabledWithToken` + `HAKASE_DISCORD_*` env + `HasChannelEnv` +
    `config.json.example` rows. Telegram paths untouched.
A2. `internal/channel/service.go`: per-transport `agentrun` drivers
    (label map; `Register` takes the label, default `Name()`).
    Telegram registers `"telegram"` — byte-identical behavior.
A3. `redact.go` audit for the new token/env names (extend if missed).

## Phase B — transport core (offline-testable)

B1. `internal/channel/discord/discord.go`: `Bot` struct (`Name()`
    `"discord"`, `Run(ctx)` gateway lifecycle via discordgo with
    reconnect/backoff, `Deps` mirroring telegram's), session interface
    seam (`SessionAPI` covering send/edit/react/open-DM) so tests never
    touch the network.
B2. Dispatch: DM-only `MESSAGE_CREATE` → auth gate (shared
    `Authenticator`, `channel="discord"`) → `pair`/`new`/`stop`/
    `status`/`sessions`/`use` commands → `startRun` (busy check,
    `resolveSession`, `RunManager`, `agentrun` driver, runView).
    Guild/own-bot/textless messages dropped with reasons logged.
B3. `MarkdownToDiscord` + 2000-char chunking (unit-tested, table rows
    for HTML-isms, fences, long splits).

## Phase C — runs, gates, push, pairing

C1. runView: `agentrun.EventSink` streaming with ≥5s edit pacing,
    overflow follow-ups, final chunked sends, receipt reaction,
    bridge mirror to the web canvas (phone→web parity with telegram).
C2. Gate buttons: approve/deny + clarify choices + free-text Other,
    immediate ACK + verdict edit, first-wins vs web, 30m choice cache
    with GC (mirror `callbacks.go`).
C3. All five `PushHandler` methods with discord destination resolvers
    (paired/allowed DMs, gate routing, running chats).
C4. Pairing: `pair <code>`, how-to-pair reply, deny cooldown reuse.

## Phase D — wiring, QA, docs

D1. `web.go`: start discord beside telegram on the shared service;
    `hakase channels` CLI audit + dual-transport status/pair-code.
D2. Tests: command/auth/gating/chunking unit tests with the session
    seam + synthetic gateway events; config validation rows; service
    label test (both drivers, correct labels). Manual sign-off with a
    real bot token + account (not in CI/sandbox): pair → run →
    approve-via-button → push delivery; recorded as open DoD if
    credentials unavailable.
D3. Docs: CHANGELOG entry, roadmap Tier-2 item 3 marked shipped on
    merge, #71 closed, #20 tick via comment.
D4. Full suite: `gofmt`, `go vet ./...`, `go test ./...` (+ `-race`
    on channel/agent), `pnpm test` backstop, dependency audit
    (`go list` CGO check on the new dep, `go mod why`).

## Risks

- discordgo API drift vs the fetched docs: pin the version in go.mod,
  verify handler signatures at implementation, record the version in
  tasks.
- Discord edit pacing vs telegram's 2s pump: 5s floor is conservative;
  if runs feel dead, interim "still working" follow-ups (not edits).
- Snowflake >MaxInt64 far-future: parse rejects loudly; widening to
  string IDs would be a state migration — today's 7x headroom says no.
- Two transports sharing one Service: driver map keeps tracing
  correct; a discord crash must not take telegram's goroutine (the
  existing per-channel panic guard covers it — verify in tests).
