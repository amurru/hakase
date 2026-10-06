# Tasks: Discord channel transport (DC)

Discord DM bot over the gateway (discordgo), mirroring Telegram.
Spec: [spec.md](spec.md) — plan: [plan.md](plan.md) — issue
[#71](https://github.com/amurru/hakase/issues/71).
House rule: tick boxes in the same PR that lands the work.

Legend: `[BE]` backend/Go, `[QA]` tests, `[DOCS]` docs.

## Phase A — config + service seams

- [x] **T1.1 [BE]** `internal/config`: `DiscordChannelConfig` +
      defaults/validate/`EnabledWithToken` + `HAKASE_DISCORD_*` env +
      `HasChannelEnv` + `config.json.example`. Spec: DC-004.
- [x] **T1.2 [BE]** service per-transport drivers (label map);
      telegram registers `"telegram"`, behavior unchanged. Spec: DC-010.
- [x] **T1.3 [QA]** config validation rows (off default,
      enabled-without-token fails, snowflake parse/range, env wins) +
      service dual-label test. Redact audit: `HAKASE_DISCORD_BOT_TOKEN`
      matches the generic env-credential rule — no change. Spec:
      DC-004, DC-010, DC-012.

## Phase B — transport core

- [x] **T2.1 [BE]** `internal/channel/discord/discord.go`: `Bot`,
      `Run` gateway lifecycle, `SessionAPI` seam, DM-only dispatch,
      auth gate, `pair/new/stop/status/sessions/use`. Spec: DC-005,
      DC-006, DC-011.
- [x] **T2.2 [BE]** `MarkdownToDiscord` + 2000-char chunking.
      Spec: DC-009.
- [x] **T2.3 [QA]** dispatch/auth/command/chunk unit tests (session
      seam + synthetic events; no network). Spec: DC-006, DC-009.

## Phase C — runs, gates, push, pairing

- [x] **T3.1 [BE]** runView streaming (≥5s edit pacing, overflow
      follow-ups, chunked finals, receipt, bridge mirror).
      Spec: DC-009.
- [x] **T3.2 [BE]** gate buttons (ACK-in-3s, verdict edit,
      free-text Other, choice cache + GC). Spec: DC-007.
- [x] **T3.3 [BE]** all five push methods + destination resolvers.
      Spec: DC-008.
- [x] **T3.4 [QA]** run/gate/push/pairing tests on the seam
      (first-wins, ACK path, routing). Caught live: reply text with
      `<code>` is eaten by the HTML-strip (now `pair CODE`).
      Spec: DC-007, DC-008, DC-011.

## Phase D — wiring, QA, docs

- [x] **T4.1 [BE]** `web.go` dual start; `hakase channels` CLI
      dual-transport audit. Spec: DC-011.
- [ ] **T4.2 [QA]** manual sign-off with real bot token (pair → run
      → button approval → push); recorded here. Full suite green
      (`gofmt`, `vet`, `go test ./...`, `-race`, `pnpm` backstop) +
      dep audit (CGO-free, pinned discordgo version recorded here:
      v0.29.0 — module tree contains zero `import "C"` files;
      `runtime/cgo` in the dep graph is Go's standard net-linked stub,
      present in every networked binary). `gofmt`/`vet` clean,
      `go test ./...` green, `pnpm` 115/115. `-race`: discord, state,
      agent, knowledge clean; PRE-EXISTING races in `channel`
      (events/router test) + `telegram` reproduce on clean develop
      (verified via `git stash -u`), out of scope — follow-up issue
      filed. Live-bot sign-off OPEN: no Discord token/account on this
      host. Spec: DC-012.
- [x] **T4.3 [DOCS]** CHANGELOG, roadmap Tier-2 item 3, #71 close,
      #20 tick via comment. Explicit deferrals: guilds, voice,
      attachments, slash registration, presence, Slack. Spec: DC-013.
