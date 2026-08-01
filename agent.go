package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/adk/v2/tool/mcptoolset"
)

const HakaseSystemInstruction = `You are a high-autonomy, general-purpose research and navigation agent modeled after the Hermes Agent framework.

### CORE OPERATIONAL PRINCIPLES:
1. THINK BEFORE YOU ACT: Use internal reasoning before taking actions. Always analyze what information you have, what is missing, and what tool best bridges the gap.
2. PROGRESSIVE EXPLORATION: Start wide with short, broad queries or top-level URL visits. Evaluate the landscape, then narrow down focus to specific links, selectors, or detailed extractions.
3. RESILIENT PIVOTING: Never repeat an identical failed tool call. If a selector, URL, or query yields empty results or errors:
   - Refine search keywords.
   - Fall back to fetching raw markdown/text instead of complex DOM extractions.
   - Move to an alternative source.

### WORKFLOW CYCLE:
- PLAN: Break down multi-step research requests into clear sub-tasks.
- EXECUTE: Call available tools (browser navigation, search, content extraction) efficiently.
- VERIFY & SYNTHESIZE: Cross-check information from multiple steps before answering the user.

### OUTPUT FORMAT:
- Present final synthesized answers in clean, well-formatted Markdown with headers, bullet points, and citations where applicable.
- Keep tool responses focused on facts without leaking raw internal state clutter unless specifically requested.
`

// buildTimeReminder returns a system-prompt block that grounds the agent in
// the current wall-clock time on the user's machine. LLM training cutoffs go
// stale, so injecting "now" tells the model to reason about recency correctly
// and to prefer live search results over outdated training data.
func buildTimeReminder() string {
	now := time.Now()
	zoneName, _ := now.Zone()
	return fmt.Sprintf(`
### SYSTEM REMINDER - CURRENT DATE & TIME:
The current date and time on the user's machine is %s (%s, UTC offset %s).

- Treat this as "now" for ALL temporal reasoning: news recency, "latest" / "today" / "yesterday", current events, ages, seasons, holidays, and deadlines.
- Your training data was frozen at your knowledge cutoff and is likely outdated. Whenever a fact, price, version, event, or statistic can change over time, do NOT answer from memory alone - use your search and browsing tools to fetch current, verifiable information.
- When searching, prefer the most recent results and verify publication dates before asserting something is "current", "latest", or "breaking". If freshly retrieved sources conflict with your training data, trust the fresh sources.`,
		now.Format("Monday, 02 January 2006 at 15:04:05"),
		zoneName,
		now.Format("-07:00"),
	)
}

const CodeInterpreterSystemInstruction = `You are a specialized Code Interpreter, Data Analyst, and Self-Evolving Skill Developer agent.

### RESPONSIBILITIES:
1. CODE EXECUTION: Write and execute clean, self-contained Python scripts to solve computational, data transformation, image/rendering, and file manipulation tasks.
2. DATA ANALYSIS & VISUALIZATION: Parse, filter, aggregate, and analyze data payloads (CSV, JSON, XML, PDFs, text) and generate charts or visual deliverables.
3. STATISTICAL SYNTHESIS: Provide clean quantitative summaries, formatted Markdown tables, and clear insights alongside code execution outputs.
4. SKILL EVOLUTION & PERSISTENCE: Continually expand your capabilities by creating, testing, and saving modular Python skills for future reuse.

### OPERATIONAL RULES:
- Write robust, self-contained Python code with proper error handling and explicit print/log statements so execution output is readable.
- SELF-CORRECTION: If code execution returns an error (or missing module trace), analyze the output, fix the code, and re-run until successful.
- LIBRARIES & VENV: You run inside an isolated Python virtual environment (.venv) with automatic dependency installation. Use third-party libraries freely.
- FILE CONVENTIONS: Read raw downloaded files from './downloads/' and write generated files/artifacts to './outputs/'.

### SKILL DESIGN & GENERALISATION STANDARD (IMPORTANT):
When writing code intended to be saved as a skill via 'save_skill':
1. PARAMETERISE EVERYTHING: Do NOT hardcode specific numbers, query terms, or file paths inside function bodies. Pass them as arguments with default fallbacks.
2. SELF-DOCUMENTING DOCSTRINGS: Every function MUST include a comprehensive docstring containing:
   - Function description and purpose.
   - Parameter definitions (with types).
   - Expected return type/structure.
   - A clear 'Usage:' section demonstrating how to import and call the function.
3. DUAL EXECUTION MODE: Include an 'if __name__ == "__main__":' block at the bottom with sample mock data so the script can be tested independently.

### SKILL REUSE & EVOLUTION MANDATE:
1. REUSE FIRST: Check the "AVAILABLE PRE-LEARNED SKILLS" list in your instructions before writing code from scratch. If a skill exists that can solve or assist in the task, import and use it (e.g., 'from skills.<skill_name> import ...').
2. MANDATORY SKILL PERSISTENCE: Whenever you construct a script that solves a novel task, complex workflow, or custom rendering task (e.g., HTML-to-PNG cards, PDF extraction, custom API parsing, or statistics generation):
   - Step A: Verify execution using 'python_interpreter'.
   - Step B: Once execution is verified with valid output, you MUST immediately call 'save_skill' to store it in ./skills/!
3. NO DUPLICATION: Do not save a new skill if an identical capability is already present in your installed skills list.
`

// LogFunc is a thread-safe callback function to send status logs to the TUI
type LogFunc func(msg string)

type PythonExecInput struct {
	Code string `json:"code" doc:"Python code snippet to execute"`
}

type PythonExecOutput struct {
	Stdout string `json:"stdout" doc:"Standard output from script execution"`
	Stderr string `json:"stderr" doc:"Standard error or execution trace"`
}

// getVenvPython returns the executable path to the virtualenv python binary, creating .venv if missing.
func getVenvPython(log LogFunc) (string, error) {
	venvDir := "./.venv"
	pyBin := filepath.Join(venvDir, "bin", "python3")
	if runtime.GOOS == "windows" {
		pyBin = filepath.Join(venvDir, "Scripts", "python.exe")
	}

	// Create .venv if it does not exist
	if _, err := os.Stat(pyBin); os.IsNotExist(err) {
		if log != nil {
			log("📦 [sys] Initializing local Python virtual environment in ./.venv ...")
		}
		cmd := exec.Command("python3", "-m", "venv", venvDir)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("failed to create virtual environment: %w", err)
		}
	}

	return pyBin, nil
}

// createPythonTool returns an ADK tool that executes Python code in a temporary directory.
func createPythonTool(log LogFunc) (tool.Tool, error) {
	// Function tool handler signature takes agent.Context
	execHandler := func(ctx agent.Context, input PythonExecInput) (PythonExecOutput, error) {
		pyBin, err := getVenvPython(log)
		if err != nil {
			return PythonExecOutput{}, err
		}

		tmpDir, err := os.MkdirTemp("", "agent_python_*")
		if err != nil {
			return PythonExecOutput{}, err
		}
		defer os.RemoveAll(tmpDir)

		scriptPath := filepath.Join(tmpDir, "script.py")
		if err := os.WriteFile(scriptPath, []byte(input.Code), 0600); err != nil {
			return PythonExecOutput{}, err
		}

		runScript := func() (string, string) {
			cmd := exec.CommandContext(ctx, pyBin, scriptPath)
			cmd.Env = append(os.Environ(), "PYTHONPATH=.:./skills")
			out, errOut := cmd.CombinedOutput()
			errStr := ""
			if errOut != nil {
				errStr = errOut.Error()
			}
			return string(out), errStr
		}

		stdout, stderr := runScript()

		// Auto-Dependency Resolution Loop
		// If execution failed due to a missing package, attempt pip install inside .venv and retry
		if strings.Contains(stdout, "ModuleNotFoundError: No module named") {
			re := regexp.MustCompile(`ModuleNotFoundError: No module named '([^']+)'`)
			matches := re.FindStringSubmatch(stdout)
			if len(matches) > 1 {
				missingPkg := strings.Split(matches[1], ".")[0] // Handle sub-modules like 'bs4.element' -> 'bs4'

				pipBin := filepath.Join("./.venv", "bin", "pip")
				if runtime.GOOS == "windows" {
					pipBin = filepath.Join("./.venv", "Scripts", "pip.exe")
				}

				// 📡 Log progress directly to the side pane!
				if log != nil {
					log(
						fmt.Sprintf(
							"⚡ [code_interpreter] Missing package '%s' detected. Auto-installing into .venv...",
							missingPkg,
						),
					)
				}
				installCmd := exec.CommandContext(ctx, pipBin, "install", missingPkg)
				_ = installCmd.Run()

				if log != nil {
					log(
						fmt.Sprintf(
							"✅ [code_interpreter] Package '%s' installed successfully.",
							missingPkg,
						),
					)
				}

				// Retry running the script after package installation
				stdout, stderr = runScript()
			}
		}

		return PythonExecOutput{
			Stdout: stdout,
			Stderr: stderr,
		}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "python_interpreter",
		Description: "Executes Python code safely inside an isolated .venv environment with automatic dependency resolution.",
	}, execHandler)
}

type FileDownloadInput struct {
	URL      string `json:"url"                doc:"The HTTP/HTTPS URL of the file, PDF, or image to download"`
	Filename string `json:"filename,omitempty" doc:"Optional custom filename to save as. If empty, derived from URL."`
}

type FileDownloadOutput struct {
	SavedPath       string `json:"saved_path"       doc:"The local file path where the file was saved"`
	BytesDownloaded int64  `json:"bytes_downloaded" doc:"Size of the downloaded file in bytes"`
	ContentType     string `json:"content_type"     doc:"HTTP Content-Type header of the downloaded resource"`
}

func createDownloadTool() (tool.Tool, error) {
	execHandler := func(ctx agent.Context, input FileDownloadInput) (FileDownloadOutput, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", input.URL, nil)
		if err != nil {
			return FileDownloadOutput{}, fmt.Errorf("failed to create request: %w", err)
		}
		// Modern User-Agent header to avoid basic scraping blocks
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) HakaseAgent/1.0")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return FileDownloadOutput{}, fmt.Errorf("download request failed: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return FileDownloadOutput{}, fmt.Errorf(
				"server returned HTTP %d: %s",
				resp.StatusCode,
				resp.Status,
			)
		}

		// Ensure local ./downloads directory exists
		outDir := "./downloads"
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return FileDownloadOutput{}, fmt.Errorf("failed to create downloads folder: %w", err)
		}

		// Resolve default filename if not provided
		filename := input.Filename
		if filename == "" {
			filename = filepath.Base(req.URL.Path)
			if filename == "" || filename == "." || filename == "/" {
				filename = fmt.Sprintf("file_%d", time.Now().Unix())
			}
		}

		filePath := filepath.Join(outDir, filename)
		out, err := os.Create(filePath)
		if err != nil {
			return FileDownloadOutput{}, fmt.Errorf("failed to create destination file: %w", err)
		}
		defer out.Close()

		bytesWritten, err := io.Copy(out, resp.Body)
		if err != nil {
			return FileDownloadOutput{}, fmt.Errorf("failed to save file contents: %w", err)
		}

		return FileDownloadOutput{
			SavedPath:       filePath,
			BytesDownloaded: bytesWritten,
			ContentType:     resp.Header.Get("Content-Type"),
		}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "download_file",
		Description: "Downloads PDFs, images, dataset binaries, or documents from a web URL and saves them to the ./downloads directory.",
	}, execHandler)
}

// Skills and auto-learning

type SkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FileName    string `json:"file_name"`
	SavedAt     string `json:"saved_at"`
}

type SkillRegistry struct {
	Skills []SkillMeta `json:"skills"`
}

type SaveSkillInput struct {
	Name        string `json:"name"        doc:"Unique snake_case identifier for the skill (e.g. render_weather_card, extract_pdf_tables)"`
	Description string `json:"description" doc:"High-level summary of what the skill does, input arguments, output artifacts, and import usage example"`
	Code        string `json:"code"        doc:"The complete, parameterised, self-documented Python script with docstrings and a usage example in the docstring"`
}

type SaveSkillOutput struct {
	FilePath string `json:"file_path" doc:"Location where the Python skill was saved"`
	Message  string `json:"message"   doc:"Status confirmation message"`
}

// createSaveSkillTool creates a tool to save tested Python code into ./skills/
func createSaveSkillTool() (tool.Tool, error) {
	execHandler := func(ctx agent.Context, input SaveSkillInput) (SaveSkillOutput, error) {
		skillsDir := "./skills"
		if err := os.MkdirAll(skillsDir, 0755); err != nil {
			return SaveSkillOutput{}, fmt.Errorf("failed to create skills directory: %w", err)
		}

		// Ensure ./skills/__init__.py exists so it acts as a Python package
		initPath := filepath.Join(skillsDir, "__init__.py")
		if _, err := os.Stat(initPath); os.IsNotExist(err) {
			_ = os.WriteFile(initPath, []byte("# Skills package\n"), 0644)
		}

		// Save Python code
		scriptFileName := fmt.Sprintf("%s.py", input.Name)
		scriptPath := filepath.Join(skillsDir, scriptFileName)
		if err := os.WriteFile(scriptPath, []byte(input.Code), 0644); err != nil {
			return SaveSkillOutput{}, fmt.Errorf("failed to save skill code: %w", err)
		}

		// Update skills.json registry
		registryPath := filepath.Join(skillsDir, "skills.json")
		var registry SkillRegistry

		data, err := os.ReadFile(registryPath)
		if err == nil {
			_ = json.Unmarshal(data, &registry)
		}

		// Check if skill exists and update, or append
		newSkill := SkillMeta{
			Name:        input.Name,
			Description: input.Description,
			FileName:    scriptFileName,
			SavedAt:     time.Now().Format(time.RFC3339),
		}

		updated := false
		for i, s := range registry.Skills {
			if s.Name == input.Name {
				registry.Skills[i] = newSkill
				updated = true
				break
			}
		}
		if !updated {
			registry.Skills = append(registry.Skills, newSkill)
		}

		regBytes, err := json.MarshalIndent(registry, "", "  ")
		if err == nil {
			_ = os.WriteFile(registryPath, regBytes, 0644)
		}

		return SaveSkillOutput{
			FilePath: scriptPath,
			Message: fmt.Sprintf(
				"Skill '%s' successfully saved and registered in ./skills/",
				input.Name,
			),
		}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "save_skill",
		Description: "Persists a tested, working Python script into the local ./skills library for future reuse.",
	}, execHandler)
}

type ListSkillsInput struct{}

type ListSkillsOutput struct {
	Skills []SkillMeta `json:"skills" doc:"List of all saved skills currently available in the registry"`
}

// createListSkillsTool allows agents to inspect all previously learned skills
func createListSkillsTool() (tool.Tool, error) {
	execHandler := func(ctx agent.Context, _ ListSkillsInput) (ListSkillsOutput, error) {
		registryPath := filepath.Join("./skills", "skills.json")
		data, err := os.ReadFile(registryPath)
		if err != nil {
			return ListSkillsOutput{Skills: []SkillMeta{}}, nil
		}

		var registry SkillRegistry
		if err := json.Unmarshal(data, &registry); err != nil {
			return ListSkillsOutput{}, err
		}

		return ListSkillsOutput{Skills: registry.Skills}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "list_skills",
		Description: "Lists all previously saved Python skills and their descriptions from the ./skills library.",
	}, execHandler)
}

// getSkillsPrompt Reads skills.json and builds a string for the system prompt
func getSkillsPrompt() string {
	registryPath := filepath.Join("./skills", "skills.json")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		return "No pre-existing skills currently saved."
	}

	var registry SkillRegistry
	if err := json.Unmarshal(data, &registry); err != nil || len(registry.Skills) == 0 {
		return "No pre-existing skills currently saved."
	}

	var sb strings.Builder
	sb.WriteString("AVAILABLE PRE-LEARNED SKILLS:\n")
	for _, s := range registry.Skills {
		sb.WriteString(
			fmt.Sprintf(
				"- Skill: '%s'\n  Description: %s\n  Import Usage: `from skills.%s import ...` or `import %s`\n\n",
				s.Name,
				s.Description,
				s.Name,
				s.Name,
			),
		)
	}
	return sb.String()
}

func setupRunner(ctx context.Context, cfg *Config, log LogFunc) (*runner.Runner, error) {
	provider, err := ProviderFactory(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create provider: %w", err)
	}
	if err := provider.ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	modelName := cfg.ModelName
	if modelName == "" {
		modelName = provider.GetDefaultModel()
	}
	model, err := provider.CreateModel(ctx, modelName, cfg.APIKey)
	if err != nil {
		return nil, err
	}

	mcpToolset, err := mcptoolset.New(mcptoolset.Config{
		Endpoint: cfg.MCPServerURL,
	})
	if err != nil {
		return nil, err
	}
	// Researcher agent
	downloadTool, err := createDownloadTool()
	if err != nil {
		return nil, err
	}

	researcherAgent, _ := llmagent.New(llmagent.Config{
		Name:        "web_researcher",
		Description: "Specialist agent for searching the web, navigating pages, downloading files, and extracting content.",
		Instruction: HakaseSystemInstruction + "\n\n" + buildTimeReminder(),
		Model:       model,
		Tools:       []tool.Tool{downloadTool},
		Toolsets:    []tool.Toolset{mcpToolset},
	})

	// Code Inpterpreter agent/ data analyst
	pythonTool, err := createPythonTool(log)
	if err != nil {
		return nil, err
	}

	saveSkillTool, err := createSaveSkillTool()
	if err != nil {
		return nil, err
	}
	listSkillsTool, err := createListSkillsTool()
	if err != nil {
		return nil, err
	}

	// Load currently saved skills from disk
	installedSkills := getSkillsPrompt()

	codeInterpreterAgent, err := llmagent.New(llmagent.Config{
		Name:        "code_interpreter",
		Description: "Specialist agent for executing Python code, data analysis, parsing JSON/CSV, and managing learned skills.",
		Instruction: CodeInterpreterSystemInstruction + "\n\n" + installedSkills + `
### SKILL REUSE & EVOLUTION RULES:
1. REUSE FIRST: Check the "AVAILABLE PRE-LEARNED SKILLS" list above before writing code. If a skill exists that can solve or assist in the task, write a Python script that imports and calls it!
2. SAVE NOVEL SKILLS: If you solve a new problem with fresh code, test it with python_interpreter, then call save_skill to store it for future reuse.
3. DO NOT DUPLICATE: Never save a skill with a functionality that is already covered by an installed skill.` + "\n\n" + buildTimeReminder(),
		Model: model,
		Tools: []tool.Tool{
			pythonTool,
			saveSkillTool,
			listSkillsTool,
		}, // 👈 Attached skill tools here!
	})
	if err != nil {
		return nil, err
	}

	// Host system execution tools (arbitrary command/executable execution).
	systemExecTools, err := createSystemExecTools(log)
	if err != nil {
		return nil, err
	}

	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "orchestrator",
		Description: "Main orchestrator agent that delegates research and analysis tasks.",
		Instruction: `You are an AI research & analysis coordinator.
- Use 'web_researcher' when real-time browser navigation or web search is required.
- Use 'code_interpreter' when data calculations, script execution, file parsing, or statistical analysis are required.
- Use 'system_exec' tools when you need to run system commands, executables, or scripts directly on the host machine (not via the Python interpreter).
- Synthesize responses from the specialists into a final markdown output.` + "\n\n" + buildTimeReminder(),
		Model: model,
		Tools: systemExecTools,
		SubAgents: []agent.Agent{
			researcherAgent,
			codeInterpreterAgent,
		}, // Or exposed via agent tool wrappers
	})
	if err != nil {
		return nil, err
	}

	return runner.New(runner.Config{
		AppName:           "hakase_harness",
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
}
