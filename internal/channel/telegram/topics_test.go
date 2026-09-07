package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel/state"

	"github.com/go-telegram/bot/models"
	"google.golang.org/genai"
)

// gateDriver stands in for the agent runtime: it records which session each
// turn ran against and blocks mid-turn until release is closed (or the run's
// ctx is cancelled, e.g. by /stop).
type gateDriver struct {
	mu       sync.Mutex
	sessions []string
	release  chan struct{}
}

func (g *gateDriver) RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink) {
	g.mu.Lock()
	g.sessions = append(g.sessions, sessionID)
	g.mu.Unlock()
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	sink.OnDone(sessionID)
}

func (g *gateDriver) ran() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.sessions...)
}

func threadMessage(userID int64, thread int, text string) *models.Message {
	m := privateMessage(userID, text)
	m.MessageThreadID = thread
	return m
}

func enableTopics(t *testing.T, b *Bot, chatID int64) {
	t.Helper()
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		ck := chatKey(chatID)
		chat := s.Chats[ck]
		chat.TopicsMode = true
		s.Chats[ck] = chat
		return nil
	}); err != nil {
		t.Fatalf("enable topics: %v", err)
	}
}

// waitRan waits until the driver has recorded n started turns.
func waitRan(t *testing.T, d *gateDriver, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ran := d.ran(); len(ran) >= n {
			return ran
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("driver recorded %d turns, want %d", len(d.ran()), n)
	return nil
}

// waitRunning waits until the conversation's run slot is (or is not) active.
func waitRunning(t *testing.T, b *Bot, c conv, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, running := b.runs.Running(threadKey(c)); running == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run in %s: want running=%v, timed out otherwise", threadKey(c), want)
}

// TestGatePromptRoutesToBoundConversation is design scenario 11: approval
// and clarify prompts go to the conversation bound to the gate's session
// (thread binding in topics mode, root binding otherwise); an unknown
// session falls back to the paired-users fan-out.
func TestGatePromptRoutesToBoundConversation(t *testing.T) {
	b, api := newRunTestBot(t)
	// A second paired user: fan-out would reach both, routing reaches one.
	if err := b.store.Update(func(s *state.State) error {
		s.PairedUsers = append(s.PairedUsers,
			state.PairedUser{Channel: ChannelName, UserID: 100},
			state.PairedUser{Channel: ChannelName, UserID: 200},
		)
		return nil
	}); err != nil {
		t.Fatalf("state update: %v", err)
	}
	enableTopics(t, b, 100)
	topic := conv{chatID: 100, threadID: 555}

	sessA, err := b.sessions.CreateSession("Topic session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := b.bindThread(topic, sessA.ID, sessA.Title); err != nil {
		t.Fatalf("bind thread: %v", err)
	}
	sessR, err := b.sessions.CreateSession("Root session")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		ck := chatKey(200)
		chat := s.Chats[ck]
		chat.SessionID = sessR.ID
		s.Chats[ck] = chat
		return nil
	}); err != nil {
		t.Fatalf("bind chat: %v", err)
	}

	// Thread-bound session: the prompt lands in the topic only, buttons on.
	b.ApprovalPrompt(sessA.ID, "appr_t", "system_exec", "high", "why", "ls")
	sends := api.sends()
	if len(sends) != 1 || sends[0].chatID != 100 || sends[0].threadID != 555 || !sends[0].hasKb || sends[0].silent {
		t.Fatalf("topic approval = %+v, want one keyboarded, loud send in 100/555", sends)
	}

	// Root-bound session (no topics for chat 200): routes to that root DM.
	b.ClarifyPrompt(sessR.ID, "clar_r", "Which one?", []string{"A"}, false)
	sends = api.sends()
	if len(sends) != 2 || sends[1].chatID != 200 || sends[1].threadID != 0 || !sends[1].hasKb {
		t.Fatalf("root clarify = %+v, want one keyboarded send to chat 200 root", sends)
	}

	// Unknown session: fan-out to every paired user's root.
	n := len(api.sends())
	b.ApprovalPrompt("sess_unknown", "appr_u", "system_exec", "high", "why", "ls")
	sends = api.sends()
	got := map[int64]bool{}
	for _, s := range sends[n:] {
		got[s.chatID] = true
		if s.threadID != 0 {
			t.Errorf("fan-out send carried thread %d, want root", s.threadID)
		}
	}
	if len(sends)-n != 2 || !got[100] || !got[200] {
		t.Fatalf("fan-out = %+v, want one send each to chats 100 and 200", sends[n:])
	}
}

// TestFirstPromptInTopicBindsAndRenames is design scenario 1: a prompt in an
// unbound topic creates a session, binds it to the thread, renames the topic,
// and runs against it — leaving the chat-level binding untouched.
func TestFirstPromptInTopicBindsAndRenames(t *testing.T) {
	b, api := newRunTestBot(t)
	enableTopics(t, b, 100)
	release := make(chan struct{})
	close(release)
	d := &gateDriver{release: release}
	b.driver = d

	b.handleMessage(context.Background(), threadMessage(100, 555, "Fix the login bug"))
	c := conv{chatID: 100, threadID: 555}
	waitRunDone(t, b, c)

	ran := d.ran()
	if len(ran) != 1 {
		t.Fatalf("runs = %v, want exactly one", ran)
	}
	th := b.store.Get().Threads[threadKey(c)]
	if th.SessionID != ran[0] || th.Title == "" {
		t.Fatalf("thread binding = %+v, want session %s", th, ran[0])
	}
	if got := b.store.Get().Chats[chatKey(100)].SessionID; got != "" {
		t.Errorf("chat binding = %q, want untouched (empty)", got)
	}

	// The topic was renamed to the session title.
	if len(api.topicRenames) != 1 || api.topicRenames[0].chatID != 100 ||
		api.topicRenames[0].threadID != 555 || api.topicRenames[0].name != th.Title {
		t.Errorf("topic renames = %+v, want one rename of 100/555 to %q", api.topicRenames, th.Title)
	}

	// Everything for this run happened inside the topic.
	for _, s := range api.sends() {
		if s.threadID != 555 {
			t.Errorf("send %q carried thread %d, want 555", s.text, s.threadID)
		}
	}
}

// TestTwoTopicsRunInParallel is design scenario 2: two topics run in
// parallel against distinct sessions, /stop inside one topic only cancels
// that topic, and the same topic refuses a second concurrent run.
func TestTwoTopicsRunInParallel(t *testing.T) {
	b, api := newRunTestBot(t)
	enableTopics(t, b, 100)
	d := &gateDriver{release: make(chan struct{})}
	b.driver = d
	ctx := context.Background()

	b.handleMessage(ctx, threadMessage(100, 555, "first"))
	b.handleMessage(ctx, threadMessage(100, 556, "second"))
	c1, c2 := conv{100, 555}, conv{chatID: 100, threadID: 556}
	waitRunning(t, b, c1, true)
	waitRunning(t, b, c2, true)

	ran := waitRan(t, d, 2)
	if ran[0] == ran[1] {
		t.Fatalf("sessions = %v, want two distinct", ran)
	}

	// Same topic refuses a second concurrent run.
	b.handleMessage(ctx, threadMessage(100, 555, "another"))
	busy := 0
	for _, s := range api.sends() {
		if contains(s.text, "already active") {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("busy replies = %d, want 1", busy)
	}

	// /stop inside topic 555 cancels only that topic's run.
	b.handleMessage(ctx, threadMessage(100, 555, "/stop"))
	waitRunDone(t, b, c1)
	if _, running := b.runs.Running(threadKey(c2)); !running {
		t.Fatal("/stop in topic 555 cancelled the run in topic 556")
	}
	close(d.release)
	waitRunDone(t, b, c2)

	th1 := b.store.Get().Threads[threadKey(c1)]
	th2 := b.store.Get().Threads[threadKey(c2)]
	if th1.SessionID == "" || th2.SessionID == "" || th1.SessionID == th2.SessionID {
		t.Fatalf("bindings = %s / %s, want two distinct sessions", th1.SessionID, th2.SessionID)
	}
}

// TestLobbyHint is design scenario 3: with topics on, a plain prompt in the
// root gets the lobby hint (never a run), while commands keep working.
func TestLobbyHint(t *testing.T) {
	b, api := newRunTestBot(t)
	enableTopics(t, b, 100)
	release := make(chan struct{})
	close(release)
	d := &gateDriver{release: release}
	b.driver = d
	ctx := context.Background()

	b.handleMessage(ctx, privateMessage(100, "plain prompt in the lobby"))
	if ran := d.ran(); len(ran) != 0 {
		t.Fatalf("lobby prompt started runs: %v", ran)
	}
	sends := api.sends()
	if len(sends) != 1 || !contains(sends[0].text, "lobby") {
		t.Fatalf("lobby hint missing: %+v", sends)
	}

	b.handleMessage(ctx, privateMessage(100, "/help"))
	sends = api.sends()
	if len(sends) != 2 || len(sends[1].text) < 50 {
		t.Fatalf("lobby /help not answered: %+v", sends)
	}
}

// TestTopicOffKeepsBindings is design scenario 4: /topic off restores the
// legacy single-conversation root, while thread conversations (and their
// bindings) keep working — with or without topics mode.
func TestTopicOffKeepsBindings(t *testing.T) {
	b, api := newRunTestBot(t)
	enableTopics(t, b, 100)
	release := make(chan struct{})
	close(release)
	d := &gateDriver{release: release}
	b.driver = d
	ctx := context.Background()
	root := rootConv(100)
	topic := conv{chatID: 100, threadID: 555}

	// A prompt in topic 555 binds session A to the thread.
	b.handleMessage(ctx, threadMessage(100, 555, "topic session"))
	waitRunDone(t, b, topic)
	bindingA := b.store.Get().Threads[threadKey(topic)].SessionID
	if bindingA == "" {
		t.Fatal("no thread binding created")
	}

	// /topic off: the root becomes a plain chat again.
	b.handleMessage(ctx, threadMessage(100, 0, "/topic off"))
	if b.topicsMode(100) {
		t.Fatal("topics mode still on after /topic off")
	}

	// Root prompt creates the legacy chat binding.
	b.handleMessage(ctx, privateMessage(100, "root session"))
	waitRunDone(t, b, root)
	rootBinding := b.store.Get().Chats[chatKey(100)].SessionID
	if rootBinding == "" || rootBinding == bindingA {
		t.Fatalf("root binding = %q, want a fresh legacy session", rootBinding)
	}

	// The thread conversation persists across /topic off: same binding,
	// replies still delivered in-thread.
	before := len(api.sends())
	b.handleMessage(ctx, threadMessage(100, 555, "back to the topic"))
	waitRunDone(t, b, topic)
	ran := d.ran()
	if len(ran) != 3 || ran[2] != bindingA {
		t.Fatalf("runs = %v, want the thread's session %q last", ran, bindingA)
	}
	for _, s := range api.sends()[before:] {
		if s.threadID != 555 {
			t.Errorf("send %q carried thread %d, want 555 (the conversation's thread)", s.text, s.threadID)
		}
	}
}

// TestClientCreatedThreadWithoutTopicsMode is the field regression from
// 2026-09-06: a user opens threads via the Telegram client (the ✚ composer)
// without ever running /topic. Those threads are conversations: the first
// prompt binds a session and every reply lands in that thread — never in
// "(All Messages)".
func TestClientCreatedThreadWithoutTopicsMode(t *testing.T) {
	b, api := newRunTestBot(t)
	if b.topicsMode(100) {
		t.Fatal("precondition: topics mode must be off")
	}
	release := make(chan struct{})
	close(release)
	d := &gateDriver{release: release}
	b.driver = d

	b.handleMessage(context.Background(), threadMessage(100, 777, "ask in a client thread"))
	topic := conv{chatID: 100, threadID: 777}
	waitRunDone(t, b, topic)

	ran := d.ran()
	if len(ran) != 1 {
		t.Fatalf("runs = %v, want exactly one", ran)
	}
	th := b.store.Get().Threads[threadKey(topic)]
	if th.SessionID != ran[0] {
		t.Fatalf("thread binding = %+v, want session %s", th, ran[0])
	}
	for _, s := range api.sends() {
		if s.threadID != 777 {
			t.Errorf("send %q carried thread %d, want 777 (never the root)", s.text, s.threadID)
		}
	}
}

// TestTopicRebindAndNewInTopic is design scenario 5: /topic <prefix> rebinds
// and re-titles the current topic; /new resets that topic's session.
func TestTopicRebindAndNewInTopic(t *testing.T) {
	b, api := newRunTestBot(t)
	enableTopics(t, b, 100)
	release := make(chan struct{})
	close(release)
	d := &gateDriver{release: release}
	b.driver = d
	ctx := context.Background()

	sessA, err := b.sessions.CreateSession("Alpha")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	b.handleMessage(ctx, threadMessage(100, 555, "/topic "+sessA.ID[:12]))
	c := conv{chatID: 100, threadID: 555}
	if got := b.boundSessionID(c); got != sessA.ID {
		t.Fatalf("binding = %q, want %q", got, sessA.ID)
	}
	renames := api.topicRenames
	if len(renames) == 0 || renames[len(renames)-1].name != "Alpha" {
		t.Fatalf("renames = %+v, want re-title to Alpha", renames)
	}

	// The bound session drives the topic's runs.
	b.handleMessage(ctx, threadMessage(100, 555, "go"))
	waitRunDone(t, b, c)
	if ran := d.ran(); len(ran) != 1 || ran[0] != sessA.ID {
		t.Fatalf("runs = %v, want %q", ran, sessA.ID)
	}

	// /new inside the topic resets it to a fresh session.
	b.handleMessage(ctx, threadMessage(100, 555, "/new Beta"))
	if got := b.boundSessionID(c); got == "" || got == sessA.ID {
		t.Fatalf("binding after /new = %q, want a fresh session", got)
	}
	if got := b.store.Get().Threads[threadKey(c)].Title; got != "Beta" {
		t.Fatalf("thread title = %q, want Beta", got)
	}
	b.handleMessage(ctx, threadMessage(100, 555, "go again"))
	waitRunDone(t, b, c)
	ran := d.ran()
	if len(ran) != 2 || ran[1] == sessA.ID {
		t.Fatalf("runs = %v, want the fresh Beta session last", ran)
	}
}
