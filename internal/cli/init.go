// init.go - the `hakase init` first-run setup wizard.
//
// It writes a minimal config (provider / model / api-key, plus base-url for
// openai-compatible endpoints) so a new user never has to hand-edit the full
// config.json.example. Interactively it prompts for any value not supplied by
// a flag or environment variable and offers to set the web admin password;
// non-interactively every required value must come from a flag or env var, so
// scripted (Docker/CI) provisioning is deterministic and never blocks.
//
// Mirrors the auth.go conventions: readLine/readPassword for input and an int
// exit code (0 = success/help, 1 = runtime failure, 2 = usage error).
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"amurru/hakase/internal/config"
	"golang.org/x/term"
)

// initConfig is the minimal on-disk shape `hakase init` writes: only the
// fields a first run needs, so the file stays readable instead of dumping the
// every-knob example. Field order is the JSON emission order.
type initConfig struct {
	Provider  string `json:"provider"`
	ModelName string `json:"model_name,omitempty"`
	APIKey    string `json:"api_key"`
	BaseURL   string `json:"base_url,omitempty"`
}

// RunInitCLI implements `hakase init`.
func RunInitCLI(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var (
		local        = fs.Bool("local", false, "write ./config.json instead of the user-level config in HAKASE_HOME")
		force        = fs.Bool("force", false, "overwrite an existing config file")
		providerFlag = fs.String("provider", "", "provider: gemini | openai | openai-compatible")
		apiKeyFlag   = fs.String("api-key", "", "API key (defaults to $HAKASE_API_KEY)")
		modelFlag    = fs.String("model", "", "model name (defaults per provider)")
		baseURLFlag  = fs.String("base-url", "", "API endpoint (required for some openai-compatible servers)")
		noPassword   = fs.Bool("no-password", false, "do not offer to set the web admin password")
	)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: hakase init [flags]

Create a minimal config for first use. Interactively, any value not supplied by
a flag or environment variable is prompted for.

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, `
Examples:
  hakase init
  hakase init --provider openai --api-key sk-...
  hakase init --provider openai-compatible --model llama-3.3-70b --base-url http://localhost:11434/v1
`)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "hakase init: unexpected argument %q\n\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	interactive := term.IsTerminal(int(os.Stdin.Fd()))

	provider, code := resolveInitProvider(*providerFlag, interactive)
	if code != 0 {
		return code
	}

	apiKey, code := resolveInitAPIKey(*apiKeyFlag, provider, interactive)
	if code != 0 {
		return code
	}

	model, code := resolveInitModel(*modelFlag, provider, interactive)
	if code != 0 {
		return code
	}

	baseURL := strings.TrimSpace(*baseURLFlag)
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("HAKASE_BASE_URL"))
	}
	if baseURL == "" && provider == "openai-compatible" && interactive {
		v, err := promptLine("Endpoint base URL", "http://localhost:11434/v1")
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase init: failed to read base URL: %v\n", err)
			return 1
		}
		baseURL = v
	}

	home := config.HakaseHome()
	target := "config.json"
	if !*local {
		if home == "" {
			fmt.Fprintln(os.Stderr, "hakase init: cannot determine the hakase home directory; set HAKASE_HOME or pass --local")
			return 1
		}
		target = filepath.Join(home, "config.json")
	}

	if _, err := os.Stat(target); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "hakase init: %s already exists (pass --force to overwrite)\n", target)
		return 1
	}
	// A local ./config.json shadows the user-level config (ResolveConfigPath
	// prefers the local file), so warn when we are about to write one that
	// will not be picked up.
	if !*local && home != "" {
		if _, err := os.Stat("config.json"); err == nil {
			fmt.Fprintf(os.Stderr,
				"hakase init: note - ./config.json exists and takes precedence over %s; pass --local to write it instead\n", target)
		}
	}

	body, err := json.MarshalIndent(initConfig{
		Provider:  provider,
		ModelName: model,
		APIKey:    apiKey,
		BaseURL:   baseURL,
	}, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase init: failed to encode config: %v\n", err)
		return 1
	}
	body = append(body, '\n')

	if dir := filepath.Dir(target); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "hakase init: cannot create %s: %v\n", dir, err)
			return 1
		}
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "hakase init: cannot write %s: %v\n", target, err)
		return 1
	}

	credsExist := false
	if home != "" {
		if _, err := os.Stat(filepath.Join(home, "credentials.json")); err == nil {
			credsExist = true
		}
	}

	if !*noPassword && interactive && !credsExist && home != "" {
		answer, err := promptLine("Set the web admin password now? (y/N)", "n")
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase init: failed to read answer: %v\n", err)
			return 1
		}
		if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
			if code := runSetPassword(nil); code != 0 {
				fmt.Fprintln(os.Stderr, "hakase init: admin password was not set; run 'hakase auth set-password' later")
			} else {
				credsExist = true
			}
		}
	}

	fmt.Printf("\nWrote %s (provider: %s, model: %s)\n\n", target, provider, model)
	fmt.Println("Next steps:")
	fmt.Println("  hakase                      start the terminal UI")
	if credsExist {
		fmt.Println("  hakase web                  start the web UI")
	} else {
		fmt.Println("  hakase auth set-password    set the web admin login")
		fmt.Println("  hakase web                  start the web UI")
	}
	return 0
}

// resolveInitProvider normalizes the --provider value, prompting with a
// numbered menu when it is empty and the terminal is interactive. Non-
// interactive runs must pass --provider; anything outside the supported set is
// a usage error (exit 2).
func resolveInitProvider(flagValue string, interactive bool) (string, int) {
	provider := strings.ToLower(strings.TrimSpace(flagValue))
	if provider == "" {
		if !interactive {
			fmt.Fprintln(os.Stderr, "hakase init: --provider is required when not running interactively (gemini|openai|openai-compatible)")
			return "", 2
		}
		fmt.Fprintln(os.Stderr, "Provider:")
		fmt.Fprintln(os.Stderr, "  1) gemini (default)")
		fmt.Fprintln(os.Stderr, "  2) openai")
		fmt.Fprintln(os.Stderr, "  3) openai-compatible (Ollama, vLLM, ...)")
		choice, err := promptLine("Choose", "1")
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase init: failed to read provider: %v\n", err)
			return "", 1
		}
		switch strings.ToLower(choice) {
		case "1", "gemini":
			provider = "gemini"
		case "2", "openai":
			provider = "openai"
		case "3", "openai-compatible":
			provider = "openai-compatible"
		default:
			provider = strings.ToLower(choice)
		}
	}
	switch provider {
	case "gemini", "openai", "openai-compatible":
		return provider, 0
	default:
		fmt.Fprintf(os.Stderr, "hakase init: unsupported provider %q (want gemini, openai, or openai-compatible)\n", provider)
		return "", 2
	}
}

// resolveInitAPIKey resolves the API key from --api-key, $HAKASE_API_KEY, or a
// prompt. It is required for gemini/openai (exit 2 when absent non-
// interactively) and optional for openai-compatible, which is prompted (blank
// allowed) only on an interactive terminal.
func resolveInitAPIKey(flagValue, provider string, interactive bool) (string, int) {
	apiKey := strings.TrimSpace(flagValue)
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("HAKASE_API_KEY"))
	}
	if apiKey != "" {
		return apiKey, 0
	}

	if provider == "openai-compatible" {
		if !interactive {
			return "", 0
		}
		entered, err := readPassword("API key (optional for local endpoints, Enter to skip): ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase init: failed to read API key: %v\n", err)
			return "", 1
		}
		return strings.TrimSpace(entered), 0
	}

	if !interactive {
		fmt.Fprintf(os.Stderr, "hakase init: --api-key (or HAKASE_API_KEY) is required for provider %q\n", provider)
		return "", 2
	}
	entered, err := readPassword("API key: ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase init: failed to read API key: %v\n", err)
		return "", 1
	}
	apiKey = strings.TrimSpace(entered)
	if apiKey == "" {
		fmt.Fprintf(os.Stderr, "hakase init: an API key is required for provider %q\n", provider)
		return "", 2
	}
	return apiKey, 0
}

// resolveInitModel resolves the model name from --model, the provider default,
// or a prompt. Providers with a built-in default (gemini, openai) fall back to
// it; openai-compatible has no universal default and requires --model (exit 2
// when absent non-interactively).
func resolveInitModel(flagValue, provider string, interactive bool) (string, int) {
	model := strings.TrimSpace(flagValue)
	if model != "" {
		return model, 0
	}

	if def := config.DefaultModelForProvider(provider); def != "" {
		if !interactive {
			return def, 0
		}
		v, err := promptLine("Model", def)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase init: failed to read model: %v\n", err)
			return "", 1
		}
		if v == "" {
			v = def
		}
		return v, 0
	}

	// openai-compatible: endpoint-specific model, no default.
	if !interactive {
		fmt.Fprintln(os.Stderr, "hakase init: --model is required for openai-compatible providers")
		return "", 2
	}
	v, err := promptLine("Model (endpoint-specific, e.g. llama-3.3-70b)", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase init: failed to read model: %v\n", err)
		return "", 1
	}
	v = strings.TrimSpace(v)
	if v == "" {
		fmt.Fprintln(os.Stderr, "hakase init: a model name is required for openai-compatible providers")
		return "", 2
	}
	return v, 0
}

// promptLine prints "prompt [default]: " to stderr and reads one line from
// stdin, returning the default when the line is empty. It reuses the auth.go
// line reader so piped and interactive input share one code path.
func promptLine(prompt, def string) (string, error) {
	label := prompt
	if def != "" {
		label = fmt.Sprintf("%s [%s]", prompt, def)
	}
	v, err := readLine(label + ": ")
	if err != nil {
		return "", err
	}
	if v = strings.TrimSpace(v); v == "" {
		return def, nil
	}
	return v, nil
}
