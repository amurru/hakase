package telegram

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"time"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/channel"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// esc HTML-escapes agent-derived text interpolated into HTML bodies (the
// plain-text send fallback covers anything that still slips through).
func esc(s string) string { return html.EscapeString(s) }

// Destination model (mirrors Hermes' "important vs all" tiers):
//   - approvals/clarifications go to every allowed user's DM (they block a
//     run, so they must reach the owner even with /notify off);
//   - cron/task completions go only to chats with /notify on;
//   - delegation outcomes go only to chats currently watching a run.

// promptDestinations lists the root-area conversations that should receive
// blocking prompts: paired users plus statically allowlisted IDs (DM chat id
// == user id). Gate routing may override this with the run's bound topic.
func (b *Bot) promptDestinations() []conv {
	seen := map[int64]bool{}
	var out []conv
	for _, u := range b.store.Get().PairedUsers {
		if u.Channel == ChannelName && !seen[u.UserID] {
			seen[u.UserID] = true
			out = append(out, rootConv(u.UserID))
		}
	}
	for _, id := range b.auth.AllowedIDs() {
		if !seen[id] {
			seen[id] = true
			out = append(out, rootConv(id))
		}
	}
	return out
}

// notifyDestinations lists root-area conversations with /notify on.
func (b *Bot) notifyDestinations() []conv {
	var out []conv
	prefix := ChannelName + ":"
	for key, chat := range b.store.Get().Chats {
		if !chat.Notify || !strings.HasPrefix(key, prefix) {
			continue
		}
		if id, err := strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64); err == nil {
			out = append(out, rootConv(id))
		}
	}
	return out
}

// runningDestinations lists the conversations with an in-flight run in
// that conversation. Run keys are thread-scoped ("telegram:<chatID>:
// <threadID>", formerly "telegram:<chatID>"): each thread is its own
// conversation, so delegation pushes go back to the thread that is
// watching the run instead of collapsing to the chat root (a collapse
// would both miss topic watchers and leak a topic's delegation into the
// root area). Keys that do not parse are skipped; unparsable legacy keys
// fall back to the chat root.
func (b *Bot) runningDestinations() []conv {
	seen := map[conv]bool{}
	var out []conv
	prefix := ChannelName + ":"
	for _, key := range b.runs.ActiveChatKeys() {
		rest := strings.TrimPrefix(key, prefix)
		chatPart, threadPart, hasThread := strings.Cut(rest, ":")
		id, err := strconv.ParseInt(chatPart, 10, 64)
		if err != nil {
			continue
		}
		if !hasThread {
			// Legacy chat-scoped key: the root area watches the run.
			c := rootConv(id)
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
			continue
		}
		threadID, err := strconv.Atoi(threadPart)
		if err != nil {
			continue
		}
		c := conv{chatID: id, threadID: threadID}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// gateConv resolves the conversation a gate prompt should go to: the one
// whose thread (client-created or topics-mode) or root area is bound to the
// gate's session. ok is false when the session is unknown or unbound, so the
// caller falls back to the fan-out.
func (b *Bot) gateConv(sessionID string) (conv, bool) {
	if sessionID == "" {
		return conv{}, false
	}
	st := b.store.Get()
	for key, th := range st.Threads {
		if th.SessionID != sessionID {
			continue
		}
		c, ok := parseThreadKey(key)
		if !ok || c.threadID == 0 {
			continue
		}
		return c, true
	}
	for key, chat := range st.Chats {
		if chat.SessionID != sessionID {
			continue
		}
		rest, ok := strings.CutPrefix(key, ChannelName+":")
		if !ok {
			continue
		}
		if id, err := strconv.ParseInt(rest, 10, 64); err == nil {
			return rootConv(id), true
		}
	}
	return conv{}, false
}

// parseThreadKey parses "<channel>:<chatID>:<threadID>" back into a conv.
func parseThreadKey(key string) (conv, bool) {
	rest, ok := strings.CutPrefix(key, ChannelName+":")
	if !ok {
		return conv{}, false
	}
	chatStr, threadStr, found := strings.Cut(rest, ":")
	if !found {
		return conv{}, false
	}
	chatID, err := strconv.ParseInt(chatStr, 10, 64)
	if err != nil {
		return conv{}, false
	}
	threadID, err := strconv.Atoi(threadStr)
	if err != nil {
		return conv{}, false
	}
	return conv{chatID: chatID, threadID: threadID}, true
}

// ApprovalPrompt implements channel.PushHandler: the prompt goes to the
// conversation that started the run (notification on — it blocks), with the
// paired-users fan-out as the fallback for unknown sessions.
func (b *Bot) ApprovalPrompt(sessionID, id, tool, risk, reason, command string) {
	kb := approvalKeyboard(id)
	var txt strings.Builder
	txt.WriteString("🔒 <b>Approval needed</b>\n")
	fmt.Fprintf(&txt, "tool: <code>%s</code>", esc(tool))
	if risk != "" {
		fmt.Fprintf(&txt, " · risk: %s", esc(risk))
	}
	txt.WriteString("\n")
	if command != "" {
		// S10: redact-only (the prompt still delivers, secrets never
		// do) via the unified redact-then-truncate helper.
		txt.WriteString("<pre>" + esc(util.RedactTruncate(command, 512)) + "</pre>\n")
	}
	if reason != "" {
		txt.WriteString(esc(util.RedactTruncate(reason, 512)) + "\n")
	}
	body := strings.TrimRight(txt.String(), "\n")
	if c, ok := b.gateConv(sessionID); ok {
		b.sendText(context.Background(), c, body, kb, false)
		return
	}
	for _, c := range b.promptDestinations() {
		b.sendText(context.Background(), c, body, kb, false)
	}
}

// ClarifyPrompt implements channel.PushHandler; routing mirrors ApprovalPrompt.
func (b *Bot) ClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	// S13/H7 clarify redaction: the question and choices travel to
	// approval cards, so secret-shaped content is redacted before
	// display (redact-then-truncate, same order as audit). The stored
	// choices keep the original text so the answer still maps.
	if len(choices) > 0 {
		b.rememberClarifyChoices(id, choices)
	}
	redChoices := make([]string, len(choices))
	for i, c := range choices {
		redChoices[i] = util.RedactTruncate(c, 48)
	}
	var txt strings.Builder
	txt.WriteString("❓ <b>Question</b>\n")
	txt.WriteString(esc(util.RedactTruncate(question, 1500)))
	kb := clarifyKeyboard(id, redChoices)
	body := strings.TrimRight(txt.String(), "\n")
	if c, ok := b.gateConv(sessionID); ok {
		b.sendText(context.Background(), c, body, kb, false)
		return
	}
	for _, c := range b.promptDestinations() {
		b.sendText(context.Background(), c, body, kb, false)
	}
}

// pushWithholdThrottle bounds the withheld-push log volume (S8 audit
// NFR): one line per recipient per interval, never one row per tick per
// user. The gate itself stays per-evaluation, never sticky.
var pushWithholdThrottle = struct {
	sync.Mutex
	last map[string]time.Time
}{last: map[string]time.Time{}}

// pushWithholdInterval is the minimum gap between withheld-push log
// lines for one recipient.
const pushWithholdInterval = 5 * time.Minute

// flowHydrated bounds registry hydration to one disk load per session per
// process: every push re-evaluates the gate, but a cold registry (restart)
// must not hit disk on every tick of every fan-out.
var flowHydrated = struct {
	sync.Mutex
	done map[string]bool
}{done: map[string]bool{}}

// flowEgress evaluates the channel egress gate for sessionID, hydrating a
// cold registry from disk first: after a restart the in-memory labels are
// gone while the persisted labels are on disk, and without hydration a
// non-narrowed session would read as unregistered and the push would
// wrongly withhold (fail-closed). Hydration prevents that false withhold;
// a session missing from disk or unloadable stays fail-closed via
// CheckChannelEgress's unregistered-session path. Hydration is read-only
// (the shared helper registers the persisted label verbatim / Meet-merges
// with a live entry, never Fold) and bounded to one Load per session per
// process. The skip is logged distinctly from withhold lines (one line per
// session per process, so no throttle is needed).
func (b *Bot) flowEgress(sessionID, recipient string) (bool, string) {
	if sessionID != "" && !hakasesession.DefaultRegistry.IsInitialized(sessionID) {
		flowHydrated.Lock()
		tried := flowHydrated.done[sessionID]
		if !tried {
			flowHydrated.done[sessionID] = true
		}
		flowHydrated.Unlock()
		if !tried && b.sessions != nil {
			if err := channel.HydrateFlowLabel(b.sessions, sessionID); err != nil {
				b.log("flow push skip hydrate session=%s: %v", sessionID, err)
			}
		}
	}
	return hakaseagent.CheckChannelEgress(sessionID, ChannelName, recipient)
}

// pushAllowed reports whether a push to c may be delivered under the
// recipient chat's own session label (S8): per-recipient gating,
// read-only (no fold, never sticky - each event re-evaluates). Unknown
// sessions and v1 policies allow via the CheckChannelEgress passthrough;
// narrowed sessions with a send_message policy withhold that recipient
// only, never the rest of the fan-out.
func (b *Bot) pushAllowed(c conv) bool {
	return b.pushAllowedForProducer("", c)
}

// pushAllowedForProducer gates a lifecycle push carrying producer taint
// (S8/H1): the push withholds per recipient when EITHER the producer
// session or the recipient chat's session fails send_message under the
// installed v2 policy. Checking both sides separately is the conservative
// (Meet-optional) form of gating on Meet(producer, recipient): any
// narrowing on either side withholds that recipient only, never the rest
// of the fan-out. An empty producer keeps the recipient-only check (cron
// and shared-board producers are session-less; v1 parity without v2).
func (b *Bot) pushAllowedForProducer(producerSessionID string, c conv) bool {
	if producerSessionID != "" {
		if allow, reason := b.flowEgress(producerSessionID, chatKey(c.chatID)); !allow {
			pushWithholdThrottle.Lock()
			recipient := chatKey(c.chatID)
			last := pushWithholdThrottle.last[recipient]
			if time.Since(last) >= pushWithholdInterval {
				pushWithholdThrottle.last[recipient] = time.Now()
				pushWithholdThrottle.Unlock()
				b.log("flow push withheld chat=%d producer=%s: %s", c.chatID, producerSessionID, reason)
			} else {
				pushWithholdThrottle.Unlock()
			}
			return false
		}
	}
	return b.pushAllowedRecipient(c)
}

// pushAllowedRecipient is the recipient-side check: the recipient
// conversation's own session label gates delivery (S8). Thread
// conversations gate on their thread binding (boundSessionID); the
// lobby (root area) inherits the narrowest thread binding in the chat,
// so a narrowed topic cannot be bypassed via the lobby fan-out. The
// thread scan runs whenever thread bindings exist for the chat (an
// empty Threads map costs nothing to range over), independent of the
// TopicsMode flag: client-created threads are conversations even when
// the chat never ran /topic. Read-only (no fold, never sticky - each
// event re-evaluates). Unknown sessions and v1 policies allow via the
// CheckChannelEgress passthrough.
func (b *Bot) pushAllowedRecipient(c conv) bool {
	recipient := chatKey(c.chatID)
	if c.threadID != 0 {
		sessionID := b.boundSessionID(c)
		if allow, reason := b.flowEgress(sessionID, recipient); !allow {
			pushWithholdThrottle.Lock()
			last := pushWithholdThrottle.last[recipient]
			if time.Since(last) >= pushWithholdInterval {
				pushWithholdThrottle.last[recipient] = time.Now()
				pushWithholdThrottle.Unlock()
				b.log("flow push withheld chat=%d thread=%d: %s", c.chatID, c.threadID, reason)
			} else {
				pushWithholdThrottle.Unlock()
			}
			return false
		}
		return true
	}
	st := b.store.Get()
	prefix := ChannelName + ":" + strconv.FormatInt(c.chatID, 10) + ":"
	for key, th := range st.Threads {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if th.SessionID == "" {
			continue
		}
		if allow, reason := b.flowEgress(th.SessionID, recipient); !allow {
			pushWithholdThrottle.Lock()
			last := pushWithholdThrottle.last[recipient]
			if time.Since(last) >= pushWithholdInterval {
				pushWithholdThrottle.last[recipient] = time.Now()
				pushWithholdThrottle.Unlock()
				b.log("flow push withheld chat=%d lobby via %s: %s", c.chatID, key, reason)
			} else {
				pushWithholdThrottle.Unlock()
			}
			return false
		}
	}
	var sessionID string
	if st := b.store.Get(); st.Chats != nil {
		if chat, ok := st.Chats[chatKey(c.chatID)]; ok {
			sessionID = chat.SessionID
		}
	}
	if allow, reason := b.flowEgress(sessionID, recipient); !allow {
		pushWithholdThrottle.Lock()
		last := pushWithholdThrottle.last[recipient]
		if time.Since(last) >= pushWithholdInterval {
			pushWithholdThrottle.last[recipient] = time.Now()
			pushWithholdThrottle.Unlock()
			b.log("flow push withheld chat=%d: %s", c.chatID, reason)
		} else {
			pushWithholdThrottle.Unlock()
		}
		return false
	}
	return true
}

// CronEvent implements channel.PushHandler.
func (b *Bot) CronEvent(producerSessionID, status, jobID, name, summary, outputPath string) {
	icon := map[string]string{"completed": "✅", "failed": "❌", "silent": "🔇"}[status]
	if icon == "" {
		return
	}
	label := name
	if label == "" {
		label = jobID
	}
	// S12: redact-only (the push still delivers per-recipient gating
	// below; secrets never do) via the unified helper. Read-only: no
	// fold, no binding - each event re-evaluates.
	body := fmt.Sprintf("%s cron <b>%s</b> %s", icon, esc(util.RedactTruncate(label, 200)), status)
	if summary != "" {
		body += "\n" + esc(util.RedactTruncate(summary, 800))
	}
	if outputPath != "" {
		body += "\n<code>" + esc(util.RedactTruncate(outputPath, 0)) + "</code>"
	}
	for _, c := range b.notifyDestinations() {
		if !b.pushAllowedForProducer(producerSessionID, c) {
			continue
		}
		b.sendText(context.Background(), c, body, nil, false)
	}
}

// TaskEvent implements channel.PushHandler.
func (b *Bot) TaskEvent(producerSessionID, action string, id, title, status string) {
	if action != "completed" && action != "failed" {
		return
	}
	icon := "✅"
	if action == "failed" {
		icon = "❌"
	}
	// S12: redact-only (see CronEvent).
	body := fmt.Sprintf("%s task <b>%s</b> is now %s\n<code>%s</code>", icon, esc(util.RedactTruncate(title, 200)), status, id)
	for _, c := range b.notifyDestinations() {
		if !b.pushAllowedForProducer(producerSessionID, c) {
			continue
		}
		b.sendText(context.Background(), c, body, nil, false)
	}
}

// DelegationEvent implements channel.PushHandler: finished delegations are
// only interesting to chats already watching a run.
func (b *Bot) DelegationEvent(producerSessionID, status, taskID, agent, message string) {
	chats := b.runningDestinations()
	if len(chats) == 0 {
		return
	}
	icon := "📦"
	switch status {
	case "failed":
		icon = "❌"
	case "timed_out":
		icon = "⏱️"
	}
	body := fmt.Sprintf("%s sub-agent <b>%s</b> %s", icon, esc(agent), status)
	if message != "" {
		// S12: redact-only (see CronEvent).
		body += "\n" + esc(util.RedactTruncate(message, 400))
	}
	for _, c := range chats {
		if !b.pushAllowedForProducer(producerSessionID, c) {
			continue
		}
		b.sendText(context.Background(), c, body, nil, false)
	}
}
