// interactions.go - gate buttons over gateway interactions (DC-007).
//
// custom_id scheme: "dc:<kind>:<gateID>:<payload>" where kind is "a"
// (approval, payload 1/0) or "c" (clarify, payload choice index or
// "other"). Late clicks on resolved gates get an ephemeral notice;
// first-responder-wins against the web UI comes from the shared
// responders. ACK happens synchronously in the handler (one REST call,
// inside Discord's 3s window).
package discord

import (
	"fmt"
	"strconv"
	"strings"

	hakaseagent "amurru/hakase/internal/agent"
	"amurru/hakase/internal/interfaces"

	"github.com/bwmarrin/discordgo"
)

func (b *Bot) onInteractionCreate(_ *discordgo.Session, ic *discordgo.InteractionCreate) {
	if ic == nil || ic.Interaction == nil {
		return
	}
	inter := ic.Interaction
	if inter.Type != discordgo.InteractionMessageComponent {
		return
	}
	mcd := inter.MessageComponentData()
	parts := strings.Split(mcd.CustomID, ":")
	if len(parts) != 4 || parts[0] != "dc" {
		_ = b.sender.RespondInteraction(inter, false, "Unknown action.")
		return
	}
	kind, gateID, payload := parts[1], parts[2], parts[3]
	userID, err := interactionUser(inter)
	if err != nil || !b.auth.IsAllowed(userID) {
		_ = b.sender.RespondInteraction(inter, false, "🔒 Not paired with this bot.")
		return
	}
	switch kind {
	case "a":
		b.answerApproval(inter, userID, gateID, payload == "1")
	case "c":
		b.answerClarify(inter, userID, gateID, payload)
	default:
		_ = b.sender.RespondInteraction(inter, false, "Unknown action.")
	}
}

// interactionUser extracts the clicking user's snowflake.
func interactionUser(inter *discordgo.Interaction) (int64, error) {
	id := ""
	if inter.Member != nil && inter.Member.User != nil {
		id = inter.Member.User.ID
	} else if inter.User != nil {
		id = inter.User.ID
	}
	if id == "" {
		return 0, fmt.Errorf("interaction without user")
	}
	return parseSnowflake(id)
}

// answerApproval delivers an approve/deny verdict and edits the prompt.
func (b *Bot) answerApproval(inter *discordgo.Interaction, userID int64, gateID string, approved bool) {
	if b.approval == nil {
		_ = b.sender.RespondInteraction(inter, false, "Approvals unavailable (no responder wired).")
		return
	}
	if !b.approval.RespondApproval(gateID, approved) {
		_ = b.sender.RespondInteraction(inter, false, "Already resolved or expired.")
		return
	}
	hakaseagent.AuditApprovalAnswer(gateID, "", approved, fmt.Sprintf("discord:%d", userID), "discord")
	verdict := "✅ Approved"
	if !approved {
		verdict = "❌ Denied"
	}
	_ = b.sender.RespondInteraction(inter, true, promptText(inter)+"\n\n**"+verdict+"**")
}

// answerClarify delivers a choice or arms free-text capture.
func (b *Bot) answerClarify(inter *discordgo.Interaction, userID int64, gateID, payload string) {
	if payload == "other" {
		b.setPendingOther(userID, gateID)
		_ = b.sender.RespondInteraction(inter, false, "Reply with your answer as text.")
		return
	}
	idx, err := strconv.Atoi(payload)
	if err != nil {
		_ = b.sender.RespondInteraction(inter, false, "Unknown action.")
		return
	}
	choice, ok := b.takeClarifyChoice(gateID, idx)
	if !ok {
		_ = b.sender.RespondInteraction(inter, false, "This question has expired — answer as text in the DM.")
		return
	}
	b.respondClarify(inter, userID, gateID, []string{choice})
}

func (b *Bot) respondClarify(inter *discordgo.Interaction, userID int64, gateID string, answer []string) {
	delivered := b.clarify != nil && b.clarify.RespondClarify(gateID, interfaces.ClarifyResponse{Answer: answer})
	if !delivered {
		_ = b.sender.RespondInteraction(inter, false, "That question was already resolved or expired.")
		return
	}
	_ = b.sender.RespondInteraction(inter, true, promptText(inter)+"\n\n✔ "+strings.Join(answer, ", "))
}

// takeClarifyChoice resolves one choice by index (single-use).
func (b *Bot) takeClarifyChoice(gateID string, idx int) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.clarifyCtx[gateID]
	if !ok || idx < 0 || idx >= len(c.choices) {
		return "", false
	}
	delete(b.clarifyCtx, gateID)
	return fmt.Sprintf("%d. %s", idx+1, c.choices[idx]), true
}

// promptText recovers the source prompt for verdict edits.
func promptText(inter *discordgo.Interaction) string {
	if inter.Message != nil {
		return inter.Message.Content
	}
	return ""
}
