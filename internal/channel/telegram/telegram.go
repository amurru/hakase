// Package telegram implements the Telegram bot transport for hakase's
// communication-channel subsystem (github.com/go-telegram/bot, long polling).
// It mirrors the Hermes-agent gateway patterns: deny-by-default pairing,
// per-chat session binding, one run per chat with a live status message
// (editMessageText), approvals/clarifications answered via inline keyboards,
// and push notifications for cron/task lifecycle. Text and photo captions
// are accepted inbound; group chats, voice, and webhooks are deferred.
package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ChannelName is the transport identifier used in state keys.
const ChannelName = "telegram"

// api is the subset of *tgbot.Bot the transport uses; tests substitute a fake.
type api interface {
	Start(ctx context.Context)
	SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error)
	GetFile(ctx context.Context, params *tgbot.GetFileParams) (*models.File, error)
	SetMyCommands(ctx context.Context, params *tgbot.SetMyCommandsParams) (bool, error)
}

// Bot is the Telegram transport. It satisfies channel.Channel (lifecycle) and
// channel.PushHandler (bridge event sink).
type Bot struct {
	api      api
	token    string
	auth     *channel.Authenticator
	runs     *channel.RunManager
	driver   *agentrun.Driver
	sessions *hakasesession.SessionService
	store    *state.Store
	approval interfaces.ApprovalResponder
	clarify  interfaces.ClarifyResponder
	log      channel.LogFunc

	limiterMu sync.Mutex
	nextSend  map[int64]time.Time // per-chat outbound pacing

	pendingMu    sync.Mutex
	pendingOther map[int64]pendingClarify // chatID -> clarify awaiting free text

	clarifyMu  sync.Mutex
	clarifyCtx map[string]clarifyChoice // gateID -> choices for callbacks

	mediaMu    sync.Mutex
	mediaGroup map[string]*mediaGroupBuf // media_group_id -> buffered photos
}

// pendingClarify is a clarify prompt waiting for the user's free-text answer.
type pendingClarify struct {
	id        string
	createdAt time.Time
}

// clarifyChoice remembers a clarify prompt's choices so callback buttons
// (which carry only an index) can answer with the choice text.
type clarifyChoice struct {
	choices   []string
	createdAt time.Time
}

// Deps wires the transport to the channel service and config.
type Deps struct {
	Service *channel.Service
	Config  config.TelegramChannelConfig
	Log     channel.LogFunc
}

// New constructs the Telegram transport and registers its command menu.
func New(d Deps) (*Bot, error) {
	logFn := d.Log
	if logFn == nil {
		logFn = func(string, ...any) {}
	}
	svc := d.Service
	b := &Bot{
		token:        strings.TrimSpace(d.Config.BotToken),
		auth:         channel.NewAuthenticator(svc.Store(), ChannelName, d.Config.AllowedUserIDs, d.Config.PairingCode),
		runs:         svc.Runs(),
		driver:       svc.Driver(),
		sessions:     svc.Sessions(),
		store:        svc.Store(),
		approval:     svc.ApprovalResponder(),
		clarify:      svc.ClarifyResponder(),
		log:          logFn,
		nextSend:     map[int64]time.Time{},
		pendingOther: map[int64]pendingClarify{},
		clarifyCtx:   map[string]clarifyChoice{},
		mediaGroup:   map[string]*mediaGroupBuf{},
	}

	api, err := tgbot.New(b.token, tgbot.WithDefaultHandler(b.handleUpdate))
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	b.api = api
	return b, nil
}

// Name satisfies channel.Channel.
func (b *Bot) Name() string { return ChannelName }

// Run starts long polling; it blocks until ctx is cancelled. A 409 from
// Telegram (another poller on this token, e.g. a second hakase instance)
// surfaces as an error the Service logs loudly.
func (b *Bot) Run(ctx context.Context) error {
	if err := b.registerCommands(context.WithoutCancel(ctx)); err != nil {
		b.log("telegram: command menu registration failed: %v", err)
	}
	if !b.auth.HasAnyUser() {
		code, err := b.auth.EnsurePairingCode()
		if err != nil {
			b.log("telegram: cannot persist pairing code: %v", err)
		} else {
			b.log("telegram: no users paired yet. In Telegram, send /start %s to your bot to pair (code valid %d minutes; `hakase channels pair-code` issues a fresh one).", code, int(channel.PairingCodeTTL.Minutes()))
		}
	}
	b.log("telegram: long polling started")
	b.api.Start(ctx)
	// Start blocks until ctx is cancelled and reports poll errors itself.
	return ctx.Err()
}

// registerCommands sets the bot's command menu.
func (b *Bot) registerCommands(ctx context.Context) error {
	_, err := b.api.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "new", Description: "Start a new session"},
			{Command: "sessions", Description: "List recent sessions"},
			{Command: "status", Description: "Show current session and run state"},
			{Command: "tasks", Description: "Show the task board"},
			{Command: "cron", Description: "List cron jobs (/cron run <name> to trigger)"},
			{Command: "stop", Description: "Cancel the running agent turn"},
			{Command: "notify", Description: "Toggle completion notifications (/notify on|off)"},
			{Command: "id", Description: "Show your Telegram user/chat id"},
			{Command: "help", Description: "Show help"},
		},
	})
	return err
}

// handleUpdate is the single entry point for all Telegram updates.
func (b *Bot) handleUpdate(ctx context.Context, _ *tgbot.Bot, update *models.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.log("telegram: panic handling update: %v", r)
		}
	}()
	switch {
	case update.CallbackQuery != nil:
		b.handleCallback(ctx, update.CallbackQuery)
	case update.Message != nil:
		b.handleMessage(ctx, update.Message)
	}
}

// handleMessage routes one inbound message through auth, commands, clarify
// free-text, photos, and finally plain prompts.
func (b *Bot) handleMessage(ctx context.Context, m *models.Message) {
	if m == nil || m.From == nil {
		return
	}
	// v1: private chats only. Group handling (mentions, topics) is deferred.
	if m.Chat.Type != models.ChatTypePrivate {
		return
	}
	userID := m.From.ID

	if strings.HasPrefix(m.Text, "/") {
		b.handleCommand(ctx, m)
		return
	}

	if !b.auth.IsAllowed(userID) {
		// Never echo the pairing code here: an unauthenticated stranger must
		// not be able to summon it. The code lives on the server console and
		// `hakase channels pair-code`.
		if b.auth.DenyReplyAllowed(userID) {
			b.sendText(ctx, m.Chat.ID, "🔒 Unauthorized. Pair with <code>/start &lt;code&gt;</code> — the code is printed on the server console (or <code>hakase channels pair-code</code>).", nil)
		}
		return
	}

	// A pending clarify "Other" answer consumes the next text message.
	if p := b.takePendingOther(m.Chat.ID); p != nil {
		answer := strings.TrimSpace(m.Text)
		if answer == "" && m.Caption != "" {
			answer = strings.TrimSpace(m.Caption)
		}
		if answer != "" {
			b.respondClarify(ctx, m.Chat.ID, p.id, []string{answer}, nil)
		}
		return
	}

	if len(m.Photo) > 0 {
		b.handlePhoto(ctx, m)
		return
	}

	if m.Text == "" {
		b.sendText(ctx, m.Chat.ID, "🤷 I can handle text messages and photo captions here (voice/files are not supported yet).", nil)
		return
	}

	b.startRun(ctx, m.Chat.ID, m.Text, nil, nil, nil)
}

// chatKey returns the state key for a chat.
func chatKey(chatID int64) string { return state.ChatKey(ChannelName, chatID) }

// takePendingOther pops the pending free-text clarify for a chat, if any and
// recent.
func (b *Bot) takePendingOther(chatID int64) *pendingClarify {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	p, ok := b.pendingOther[chatID]
	if !ok {
		return nil
	}
	delete(b.pendingOther, chatID)
	if time.Since(p.createdAt) > 10*time.Minute {
		return nil
	}
	return &p
}

// setPendingOther records a clarify awaiting free text.
func (b *Bot) setPendingOther(chatID int64, id string) {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	b.pendingOther[chatID] = pendingClarify{id: id, createdAt: time.Now()}
}
