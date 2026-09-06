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
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/config"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"

	"google.golang.org/genai"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ChannelName is the transport identifier used in state keys.
const ChannelName = "telegram"

// api is the subset of *tgbot.Bot the transport uses; tests substitute a fake.
type api interface {
	Start(ctx context.Context)
	GetWebhookInfo(ctx context.Context) (*models.WebhookInfo, error)
	SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error)
	EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error)
	AnswerCallbackQuery(ctx context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error)
	GetFile(ctx context.Context, params *tgbot.GetFileParams) (*models.File, error)
	SetMyCommands(ctx context.Context, params *tgbot.SetMyCommandsParams) (bool, error)
	SetMessageReaction(ctx context.Context, params *tgbot.SetMessageReactionParams) (bool, error)
	PinChatMessage(ctx context.Context, params *tgbot.PinChatMessageParams) (bool, error)
	UnpinChatMessage(ctx context.Context, params *tgbot.UnpinChatMessageParams) (bool, error)
	DeleteMessage(ctx context.Context, params *tgbot.DeleteMessageParams) (bool, error)
	EditForumTopic(ctx context.Context, params *tgbot.EditForumTopicParams) (bool, error)
}

// Bot is the Telegram transport. It satisfies channel.Channel (lifecycle) and
// channel.PushHandler (bridge event sink).
type Bot struct {
	api      api
	token    string
	auth     *channel.Authenticator
	runs     *channel.RunManager
	driver   runTurner
	sessions *hakasesession.SessionService
	store    *state.Store
	approval interfaces.ApprovalResponder
	clarify  interfaces.ClarifyResponder
	log      channel.LogFunc

	limiterMu sync.Mutex
	nextSend  map[conv]time.Time // per-conversation outbound pacing

	pendingMu    sync.Mutex
	pendingOther map[conv]pendingClarify // conv -> clarify awaiting free text

	// pins enables the Hermes-style prompt pin for the duration of a run
	// (channels.telegram.pins).
	pins bool

	healMu           sync.Mutex
	lastConflictHeal time.Time // last conflict-triggered healing attempt
	// deleteWebhookFn performs the actual deleteWebhook call; a seam so
	// tests can simulate flaky deletions. Defaults to deleteWebhookDirect,
	// plain HTTP GET - the library's deleteWebhook returned empty bodies
	// deterministically in the field while every other method worked.
	deleteWebhookFn func(ctx context.Context) error

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

// runTurner drives one agent turn; *agentrun.Driver satisfies it. A seam so
// tests can script turns instead of booting the ADK runner.
type runTurner interface {
	RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink)
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
		pins:         d.Config.Pins,
		nextSend:     map[conv]time.Time{},
		pendingOther: map[conv]pendingClarify{},
		clarifyCtx:   map[string]clarifyChoice{},
		mediaGroup:   map[string]*mediaGroupBuf{},
	}

	api, err := tgbot.New(b.token,
		tgbot.WithDefaultHandler(b.handleUpdate),
		// Route poller/API errors through our logger and let a 409
		// webhook conflict trigger the healing path (see handleBotError).
		tgbot.WithErrorsHandler(b.handleBotError),
	)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	b.deleteWebhookFn = b.deleteWebhookDirect
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
		b.log("command menu registration failed: %v", err)
	}
	b.clearStaleWebhook(context.WithoutCancel(ctx))
	if !b.auth.HasAnyUser() {
		code, err := b.auth.EnsurePairingCode()
		if err != nil {
			b.log("cannot persist pairing code: %v", err)
		} else {
			b.log("no users paired yet. In Telegram, send /start %s to your bot to pair (code valid %d minutes; `hakase channels pair-code` issues a fresh one).", code, int(channel.PairingCodeTTL.Minutes()))
		}
	}
	b.log("long polling started")
	b.api.Start(ctx)
	// Start blocks until ctx is cancelled and reports poll errors itself.
	return ctx.Err()
}

// conflictHealCooldown spaces conflict-triggered healing attempts; Telegram
// repeats the 409 on every poll (~5s) and each heal does two API round-trips.
const conflictHealCooldown = 30 * time.Second

// clearStaleWebhook deletes any webhook left on the bot token by a previous
// integration, verifying the result and retrying up to three times. hakase
// is polling-only: while a webhook is active, Telegram rejects every
// getUpdates with 409 Conflict and the bot appears completely silent.
// Pending updates are kept.
func (b *Bot) clearStaleWebhook(ctx context.Context) {
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		info, err := b.api.GetWebhookInfo(ctx)
		if err == nil && (info == nil || info.URL == "") {
			return // clean token: nothing to do
		}
		if err == nil && info != nil {
			b.log("stale webhook detected on this token (%s, %d pending updates) - deleting it so long polling can start", info.URL, info.PendingUpdateCount)
		}
		if err := b.deleteWebhookFn(ctx); err != nil {
			b.log("deleteWebhook attempt %d failed: %v", attempt, err)
		}

		// Verify before retrying: a successful delete needs no backoff sleep.
		if info, err := b.api.GetWebhookInfo(ctx); err == nil && (info == nil || info.URL == "") {
			return
		}

		if attempt >= maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}

	// Still active after retries: either the API is flaky from here or
	// something else is actively managing this token (it would re-register
	// its webhook no matter how often we delete).
	if info, err := b.api.GetWebhookInfo(ctx); err == nil && info != nil && info.URL != "" {
		b.log("WARNING: webhook %s is still active after %d attempts. Manual fix: open https://api.telegram.org/bot<token>/deleteWebhook once. If it keeps coming back, another bot platform is still using this token - give hakase its own token via @BotFather (/revoke or /token).", info.URL, maxAttempts)
	}
}

// deleteWebhookDirect calls deleteWebhook with plain HTTP GET, bypassing the
// library client: in the field the library's POST returned empty bodies for
// this one method (every other method worked) and the failure was opaque.
// GET is the same request a browser makes for the manual fix. The token
// stays in the URL and is never logged; errors carry status + a body snippet.
func (b *Bot) deleteWebhookDirect(ctx context.Context) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/deleteWebhook?drop_pending_updates=false", b.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var apiResp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return fmt.Errorf("HTTP %d, undecodable body %q", resp.StatusCode, truncateRunes(string(body), 200))
	}
	if !apiResp.OK {
		return fmt.Errorf("HTTP %d, telegram said: %s", resp.StatusCode, apiResp.Description)
	}
	return nil
}

// handleBotError is the tgbot errors handler: poll/API errors are logged
// through our logger (replacing the library's [TGBOT] output), and a 409
// webhook conflict re-runs the healing path with a cooldown.
func (b *Bot) handleBotError(err error) {
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "webhook is active") {
		b.healWebhookConflict()
		return
	}
	b.log("api error: %v", err)
}

// healWebhookConflict runs clearStaleWebhook at most once per cooldown.
func (b *Bot) healWebhookConflict() {
	b.healMu.Lock()
	if time.Since(b.lastConflictHeal) < conflictHealCooldown {
		b.healMu.Unlock()
		return
	}
	b.lastConflictHeal = time.Now()
	b.healMu.Unlock()

	b.log("getUpdates rejected: a webhook is active on this token - retrying cleanup")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	b.clearStaleWebhook(ctx)
}

// registerCommands sets the bot's command menu.
func (b *Bot) registerCommands(ctx context.Context) error {
	_, err := b.api.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "new", Description: "Start a new session (in a topic: resets it)"},
			{Command: "sessions", Description: "List recent sessions"},
			{Command: "use", Description: "Bind this chat/topic to a session by id"},
			{Command: "topic", Description: "Topics mode: /topic, /topic off, /topic <id>"},
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
			b.log("panic handling update: %v", r)
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
	// v1: private chats only. Group handling (mentions) is deferred; forum
	// topics inside the DM are the topics-mode conversations.
	if m.Chat.Type != models.ChatTypePrivate {
		return
	}
	userID := m.From.ID
	c := b.effectiveConv(m)
	b.log("message from user %d (%s), %d chars, thread %d", userID, m.From.Username, len(m.Text), c.threadID)

	if strings.HasPrefix(m.Text, "/") {
		b.handleCommand(ctx, c, m)
		return
	}

	if !b.auth.IsAllowed(userID) {
		// Never echo the pairing code here: an unauthenticated stranger must
		// not be able to summon it. The code lives on the server console and
		// `hakase channels pair-code`.
		if b.auth.DenyReplyAllowed(userID) {
			b.sendText(ctx, c, "🔒 Unauthorized. Pair with <code>/start &lt;code&gt;</code> — the code is printed on the server console (or <code>hakase channels pair-code</code>).", nil, false)
		}
		b.log("denied message from unpaired user %d", userID)
		return
	}

	// A pending clarify "Other" answer consumes the next text message.
	if p := b.takePendingOther(c); p != nil {
		answer := strings.TrimSpace(m.Text)
		if answer == "" && m.Caption != "" {
			answer = strings.TrimSpace(m.Caption)
		}
		if answer != "" {
			b.respondClarify(ctx, c, p.id, []string{answer}, nil)
		}
		return
	}

	// Topics-mode lobby: the root area takes commands but not prompts.
	if b.inLobby(c) {
		b.sendText(ctx, c, lobbyHint, nil, false)
		return
	}

	if len(m.Photo) > 0 {
		b.handlePhoto(ctx, m)
		return
	}

	if m.Text == "" {
		b.sendText(ctx, c, "🤷 I can handle text messages and photo captions here (voice/files are not supported yet).", nil, false)
		return
	}

	b.startRun(ctx, c, m.ID, m.Text, nil, nil, nil)
}

// lobbyHint points at the ✚ composer button; commands keep working in the root.
const lobbyHint = "💬 Topics mode is on — this root area is a lobby. Tap the ✚ / “All Messages” composer button to open a topic and prompt there. Commands (like /status and /new) still work here; /topic off returns to a single chat."

// takePendingOther pops the pending free-text clarify for a conversation, if
// any and recent.
func (b *Bot) takePendingOther(c conv) *pendingClarify {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	p, ok := b.pendingOther[c]
	if !ok {
		return nil
	}
	delete(b.pendingOther, c)
	if time.Since(p.createdAt) > 10*time.Minute {
		return nil
	}
	return &p
}

// setPendingOther records a clarify awaiting free text.
func (b *Bot) setPendingOther(c conv, id string) {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	b.pendingOther[c] = pendingClarify{id: id, createdAt: time.Now()}
}
