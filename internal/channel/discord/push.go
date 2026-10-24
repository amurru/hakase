// push.go - PushHandler: gate prompts, cron/task/delegation events to DMs.
package discord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/agent"
	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/util"
)

// pairedUsers returns every DM-able user: static allowlist + paired users.
func (b *Bot) pairedUsers() []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, id := range b.auth.AllowedIDs() {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, u := range b.store.Get().PairedUsers {
		if u.Channel == ChannelName && !seen[u.UserID] {
			seen[u.UserID] = true
			out = append(out, u.UserID)
		}
	}
	return out
}

// chatUserID parses a "discord:<id>" chat key back to its user.
func chatUserID(key string) (int64, bool) {
	rest, ok := strings.CutPrefix(key, ChannelName+":")
	if !ok || strings.Contains(rest, ":") {
		return 0, false // threads or foreign transports
	}
	id, err := parseSnowflake(rest)
	if err != nil {
		return 0, false
	}
	return id, true
}

// gateUser routes a gate prompt to the DM that started the run (via the
// session binding); empty when the origin is unknown.
func (b *Bot) gateUser(sessionID string) (int64, bool) {
	if sessionID == "" {
		return 0, false
	}
	for key, ch := range b.store.Get().Chats {
		if ch.SessionID != sessionID {
			continue
		}
		if id, ok := chatUserID(key); ok {
			return id, true
		}
	}
	return 0, false
}

// notifyUsers returns chats with Notify on.
func (b *Bot) notifyUsers() []int64 {
	var out []int64
	for key, ch := range b.store.Get().Chats {
		if !ch.Notify {
			continue
		}
		if id, ok := chatUserID(key); ok {
			out = append(out, id)
		}
	}
	return out
}

// runningUsers returns users with an active run.
func (b *Bot) runningUsers() []int64 {
	var out []int64
	for _, u := range b.pairedUsers() {
		if _, ok := b.runs.Running(runKey(u)); ok {
			out = append(out, u)
		}
	}
	return out
}

// ApprovalPrompt implements channel.PushHandler. S10: redact-only (the
// prompt still delivers, secrets never do) via the unified
// redact-then-truncate helper, with the same 512-rune bound as the
// Telegram transport (parity).
func (b *Bot) ApprovalPrompt(sessionID, id, tool, risk, reason, command string) {
	text := fmt.Sprintf("Approve `%s` (%s)?\n%s\n```\n%s\n```", tool, risk,
		util.RedactTruncate(reason, 512), util.RedactTruncate(command, 512))
	buttons := []Button{
		{Label: "Approve", Style: "primary", CustomID: "dc:a:" + id + ":1"},
		{Label: "Deny", Style: "danger", CustomID: "dc:a:" + id + ":0"},
	}
	if userID, ok := b.gateUser(sessionID); ok {
		b.sendButtons(userID, text, buttons)
		return
	}
	for _, userID := range b.pairedUsers() {
		b.sendButtons(userID, text, buttons)
	}
}

// ClarifyPrompt implements channel.PushHandler.
// S13/H7 clarify redaction (mirrors Telegram): redact-then-truncate.
func (b *Bot) ClarifyPrompt(sessionID, id, question string, choices []string, multiSelect bool) {
	b.rememberClarifyChoices(id, choices)
	var buttons []Button
	for i, c := range choices {
		if i >= 24 {
			break // one row kept for Other; 5x5 grid max
		}
		label := util.RedactTruncate(c, 80)
		buttons = append(buttons, Button{Label: fmt.Sprintf("%d. %s", i+1, label), Style: "secondary", CustomID: fmt.Sprintf("dc:c:%s:%d", id, i)})
	}
	if !multiSelect {
		buttons = append(buttons, Button{Label: "Other (reply as text)", Style: "secondary", CustomID: "dc:c:" + id + ":other"})
	}
	text := util.RedactTruncate(question, 1500)
	if multiSelect {
		text += "\n\n(Multi-select is not supported over buttons — reply with your answers as text.)"
	}
	if userID, ok := b.gateUser(sessionID); ok {
		if multiSelect {
			b.setPendingOther(userID, id)
		}
		b.sendButtons(userID, text, buttons)
		return
	}
	for _, userID := range b.pairedUsers() {
		if multiSelect {
			b.setPendingOther(userID, id)
		}
		b.sendButtons(userID, text, buttons)
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
// must not hit disk on every tick of every fan-out. Mirrors the Telegram
// transport; the load itself goes through the shared channel helper so both
// transports hydrate identically.
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
// process.
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
	return agent.CheckChannelEgress(sessionID, ChannelName, recipient)
}

// pushAllowed reports whether a push to userID may be delivered under
// that user's own session label (S8): per-recipient gating, read-only
// (no fold, never sticky - each event re-evaluates). Unknown sessions
// and v1 policies allow via the CheckChannelEgress passthrough;
// narrowed sessions with a send_message policy withhold that recipient
// only, never the rest of the fan-out.
func (b *Bot) pushAllowed(userID int64) bool {
	return b.pushAllowedForProducer("", userID)
}

// pushAllowedForProducer gates a lifecycle push carrying producer taint
// (S8/H1): the push withholds per recipient when EITHER the producer
// session or the recipient user's session fails send_message under the
// installed v2 policy (conservative Meet-optional form: any narrowing on
// either side withholds that recipient only). An empty producer keeps
// the recipient-only check (session-less producers; v1 parity without
// v2).
func (b *Bot) pushAllowedForProducer(producerSessionID string, userID int64) bool {
	if producerSessionID != "" {
		if allow, reason := b.flowEgress(producerSessionID, fmt.Sprintf("%s:%d", ChannelName, userID)); !allow {
			pushWithholdThrottle.Lock()
			recipient := fmt.Sprintf("%s:%d", ChannelName, userID)
			last := pushWithholdThrottle.last[recipient]
			if time.Since(last) >= pushWithholdInterval {
				pushWithholdThrottle.last[recipient] = time.Now()
				pushWithholdThrottle.Unlock()
				b.log("flow push withheld user=%d producer=%s: %s", userID, producerSessionID, reason)
			} else {
				pushWithholdThrottle.Unlock()
			}
			return false
		}
	}
	return b.pushAllowedRecipient(userID)
}

// pushAllowedRecipient is the recipient-side check: the recipient user's
// own session label gates delivery (S8). Read-only, never sticky.
func (b *Bot) pushAllowedRecipient(userID int64) bool {
	var sessionID string
	if st := b.store.Get(); st.Chats != nil {
		if chat, ok := st.Chats[state.ChatKey(ChannelName, userID)]; ok {
			sessionID = chat.SessionID
		}
	}
	recipient := fmt.Sprintf("%s:%d", ChannelName, userID)
	if allow, reason := b.flowEgress(sessionID, recipient); !allow {
		pushWithholdThrottle.Lock()
		last := pushWithholdThrottle.last[recipient]
		if time.Since(last) >= pushWithholdInterval {
			pushWithholdThrottle.last[recipient] = time.Now()
			pushWithholdThrottle.Unlock()
			b.log("flow push withheld user=%d: %s", userID, reason)
		} else {
			pushWithholdThrottle.Unlock()
		}
		return false
	}
	return true
}

// CronEvent implements channel.PushHandler. S12: redact-only (the push
// still delivers per-recipient gating below; secrets never do) via the
// unified helper. Read-only: no fold, no binding - each event
// re-evaluates.
func (b *Bot) CronEvent(producerSessionID, status, jobID, name, summary, outputPath string) {
	text := fmt.Sprintf("Cron `%s` %s: %s", util.RedactTruncate(name, 200), status, util.RedactTruncate(summary, 800))
	if outputPath != "" {
		text += "\n" + util.RedactTruncate(outputPath, 0)
	}
	for _, userID := range b.notifyUsers() {
		if !b.pushAllowedForProducer(producerSessionID, userID) {
			continue
		}
		b.sendText(context.Background(), userID, text)
	}
}

// TaskEvent implements channel.PushHandler. S12: redact-only (see
// CronEvent).
func (b *Bot) TaskEvent(producerSessionID, action, id, title, status string) {
	text := fmt.Sprintf("Task %s `%s` (%s): %s", action, id, util.RedactTruncate(title, 200), status)
	for _, userID := range b.notifyUsers() {
		if !b.pushAllowedForProducer(producerSessionID, userID) {
			continue
		}
		b.sendText(context.Background(), userID, text)
	}
}

// DelegationEvent implements channel.PushHandler. S12: redact-only (see
// CronEvent).
func (b *Bot) DelegationEvent(producerSessionID, status, taskID, agent, message string) {
	text := fmt.Sprintf("Delegation %s `%s` (%s): %s", status, taskID, agent, util.RedactTruncate(message, 400))
	for _, userID := range b.runningUsers() {
		if !b.pushAllowedForProducer(producerSessionID, userID) {
			continue
		}
		b.sendText(context.Background(), userID, text)
	}
}

// sendButtons posts text with buttons to a user's DM.
func (b *Bot) sendButtons(userID int64, text string, buttons []Button) {
	ch, err := b.openDM(userID)
	if err != nil {
		b.log("open DM for %d: %v", userID, err)
		return
	}
	if _, err := b.sender.SendButtons(ch, text, buttons); err != nil {
		b.log("send buttons to %d: %v", userID, err)
	}
}
