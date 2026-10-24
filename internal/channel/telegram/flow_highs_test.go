// flow_highs_test.go - S2 streaming freeze + S8 push fan-out tests
// (highs fix-up lane, epic #124).
package telegram

import (
	"context"
	"strings"
	"testing"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel/state"
	"amurru/hakase/internal/permissions"
	hakasesession "amurru/hakase/internal/session"
)

// installFlowV2 compiles and installs a v2 policy for the test (the
// flowExperimental gate is required for version 2 to load).
func installFlowV2(t *testing.T, p permissions.Policy) {
	t.Helper()
	t.Setenv("HAKASE_FLOW_EXPERIMENTAL", "1")
	cp, err := permissions.Compile(p)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	permissions.Install(cp)
	t.Cleanup(func() { permissions.Install(nil) })
}

// bindChatSession creates a session and binds the chat to it.
func bindChatSession(t *testing.T, b *Bot, chatID int64, notify bool) string {
	t.Helper()
	sess, err := b.sessions.CreateSession("flow test")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[chatKey(chatID)] = state.Chat{SessionID: sess.ID, Notify: notify}
		return nil
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	return sess.ID
}

func flowV2SendRequiresPublic() permissions.Policy {
	return permissions.Policy{Version: 2, Default: "ask", Rules: []permissions.Rule{
		{Action: "send_message", Resource: "*", Effect: "allow", Requires: &permissions.FlowRequires{Trust: "trusted", AudienceContains: []string{"public"}}},
	}}
}

// S8: push fan-out gates per recipient (narrowed chats skip, the rest
// still deliver).
func TestPushFanoutPerRecipientGating(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	narrowed := bindChatSession(t, b, 100, true)
	open := bindChatSession(t, b, 101, true)
	hakasesession.DefaultRegistry.Fold(narrowed,
		permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	hakasesession.DefaultRegistry.Ensure(open)

	b.CronEvent("", "completed", "j1", "nightly", "all good", "")
	b.TaskEvent("", "completed", "t1", "job", "done")

	var to100, to101 int
	for _, s := range api.sends() {
		if !strings.Contains(s.text, "nightly") && !strings.Contains(s.text, "task") {
			continue
		}
		switch s.chatID {
		case 100:
			to100++
		case 101:
			to101++
		}
	}
	if to100 != 0 {
		t.Errorf("narrowed chat 100 received %d pushes, want 0 (withheld)", to100)
	}
	if to101 != 2 {
		t.Errorf("open chat 101 received %d pushes, want 2 (cron+task)", to101)
	}
}

// S8: without a v2 policy every push delivers (v1 parity).
func TestPushFanoutV1Parity(t *testing.T) {
	b, api := newRunTestBot(t)
	permissions.Install(nil)
	bindChatSession(t, b, 100, true)
	bindChatSession(t, b, 101, true)

	b.CronEvent("", "completed", "j1", "nightly", "all good", "")

	got := 0
	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			got++
		}
	}
	if got != 2 {
		t.Errorf("v1 cron pushes = %d, want 2 (no gating without v2)", got)
	}
}

// S2: a run narrowed before streaming never creates the answer message:
// the status line keeps ticking (liveness) and finalize delivers exactly
// one withhold notice.
func TestStreamingFreezeWithholdNoAnswerOneNotice(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	sessID := bindChatSession(t, b, 100, false)
	hakasesession.DefaultRegistry.Fold(sessID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")

	b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
		sink.OnStream("s", "hello world answer that must never render", "")
		time.Sleep(120 * time.Millisecond)
	}}

	c := rootConv(100)
	b.startRun(context.Background(), c, 50, "prompt", nil, nil, nil, "", false)
	waitRunDone(t, b, c)

	var notices, answers int
	var statusID int
	for _, s := range api.sends() {
		switch {
		case strings.HasPrefix(s.text, "⛔"):
			notices++
		case strings.HasPrefix(s.text, "⚙"):
			statusID = s.msgID
		default:
			answers++
		}
	}
	if answers != 0 {
		t.Errorf("answer creations = %d, want 0 (frozen under withhold)", answers)
	}
	if notices != 1 {
		t.Errorf("withhold notices = %d, want exactly 1", notices)
	}
	if statusID == 0 {
		t.Fatal("status line was never created")
	}
	// Liveness: the status line ticks while the answer is frozen, then
	// the done line lands.
	if edits := api.editsFor(statusID); len(edits) < 2 {
		t.Errorf("status edits = %d, want >= 2 (ticks + done)", len(edits))
	} else if last := edits[len(edits)-1]; !strings.HasPrefix(last, "✓") {
		t.Errorf("final status = %q, want the done line", last)
	}
	if got := api.reactionsFor(50); len(got) != 2 || got[1].emoji != reactionFailed {
		t.Fatalf("reactions = %+v, want [👀 ❌]", got)
	}
}

// S2: narrowing mid-run freezes answer edits (post-narrow deltas never
// leak into the created message); finalize swaps it for the one notice.
func TestStreamingFreezeMidRunNarrow(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	sessID := bindChatSession(t, b, 100, false)
	hakasesession.DefaultRegistry.Ensure(sessID)

	b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
		sink.OnStream("s", "FIRST-CHUNK-AAA ", "")
		time.Sleep(80 * time.Millisecond) // answer created + edited pre-narrow
		hakasesession.DefaultRegistry.Fold(sessID,
			permissions.FlowDelta{Audience: []string{"internal"}}, "mid", "h")
		sink.OnStream("s", "SECOND-CHUNK-BBB", "")
		time.Sleep(80 * time.Millisecond)
	}}

	c := rootConv(100)
	b.startRun(context.Background(), c, 51, "prompt", nil, nil, nil, "", false)
	waitRunDone(t, b, c)

	var answerID int
	var creation string
	for _, s := range api.sends() {
		if !strings.HasPrefix(s.text, "⚙") && !strings.HasPrefix(s.text, "⛔") {
			answerID = s.msgID
			creation = s.text
		}
	}
	if answerID == 0 {
		t.Fatal("answer was never created pre-narrow")
	}
	if !strings.Contains(creation, "FIRST-CHUNK-AAA") {
		t.Errorf("creation text = %q, want pre-narrow content", creation)
	}
	if strings.Contains(creation, "SECOND-CHUNK-BBB") {
		t.Fatalf("post-narrow delta in creation text: %q", creation)
	}
	edits := api.editsFor(answerID)
	for _, e := range edits {
		if strings.Contains(e, "SECOND-CHUNK-BBB") {
			t.Fatalf("post-narrow delta leaked into the answer: %q", e)
		}
	}
	if len(edits) == 0 {
		t.Fatal("finalize never replaced the answer with the notice")
	}
	last := edits[len(edits)-1]
	if !strings.Contains(last, "⛔") {
		t.Errorf("final answer = %q, want the single withhold notice", last)
	}
}

// S8/H1: a narrowed producer withholds the push on every recipient, even
// when all recipient chats are open. The payload's taint rides the
// producer session, not the fan-out list.
func TestPushFanoutProducerNarrowedWithholds(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	// Recipients stay open.
	bindChatSession(t, b, 100, true)
	bindChatSession(t, b, 101, true)
	// Producer narrowed (a tainted run's session).
	prod, err := b.sessions.CreateSession("flow producer")
	if err != nil {
		t.Fatalf("create producer session: %v", err)
	}
	hakasesession.DefaultRegistry.Fold(prod.ID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "prod-k", "h")

	b.CronEvent(prod.ID, "completed", "j1", "nightly", "all good", "")
	b.TaskEvent(prod.ID, "completed", "t1", "job", "done")

	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") || strings.Contains(s.text, "task") {
			t.Errorf("narrowed producer push delivered to chat %d: %q", s.chatID, s.text)
		}
	}
}

// S8/H1: an open producer still withholds per recipient (recipient-side
// narrowing is independent of the producer).
func TestPushFanoutProducerOpenRecipientNarrowed(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	narrowed := bindChatSession(t, b, 100, true)
	open := bindChatSession(t, b, 101, true)
	hakasesession.DefaultRegistry.Fold(narrowed,
		permissions.FlowDelta{Audience: []string{"internal"}}, "k", "h")
	hakasesession.DefaultRegistry.Ensure(open)

	prod, err := b.sessions.CreateSession("flow producer open")
	if err != nil {
		t.Fatalf("create producer session: %v", err)
	}
	hakasesession.DefaultRegistry.Ensure(prod.ID)

	b.CronEvent(prod.ID, "completed", "j1", "nightly", "all good", "")

	var to100, to101 int
	for _, s := range api.sends() {
		if !strings.Contains(s.text, "nightly") {
			continue
		}
		switch s.chatID {
		case 100:
			to100++
		case 101:
			to101++
		}
	}
	if to100 != 0 {
		t.Errorf("narrowed chat 100 received %d pushes, want 0", to100)
	}
	if to101 != 1 {
		t.Errorf("open chat 101 received %d pushes, want 1", to101)
	}
}

// S8/H-2: in topics mode a root/lobby cron push withholds when a bound
// thread session is narrowed, even though the lobby chat itself carries
// no session. Thread-only taint must not bypass via the lobby fan-out.
func TestPushFanoutTopicsModeNarrowedThread(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		s.Chats[chatKey(100)] = state.Chat{Notify: true, TopicsMode: true}
		return nil
	}); err != nil {
		t.Fatalf("enable topics lobby: %v", err)
	}
	threadSess, err := b.sessions.CreateSession("flow thread")
	if err != nil {
		t.Fatalf("create thread session: %v", err)
	}
	hakasesession.DefaultRegistry.Fold(threadSess.ID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "thread-k", "h")
	if err := b.bindThread(conv{chatID: 100, threadID: 5}, threadSess.ID, threadSess.Title); err != nil {
		t.Fatalf("bind thread: %v", err)
	}
	// Open-thread control: a second thread bound to an open session must
	// not lift the withhold - the lobby inherits the narrowest binding.
	openSess, err := b.sessions.CreateSession("flow thread open")
	if err != nil {
		t.Fatalf("create open thread session: %v", err)
	}
	hakasesession.DefaultRegistry.Ensure(openSess.ID)
	if err := b.bindThread(conv{chatID: 100, threadID: 6}, openSess.ID, openSess.Title); err != nil {
		t.Fatalf("bind open thread: %v", err)
	}

	b.CronEvent("", "completed", "j1", "nightly", "all good", "")

	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			t.Errorf("topics-mode lobby push delivered to chat %d despite narrowed thread: %q", s.chatID, s.text)
		}
	}

	// Causality check: dropping the narrowed binding lets the same lobby
	// push through, so the withhold above came from the narrowed thread
	// and not from a blanket lobby block.
	if err := b.store.Update(func(s *state.State) error {
		delete(s.Threads, threadKey(conv{chatID: 100, threadID: 5}))
		return nil
	}); err != nil {
		t.Fatalf("unbind narrowed thread: %v", err)
	}

	b.CronEvent("", "completed", "j2", "nightly", "all good", "")

	// The first push was withheld (zero sends); only the second lands.
	got := 0
	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			got++
		}
	}
	if got != 1 {
		t.Errorf("lobby push after unbinding the narrowed thread = %d sends, want 1 (open threads deliver)", got)
	}
}

// S8/H-2: without TopicsMode a client-created thread still narrows the
// lobby. The thread scan runs independent of the TopicsMode flag (threads
// are conversations even when the chat never ran /topic), so a narrowed
// client-created thread withholds the lobby fan-out.
func TestPushFanoutLobbyClientThreadNarrowedWithholds(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	if err := b.store.Update(func(s *state.State) error {
		if s.Chats == nil {
			s.Chats = map[string]state.Chat{}
		}
		// No TopicsMode flag: plain lobby with a client-created thread.
		s.Chats[chatKey(100)] = state.Chat{Notify: true}
		return nil
	}); err != nil {
		t.Fatalf("enable lobby: %v", err)
	}
	threadSess, err := b.sessions.CreateSession("flow client thread")
	if err != nil {
		t.Fatalf("create thread session: %v", err)
	}
	hakasesession.DefaultRegistry.Fold(threadSess.ID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "client-k", "h")
	if err := b.bindThread(conv{chatID: 100, threadID: 5}, threadSess.ID, threadSess.Title); err != nil {
		t.Fatalf("bind thread: %v", err)
	}
	// Open-thread control: a second thread bound to an open session must
	// not lift the withhold - the lobby inherits the narrowest binding.
	openSess, err := b.sessions.CreateSession("flow client thread open")
	if err != nil {
		t.Fatalf("create open thread session: %v", err)
	}
	hakasesession.DefaultRegistry.Ensure(openSess.ID)
	if err := b.bindThread(conv{chatID: 100, threadID: 6}, openSess.ID, openSess.Title); err != nil {
		t.Fatalf("bind open thread: %v", err)
	}

	b.CronEvent("", "completed", "j1", "nightly", "all good", "")

	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			t.Errorf("client-thread lobby push delivered to chat %d despite narrowed thread: %q", s.chatID, s.text)
		}
	}

	// Causality check: dropping the narrowed binding lets the same lobby
	// push through, so the withhold above came from the narrowed thread
	// and not from a blanket lobby block.
	if err := b.store.Update(func(s *state.State) error {
		delete(s.Threads, threadKey(conv{chatID: 100, threadID: 5}))
		return nil
	}); err != nil {
		t.Fatalf("unbind narrowed thread: %v", err)
	}

	b.CronEvent("", "completed", "j2", "nightly", "all good", "")

	// The first push was withheld (zero sends); only the second lands.
	got := 0
	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			got++
		}
	}
	if got != 1 {
		t.Errorf("lobby push after unbinding the narrowed thread = %d sends, want 1 (open threads deliver)", got)
	}
}

// S8: delegation pushes go back to the watching thread, not the chat
// root (topics-mode collapse fix: one run per topic stays in its topic).
func TestRunningDestinationsKeepsThreads(t *testing.T) {
	b, _ := newRunTestBot(t)
	c1 := conv{chatID: 100, threadID: 5}
	c2 := conv{chatID: 100, threadID: 6}
	_, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if !b.runs.TryStart(threadKey(c1), "sess-1", cancel1) {
		t.Fatal("TryStart c1")
	}
	defer b.runs.Finish(threadKey(c1))
	if !b.runs.TryStart(threadKey(c2), "sess-2", cancel2) {
		t.Fatal("TryStart c2")
	}
	defer b.runs.Finish(threadKey(c2))

	dests := b.runningDestinations()
	if len(dests) != 2 {
		t.Fatalf("running destinations = %v, want 2 thread convs", dests)
	}
	seen := map[conv]bool{}
	for _, d := range dests {
		seen[d] = true
	}
	if !seen[c1] || !seen[c2] {
		t.Errorf("destinations = %v, want [%v %v]", dests, c1, c2)
	}
}

// S8/H-2: a delegation outcome withholds on the narrowed watching thread
// while the open watching thread still delivers. The payload fans out per
// recipient, never one verdict for the whole fan-out.
func TestDelegationEventNarrowedWatchingThread(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	narrowedSess, err := b.sessions.CreateSession("flow watch narrowed")
	if err != nil {
		t.Fatalf("create narrowed session: %v", err)
	}
	hakasesession.DefaultRegistry.Fold(narrowedSess.ID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "watch-k", "h")
	cNarrowed := conv{chatID: 100, threadID: 5}
	if err := b.bindThread(cNarrowed, narrowedSess.ID, narrowedSess.Title); err != nil {
		t.Fatalf("bind narrowed thread: %v", err)
	}
	openSess, err := b.sessions.CreateSession("flow watch open")
	if err != nil {
		t.Fatalf("create open session: %v", err)
	}
	hakasesession.DefaultRegistry.Ensure(openSess.ID)
	cOpen := conv{chatID: 100, threadID: 6}
	if err := b.bindThread(cOpen, openSess.ID, openSess.Title); err != nil {
		t.Fatalf("bind open thread: %v", err)
	}

	_, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if !b.runs.TryStart(threadKey(cNarrowed), "sess-1", cancel1) {
		t.Fatal("TryStart narrowed")
	}
	defer b.runs.Finish(threadKey(cNarrowed))
	if !b.runs.TryStart(threadKey(cOpen), "sess-2", cancel2) {
		t.Fatal("TryStart open")
	}
	defer b.runs.Finish(threadKey(cOpen))

	b.DelegationEvent("", "completed", "t1", "helper", "did the thing")

	var toNarrowed, toOpen int
	for _, s := range api.sends() {
		if !strings.Contains(s.text, "sub-agent") {
			continue
		}
		switch s.threadID {
		case 5:
			toNarrowed++
		case 6:
			toOpen++
		}
	}
	if toNarrowed != 0 {
		t.Errorf("narrowed watching thread received %d delegation pushes, want 0 (withheld)", toNarrowed)
	}
	if toOpen != 1 {
		t.Errorf("open watching thread received %d delegation pushes, want 1", toOpen)
	}
}

// S8/H-2: after a process restart (cold flow registry) a persisted
// narrowed label still withholds the lobby push: the gate hydrates the
// registry from disk (one bounded load per session) instead of treating
// the session as unregistered. The session's completed history survives
// the resume round-trip unmodified.
func TestPushFanoutResumeHydratesNarrowed(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	sessID := bindChatSession(t, b, 100, true)
	if err := b.sessions.AddMessage("user", "do the thing", ""); err != nil {
		t.Fatalf("add user message: %v", err)
	}
	if err := b.sessions.AddMessage("assistant", "done", ""); err != nil {
		t.Fatalf("add assistant message: %v", err)
	}
	hakasesession.DefaultRegistry.Fold(sessID,
		permissions.FlowDelta{Audience: []string{"internal"}}, "resume-k", "h")
	// Persist the narrowed label alongside the completed history.
	sess, err := b.sessions.Store().Load(sessID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	hakasesession.DefaultRegistry.Sync(sess)
	if err := b.sessions.Store().Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Simulate a process restart: cold registry, hydration ledger cleared.
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })
	flowHydrated.Lock()
	flowHydrated.done = map[string]bool{}
	flowHydrated.Unlock()

	b.CronEvent("", "completed", "j1", "nightly", "all good", "")

	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			t.Errorf("resumed narrowed push delivered to chat %d: %q", s.chatID, s.text)
		}
	}

	// Boundedness: the cold-restart hydration ran exactly once for the
	// recipient session, and the registry now holds the live entry.
	flowHydrated.Lock()
	hydrated := flowHydrated.done[sessID]
	flowHydrated.Unlock()
	if !hydrated {
		t.Errorf("flowHydrated.done[%q] = false, want true (one bounded load per session)", sessID)
	}
	if !hakasesession.DefaultRegistry.IsInitialized(sessID) {
		t.Errorf("registry not initialized for %q after hydration", sessID)
	}

	// The completed history survives the resume round-trip unmodified.
	got, err := b.sessions.Store().Load(sessID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(got.Messages) != 2 {
		t.Errorf("messages after resume = %d, want 2 (completed history intact)", len(got.Messages))
	}
	if got.Flow == nil || got.Flow.Audience != permissions.AudienceInternal {
		t.Errorf("label after resume = %+v, want narrowed/internal", got.Flow)
	}
}

// S8/H-2: after a process restart (cold flow registry) a persisted start
// (open) label still delivers the lobby push: without hydration the cold
// session would read as unregistered and wrongly withhold (fail-closed).
// This is the control that proves hydration runs - the narrowed test above
// withholds either way, so only an open session distinguishes hydrated
// delivery from fail-closed withhold. Bounded to one load per session.
func TestPushFanoutResumeHydratesOpenDelivers(t *testing.T) {
	b, api := newRunTestBot(t)
	installFlowV2(t, flowV2SendRequiresPublic())
	sessID := bindChatSession(t, b, 100, true)
	hakasesession.DefaultRegistry.Ensure(sessID)
	// Persist the start label alongside the completed history.
	sess, err := b.sessions.Store().Load(sessID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	hakasesession.DefaultRegistry.Sync(sess)
	if err := b.sessions.Store().Save(sess); err != nil {
		t.Fatalf("save: %v", err)
	}
	if sess.Flow == nil || !permissions.IsStart(*sess.Flow) {
		t.Fatalf("persisted label = %+v, want start (open)", sess.Flow)
	}

	// Simulate a process restart: cold registry, hydration ledger cleared.
	origReg := hakasesession.DefaultRegistry
	hakasesession.DefaultRegistry = hakasesession.NewFlowRegistry()
	t.Cleanup(func() { hakasesession.DefaultRegistry = origReg })
	flowHydrated.Lock()
	flowHydrated.done = map[string]bool{}
	flowHydrated.Unlock()

	b.CronEvent("", "completed", "j1", "nightly", "all good", "")

	got := 0
	for _, s := range api.sends() {
		if strings.Contains(s.text, "nightly") {
			got++
		}
	}
	if got != 1 {
		t.Errorf("resumed open push sends = %d, want 1 (hydration prevents false withhold)", got)
	}

	// Boundedness: exactly one load for the session per process.
	flowHydrated.Lock()
	hydrated := flowHydrated.done[sessID]
	flowHydrated.Unlock()
	if !hydrated {
		t.Errorf("flowHydrated.done[%q] = false, want true (one bounded load per session)", sessID)
	}
	if !hakasesession.DefaultRegistry.IsInitialized(sessID) {
		t.Errorf("registry not initialized for %q after hydration", sessID)
	}
}
