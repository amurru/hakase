package discord

import (
	"context"
	"strings"
	"sync"
	"testing"
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

// fakeSender records Discord output without the network.
type fakeSender struct {
	mu           sync.Mutex
	dmChannels   map[string]string
	sends        []string // channelID + "\x00" + text
	edits        []string
	reacts       []string
	buttons      []string // channelID + "\x00" + text + customIDs
	interactions []string // update flag + "\x00" + text
	me           string
}

func (f *fakeSender) OpenDM(userID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dmChannels == nil {
		f.dmChannels = map[string]string{}
	}
	if ch, ok := f.dmChannels[userID]; ok {
		return ch, nil
	}
	ch := "dm-" + userID
	f.dmChannels[userID] = ch
	return ch, nil
}

func (f *fakeSender) Send(channelID, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, channelID+"\x00"+text)
	return "msg", nil
}

func (f *fakeSender) Edit(channelID, messageID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.edits = append(f.edits, channelID+"\x00"+messageID+"\x00"+text)
	return nil
}

func (f *fakeSender) React(channelID, messageID, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reacts = append(f.reacts, channelID+"\x00"+messageID+"\x00"+emoji)
	return nil
}

func (f *fakeSender) SendButtons(channelID, text string, buttons []Button) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, btn := range buttons {
		ids = append(ids, btn.CustomID)
	}
	f.buttons = append(f.buttons, channelID+"\x00"+text+"\x00"+strings.Join(ids, ","))
	return "btnmsg", nil
}

func (f *fakeSender) RespondInteraction(inter *discordgo.Interaction, update bool, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	flag := "ephemeral"
	if update {
		flag = "update"
	}
	f.interactions = append(f.interactions, flag+"\x00"+text)
	return nil
}

func (f *fakeSender) Me() string { return f.me }

func (f *fakeSender) lastSend() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return ""
	}
	return f.sends[len(f.sends)-1]
}

func (f *fakeSender) sendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// stubResponders records gate answers.
type stubResponders struct {
	mu       sync.Mutex
	approved map[string]bool
	answered map[string][]string
}

func (r *stubResponders) RespondApproval(id string, approved bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.approved == nil {
		r.approved = map[string]bool{}
	}
	r.approved[id] = approved
	return true
}

func (r *stubResponders) RespondClarify(id string, resp interfaces.ClarifyResponse) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answered == nil {
		r.answered = map[string][]string{}
	}
	r.answered[id] = resp.Answer
	return true
}

// fakeTurner scripts agent turns: streams one chunk, then done.
type fakeTurner struct {
	mu      sync.Mutex
	started []string
}

func (f *fakeTurner) RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink) {
	f.mu.Lock()
	f.started = append(f.started, sessionID)
	f.mu.Unlock()
	sink.OnLog(sessionID, "Call: system_exec(echo)")
	sink.OnStream(sessionID, "fake answer", "")
	sink.OnDone(sessionID)
}

func newTestBot(t *testing.T, allowed []int64) (*Bot, *fakeSender, *stubResponders) {
	t.Helper()
	oldPace := sendPacing
	sendPacing = 0
	t.Cleanup(func() { sendPacing = oldPace })
	sstore, err := hakasesession.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(sstore)
	if err != nil {
		t.Fatalf("session service: %v", err)
	}
	responders := &stubResponders{}
	service, err := channel.NewService(channel.Deps{
		Bridge:    sse.NewEventBridge(),
		Sessions:  svc,
		Approval:  responders,
		Clarify:   responders,
		StatePath: t.TempDir() + "/channels.json",
	})
	if err != nil {
		t.Fatalf("channel service: %v", err)
	}
	sender := &fakeSender{}
	b := &Bot{
		auth:         channel.NewAuthenticator(service.Store(), ChannelName, allowed, ""),
		runs:         service.Runs(),
		driver:       &fakeTurner{},
		sessions:     svc,
		store:        service.Store(),
		bridge:       service.Bridge(),
		approval:     responders,
		clarify:      responders,
		log:          func(string, ...any) {},
		sender:       sender,
		nextSend:     map[int64]time.Time{},
		dmCache:      map[int64]string{},
		pendingOther: map[int64]pendingClarify{},
		clarifyCtx:   map[string]clarifyChoice{},
	}
	return b, sender, responders
}

func dmMessage(userID, content string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID:        "m1",
		ChannelID: "dm-" + userID,
		GuildID:   "",
		Content:   content,
		Author:    &discordgo.User{ID: userID, Username: "tester"},
	}}
}

func componentInteraction(userID, customID, prompt string) *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:        "i1",
		Type:      discordgo.InteractionMessageComponent,
		ChannelID: "dm-" + userID,
		Member:    &discordgo.Member{User: &discordgo.User{ID: userID}},
		Message:   &discordgo.Message{ID: "pm", ChannelID: "dm-" + userID, Content: prompt},
		Data:      discordgo.MessageComponentInteractionData{CustomID: customID},
	}}
}

func discordTestConfig() config.DiscordChannelConfig {
	return config.DiscordChannelConfig{BotToken: "test-token"}
}

func TestNewGuardsAndIntents(t *testing.T) {
	sstore, err := hakasesession.NewSessionStore(t.TempDir())
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	svc, err := hakasesession.NewSessionService(sstore)
	if err != nil {
		t.Fatalf("session service: %v", err)
	}
	service, err := channel.NewService(channel.Deps{
		Sessions:  svc,
		StatePath: t.TempDir() + "/channels.json",
	})
	if err != nil {
		t.Fatalf("channel service: %v", err)
	}
	// Empty token rejected before any network.
	if _, err := New(Deps{Service: service}); err == nil {
		t.Error("empty bot_token must fail")
	}
	b, err := New(Deps{Service: service, Config: discordTestConfig()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.Name() != ChannelName {
		t.Errorf("Name = %q, want discord", b.Name())
	}
	// DM-only v1: exactly the two non-privileged intents (DC-003). Any
	// privileged bit here breaks bots without portal approval.
	want := int64(1<<12 | 1<<13)
	if int64(b.dg.Identify.Intents) != want {
		t.Errorf("intents = %b, want DIRECT_MESSAGES|DIRECT_MESSAGE_REACTIONS", b.dg.Identify.Intents)
	}
}

func TestParseSnowflake(t *testing.T) {
	if id, err := parseSnowflake("123456789012345678"); err != nil || id != 123456789012345678 {
		t.Errorf("valid snowflake: %d %v", id, err)
	}
	for _, bad := range []string{"", "abc", "-5", "0", "12.5", "99999999999999999999"} {
		if _, err := parseSnowflake(bad); err == nil {
			t.Errorf("snowflake %q must fail", bad)
		}
	}
}

func TestMarkdownToDiscord(t *testing.T) {
	in := "<b>hi</b> &amp; <code>x</code>\n\n\nbye"
	got := MarkdownToDiscord(in)
	if strings.Contains(got, "<") || !strings.Contains(got, "hi & x") {
		t.Errorf("converted = %q", got)
	}
	if got := MarkdownToDiscord("```go\nfmt.Println()\n```"); !strings.Contains(got, "```go") {
		t.Errorf("fences must survive: %q", got)
	}
}

func TestChunkText(t *testing.T) {
	long := strings.Repeat("a\n", 1500) // 3000 runes, newline-rich
	chunks := ChunkText(long)
	for i, c := range chunks {
		if len([]rune(c)) > MaxMessageLen {
			t.Errorf("chunk %d too long: %d", i, len([]rune(c)))
		}
	}
	if len(chunks) < 2 {
		t.Errorf("expected splits, got %d chunk(s)", len(chunks))
	}
	joined := strings.Join(chunks, "\n")
	if len([]rune(strings.ReplaceAll(joined, "\n", ""))) != 1500 {
		t.Error("chunking must preserve content")
	}
}

func TestDispatchDrops(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	// Guild message dropped.
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "m", ChannelID: "g1", GuildID: "guild",
		Content: "hi", Author: &discordgo.User{ID: "100"},
	}})
	// Own bot message dropped.
	sender.me = "999"
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "m", ChannelID: "dm-999", Content: "hi",
		Author: &discordgo.User{ID: "999", Bot: true},
	}})
	sender.me = ""
	// Empty text dropped.
	b.onMessageCreate(nil, dmMessage("100", "   "))
	if n := sender.sendCount(); n != 0 {
		t.Errorf("drops sent %d messages, want 0", n)
	}
}

func TestUnauthorizedAndPair(t *testing.T) {
	b, sender, _ := newTestBot(t, nil) // nobody allowed, no static code
	b.onMessageCreate(nil, dmMessage("200", "hello"))
	if got := sender.lastSend(); !strings.Contains(got, "pair CODE") {
		t.Errorf("unauthorized reply = %q, want pairing hint", got)
	}
	// Bad code stays unauthorized.
	b.handleCommand(200, "tester", "/pair 000000")
	if got := sender.lastSend(); !strings.Contains(got, "didn't work") {
		t.Errorf("bad code reply = %q", got)
	}
}

func TestPairStaticCode(t *testing.T) {
	b, sender, _ := newTestBot(t, nil)
	b.auth = channel.NewAuthenticator(b.store, ChannelName, nil, "123456")
	b.handleCommand(300, "tester", "/pair 123456")
	if got := sender.lastSend(); !strings.Contains(got, "Paired") {
		t.Fatalf("pair reply = %q, want Paired", got)
	}
	if !b.auth.IsAllowed(300) {
		t.Error("user must be allowed after pairing")
	}
	// Pairing persists namespaced by channel.
	found := false
	for _, u := range b.store.Get().PairedUsers {
		if u.Channel == ChannelName && u.UserID == 300 {
			found = true
		}
	}
	if !found {
		t.Error("paired user must persist under the discord channel")
	}
}

func TestCommands(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	b.handleCommand(100, "tester", "/help")
	if got := sender.lastSend(); !strings.Contains(got, "/stop") {
		t.Errorf("help = %q", got)
	}
	b.handleCommand(100, "tester", "/status")
	if got := sender.lastSend(); !strings.Contains(got, "Idle") {
		t.Errorf("status = %q", got)
	}
	b.handleCommand(100, "tester", "/stop")
	if got := sender.lastSend(); !strings.Contains(got, "No active run") {
		t.Errorf("stop = %q", got)
	}
	b.handleCommand(100, "tester", "/bogus")
	if got := sender.lastSend(); !strings.Contains(got, "Unknown command") {
		t.Errorf("bogus = %q", got)
	}
	// Attachments declined, not run.
	b.onMessageCreate(nil, &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "m", ChannelID: "dm-100", Content: "",
		Author:      &discordgo.User{ID: "100"},
		Attachments: []*discordgo.MessageAttachment{{ID: "a"}},
	}})
	if got := sender.lastSend(); !strings.Contains(got, "attachments") {
		t.Errorf("attachment reply = %q", got)
	}
}

func TestStartRunFlow(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	b.onMessageCreate(nil, dmMessage("100", "hello agent"))
	deadline := time.Now().Add(5 * time.Second)
	for {
		sender.mu.Lock()
		n := len(sender.sends)
		sender.mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	sender.mu.Lock()
	var texts []string
	for _, s := range sender.sends {
		texts = append(texts, strings.SplitN(s, "\x00", 2)[1])
	}
	sender.mu.Unlock()
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "Working on it") {
		t.Errorf("no status message in %q", joined)
	}
	if !strings.Contains(joined, "fake answer") {
		t.Errorf("no final answer in %q", joined)
	}
	if _, ok := b.runs.Running(runKey(100)); ok {
		t.Error("run must finish")
	}
	// Session bound under the discord key.
	st := b.store.Get()
	if ch, ok := st.Chats[state.ChatKey(ChannelName, 100)]; !ok || ch.SessionID == "" {
		t.Error("chat binding must persist the session")
	}
}

func TestStartRunBusy(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100})
	cancel := func() {}
	if !b.runs.TryStart(runKey(100), "sess", cancel) {
		t.Fatal("TryStart")
	}
	defer b.runs.Finish(runKey(100))
	b.onMessageCreate(nil, dmMessage("100", "another"))
	if got := sender.lastSend(); !strings.Contains(got, "already active") {
		t.Errorf("busy reply = %q", got)
	}
}

func TestApprovalButtonFlow(t *testing.T) {
	b, sender, responders := newTestBot(t, []int64{100})
	b.ApprovalPrompt("sess1", "gate1", "system_exec", "high", "runs a command", "echo hi")
	sender.mu.Lock()
	nb := len(sender.buttons)
	sender.mu.Unlock()
	if nb != 1 {
		t.Fatalf("approval sent %d button messages, want 1", nb)
	}
	sender.mu.Lock()
	btnMsg := sender.buttons[0]
	sender.mu.Unlock()
	if !strings.Contains(btnMsg, "dc:a:gate1:1") || !strings.Contains(btnMsg, "dc:a:gate1:0") {
		t.Errorf("button custom_ids = %q", btnMsg)
	}
	// Unknown gate kind rejected.
	b.onInteractionCreate(nil, componentInteraction("100", "xx:yy", "prompt"))
	sender.mu.Lock()
	last := sender.interactions[len(sender.interactions)-1]
	sender.mu.Unlock()
	if !strings.Contains(last, "Unknown action") {
		t.Errorf("bad custom_id reply = %q", last)
	}
	// Approve delivers + edits with verdict.
	b.onInteractionCreate(nil, componentInteraction("100", "dc:a:gate1:1", "Approve `system_exec`?"))
	responders.mu.Lock()
	approved := responders.approved["gate1"]
	responders.mu.Unlock()
	if !approved {
		t.Error("approve click must deliver approval")
	}
	sender.mu.Lock()
	last = sender.interactions[len(sender.interactions)-1]
	sender.mu.Unlock()
	if !strings.HasPrefix(last, "update\x00") || !strings.Contains(last, "Approved") {
		t.Errorf("verdict edit = %q", last)
	}
	// Unpaired clicker rejected.
	b.onInteractionCreate(nil, componentInteraction("200", "dc:a:gate1:1", "prompt"))
	sender.mu.Lock()
	last = sender.interactions[len(sender.interactions)-1]
	sender.mu.Unlock()
	if !strings.Contains(last, "Not paired") {
		t.Errorf("unpaired click reply = %q", last)
	}
}

func TestClarifyButtonAndOtherFlow(t *testing.T) {
	b, sender, responders := newTestBot(t, []int64{100})
	b.ClarifyPrompt("sess1", "c1", "Which color?", []string{"red", "blue"}, false)
	b.onInteractionCreate(nil, componentInteraction("100", "dc:c:c1:1", "Which color?"))
	responders.mu.Lock()
	ans := responders.answered["c1"]
	responders.mu.Unlock()
	if len(ans) != 1 || !strings.Contains(ans[0], "blue") {
		t.Errorf("choice answer = %v, want blue", ans)
	}
	// Other arms free-text capture; the next DM answers.
	b.ClarifyPrompt("sess1", "c2", "Which size?", []string{"s", "m"}, false)
	b.onInteractionCreate(nil, componentInteraction("100", "dc:c:c2:other", "Which size?"))
	b.onMessageCreate(nil, dmMessage("100", "extra large"))
	responders.mu.Lock()
	ans2 := responders.answered["c2"]
	responders.mu.Unlock()
	if len(ans2) != 1 || ans2[0] != "extra large" {
		t.Errorf("free-text answer = %v", ans2)
	}
	_ = sender
}

func TestPushRouting(t *testing.T) {
	b, sender, _ := newTestBot(t, []int64{100, 101})
	// Bind session sess9 to user 100 only.
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[state.ChatKey(ChannelName, 100)] = state.Chat{SessionID: "sess9", Notify: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b.ApprovalPrompt("sess9", "g9", "read", "low", "r", "c")
	sender.mu.Lock()
	nb := len(sender.buttons)
	sender.mu.Unlock()
	if nb != 1 {
		t.Errorf("gate-routed approval sent %d messages, want 1 (originating DM only)", nb)
	}
	b.TaskEvent("completed", "t1", "Title", "done")
	sender.mu.Lock()
	ns := len(sender.sends)
	sender.mu.Unlock()
	if ns != 1 {
		t.Errorf("task event sent %d messages, want 1 (notify user only)", ns)
	}
}
