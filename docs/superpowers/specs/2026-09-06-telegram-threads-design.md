# Telegram threads & Hermes-style interactivity — design

Date: 2026-09-06
Status: approved (pending implementation plan)

## Context

The Telegram transport (`internal/channel/telegram`) works but the
interactivity feels awkward: one status bubble with tool lines, answers
delivered only as end-of-run chunks, one bound session per DM, one run per DM,
and every progress message buzzing the phone. We also fixed a send-limiter
livelock the same day (see `send.go` `waitTurn`); that fix is a prerequisite
and is already in place.

The reference UX is [Hermes Agent by Nous
Research](https://hermes-agent.nousresearch.com/docs/user-guide/messaging/telegram),
whose Telegram gateway feels comfortable because of a few concrete patterns:

- **Forum topics as conversations** — Bot API 9.4 "topics in private chats";
  each topic is an isolated session (`chat_id:thread_id → session`), the root
  DM area is a lobby, topics auto-rename to the session title, `/topic <id>`
  restores an old session into a topic.
- **Answers stream in place** — one message is progressively edited as tokens
  arrive (default), with a small status line edited alongside.
- **Progress is silent** — `disable_notification=true` on all progress; only
  final answers and approvals notify ("important" mode).
- **Reactions as receipts** — 👀 processing, ✅ delivered, ❌ error.
- **Pin the incoming message** during the turn, unpin when done.

Our library (`go-telegram/bot` v1.25.0) supports all needed primitives:
`message_thread_id` on sends/edits, `CreateForumTopic`/`EditForumTopic`,
`SetMessageReaction`, `PinChatMessage`/`UnpinChatMessage`, `DeleteMessage`.

## Goals

1. Conversation threads in the DM: one Telegram topic = one hakase session,
   with parallel runs across topics.
2. Streamed answers (edit-based) with a quiet status line, replacing the
   status-bubble + final-chunks model.
3. Hermes comfort details: silent progress, reaction receipts, optional pin
   during turn (config toggle, default off).
4. Full backward compatibility: DMs without topics behave exactly as today.

## Non-goals (this round)

- Voice notes STT/TTS — wanted, deferred to its own follow-up project.
- Rich messages (tables), native draft streaming (Bot API 9.5), group forum
  topics with per-topic skills, multi-user/team mode.

## Design

### 1. Conversation identity and state

A conversation ("conv") is `{chatID, threadID}`. `threadID == 0` is the DM's
root area (also the legacy path). Every chat-scoped map and store key moves
from `chatID`/`chatKey(chatID)` to a thread-scoped key:

- pacing map, pending free-text clarify map: keyed by conv (in memory).
- `RunManager` keys: `telegram:<chatID>:<threadID>` — no code change needed,
  the keys just become thread-scoped; one run per conversation, parallel runs
  across conversations fall out naturally.
- Session bindings persist per thread in `~/.hakase/channels.json`:

```jsonc
{
  "chats":   { "telegram:50701365": { "notify": true, "topics_mode": false } },
  "threads": { "telegram:50701365:1234": { "session_id": "sess_…", "title": "…" } }
}
```

`state.Chat` gains `TopicsMode bool`. `state` gains a `threads` map
(`state.Thread{SessionID, Title}`). The legacy `chats[...].session_id` binding
keeps working: when `topics_mode` is false the transport uses exactly the old
code path (root area, one bound session).

Normalization: a message whose `message_thread_id` equals the DM's general
topic id, or 0/absent, maps to thread 0 (lobby/root).

### 2. Topics lifecycle

- `/topic` — enables `topics_mode` for the DM; replies in the root area with a
  short instruction (create topics via the ✚ / "All Messages" composer
  button). From then on the root area is a **lobby**: commands work, plain
  prompts get a one-line hint pointing at the ✚ button.
- First prompt in an unbound topic — creates a session titled from the prompt
  (existing `resolveSession` logic), binds it to the thread, and renames the
  topic to the session title (`EditForumTopic`).
- `/topic <session-id-prefix>` — binds the current topic to an existing
  session (same prefix rules as `/use`) and re-titles the topic.
- `/new [title]` inside a topic — fresh session bound to that topic
  (re-titles). In the lobby with topics on: hint to use a topic. With topics
  off: today's behavior (rebind root).
- `/topic off` — disables topics mode; legacy behavior returns; thread
  bindings stay persisted for when it is re-enabled.
- `/stop`, `/status`, `/sessions`, `/use` inside a topic act on / mark that
  topic's session and run.

### 3. Answer streaming (runView)

`runStatus` is reworked into a run view with two artifacts:

- **Answer message** — created when the **first answer text arrives** (not at
  turn start), then progressively edited as more deltas arrive: at most one
  edit per ~2s, only when content changed, markdown → Telegram HTML via the
  existing converter (`MarkdownToTelegramHTML`), plain-text fallback on parse
  errors (existing). When the streamed text would exceed ~3,800 chars, the
  current message is finalized and a new streaming message continues the
  remainder (the final render always equals today's `ChunkReply` output
  quality).
- **Status line** — a separate silent message, edited in place, showing
  `⚙ <last tool> · <elapsed>` while nothing has streamed yet. Once streaming
  starts it stops competing (no further edits until the end), and at
  completion becomes a compact `✓ 54s · 39.8k tok` line (❌ + error summary on
  failure). If the status message was never created (API hiccup), completion
  info is skipped silently — never blocks the reply.

Terminal behavior: final edit renders the complete answer; the last pacing
slot is respected; the run's `runs.Finish` happens after delivery as today
(the livelock fix makes this safe).

### 4. Silence, receipts, pin

- Notification model (Telegram edits never notify; only message creation
  does): the **answer message creation notifies** — that is the one buzz per
  turn, and it happens exactly when the answer starts (Hermes' "important"
  mode). Everything else — status line, streaming edits, completion line — is
  silent. Approvals/clarifies are created with notification **on** (they
  block). A run that ends without text (or with an error) only edits the
  silent status line and sets the ❌ reaction, so it never buzzes.
- Reactions on the user's prompt message: 👀 at turn start, ✅ after the final
  render, ❌ on failure (`SetMessageReaction`; replaces atomically).
- Pin: when `channels.telegram.pins` is true, silently pin the prompt message
  at turn start and unpin at completion.

### 5. Transport mechanics

- `sendText`/`editText` take the conv and set `MessageThreadID`; a `silent`
  flag replaces ad-hoc nil-markup coupling where needed.
- `api` seam additions: `SetMessageReaction`, `PinChatMessage`,
  `UnpinChatMessage`, `DeleteMessage`, `EditForumTopic` (+ fake
  implementations recording calls).
- Inbound routing: `handleMessage`/`handleCallback` derive the conv from
  `message_thread_id`; commands operate on the current topic; the lobby
  applies lobby rules.
- Rate-limit sanity: streaming edits (2s) vs pacing (1.1s) — streaming rarely
  hits the pacer; the fixed `waitTurn` makes any overlap safe.

### 6. Approval / clarify routing

Gate prompts (approval/clarify) currently fan out to every paired user's DM.
New rule: if the gate's session maps to a bound thread, the prompt is sent to
that conversation (with buttons, notification on); otherwise fall back to
today's fan-out. This needs the bridge gate payloads to carry the session id
(small extension in the web gate senders; the router passes it through).
First-responder-wins is unchanged.

### 7. Config

```jsonc
"channels": { "telegram": {
  "enabled": true, "bot_token": "…",
  "pins": false
} }
```

Only `pins` is new (default false). Stream throttle (2s), silence policy and
reaction set are constants in the transport.

## Error handling

- Topic rename failure (bots may lack rights in edge cases): log and continue;
  binding already persisted.
- Reaction/pin/delete failures: logged, never fatal, never block a run.
- Unbound topic + session store error: existing ⚠️ path, in-topic.
- Legacy clients that never send `message_thread_id`: unchanged behavior.

## Testing

Extend the fake API: thread-aware recording, reaction/pin/delete/topic-rename
counters, per-conv pacing. New tests:

1. Prompt in unbound topic → session created, bound, topic renamed, run
   started against it.
2. Two topics → two parallel runs, distinct sessions; same topic refuses a
   second concurrent run.
3. Lobby: topics on → plain prompt gets the hint; commands still work.
4. `/topic off` → legacy single-session behavior; re-enable keeps bindings.
5. `/topic <prefix>` rebind + re-title; `/new` in topic resets that topic.
6. Streaming: edits throttled and content-ordered; final render is the exact
   converted answer; overflow produces continuation messages.
7. Silence: progress sends carry disable_notification; answer/approval sends
   do not.
8. Reactions: 👀→✅ and 👀→❌ sequences on the right message.
9. Pin toggle: on → pin+unpin around the run; off → no pin calls.
10. No-thread messages → legacy path (regression).
11. Approval routed to the run's topic; unknown session → fan-out fallback.

## Future work

- Voice notes STT/TTS (next candidate project).
- Rich messages (tables), draft streaming, group forum topics with per-topic
  skill bindings, `/bg` background prompts.
