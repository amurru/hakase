# Spec: Discord channel transport (DC)

Second `channel.Channel` transport: a Discord DM bot over the gateway
websocket. Telegram proves the flow; this mirrors it. Issue
[#71](https://github.com/amurru/hakase/issues/71) — Tier-2 item 3
from #20.

## DC-001 Platform choice (recorded, not relitigated)

Discord over Slack. Reasons: (1) connectivity — gateway websocket is
outbound-only, NAT-friendly exactly like Telegram's long polling, while
Slack's primary Events API needs a public HTTPS URL (Socket Mode
removes that but adds workspace-install + two-token complexity);
(2) auth model — one bot token and users DM the bot, mirroring
Telegram's DM + pairing-code flow, versus Slack workspace install +
channel joins; (3) intent cost — DM content is exempt from the
MESSAGE_CONTENT privileged intent (verified in Discord's official
gateway docs, Oct 2026), so v1 needs zero portal approvals. Guild
channels and Slack remain future options; the `Channel` seam supports
either.

## DC-002 Library

discordgo (mature, pure Go — no CGO, keeps the single-binary story).
It owns gateway lifecycle (identify/heartbeat/resume), REST sends, and
interaction responses. Verify at implementation: `go list` shows no CGO
in its tree; if voice deps leak in, pin the no-voice build (v1 sends
no voice).

## DC-003 Intents (minimal, non-privileged)

`DIRECT_MESSAGES (1<<12)` + `DIRECT_MESSAGE_REACTIONS (1<<13)` only.
No `MESSAGE_CONTENT` (DMs exempt — verified), no `GUILD_*` (v1 drops
all guild messages with a debug log). No privileged intents means no
Developer Portal approval and the 4014 close-code class cannot occur.

## DC-004 Configuration (telegram mirror)

`channels.discord`: `enabled *bool` (nil = off), `bot_token`,
`allowed_user_ids []int64` (decimal snowflakes, strict parse +
`<= MaxInt64` reject at load — today's snowflakes ~1.2e18 vs 9.2e18
max, so int64 stays compatible with the shared state store and no
migration is needed), `pairing_code` (static override). Env:
`HAKASE_DISCORD_ENABLED` / `HAKASE_DISCORD_BOT_TOKEN` (env wins) +
`HasChannelEnv` extension. `Validate` (enabled-without-token fails) +
`EnabledWithToken` mirror `config_channels_test.go` rows. STT/TTS
blocks are transport-agnostic already; voice pipeline stays
Telegram-only in v1 (DC-009).

## DC-005 Identity mapping

Discord user ID (DM author, string snowflake) → int64 (parse or
reject). DM channel per user opened via REST for sends. State keys:
`ChatKey("discord", userID)`; DMs have no threads — always thread 0
(telegram maps General(<=1) to root 0; same shape). One-run-per-user
via the shared `RunManager`. Pairing reuses `Authenticator` +
`channels.json` with `channel="discord"` (namespaced keys, no
cross-transport collision).

## DC-006 Inbound (DM-only)

Gateway `MESSAGE_CREATE` in DM channels only; guild messages dropped +
debug-logged; own bot messages dropped; textless messages dropped.
Leading-`/` text treated as commands (telegram convention, documented;
NO slash-command registration in v1 — avoids the registration +
propagation-wait complexity; buttons cover gates). Command surface:
`pair`, `new`, `stop`, `status`, `sessions`, `use` (run-management
subset). Deferred with reasons: `tasks`/`cron`/`notify` (push covers
the read path; management stays Telegram/TUI/web until proven),
`topic` (no Discord threads in v1), `voice` (DC-009). Unpaired users
get the unauthorized reply (never echoes codes) under the existing
deny cooldown. Attachments v1: politely declined ("attachments not yet
supported") — the file pipeline exists for a later slice.

## DC-007 Gates via buttons (no public endpoint)

Approval → Approve/Deny buttons; clarify → choice buttons + free-text
"Other" consumed from the next DM (mirrors telegram's pendingOther).
Verified in Discord's official interactions docs (Oct 2026):
interactions arrive over the gateway by default — no HTTP endpoint,
no signature verification, no public URL. `custom_id` encodes routing
(`d:approve:<gateID>` style, mirroring `a:<gateID>:<0|1>`).
Discord requires ACK within 3s: ACK immediately (deferred update),
then edit the prompt with the verdict like telegram. First-responder
wins vs web (existing responder semantics).

## DC-008 Push (all five methods)

`ApprovalPrompt` (gate-routed to originating DM else fan-out),
`ClarifyPrompt`, `CronEvent`, `TaskEvent`, `DelegationEvent` — to
paired/allowed DM users, mirroring telegram's destination resolvers
(prompt destinations / notify destinations / running chats).

## DC-009 Streaming + formatting (Discord-shaped, not Telegram-shaped)

Discord limit 2000 chars (not 4096): chunk at 2000. Message edits are
rate-limited harder than Telegram: status-message edits at most every
5s (not telegram's 2s pump), overflow splits into follow-ups, final
answer as chunked sends. Markdown passes through as Discord-flavored
markdown (code fences fine); telegram's HTML path does NOT transfer
(no HTML in Discord) — a small `MarkdownToDiscord` transform
(strip/convert HTML-isms, telegram `format.go` stays untouched).
Reactions for run-start receipt (mirrors telegram react); pins
deferred (DM pins need Manage Messagesomatically — out of v1 scope).

## DC-010 Service changes (minimal, telegram-untouched)

`NewService` hardcodes the `agentrun` transport label `"telegram"`
(`service.go:78`). Parameterize: service keeps a per-transport driver
(`map[string]*agentrun.Driver`, same runner/sessions, own label);
`Register` takes the label (default from `Channel.Name()`). One
`Service` runs both transports (already one goroutine per channel).
`runTurner` test seam pattern replicated for the discord bot.

## DC-011 Pairing

`pair <code>` DM command (static code wins else rotating
`EnsurePairingCode`, 15m TTL, same as telegram). Bare `pair` (or any
first DM while unpaired) replies with how-to-pair instructions.
`hakase channels` CLI audited for telegram-isms and extended to show
both transports (leaf `state` package already channel-keyed).

## DC-012 Security

Token via config/env only, never logged (verify `redact.go` covers
the new env names or extend it); pairing codes never echoed; deny
cooldown reused; snowflake parse strict (reject non-numeric +
overflow); DM-only means no guild admin/permissions attack surface;
gateway token is a bearer secret — reconnect path must never log the
identify payload. Voice pipeline untouched.

## DC-013 Non-goals (explicit, not drift)

Guild channels/threads, voice in/out, file attachments inbound,
slash-command registration, presence/activity, message reactions as
input (beyond receipts), Slack.
