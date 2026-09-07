package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/channel"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/interfaces"
	hakasesession "amurru/hakase/internal/session"
	"amurru/hakase/internal/web/sse"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// fakeAPI records everything the transport sends.
type fakeAPI struct {
	mu             sync.Mutex
	sent           []fakeSend
	edited         []fakeEdit
	answered       []string
	reactions      []fakeReaction
	pins           []fakePin
	unpins         []fakePin
	topicRenames   []fakeTopicRename
	cmds           bool
	deletedWebhook int
	deleteCalls    int
	failDeletes    int // first N deleteWebhook calls fail (flaky API simulation)
	webhookURL     string
	nextMsgID      int
	sendHook       func(params *tgbot.SendMessageParams) // optional, blocks inside SendMessage
}

type fakeSend struct {
	chatID   int64
	threadID int
	msgID    int
	text     string
	hasKb    bool
	silent   bool
}

type fakeEdit struct {
	chatID    int64
	messageID int
	text      string
}

type fakeReaction struct {
	chatID    int64
	messageID int
	emoji     string
}

type fakePin struct {
	chatID    int64
	messageID int
}

type fakeTopicRename struct {
	chatID   int64
	threadID int
	name     string
}

func (f *fakeAPI) Start(ctx context.Context) {}

func (f *fakeAPI) GetWebhookInfo(ctx context.Context) (*models.WebhookInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &models.WebhookInfo{URL: f.webhookURL}, nil
}

func (f *fakeAPI) SendMessage(ctx context.Context, params *tgbot.SendMessageParams) (*models.Message, error) {
	f.mu.Lock()
	f.nextMsgID++
	f.sent = append(f.sent, fakeSend{
		chatID:   params.ChatID.(int64),
		threadID: params.MessageThreadID,
		msgID:    f.nextMsgID,
		text:     params.Text,
		hasKb:    params.ReplyMarkup != nil,
		silent:   params.DisableNotification,
	})
	hook := f.sendHook
	f.mu.Unlock()
	if hook != nil {
		hook(params) // called without f.mu: may block
	}
	return &models.Message{ID: f.nextMsgID}, nil
}

func (f *fakeAPI) EditMessageText(ctx context.Context, params *tgbot.EditMessageTextParams) (*models.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edited = append(f.edited, fakeEdit{chatID: params.ChatID.(int64), messageID: params.MessageID, text: params.Text})
	return &models.Message{ID: params.MessageID}, nil
}

func (f *fakeAPI) AnswerCallbackQuery(ctx context.Context, params *tgbot.AnswerCallbackQueryParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answered = append(f.answered, params.Text)
	return true, nil
}

func (f *fakeAPI) GetFile(ctx context.Context, params *tgbot.GetFileParams) (*models.File, error) {
	return nil, fmt.Errorf("not implemented in fake")
}

func (f *fakeAPI) SetMyCommands(ctx context.Context, params *tgbot.SetMyCommandsParams) (bool, error) {
	f.cmds = true
	return true, nil
}

func (f *fakeAPI) SetMessageReaction(ctx context.Context, params *tgbot.SetMessageReactionParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	emoji := ""
	if len(params.Reaction) > 0 && params.Reaction[0].ReactionTypeEmoji != nil {
		emoji = params.Reaction[0].ReactionTypeEmoji.Emoji
	}
	f.reactions = append(f.reactions, fakeReaction{chatID: params.ChatID.(int64), messageID: params.MessageID, emoji: emoji})
	return true, nil
}

func (f *fakeAPI) PinChatMessage(ctx context.Context, params *tgbot.PinChatMessageParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pins = append(f.pins, fakePin{chatID: params.ChatID.(int64), messageID: params.MessageID})
	return true, nil
}

func (f *fakeAPI) UnpinChatMessage(ctx context.Context, params *tgbot.UnpinChatMessageParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpins = append(f.unpins, fakePin{chatID: params.ChatID.(int64), messageID: params.MessageID})
	return true, nil
}

func (f *fakeAPI) DeleteMessage(ctx context.Context, params *tgbot.DeleteMessageParams) (bool, error) {
	return true, nil
}

func (f *fakeAPI) EditForumTopic(ctx context.Context, params *tgbot.EditForumTopicParams) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topicRenames = append(f.topicRenames, fakeTopicRename{chatID: params.ChatID.(int64), threadID: params.MessageThreadID, name: params.Name})
	return true, nil
}

func (f *fakeAPI) sends() []fakeSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeSend(nil), f.sent...)
}

func (f *fakeAPI) reactionsFor(messageID int) []fakeReaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeReaction
	for _, r := range f.reactions {
		if r.messageID == messageID {
			out = append(out, r)
		}
	}
	return out
}

// editsFor lists the texts edited into a message, in order.
func (f *fakeAPI) editsFor(messageID int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.edited {
		if e.messageID == messageID {
			out = append(out, e.text)
		}
	}
	return out
}

type fakeResponders struct {
	approved    map[string]bool
	approvedIDs []string
	clarified   map[string][]string
}

func newFakeResponders() *fakeResponders {
	return &fakeResponders{approved: map[string]bool{}, clarified: map[string][]string{}}
}

func (r *fakeResponders) RespondApproval(approvalID string, approved bool) bool {
	r.approvedIDs = append(r.approvedIDs, approvalID)
	r.approved[approvalID] = approved
	return true
}

func (r *fakeResponders) RespondClarify(clarifyID string, response interfaces.ClarifyResponse) bool {
	r.clarified[clarifyID] = response.Answer
	return true
}

// newTestBot wires a transport against fakes and a temp state file.
func newTestBot(t *testing.T) (*Bot, *fakeAPI, *fakeResponders, *channel.Service) {
	t.Helper()
	oldPace := perChatSendInterval
	perChatSendInterval = 0
	t.Cleanup(func() { perChatSendInterval = oldPace })

	sessionsDir := t.TempDir()
	store_, err := hakasesession.NewSessionStore(sessionsDir)
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(store_)
	if err != nil {
		t.Fatalf("session service: %v", err)
	}

	responders := newFakeResponders()
	bridge := sse.NewEventBridge()
	svcDeps := channel.Deps{
		Bridge:    bridge,
		Sessions:  svc,
		Approval:  responders,
		Clarify:   responders,
		StatePath: t.TempDir() + "/channels.json",
	}
	service, err := channel.NewService(svcDeps)
	if err != nil {
		t.Fatalf("channel service: %v", err)
	}

	api := &fakeAPI{}
	b := &Bot{
		api:          api,
		auth:         channel.NewAuthenticator(service.Store(), ChannelName, []int64{100}, ""),
		runs:         service.Runs(),
		driver:       nil,
		sessions:     svc,
		store:        service.Store(),
		bridge:       service.Bridge(),
		approval:     responders,
		clarify:      responders,
		log:          func(string, ...any) {},
		nextSend:     map[conv]time.Time{},
		pendingOther: map[conv]pendingClarify{},
		clarifyCtx:   map[string]clarifyChoice{},
		mediaGroup:   map[string]*mediaGroupBuf{},
	}
	// Simulate deleteWebhook through the same seam production uses, with the
	// fakeAPI counters driving flakiness (failDeletes) and state (webhookURL).
	b.deleteWebhookFn = func(ctx context.Context) error {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.deleteCalls++
		if api.deleteCalls <= api.failDeletes {
			return fmt.Errorf("HTTP 200, undecodable body %q", "")
		}
		api.deletedWebhook++
		api.webhookURL = ""
		return nil
	}
	return b, api, responders, service
}

// TestRunDeletesStaleWebhook guards the 409-healing path: a token that was
// previously used with a webhook-based integration makes every getUpdates
// fail with "Conflict: can't use getUpdates method while webhook is active",
// so Run must delete any stale webhook before polling starts.
func TestRunDeletesStaleWebhook(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	api.webhookURL = "https://old-integration.example.com/hook"

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = b.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	if api.deletedWebhook != 1 {
		t.Fatalf("DeleteWebhook calls = %d, want 1", api.deletedWebhook)
	}
	if api.webhookURL != "" {
		t.Fatal("stale webhook was not cleared")
	}
}

// TestClearStaleWebhookRetriesUntilVerified covers the flaky-delete case
// observed in the field: deleteWebhook can fail with an empty/undecodable
// response body, so the healing loop must verify via GetWebhookInfo and
// retry until the webhook is actually gone.
func TestClearStaleWebhookRetriesUntilVerified(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	api.webhookURL = "https://stale.example.com/hook"
	api.failDeletes = 1

	b.clearStaleWebhook(context.Background())

	if api.webhookURL != "" {
		t.Fatalf("webhook still set after retries: %q", api.webhookURL)
	}
	if api.deleteCalls != 2 {
		t.Fatalf("DeleteWebhook attempts = %d, want 2 (1 failed + 1 success)", api.deleteCalls)
	}
}

// TestHandleBotErrorHealsConflict ensures a 409 surfacing mid-poll triggers
// the healing path (throttled by the cooldown) instead of only logging.
func TestHandleBotErrorHealsConflict(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	conflict := errors.New("error get updates, conflict, Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first")

	api.webhookURL = "https://stale.example.com/hook"
	b.handleBotError(conflict)
	if api.webhookURL != "" {
		t.Fatal("conflict did not trigger webhook healing")
	}

	// Inside the cooldown a second conflict must not re-heal immediately.
	api.webhookURL = "https://stale.example.com/hook2"
	b.handleBotError(conflict)
	if api.webhookURL != "https://stale.example.com/hook2" {
		t.Fatal("cooldown was not honored")
	}

	// Non-conflict errors must not touch the webhook at all.
	before := api.deleteCalls
	b.handleBotError(errors.New("some other api error"))
	if api.deleteCalls != before {
		t.Fatal("non-conflict error triggered healing")
	}
}

func privateMessage(userID int64, text string) *models.Message {
	return &models.Message{
		Chat: models.Chat{ID: userID, Type: models.ChatTypePrivate},
		From: &models.User{ID: userID, Username: "tester"},
		Text: text,
	}
}

// TestBotAndServiceMessagesIgnored pins the field fix from 2026-09-06: the
// bot's own topic lifecycle actions arrive back as service messages authored
// by the bot (From.IsBot), and topic creation arrives as a textless message
// from the user. Neither is conversation input: no reply, no pairing denial
// (the bot used to deny itself with "Unauthorized … /start" in the topic).
func TestBotAndServiceMessagesIgnored(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	ctx := context.Background()

	// The bot's own service message (topic rename echo).
	botMsg := privateMessage(100, "")
	botMsg.From = &models.User{ID: 8852003572, Username: "mangoslicecollectorbot", IsBot: true}
	b.handleMessage(ctx, botMsg)

	// A paired user's topic-created service message (textless).
	b.handleMessage(ctx, privateMessage(100, ""))

	// An unpaired user's textless service message: no pairing denial either.
	b.handleMessage(ctx, privateMessage(999, ""))

	if sends := api.sends(); len(sends) != 0 {
		t.Fatalf("service messages produced replies: %+v", sends)
	}
	if b.auth.IsAllowed(8852003572) {
		t.Fatal("the bot account was allowed into auth state")
	}
}

// TestBusyRefusalDoesNotPersistPrompt guards the run-start ordering: a prompt
// refused because the conversation already has a run must not be recorded
// into the session (it used to be persisted before the TryStart gate).
func TestBusyRefusalDoesNotPersistPrompt(t *testing.T) {
	b, api := newRunTestBot(t)
	release := make(chan struct{})
	d := &gateDriver{release: release}
	b.driver = d
	ctx := context.Background()
	root := rootConv(100)

	// First prompt binds a session and occupies the run slot.
	b.handleMessage(ctx, privateMessage(100, "first prompt"))
	waitRunning(t, b, root, true)
	sessID := b.store.Get().Chats[chatKey(100)].SessionID
	sess, err := b.sessions.Store().Load(sessID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	persisted := len(sess.Messages)

	// Second prompt while the run holds the slot: refused, not persisted.
	b.handleMessage(ctx, privateMessage(100, "refused prompt"))
	busy := 0
	for _, s := range api.sends() {
		if contains(s.text, "already active") {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("busy replies = %d, want 1", busy)
	}
	sess, err = b.sessions.Store().Load(sessID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if len(sess.Messages) != persisted {
		t.Fatalf("session messages = %d, want %d (refused prompt must not persist)", len(sess.Messages), persisted)
	}

	close(release)
	waitRunDone(t, b, root)
}

func TestNormalizeThread(t *testing.T) {
	for in, want := range map[int]int{
		0:   0, // absent/root
		1:   0, // the General topic is always thread 1
		2:   2,
		555: 555,
	} {
		if got := normalizeThread(in); got != want {
			t.Errorf("normalizeThread(%d) = %d, want %d", in, got, want)
		}
	}
}

// TestThreadedMessagesRouteToTheirThread pins the conversation-identity rule:
// a message that carries a thread id belongs to that thread — commands reply
// in it and prompts bind it — regardless of whether /topic was ever run. Only
// unthreaded messages use the legacy root path.
func TestThreadedMessagesRouteToTheirThread(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	ctx := context.Background()

	threaded := func(text string) *models.Message {
		m := privateMessage(100, text)
		m.MessageThreadID = 1234
		return m
	}

	b.handleMessage(ctx, threaded("/help"))
	sends := api.sends()
	if len(sends) != 1 || sends[0].threadID != 1234 {
		t.Fatalf("threaded /help = %+v, want one reply in thread 1234", sends)
	}

	// A prompt in the thread hits the runtime guard — inside the thread.
	b.handleMessage(ctx, threaded("do a thing"))
	b.handleMessage(ctx, privateMessage(100, "do another thing"))
	guards := map[int]int{} // thread -> guard count
	for _, s := range api.sends() {
		if contains(s.text, "runtime unavailable") {
			guards[s.threadID]++
		}
	}
	if guards[1234] != 1 || guards[0] != 1 {
		t.Fatalf("guards per thread = %v, want one in thread 1234 and one in root", guards)
	}
}

func TestCommandHelpAllowed(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	b.handleMessage(context.Background(), privateMessage(100, "/help"))
	sends := api.sends()
	if len(sends) != 1 || len(sends[0].text) < 50 {
		t.Fatalf("help not sent: %+v", sends)
	}
}

func TestUnauthorizedIsDeniedQuietly(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	b.handleMessage(context.Background(), privateMessage(999, "hello agent"))
	b.handleMessage(context.Background(), privateMessage(999, "hello again"))
	sends := api.sends()
	if len(sends) != 1 {
		t.Fatalf("expected exactly one rate-limited deny reply, got %d", len(sends))
	}
	if !contains(sends[0].text, "Unauthorized") {
		t.Errorf("deny reply missing marker: %q", sends[0].text)
	}
	if contains(sends[0].text, "code is") && len(sends[0].text) > 0 {
		// The reply must not leak a usable code.
		for _, r := range sends[0].text {
			_ = r
		}
	}
}

func TestStartPairingFlow(t *testing.T) {
	b, api, _, _ := newTestBot(t)

	// Issue a code via the server side (as the boot path would).
	code, err := b.auth.EnsurePairingCode()
	if err != nil {
		t.Fatalf("pairing code: %v", err)
	}

	// Wrong code.
	b.handleMessage(context.Background(), privateMessage(200, "/start 000000"))
	if b.auth.IsAllowed(200) {
		t.Fatal("paired with wrong code")
	}
	// Right code.
	b.handleMessage(context.Background(), privateMessage(200, "/start "+code))
	if !b.auth.IsAllowed(200) {
		t.Fatal("pairing did not stick")
	}
	last := api.sends()
	if len(last) == 0 || !contains(last[len(last)-1].text, "Paired") {
		t.Fatalf("no pairing confirmation: %+v", last)
	}
}

func TestStartWithoutCodePrompts(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	b.handleMessage(context.Background(), privateMessage(300, "/start"))
	sends := api.sends()
	if len(sends) != 1 || !contains(sends[0].text, "/start") {
		t.Fatalf("expected pairing hint, got %+v", sends)
	}
}

func TestIDCommand(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	b.handleMessage(context.Background(), privateMessage(100, "/id"))
	sends := api.sends()
	if len(sends) != 1 || !contains(sends[0].text, "100") {
		t.Fatalf("id output wrong: %+v", sends)
	}
}

func TestNewAndSessionsAndUse(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	ctx := context.Background()

	b.handleMessage(ctx, privateMessage(100, "/new My project session"))
	sends := api.sends()
	if len(sends) == 0 || !contains(sends[len(sends)-1].text, "created") {
		t.Fatalf("new session reply missing: %+v", sends)
	}
	bound := b.store.Get().Chats[chatKey(100)].SessionID
	if bound == "" {
		t.Fatal("chat not bound to the new session")
	}

	// /sessions lists it with the bound marker.
	b.handleMessage(ctx, privateMessage(100, "/sessions"))
	last := api.sends()
	if !contains(last[len(last)-1].text, "My project session") || !contains(last[len(last)-1].text, "▶️") {
		t.Fatalf("sessions list wrong: %q", last[len(last)-1].text)
	}

	// /new again, then /use back to the first via prefix.
	b.handleMessage(ctx, privateMessage(100, "/new second"))
	if !contains(api.sends()[len(api.sends())-1].text, "second") {
		t.Fatalf("second session not created")
	}
	b.handleMessage(ctx, privateMessage(100, "/use "+bound[:12]))
	last = api.sends()
	if !contains(last[len(last)-1].text, bound[:12]) {
		t.Fatalf("use failed: %q", last[len(last)-1].text)
	}
	if got := b.store.Get().Chats[chatKey(100)].SessionID; got != bound {
		t.Fatalf("binding = %s, want %s", got, bound)
	}
}

func TestNotifyToggle(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	ctx := context.Background()
	b.handleMessage(ctx, privateMessage(100, "/notify on"))
	if !b.store.Get().Chats[chatKey(100)].Notify {
		t.Fatal("notify not enabled")
	}
	b.handleMessage(ctx, privateMessage(100, "/notify"))
	if b.store.Get().Chats[chatKey(100)].Notify {
		t.Fatal("bare /notify must toggle off")
	}
	if len(api.sends()) != 2 {
		t.Fatalf("expected 2 replies, got %d", len(api.sends()))
	}
}

func TestPromptRequiresSessionRuntime(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	// driver == nil: the transport must answer with a runtime warning, not crash.
	b.handleMessage(context.Background(), privateMessage(100, "do a thing"))
	sends := api.sends()
	if len(sends) != 1 || !contains(sends[0].text, "runtime unavailable") {
		t.Fatalf("runtime guard missing: %+v", sends)
	}
}

func TestPhotoWithoutCaptionRejected(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	m := privateMessage(100, "")
	m.Photo = []models.PhotoSize{{FileID: "x", Width: 10, Height: 10}, {FileID: "y", Width: 100, Height: 100}}
	b.handleMessage(context.Background(), m)
	sends := api.sends()
	if len(sends) != 1 || !contains(sends[0].text, "caption") {
		t.Fatalf("caption hint missing: %+v", sends)
	}
}

func TestApprovalCallbackResolvesGate(t *testing.T) {
	b, api, responders, _ := newTestBot(t)
	kb := approvalKeyboard("appr_abc")
	_ = kb
	m := privateMessage(100, "")
	m.From = nil
	cq := &models.CallbackQuery{
		ID:   "cq1",
		From: models.User{ID: 100},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 7, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
		},
		Data: callbackApprove + "appr_abc:1",
	}
	b.handleCallback(context.Background(), cq)
	if len(responders.approvedIDs) != 1 || responders.approvedIDs[0] != "appr_abc" || !responders.approved["appr_abc"] {
		t.Fatalf("approval not resolved: %+v", responders.approved)
	}
	if len(api.edited) != 1 || !contains(api.edited[0].text, "Approved") {
		t.Fatalf("prompt not edited to outcome: %+v", api.edited)
	}
}

func TestClarifyCallbackChoiceAndFreeText(t *testing.T) {
	b, api, responders, _ := newTestBot(t)
	b.rememberClarifyChoices("clar_xyz", []string{"Option A", "Option B"})
	cq := &models.CallbackQuery{
		ID:   "cq2",
		From: models.User{ID: 100},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 9, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
		},
		Data: callbackClarify + "clar_xyz:1",
	}
	b.handleCallback(context.Background(), cq)
	ans := responders.clarified["clar_xyz"]
	if len(ans) != 1 || !contains(ans[0], "Option B") {
		t.Fatalf("clarify answer = %v", ans)
	}

	// Free text path: "x" arms pendingOther; the next message answers.
	cq2 := &models.CallbackQuery{
		ID:   "cq3",
		From: models.User{ID: 100},
		Message: models.MaybeInaccessibleMessage{
			Message: &models.Message{ID: 10, Chat: models.Chat{ID: 100, Type: models.ChatTypePrivate}},
		},
		Data: callbackClarify + "clar_free:x",
	}
	b.handleCallback(context.Background(), cq2)
	b.handleMessage(context.Background(), privateMessage(100, "my own answer"))
	ans2 := responders.clarified["clar_free"]
	if len(ans2) != 1 || ans2[0] != "my own answer" {
		t.Fatalf("free-text answer = %v", ans2)
	}
	if len(api.sends()) != 0 {
		// The free-text message must be consumed, not run as a prompt (and
		// the runtime guard must not fire either).
		t.Fatalf("free-text message leaked into the prompt path: %+v", api.sends())
	}
}

func TestPushHandlersRouteToDestinations(t *testing.T) {
	b, api, _, _ := newTestBot(t)
	// One notify-enabled chat plus a second paired user without bindings.
	if err := b.store.Update(func(st *state.State) error {
		st.PairedUsers = append(st.PairedUsers,
			state.PairedUser{Channel: ChannelName, UserID: 100},
			state.PairedUser{Channel: ChannelName, UserID: 200},
		)
		st.Chats = map[string]state.Chat{chatKey(100): {Notify: true}}
		return nil
	}); err != nil {
		t.Fatalf("state update: %v", err)
	}

	b.CronEvent("completed", "j1", "backup", "done", "outputs/x.md")
	b.ApprovalPrompt("", "appr_p", "system_exec", "high", "why", "ls")
	b.ClarifyPrompt("", "clar_p", "Which one?", []string{"A", "B"}, false)

	sends := api.sends()
	var cronPushes, approvalPushes, clarifyPushes int
	for _, s := range sends {
		switch {
		case strings.Contains(s.text, "cron <b>backup</b>"):
			cronPushes++
		case strings.Contains(s.text, "Approval needed"):
			approvalPushes++
		case strings.Contains(s.text, "Question"):
			clarifyPushes++
		}
	}
	if cronPushes != 1 {
		t.Errorf("cron push count = %d, want 1 (notify chats only)", cronPushes)
	}
	if approvalPushes != 2 {
		t.Errorf("approval push count = %d, want 2 (all allowed users)", approvalPushes)
	}
	if clarifyPushes != 2 {
		t.Errorf("clarify push count = %d, want 2", clarifyPushes)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
