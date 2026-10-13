package agent

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// ValidThinkingLevels is the canonical per-turn effort menu: the levels both
// providers honor (genai ThinkingLevel on Gemini; the Responses
// reasoning-effort map in ADK's openaimodel). "" (unset) means "no override,
// use the server's configured thinking_level".
var ValidThinkingLevels = []string{"off", "minimal", "low", "medium", "high"}

// thinkingLevelAliases maps historically accepted names onto the canonical
// set. "maximum"/"xhigh" were accepted by the config endpoint but rejected by
// both providers (genai knows only MINIMAL..HIGH and openaimodel errors on
// anything outside that map), so every turn failed. They now mean HIGH —
// the strongest effort either backend supports — instead of breaking the run.
var thinkingLevelAliases = map[string]string{
	"maximum": "high",
	"xhigh":   "high",
}

// NormalizeThinkingLevel canonicalizes a user-supplied thinking level:
// trims, lowercases, resolves aliases, and rejects anything neither provider
// can honor. "" passes through as "no override". "on"/"default" pass through
// unchanged (provider-default thinking depth, thoughts included).
func NormalizeThinkingLevel(level string) (string, error) {
	norm := strings.ToLower(strings.TrimSpace(level))
	switch norm {
	case "":
		return "", nil
	case "on", "default":
		return norm, nil
	}
	if alias, ok := thinkingLevelAliases[norm]; ok {
		return alias, nil
	}
	for _, valid := range ValidThinkingLevels {
		if norm == valid {
			return norm, nil
		}
	}
	return "", fmt.Errorf("invalid thinking_level %q: must be one of off, minimal, low, medium, high", level)
}

// thinkingLevelKey carries the per-turn thinking override through the run
// context from the transport (web chat POST) to the BeforeModelCallback.
// Unexported: transports use WithThinkingLevel, the callback reads it back.
type thinkingLevelKey struct{}

// WithThinkingLevel returns ctx carrying a per-turn thinking-level override
// for ThinkingOverrideCallback. An empty level returns ctx unchanged (no
// override — the agent's startup generation config applies).
func WithThinkingLevel(ctx context.Context, level string) context.Context {
	if strings.TrimSpace(level) == "" {
		return ctx
	}
	return context.WithValue(ctx, thinkingLevelKey{}, level)
}

// ThinkingLevelFrom reports the per-turn override carried by ctx, if any.
func ThinkingLevelFrom(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	level, ok := ctx.Value(thinkingLevelKey{}).(string)
	if !ok || strings.TrimSpace(level) == "" {
		return "", false
	}
	return level, true
}

// thinkingConfigFor builds the ThinkingConfig for a normalized level,
// mirroring BuildGenerationConfig's mapping: "off" disables thoughts, "on"/
// "default" take the provider default depth, anything else names the level
// verbatim (upper-cased) for the provider.
func thinkingConfigFor(norm string) *genai.ThinkingConfig {
	tc := &genai.ThinkingConfig{IncludeThoughts: true}
	switch norm {
	case "off":
		tc.IncludeThoughts = false
	case "on", "default":
		// Provider default level; only IncludeThoughts is set.
	default:
		tc.ThinkingLevel = genai.ThinkingLevel(strings.ToUpper(norm))
	}
	return tc
}

// ThinkingOverrideCallback is a BeforeModelCallback that applies the run's
// per-turn thinking-level override (see WithThinkingLevel) to the outgoing
// model request. Without an override it returns nil, nil — the agent's
// startup generation config applies untouched.
//
// It never mutates the agent's shared GenerateContentConfig in place: the
// request config is cloned first, so concurrent turns with different effort
// levels cannot race. A nil request config is replaced with a fresh build.
func ThinkingOverrideCallback(ctx agent.Context, req *model.LLMRequest) (*model.LLMResponse, error) {
	level, ok := ThinkingLevelFrom(ctx)
	if !ok {
		return nil, nil
	}
	norm, err := NormalizeThinkingLevel(level)
	if err != nil || norm == "" {
		// Unset validates to "" (nothing to apply); garbage was rejected at
		// the transport edge, so fall back to the startup config rather than
		// breaking the turn.
		return nil, nil
	}
	if req == nil {
		return nil, nil
	}
	if req.Config == nil {
		req.Config = BuildGenerationConfig(norm)
		return nil, nil
	}
	cloned := *req.Config
	cloned.ThinkingConfig = thinkingConfigFor(norm)
	req.Config = &cloned
	return nil, nil
}
