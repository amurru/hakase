package agent

import (
	"amurru/hakase/internal/config"
	"strings"
	"testing"
)

func TestGuardDefaultsFill(t *testing.T) {
	c := LoopGuardConfig(config.LoopGuardConfig{})
	if c.MaxOutputTokens != defaultMaxOutputTokens {
		t.Errorf("MaxOutputTokens default = %d, want %d", c.MaxOutputTokens, defaultMaxOutputTokens)
	}
	if c.RepetitionLimit != defaultRepetitionLimit {
		t.Errorf("RepetitionLimit default = %d, want %d", c.RepetitionLimit, defaultRepetitionLimit)
	}
	if c.MaxTextWithoutTool != defaultMaxTextWithoutTool {
		t.Errorf("MaxTextWithoutTool default = %d, want %d", c.MaxTextWithoutTool, defaultMaxTextWithoutTool)
	}

	// Explicit nonzero values are preserved.
	c2 := LoopGuardConfig(config.LoopGuardConfig{MaxOutputTokens: 100, RepetitionLimit: 2, MaxTextWithoutTool: 50})
	if c2.MaxOutputTokens != 100 || c2.RepetitionLimit != 2 || c2.MaxTextWithoutTool != 50 {
		t.Errorf("explicit values not preserved: %+v", c2)
	}
}

func TestGuardRepetitionLoop(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{RepetitionLimit: 4, MaxTextWithoutTool: 1000000})
	sentence := "Let me load the skill and search the knowledge base."
	for i := 0; i < 3; i++ {
		if reason := g.Feed(false, sentence); reason != "" {
			t.Fatalf("feed %d triggered early: %q", i+1, reason)
		}
	}
	// 4th identical chunk triggers.
	if reason := g.Feed(false, sentence); reason != "repetition_loop" {
		t.Errorf("expected repetition_loop, got %q", reason)
	}
}

func TestGuardRepetitionDifferentTextDoesNotTrigger(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{RepetitionLimit: 4, MaxTextWithoutTool: 1000000})
	for i := 0; i < 20; i++ {
		if reason := g.Feed(false, "distinct sentence "+strings.Repeat("x", i)); reason != "" {
			t.Fatalf("feed %d triggered unexpectedly: %q", i+1, reason)
		}
	}
}

func TestGuardRepetitionNormalizesWhitespace(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{RepetitionLimit: 3, MaxTextWithoutTool: 1000000})
	g.Feed(false, " identical ")
	g.Feed(false, "identical")
	// Whitespace variants trim to the same sentence, so they still count as a
	// repetition rather than resetting the counter.
	if reason := g.Feed(false, "identical"); reason != "repetition_loop" {
		t.Errorf("expected repetition_loop after 3 whitespace-variant matches, got %q", reason)
	}
}

func TestGuardToolCallResetsRepetition(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{RepetitionLimit: 3, MaxTextWithoutTool: 1000000})
	g.Feed(false, "repeated")
	g.Feed(false, "repeated")
	g.Feed(true, "") // tool call resets repetition
	g.Feed(false, "repeated")
	g.Feed(false, "repeated")
	if reason := g.Feed(false, "repeated"); reason != "repetition_loop" {
		t.Errorf("expected repetition_loop after reset, got %q", reason)
	}
}

func TestGuardNoToolCallBloat(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{MaxTextWithoutTool: 100, RepetitionLimit: 1000000})
	var total int
	for total < 90 {
		chunk := strings.Repeat("a", 30)
		g.Feed(false, chunk)
		total += len(chunk)
	}
	if reason := g.Feed(false, strings.Repeat("a", 30)); reason != "no_tool_call" {
		t.Errorf("expected no_tool_call, got %q", reason)
	}
}

func TestGuardToolCallDisablesNoToolWatchdog(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{MaxTextWithoutTool: 50, RepetitionLimit: 1000000})
	g.Feed(true, "") // a tool call happens early
	for i := 0; i < 10; i++ {
		if reason := g.Feed(false, strings.Repeat("b", 40)); reason != "" {
			t.Fatalf("feed after tool call triggered unexpectedly: %q", reason)
		}
	}
}

func TestGuardEmptyTextIgnored(t *testing.T) {
	g := GuardDefaults(config.LoopGuardConfig{MaxTextWithoutTool: 1, RepetitionLimit: 2})
	if reason := g.Feed(false, "   "); reason != "" {
		t.Errorf("whitespace-only text should not trigger: %q", reason)
	}
	if reason := g.Feed(false, ""); reason != "" {
		t.Errorf("empty text should not trigger: %q", reason)
	}
}

func TestGuardZeroValueDisabled(t *testing.T) {
	var g DegenerationGuard // zero value: all limits 0 -> disabled
	for i := 0; i < 100; i++ {
		if reason := g.Feed(false, "same text"); reason != "" {
			t.Fatalf("zero-value guard triggered: %q", reason)
		}
	}
}

func TestBuildGenerationConfigCapsOutput(t *testing.T) {
	old := deps
	defer func() { deps = old }()
	deps = &Deps{LoopGuard: config.LoopGuardConfig{MaxOutputTokens: 4096}}

	gc := BuildGenerationConfig("")
	if gc == nil {
		t.Fatal("BuildGenerationConfig returned nil for empty level")
	}
	if gc.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096", gc.MaxOutputTokens)
	}

	// Thinking off still keeps the cap.
	gcOff := BuildGenerationConfig("off")
	if gcOff == nil || gcOff.MaxOutputTokens != 4096 {
		t.Errorf("off config missing cap: %+v", gcOff)
	}
	if gcOff.ThinkingConfig == nil || gcOff.ThinkingConfig.IncludeThoughts {
		t.Errorf("off config thoughts not disabled: %+v", gcOff.ThinkingConfig)
	}
}

func TestGuardReasonLog(t *testing.T) {
	cases := map[string]string{
		"repetition_loop": "loop",
		"no_tool_call":    "tool",
		"custom":          "custom",
	}
	for reason, want := range cases {
		if got := GuardReasonLog(reason); !strings.Contains(got, want) {
			t.Errorf("GuardReasonLog(%q) = %q, want it to contain %q", reason, got, want)
		}
	}
}
