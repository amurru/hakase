package agent

import (
	"amurru/hakase/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/model/gemini"
	"google.golang.org/adk/v2/model/openaimodel"
	"google.golang.org/genai"
)

// boolPtr returns a pointer to the given bool value.
func boolPtr(b bool) *bool { return &b }

// LLMProvider abstracts model creation and configuration validation across
// supported backends. The provider factory returns the concrete provider
// matching a config, isolating backend-specific APIs behind one interface.
type LLMProvider interface {
	CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error)
	ValidateConfig(cfg *config.Config) error
	GetDefaultModel() string
	GetModelInfo(ctx context.Context, cfg *config.Config, modelName string) (*ModelInfo, error)
}

// GeminiProvider creates models through the Google Gemini backend.
type GeminiProvider struct{}

// CreateModel constructs a Gemini model via the ADK v2 gemini package.
func (p *GeminiProvider) CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error) {
	return gemini.NewModel(ctx, modelName, &genai.ClientConfig{APIKey: apiKey})
}

// ValidateConfig returns an error when no API key is configured.
func (p *GeminiProvider) ValidateConfig(cfg *config.Config) error {
	if cfg.APIKey == "" {
		return fmt.Errorf("gemini provider requires an api_key")
	}
	return nil
}

// GetDefaultModel returns the default Gemini model name.
func (p *GeminiProvider) GetDefaultModel() string {
	return config.DefaultModelForProvider("gemini")
}

// GetModelInfo queries the Gemini API for the model's context window and
// thinking support via the models.get endpoint.
func (p *GeminiProvider) GetModelInfo(ctx context.Context, cfg *config.Config, modelName string) (*ModelInfo, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.APIKey})
	if err != nil {
		return nil, err
	}
	m, err := client.Models.Get(ctx, modelName, nil)
	if err != nil {
		return nil, err
	}
	return &ModelInfo{
		Name:            modelName,
		ContextWindow:   int64(m.InputTokenLimit),
		MaxInputTokens:  int64(m.InputTokenLimit),
		ThinkingEnabled: m.Thinking,
		SupportsVision:  boolPtr(true),
		Source:          "gemini models.get",
	}, nil
}

// OpenAIProvider creates models through OpenAI or any OpenAI-compatible
// endpoint. v2 openaimodel is marked EXPERIMENTAL; factory isolates it so
// swapping later is one-file change.
type OpenAIProvider struct {
	BaseURL string
}

// CreateModel constructs an OpenAI-compatible model via the ADK v2
// openaimodel package. BaseURL is only set when non-empty so the default
// OpenAI endpoint is used otherwise.
func (p *OpenAIProvider) CreateModel(ctx context.Context, modelName, apiKey string) (model.LLM, error) {
	cfg := &openaimodel.ClientConfig{APIKey: apiKey}
	if p.BaseURL != "" {
		cfg.BaseURL = p.BaseURL
	}
	return openaimodel.NewModel(ctx, modelName, cfg)
}

// ValidateConfig returns an error when no API key is configured, or when an
// OpenAI-compatible endpoint is selected without an explicit model_name (the
// model identifier is endpoint-specific, so there is no universal default).
func (p *OpenAIProvider) ValidateConfig(cfg *config.Config) error {
	if cfg.APIKey == "" {
		return fmt.Errorf("openai provider requires an api_key")
	}
	if cfg.Provider == "openai-compatible" && strings.TrimSpace(cfg.ModelName) == "" {
		return fmt.Errorf("openai-compatible provider requires a model_name")
	}
	return nil
}

// GetDefaultModel returns the default OpenAI model name.
func (p *OpenAIProvider) GetDefaultModel() string {
	return config.DefaultModelForProvider("openai")
}

// openAIModelInfo mirrors the model objects returned by OpenAI-compatible
// model endpoints. context_length is an OpenRouter extension; input_token_limit
// appears on some self-hosted endpoints. reasoning is either a bool or an
// object (e.g. {"effort": true}), so it is captured raw and inspected.
type openAIModelInfo struct {
	ID                  string          `json:"id"`
	ContextLength       int64           `json:"context_length"`
	InputTokenLimit     int64           `json:"input_token_limit"`
	Reasoning           json.RawMessage `json:"reasoning"`
	SupportedParameters []string        `json:"supported_parameters"`
	InputModalities     []string        `json:"input_modalities"`
}

// reasoningDetail carries the fields of the OpenRouter reasoning object that
// are relevant for display: the default effort level and the supported ones.
type reasoningDetail struct {
	DefaultEffort    string   `json:"default_effort"`
	SupportedEfforts []string `json:"supported_efforts"`
}

// reasoningSupported reports whether the raw reasoning field (or the
// supported_parameters list) indicates a reasoning/thinking-capable model.
func (m *openAIModelInfo) reasoningSupported() bool {
	if len(m.Reasoning) > 0 && string(m.Reasoning) != "false" && string(m.Reasoning) != "null" {
		return true
	}
	for _, p := range m.SupportedParameters {
		if p == "reasoning" || p == "thinking" {
			return true
		}
	}
	return false
}

// visionSupported reports whether the model accepts image input. The
// authoritative signal is OpenRouter's input_modalities list. When that is
// absent, supported_parameters entries like "image_url" imply vision.
// Returns nil when the endpoint reports neither (unknown).
func (m *openAIModelInfo) visionSupported() *bool {
	if len(m.InputModalities) > 0 {
		for _, mod := range m.InputModalities {
			if mod == "image" {
				return boolPtr(true)
			}
		}
		return boolPtr(false)
	}
	for _, p := range m.SupportedParameters {
		if p == "image_url" || p == "image" || p == "images" {
			return boolPtr(true)
		}
	}
	return nil
}

// thinkingLevel reports the provider's default reasoning effort, if exposed.
func (m *openAIModelInfo) thinkingLevel() string {
	if len(m.Reasoning) == 0 || string(m.Reasoning) == "false" || string(m.Reasoning) == "null" {
		return ""
	}
	var d reasoningDetail
	if err := json.Unmarshal(m.Reasoning, &d); err != nil || d.DefaultEffort == "" {
		return ""
	}
	return d.DefaultEffort
}

// GetModelInfo queries the OpenAI-compatible models endpoint for the model's
// context window and reasoning support. It tries the per-model endpoint first
// (GET /models/{name}) and falls back to scanning the full model list when the
// endpoint does not exist.
func (p *OpenAIProvider) GetModelInfo(ctx context.Context, cfg *config.Config, modelName string) (*ModelInfo, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	client := &http.Client{Timeout: 15 * time.Second}

	fetch := func(path string) (*openAIModelInfo, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
		}
		var info openAIModelInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return nil, err
		}
		return &info, nil
	}

	info, err := fetch("/models/" + modelName)
	if err == nil && info.ID != "" {
		return info.toModelInfo(modelName), nil
	}

	// Some endpoints only support listing; find the model in the list.
	list := struct {
		Data []openAIModelInfo `json:"data"`
	}{}
	if lerr := func() error {
		body, gerr := fetchRaw(client, ctx, base, "/models", cfg.APIKey)
		if gerr != nil {
			return gerr
		}
		return json.Unmarshal(body, &list)
	}(); lerr != nil {
		if err == nil {
			return nil, lerr
		}
		return nil, err
	}
	for i := range list.Data {
		if list.Data[i].ID == modelName {
			return list.Data[i].toModelInfo(modelName), nil
		}
	}
	return nil, fmt.Errorf("model %q not found in %s", modelName, base+"/models")
}

func (m *openAIModelInfo) toModelInfo(name string) *ModelInfo {
	limit := m.ContextLength
	if limit == 0 {
		limit = m.InputTokenLimit
	}
	return &ModelInfo{
		Name:            name,
		ContextWindow:   limit,
		MaxInputTokens:  m.InputTokenLimit,
		ThinkingEnabled: m.reasoningSupported(),
		ThinkingLevel:   m.thinkingLevel(),
		SupportsVision:  m.visionSupported(),
		Source:          "models endpoint",
	}
}

func fetchRaw(client *http.Client, ctx context.Context, base, path, apiKey string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// embedBatchSize caps the inputs per embeddings request. OpenAI allows
// 2048, but self-hosted OpenAI-compatible endpoints (Ollama, vLLM) vary;
// 32 stays well inside every known limit while keeping backfills to a
// handful of calls.
const embedBatchSize = 32

// embedTimeout bounds a single embeddings batch request.
const embedTimeout = 30 * time.Second

// openAIEmbeddingResponse mirrors the OpenAI embeddings response. Data
// entries carry their input index; placement below is by index so
// out-of-order batches cannot scramble vectors.
type openAIEmbeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// EmbedTexts returns one embedding vector per input text, in input order,
// via POST {base}/embeddings. baseURL empty selects the OpenAI default.
// Large inputs are split into sequential batches of embedBatchSize.
// A non-200 response is an error quoting the status and the body head so
// misbehaving self-hosted endpoints are diagnosable.
func (p *OpenAIProvider) EmbedTexts(ctx context.Context, apiKey, baseURL, model string, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	base := strings.TrimRight(baseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += embedBatchSize {
		end := start + embedBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		vecs, err := p.embedBatch(ctx, base, apiKey, model, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (p *OpenAIProvider) embedBatch(ctx context.Context, base, apiKey, model string, texts []string) ([][]float32, error) {
	payload, err := json.Marshal(map[string]any{"model": model, "input": texts})
	if err != nil {
		return nil, err
	}
	bctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(bctx, http.MethodPost, base+"/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		head := string(body)
		if len(head) > 300 {
			head = head[:300]
		}
		return nil, fmt.Errorf("embeddings endpoint returned HTTP %d: %s", resp.StatusCode, head)
	}
	var er openAIEmbeddingResponse
	if err := json.Unmarshal(body, &er); err != nil {
		return nil, fmt.Errorf("decoding embeddings response: %w", err)
	}
	vecs := make([][]float32, len(texts))
	seen := make([]bool, len(texts))
	for _, d := range er.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embeddings response index %d out of range for %d inputs", d.Index, len(texts))
		}
		vecs[d.Index] = d.Embedding
		seen[d.Index] = true
	}
	for i, ok := range seen {
		if !ok {
			return nil, fmt.Errorf("embeddings response missing vector for input %d", i)
		}
	}
	return vecs, nil
}

// providerForName returns the single provider matching name, defaulting to
// Gemini when the name is empty. It never wraps in a fallback chain; use
// ProviderFactory for the config-driven (possibly fallback-aware) provider.
func providerForName(provider, baseURL string) (LLMProvider, error) {
	switch provider {
	case "gemini", "":
		return &GeminiProvider{}, nil
	case "openai", "openai-compatible":
		return &OpenAIProvider{BaseURL: baseURL}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// ProviderFactory returns the provider matching cfg.Provider, defaulting to
// Gemini when the field is empty. When cfg.FallbackProviders is non-empty the
// returned provider is a *FallbackProvider whose CreateModel yields a
// fallback-aware model: per-request provider outages fall over to the next
// provider in the chain (primary first) instead of failing the run.
func ProviderFactory(cfg *config.Config) (LLMProvider, error) {
	primary, err := providerForName(cfg.Provider, cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if len(cfg.FallbackProviders) == 0 {
		return primary, nil
	}
	providers := []LLMProvider{primary}
	for _, name := range cfg.FallbackProviders {
		p, err := providerForName(name, cfg.BaseURL)
		if err != nil {
			log.Printf("fallback: skipping broken fallback provider %q: %v", name, err)
			continue
		}
		providers = append(providers, p)
	}
	return &FallbackProvider{cfg: cfg, providers: providers}, nil
}
