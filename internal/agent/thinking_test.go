package agent

import (
	"context"
	"testing"

	"amurru/hakase/internal/config"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// thinkingTestCtx wraps an agent.Context to serve values from a plain
// context carrying the thinking override, mirroring how the runner's
// invocation context carries run values in production.
type thinkingTestCtx struct {
	agent.Context
	values context.Context
}

func (c thinkingTestCtx) Value(key any) any { return c.values.Value(key) }

func testAgentCtx(level string) agent.Context {
	base := agent.NewContext(&agent.ContextMock{})
	if level == "" {
		return base
	}
	return thinkingTestCtx{Context: base, values: WithThinkingLevel(context.Background(), level)}
}

func TestNormalizeThinkingLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"off", "off", false},
		{"OFF", "off", false},
		{"  low  ", "low", false},
		{"minimal", "minimal", false},
		{"medium", "medium", false},
		{"HIGH", "high", false},
		// Legacy aliases resolve to the strongest supported effort.
		{"maximum", "high", false},
		{"MAXIMUM", "high", false},
		{"xhigh", "high", false},
		// Provider-default markers pass through.
		{"on", "on", false},
		{"default", "default", false},
		// Anything else is rejected — neither provider honors it.
		{"uber", "", true},
		{"ultra", "", true},
		{"none", "", true},
	}
	for _, tc := range cases {
		got, err := NormalizeThinkingLevel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeThinkingLevel(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeThinkingLevel(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeThinkingLevel(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildGenerationConfigAliases(t *testing.T) {
	// BuildGenerationConfig reads the loop-guard cap from the package deps.
	old := deps
	deps = &Deps{LoopGuard: config.LoopGuardConfig{MaxOutputTokens: 4096}}
	defer func() { deps = old }()

	// "maximum"/"xhigh" previously passed through verbatim (MAXIMUM/XHIGH),
	// which both providers reject — every turn failed. They now map to HIGH.
	for _, level := range []string{"maximum", "xhigh", "MAXIMUM"} {
		gc := BuildGenerationConfig(level)
		if gc == nil || gc.ThinkingConfig == nil {
			t.Fatalf("BuildGenerationConfig(%q) missing ThinkingConfig", level)
		}
		if gc.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelHigh {
			t.Errorf("BuildGenerationConfig(%q).ThinkingLevel = %q, want HIGH",
				level, gc.ThinkingConfig.ThinkingLevel)
		}
	}
	// "minimal" is provider-supported but was missing from validation.
	gc := BuildGenerationConfig("minimal")
	if gc.ThinkingConfig == nil || gc.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelMinimal {
		t.Errorf("BuildGenerationConfig(minimal) = %+v, want MINIMAL level", gc.ThinkingConfig)
	}
	// Garbage no longer emits a provider-rejected level: it falls back to
	// provider-default thinking instead of breaking the run.
	gc = BuildGenerationConfig("uber")
	if gc.ThinkingConfig == nil {
		t.Fatal("BuildGenerationConfig(uber) has nil ThinkingConfig, want fallback")
	}
	if gc.ThinkingConfig.ThinkingLevel != "" {
		t.Errorf("BuildGenerationConfig(uber).ThinkingLevel = %q, want unset fallback",
			gc.ThinkingConfig.ThinkingLevel)
	}
	if !gc.ThinkingConfig.IncludeThoughts {
		t.Error("BuildGenerationConfig(uber) should keep IncludeThoughts in fallback")
	}
}

func TestThinkingOverrideCallbackNoOverride(t *testing.T) {
	base := &genai.GenerateContentConfig{
		MaxOutputTokens: 100,
		ThinkingConfig:  &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: genai.ThinkingLevelLow},
	}
	req := &model.LLMRequest{Config: base}
	resp, err := ThinkingOverrideCallback(testAgentCtx(""), req)
	if err != nil || resp != nil {
		t.Fatalf("no-override callback = (%v, %v), want (nil, nil)", resp, err)
	}
	if req.Config != base {
		t.Error("no-override callback must leave the request config pointer untouched")
	}
}

func TestThinkingOverrideCallbackApplies(t *testing.T) {
	base := &genai.GenerateContentConfig{
		MaxOutputTokens: 100,
		ThinkingConfig:  &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: genai.ThinkingLevelLow},
	}
	req := &model.LLMRequest{Config: base}
	if _, err := ThinkingOverrideCallback(testAgentCtx("high"), req); err != nil {
		t.Fatalf("override callback error: %v", err)
	}
	if req.Config == base {
		t.Fatal("override callback must clone the config, not mutate the shared pointer")
	}
	if req.Config.ThinkingConfig == nil || req.Config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelHigh {
		t.Errorf("override did not apply HIGH: %+v", req.Config.ThinkingConfig)
	}
	if req.Config.MaxOutputTokens != 100 {
		t.Errorf("override dropped MaxOutputTokens: %d", req.Config.MaxOutputTokens)
	}
	// The shared startup config is untouched for other concurrent turns.
	if base.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelLow {
		t.Errorf("shared config mutated: level = %q", base.ThinkingConfig.ThinkingLevel)
	}
}

func TestThinkingOverrideCallbackOffAndAlias(t *testing.T) {
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
	if _, err := ThinkingOverrideCallback(testAgentCtx("off"), req); err != nil {
		t.Fatalf("off callback error: %v", err)
	}
	if req.Config.ThinkingConfig == nil || req.Config.ThinkingConfig.IncludeThoughts {
		t.Errorf("off must disable thoughts: %+v", req.Config.ThinkingConfig)
	}

	req = &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
	if _, err := ThinkingOverrideCallback(testAgentCtx("xhigh"), req); err != nil {
		t.Fatalf("alias callback error: %v", err)
	}
	if req.Config.ThinkingConfig == nil || req.Config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelHigh {
		t.Errorf("xhigh must resolve to HIGH: %+v", req.Config.ThinkingConfig)
	}
}

func TestThinkingOverrideCallbackNilConfig(t *testing.T) {
	old := deps
	deps = &Deps{LoopGuard: config.LoopGuardConfig{MaxOutputTokens: 4096}}
	defer func() { deps = old }()

	req := &model.LLMRequest{}
	if _, err := ThinkingOverrideCallback(testAgentCtx("low"), req); err != nil {
		t.Fatalf("nil-config callback error: %v", err)
	}
	if req.Config == nil || req.Config.ThinkingConfig == nil ||
		req.Config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelLow {
		t.Errorf("nil config must be replaced with a LOW build: %+v", req.Config)
	}
}

func TestWithThinkingLevelRoundTrip(t *testing.T) {
	if _, ok := ThinkingLevelFrom(context.Background()); ok {
		t.Error("empty ctx must carry no override")
	}
	ctx := WithThinkingLevel(context.Background(), "")
	if _, ok := ThinkingLevelFrom(ctx); ok {
		t.Error("empty level must not install an override")
	}
	ctx = WithThinkingLevel(context.Background(), "medium")
	level, ok := ThinkingLevelFrom(ctx)
	if !ok || level != "medium" {
		t.Errorf("round trip = (%q, %v), want (medium, true)", level, ok)
	}
}
