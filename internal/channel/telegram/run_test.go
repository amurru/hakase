package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"amurru/hakase/internal/agentrun"
	"amurru/hakase/internal/channel"

	tgbot "github.com/go-telegram/bot"
	"google.golang.org/genai"
)

// fakeDriver scripts one agent turn: script feeds the sink while RunTurn is
// "running". turned closes when the turn returns, mirroring the driver
// contract the transport relies on.
type fakeDriver struct {
	script func(sink agentrun.EventSink)
	turned chan struct{}
}

func (f *fakeDriver) RunTurn(ctx context.Context, sessionID string, content *genai.Content, sink agentrun.EventSink) {
	defer close(f.turned)
	if f.script != nil {
		f.script(sink)
	}
	sink.OnDone(sessionID)
}

// waitRunDone blocks until the conversation's run (if any) has fully
// completed — RunManager.Finish is deferred past finalize, so an empty run
// slot means the terminal renders have landed too.
func waitRunDone(t *testing.T, b *Bot, c conv) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, running := b.runs.Running(threadKey(c)); !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run did not finish within 3s")
}

func fastTimers(t *testing.T) {
	t.Helper()
	oldStream := streamEditInterval
	streamEditInterval = 20 * time.Millisecond
	t.Cleanup(func() { streamEditInterval = oldStream })
}

func newRunTestBot(t *testing.T) (*Bot, *fakeAPI) {
	t.Helper()
	fastTimers(t)
	b, api, _, _ := newTestBot(t)
	return b, api
}

// TestStreamingThrottledExactFinalRender covers design scenario 6: edits are
// throttled and content-ordered, the final render is the exact converted
// answer, and progress traffic is silent while the answer creation is not
// (scenario 7).
func TestStreamingThrottledExactFinalRender(t *testing.T) {
	b, api := newRunTestBot(t)
	raw := ""
	for i := 0; i < 10; i++ {
		raw += "para **bold** " + string(rune('a'+i)) + " with `code`.\n\n"
	}
	b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
		sink.OnLog("s", "Call: read_file(path)")
		time.Sleep(60 * time.Millisecond) // let the status line post
		for i := 0; i < 10; i++ {         // 10 deltas in ~50ms: far faster than edits
			sink.OnStream("s", raw[i*len(raw)/10:(i+1)*len(raw)/10], "")
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(100 * time.Millisecond)
		sink.OnUsage("s", 39800, 0)
	}}

	c := rootConv(100)
	b.startRun(context.Background(), c, 77, "write me a thing", nil, nil, nil)
	waitRunDone(t, b, c)

	// Answer creation is the one loud send; the status line is silent.
	var loud, silentStatus int
	for _, s := range api.sends() {
		if strings.HasPrefix(s.text, "⚙") {
			if !s.silent {
				t.Errorf("status line created with notification on: %q", s.text)
			}
			silentStatus++
		} else if s.silent {
			t.Errorf("answer message created silently: %q", s.text)
		} else {
			loud++
		}
	}
	if silentStatus == 0 {
		t.Error("status line was never created")
	}
	if loud != 1 {
		t.Fatalf("answer message creations = %d, want exactly 1 (one buzz per turn)", loud)
	}

	// Final edit of the answer message is the exact conversion of the answer.
	answerID := 0
	for _, s := range api.sends() {
		if !strings.HasPrefix(s.text, "⚙") {
			answerID = s.msgID
		}
	}
	edits := api.editsFor(answerID)
	if len(edits) == 0 {
		t.Fatal("answer message was never edited")
	}
	if want := channel.MarkdownToTelegramHTML(raw); edits[len(edits)-1] != want {
		t.Errorf("final render != exact conversion:\n got %q\nwant %q", edits[len(edits)-1], want)
	}
	// Content-ordered: edits never shrink below the previous render length.
	for i := 1; i < len(edits); i++ {
		if len(edits[i]) < len(edits[i-1]) {
			t.Fatalf("edit %d shorter than edit %d (content went backwards)", i, i-1)
		}
	}
	// Throttled: 10 deltas must not produce 10 edits.
	if len(edits) > 8 {
		t.Errorf("edits = %d for 10 fast deltas — throttle not applied", len(edits))
	}

	// Completion line on the status message.
	statusID := 0
	for _, s := range api.sends() {
		if strings.HasPrefix(s.text, "⚙") {
			statusID = s.msgID
		}
	}
	statusEdits := api.editsFor(statusID)
	last := statusEdits[len(statusEdits)-1]
	if !strings.HasPrefix(last, "✓") || !strings.Contains(last, "39.8k tok") {
		t.Errorf("completion line = %q, want ✓ · tokens", last)
	}
}

// TestStreamingOverflowContinuation covers the overflow half of scenario 6:
// a long answer continues in new streaming messages.
func TestStreamingOverflowContinuation(t *testing.T) {
	b, api := newRunTestBot(t)
	var sb strings.Builder
	sb.WriteString("START-MARKER\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("line of reasonably long answer text to fill the bubble quickly\n")
	}
	sb.WriteString("END-MARKER\n")
	raw := sb.String()

	b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
		sink.OnStream("s", raw, "")
		time.Sleep(120 * time.Millisecond)
	}}

	b.startRun(context.Background(), rootConv(100), 42, "long answer please", nil, nil, nil)
	waitRunDone(t, b, rootConv(100))

	// Answer creations: the loud first message plus silent continuations.
	var creations []int
	for _, s := range api.sends() {
		if !strings.HasPrefix(s.text, "⚙") {
			creations = append(creations, s.msgID)
		}
	}
	if len(creations) < 2 {
		t.Fatalf("answer message creations = %d, want ≥2 (overflow continuation)", len(creations))
	}
	first, last := creations[0], creations[len(creations)-1]
	for _, id := range creations[1:] {
		for _, s := range api.sends() {
			if s.msgID == id && !s.silent {
				t.Error("continuation message created with notification on")
			}
		}
	}

	// finalTextOf returns a message's last edit, falling back to its creation
	// text (a message created with its complete segment is never edited).
	finalTextOf := func(id int) string {
		text := ""
		for _, s := range api.sends() {
			if s.msgID == id {
				text = s.text
			}
		}
		if edits := api.editsFor(id); len(edits) > 0 {
			text = edits[len(edits)-1]
		}
		return text
	}
	if !strings.Contains(finalTextOf(first), "START-MARKER") {
		t.Error("first message lost its beginning after overflow split")
	}
	if !strings.Contains(finalTextOf(last), "END-MARKER") {
		t.Error("continuation message missing the answer tail")
	}
}

// TestOverflowKeepsDeltasDuringRender guards the lost-update race in the
// overflow path: a delta appended while the overflow's render call is in
// flight must land in the continuation, never be clobbered by the commit.
func TestOverflowKeepsDeltasDuringRender(t *testing.T) {
	b, api := newRunTestBot(t)

	var blockOnce sync.Once
	blocked := make(chan struct{})
	release := make(chan struct{})
	// Block the first answer-message creation (not the status line) so the
	// next delta demonstrably arrives while the overflow render is in flight.
	api.sendHook = func(params *tgbot.SendMessageParams) {
		if strings.HasPrefix(params.Text, "⚙") {
			return
		}
		blockOnce.Do(func() {
			close(blocked)
			<-release
		})
	}

	b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
		sink.OnStream("s", strings.Repeat("chunk of long answer text\n", 200), "") // ~5400 runes
		time.Sleep(60 * time.Millisecond)                                          // pump tick: overflow commit + blocked creation
		sink.OnStream("s", "TAIL-MARKER", "")                                      // lands during the blocked render
		close(release)
		time.Sleep(60 * time.Millisecond)
	}}

	b.startRun(context.Background(), rootConv(100), 3, "long", nil, nil, nil)
	waitRunDone(t, b, rootConv(100))
	select {
	case <-blocked:
	default:
		t.Fatal("answer creation was never observed (hook did not trigger)")
	}

	// Every delivered answer text, last render per message.
	finals := map[int]string{}
	for _, s := range api.sends() {
		if !strings.HasPrefix(s.text, "⚙") {
			finals[s.msgID] = s.text
		}
	}
	for id := range finals {
		if edits := api.editsFor(id); len(edits) > 0 {
			finals[id] = edits[len(edits)-1]
		}
	}
	joined := ""
	for _, txt := range finals {
		joined += txt + "\n"
	}
	if !strings.Contains(joined, "TAIL-MARKER") {
		t.Fatalf("delta streamed during the overflow render was lost; finals: %v", finals)
	}
	if !strings.Contains(joined, "chunk of long answer text") {
		t.Fatal("the overflowed head was lost")
	}
}

// TestReactionReceipts covers scenario 8: 👀 at turn start, then ✅ on
// success and ❌ on failure, all on the prompt message.
func TestReactionReceipts(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		b, api := newRunTestBot(t)
		b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
			sink.OnStream("s", "all done", "")
			time.Sleep(40 * time.Millisecond)
		}}
		b.startRun(context.Background(), rootConv(100), 5, "prompt", nil, nil, nil)
		waitRunDone(t, b, rootConv(100))

		got := api.reactionsFor(5)
		if len(got) != 2 || got[0].emoji != reactionLooking || got[1].emoji != reactionDone {
			t.Fatalf("reactions = %+v, want [👀 ✅]", got)
		}
	})

	t.Run("failure", func(t *testing.T) {
		b, api := newRunTestBot(t)
		b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
			sink.OnLog("s", "Call: system_exec(cmd)")
			sink.OnLog("s", "Error: provider returned 500")
			time.Sleep(40 * time.Millisecond)
		}}
		b.startRun(context.Background(), rootConv(100), 6, "prompt", nil, nil, nil)
		waitRunDone(t, b, rootConv(100))

		got := api.reactionsFor(6)
		if len(got) != 2 || got[0].emoji != reactionLooking || got[1].emoji != reactionFailed {
			t.Fatalf("reactions = %+v, want [👀 ❌]", got)
		}
		// Failed runs must not buzz: everything silent, error on the status line.
		for _, s := range api.sends() {
			if !s.silent {
				t.Errorf("failed run produced a loud send: %q", s.text)
			}
		}
		var statusEdits []string
		for _, e := range api.edited {
			statusEdits = append(statusEdits, e.text)
		}
		found := false
		for _, txt := range statusEdits {
			if strings.HasPrefix(txt, "❌ provider returned 500") {
				found = true
			}
		}
		if !found {
			t.Fatalf("failure summary missing from edits: %v", statusEdits)
		}
	})
}

// TestPinToggle covers scenario 9: pins=true pins and unpins the prompt
// around the run; pins=false never calls pin.
func TestPinToggle(t *testing.T) {
	t.Run("on", func(t *testing.T) {
		b, api := newRunTestBot(t)
		b.pins = true
		b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
			sink.OnStream("s", "hi", "")
			time.Sleep(40 * time.Millisecond)
		}}
		b.startRun(context.Background(), rootConv(100), 9, "prompt", nil, nil, nil)
		waitRunDone(t, b, rootConv(100))

		if len(api.pins) != 1 || api.pins[0].messageID != 9 {
			t.Fatalf("pins = %+v, want one pin of message 9", api.pins)
		}
		if len(api.unpins) != 1 || api.unpins[0].messageID != 9 {
			t.Fatalf("unpins = %+v, want one unpin of message 9", api.unpins)
		}
	})

	t.Run("off", func(t *testing.T) {
		b, api := newRunTestBot(t)
		b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
			sink.OnStream("s", "hi", "")
			time.Sleep(40 * time.Millisecond)
		}}
		b.startRun(context.Background(), rootConv(100), 9, "prompt", nil, nil, nil)
		waitRunDone(t, b, rootConv(100))

		if len(api.pins) != 0 || len(api.unpins) != 0 {
			t.Fatalf("pins=%v unpins=%v, want none with pins disabled", api.pins, api.unpins)
		}
	})
}

// TestRunWithoutTextNeverBuzzes extends scenario 7: a run that ends without
// text only moves the silent status line and sets ❌.
func TestRunWithoutTextNeverBuzzes(t *testing.T) {
	b, api := newRunTestBot(t)
	b.driver = &fakeDriver{turned: make(chan struct{}), script: func(sink agentrun.EventSink) {
		sink.OnLog("s", "Call: read_file(path)")
		time.Sleep(60 * time.Millisecond)
	}}
	b.startRun(context.Background(), rootConv(100), 11, "prompt", nil, nil, nil)
	waitRunDone(t, b, rootConv(100))

	for _, s := range api.sends() {
		if !s.silent {
			t.Errorf("reply-less run buzzed: %q", s.text)
		}
	}
	if got := api.reactionsFor(11); len(got) != 2 || got[1].emoji != reactionFailed {
		t.Fatalf("reactions = %+v, want 👀 then ❌", got)
	}
}
