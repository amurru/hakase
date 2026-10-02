// Package discord implements hakase's Discord channel transport: a DM-only
// bot over the gateway websocket (spec docs/discord/spec.md).
//
// It mirrors the Telegram transport's shape (auth gate, commands, runs,
// gates, push) against Discord-shaped primitives: DMs instead of private
// chats, buttons over gateway interactions instead of callback queries,
// 2000-char chunks and ≥5s edit pacing instead of Telegram's limits.
// v1 is text-only: guild messages dropped, attachments declined, no
// voice, no threads, no slash-command registration (see DC-013).
package discord

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/web/sse"

	"github.com/bwmarrin/discordgo"
	"google.golang.org/genai"
)

// ChannelName identifies the transport in state keys and trace labels.
const ChannelName = "discord"

// Deps wires the transport to the channel service and config.
type Deps struct {
	Service *channel.Service
	Config  config.DiscordChannelConfig
	Log     channel.LogFunc
}

// SessionAPI is the Discord surface the Bot uses, as an interface so
// tests script sends/edits/reactions/interactions without the network.
// The live implementation wraps *discordgo.Session.
type SessionAPI interface {
	// OpenDM returns (creating if needed) the DM channel with a user.
	OpenDM(userID string) (string, error)
	// Send posts a text message, returning its ID.
	Send(channelID, text string) (string, error)
	// Edit replaces a message's text.
	Edit(channelID, messageID, text string) error
	// React adds a receipt reaction to a message.
	React(channelID, messageID, emoji string) error
	// RespondInteraction answers a component interaction. update=true
	// edits the source message; false sends an ephemeral follow-up.
	RespondInteraction(inter *discordgo.Interaction, update bool, text string) error
	// SendButtons posts text with an action row of buttons, returning the
	// message ID. Styles: "primary", "danger", "secondary".
	SendButtons(channelID, text string, buttons []Button) (string, error)
	// Me returns the bot's own user ID (to drop echoes).
	Me() string
}

// Button is one message-component button.
type Button struct {
	Label    string
	Style    string
	CustomID string
}

// liveSession adapts *discordgo.Session to SessionAPI.
type liveSession struct {
	dg *discordgo.Session
}

func (l *liveSession) OpenDM(userID string) (string, error) {
	ch, err := l.dg.UserChannelCreate(userID)
	if err != nil {
		return "", err
	}
	return ch.ID, nil
}

func (l *liveSession) Send(channelID, text string) (string, error) {
	m, err := l.dg.ChannelMessageSend(channelID, text)
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

func (l *liveSession) Edit(channelID, messageID, text string) error {
	_, err := l.dg.ChannelMessageEdit(channelID, messageID, text)
	return err
}

func (l *liveSession) React(channelID, messageID, emoji string) error {
	return l.dg.MessageReactionAdd(channelID, messageID, emoji)
}

func (l *liveSession) SendButtons(channelID, text string, buttons []Button) (string, error) {
	comps := make([]discordgo.MessageComponent, 0, len(buttons))
	for _, btn := range buttons {
		style := discordgo.SecondaryButton
		switch btn.Style {
		case "primary":
			style = discordgo.PrimaryButton
		case "danger":
			style = discordgo.DangerButton
		}
		comps = append(comps, discordgo.Button{
			Label:    btn.Label,
			Style:    style,
			CustomID: btn.CustomID,
		})
	}
	m, err := l.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:    text,
		Components: []discordgo.MessageComponent{discordgo.ActionsRow{Components: comps}},
	})
	if err != nil {
		return "", err
	}
	return m.ID, nil
}

func (l *liveSession) RespondInteraction(inter *discordgo.Interaction, update bool, text string) error {
	resp := &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Content: text},
	}
	if update {
		resp.Type = discordgo.InteractionResponseUpdateMessage
	}
	if text == "" {
		resp.Type = discordgo.InteractionResponseDeferredMessageUpdate
		resp.Data = nil
	}
	return l.dg.InteractionRespond(inter, resp)
}

func (l *liveSession) Me() string {
	if l.dg.State == nil || l.dg.State.User == nil {
		return ""
	}
	return l.dg.State.User.ID
}

// pendingClarify is a clarify prompt waiting for free-text ("Other").
type pendingClarify struct {
	id        string
	createdAt time.Time
}

// clarifyChoice remembers a clarify prompt's choices so buttons (which
// carry only an index) answer with the choice text.
type clarifyChoice struct {
	choices   []string
	createdAt time.Time
}

// Bot is the Discord transport: a channel.Channel and PushHandler.
type Bot struct {
	token    string
	auth     *channel.Authenticator
	runs     *channel.RunManager
	driver   runTurner
	sessions *hakasesession.SessionService
	store    *state.Store
	bridge   *sse.EventBridge
	approval interfaces.ApprovalResponder
	clarify  interfaces.ClarifyResponder
	log      channel.LogFunc

	dg     *discordgo.Session
	sender SessionAPI

	mu           sync.Mutex
	nextSend     map[int64]time.Time
	dmCache      map[int64]string
	pendingOther map[int64]pendingClarify
	clarifyCtx   map[string]clarifyChoice
}

// runTurner drives one agent turn; *agentrun.Driver satisfies it. A seam so
// tests can script turns instead of booting the ADK runner.
type runTurner interface {
	RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink)
}

// New constructs the Discord transport. The token must be non-empty
// (config validation enforces this; New guards anyway for direct use).
func New(d Deps) (*Bot, error) {
	token := strings.TrimSpace(d.Config.BotToken)
	if token == "" {
		return nil, fmt.Errorf("discord: bot_token is empty")
	}
	logFn := d.Log
	if logFn == nil {
		logFn = func(string, ...any) {}
	}
	svc := d.Service
	dg, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, fmt.Errorf("discord: %w", err)
	}
	// DM-only v1: no privileged intents (DC-003). DM content arrives
	// without MESSAGE_CONTENT; guild traffic is never subscribed.
	dg.Identify.Intents = discordgo.IntentsDirectMessages | discordgo.IntentsDirectMessageReactions
	b := &Bot{
		token:        token,
		auth:         channel.NewAuthenticator(svc.Store(), ChannelName, d.Config.AllowedUserIDs, d.Config.PairingCode),
		runs:         svc.Runs(),
		driver:       svc.DriverFor(ChannelName),
		sessions:     svc.Sessions(),
		store:        svc.Store(),
		bridge:       svc.Bridge(),
		approval:     svc.ApprovalResponder(),
		clarify:      svc.ClarifyResponder(),
		log:          logFn,
		dg:           dg,
		nextSend:     map[int64]time.Time{},
		dmCache:      map[int64]string{},
		pendingOther: map[int64]pendingClarify{},
		clarifyCtx:   map[string]clarifyChoice{},
	}
	b.sender = &liveSession{dg: dg}
	return b, nil
}

// Name satisfies channel.Channel.
func (b *Bot) Name() string { return ChannelName }

// Run opens the gateway and blocks until ctx is cancelled. discordgo owns
// reconnect/resume; a second instance on the same token invalidates this
// session (Discord closes with 4000-level codes), which surfaces here.
func (b *Bot) Run(ctx context.Context) error {
	b.dg.AddHandler(b.onMessageCreate)
	b.dg.AddHandler(b.onInteractionCreate)
	if err := b.dg.Open(); err != nil {
		return fmt.Errorf("discord: gateway open: %w", err)
	}
	defer b.dg.Close()
	if !b.auth.HasAnyUser() {
		code, err := b.auth.EnsurePairingCode()
		if err != nil {
			b.log("cannot persist pairing code: %v", err)
		} else {
			b.log("no users paired yet. DM the bot `pair %s` to pair (code valid %d minutes; `hakase channels pair-code` issues a fresh one).", code, int(channel.PairingCodeTTL.Minutes()))
		}
	}
	b.log("gateway connected (DM-only, no privileged intents)")
	<-ctx.Done()
	return ctx.Err()
}

// parseSnowflake parses a decimal Discord snowflake into the int64 ID
// space shared with the state store. Rejects non-numeric and overflow
// (DC-004: no silent truncation, ever).
func parseSnowflake(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid discord id %q", s)
	}
	if uint64(id) > math.MaxInt64 {
		return 0, fmt.Errorf("discord id %q overflows int64", s)
	}
	return id, nil
}
