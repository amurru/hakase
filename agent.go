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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/adk/tool/mcptoolset"
	"google.golang.org/genai"
)

const HermesSystemInstruction = `You are a high-autonomy, general-purpose research and navigation agent modeled after the Hermes Agent framework.

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

const CodeInterpreterSystemInstruction = `You are a specialized Code Interpreter and Data Analyst agent.

### RESPONSIBILITIES:
1. CODE EXECUTION: Write and execute clean, self-contained Python scripts or shell commands to solve computational problems.
2. DATA ANALYSIS: Parse, filter, aggregate, and analyze data payloads (CSV, JSON, XML, or unstructured text) provided by the user or other agents.
3. STATISTICAL SYNTHESIS: Provide clear quantitative summaries, data tables, and key insights derived from executed code outputs.

### OPERATIONAL RULES:
- Write robust, self-contained code with proper error handling and explicit print/log statements so execution output is readable.
- If code execution returns an error, analyze the trace, fix the issue, and re-run.
- Always output clean Markdown tables or formatted summaries alongside code execution results.
`

type PythonExecInput struct {
	Code string `json:"code" doc:"Python code snippet to execute"`
}

type PythonExecOutput struct {
	Stdout string `json:"stdout" doc:"Standard output from script execution"`
	Stderr string `json:"stderr" doc:"Standard error or execution trace"`
}

// createPythonTool returns an ADK tool that executes Python code in a temporary directory.
func createPythonTool() (tool.Tool, error) {
	// Function tool handler signature takes agent.ToolContext
	execHandler := func(ctx agent.ToolContext, input PythonExecInput) (PythonExecOutput, error) {
		tmpDir, err := os.MkdirTemp("", "agent_python_*")
		if err != nil {
			return PythonExecOutput{}, err
		}
		defer os.RemoveAll(tmpDir)

		scriptPath := filepath.Join(tmpDir, "script.py")
		if err := os.WriteFile(scriptPath, []byte(input.Code), 0600); err != nil {
			return PythonExecOutput{}, err
		}

		// Execute Python using the invocation context
		// Execute Python with local directory in PYTHONPATH so 'import skills.<name>' works!
		cmd := exec.CommandContext(ctx, "python3", scriptPath)
		cmd.Env = append(os.Environ(), "PYTHONPATH=.:./skills")
		stdout, errOut := cmd.CombinedOutput()

		stderrStr := ""
		if errOut != nil {
			stderrStr = errOut.Error()
		}

		return PythonExecOutput{
			Stdout: string(stdout),
			Stderr: stderrStr,
		}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "python_interpreter",
		Description: "Executes a string of Python code locally and returns stdout and stderr.",
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
	execHandler := func(ctx agent.ToolContext, input FileDownloadInput) (FileDownloadOutput, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", input.URL, nil)
		if err != nil {
			return FileDownloadOutput{}, fmt.Errorf("failed to create request: %w", err)
		}
		// Modern User-Agent header to avoid basic scraping blocks
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) HermesAgent/1.0")

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
	Name        string `json:"name"        doc:"Unique identifier for the skill (e.g. pdf_table_extractor, stock_growth_calculator)"`
	Description string `json:"description" doc:"Detailed explanation of what the script does, its inputs, and how to use/import it"`
	Code        string `json:"code"        doc:"The complete, verified Python script code"`
}

type SaveSkillOutput struct {
	FilePath string `json:"file_path" doc:"Location where the Python skill was saved"`
	Message  string `json:"message"   doc:"Status confirmation message"`
}

// createSaveSkillTool creates a tool to save tested Python code into ./skills/
func createSaveSkillTool() (tool.Tool, error) {
	execHandler := func(ctx agent.ToolContext, input SaveSkillInput) (SaveSkillOutput, error) {
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
	execHandler := func(ctx agent.ToolContext, _ ListSkillsInput) (ListSkillsOutput, error) {
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

func setupRunner(ctx context.Context, cfg *Config) (*runner.Runner, error) {
	model, err := gemini.NewModel(ctx, cfg.ModelName, &genai.ClientConfig{
		APIKey: cfg.APIKey,
	})
	if err != nil {
		return nil, err
	}

	mcpToolset, err := mcptoolset.New(mcptoolset.Config{
		Transport: &mcp.StreamableClientTransport{
			Endpoint: cfg.MCPServerURL,
		},
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
		Instruction: HermesSystemInstruction,
		Model:       model,
		Tools:       []tool.Tool{downloadTool},
		Toolsets:    []tool.Toolset{mcpToolset},
	})

	// Code Inpterpreter agent/ data analyst
	pythonTool, err := createPythonTool()
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
3. DO NOT DUPLICATE: Never save a skill with a functionality that is already covered by an installed skill.`,
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

	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "orchestrator",
		Description: "Main orchestrator agent that delegates research and analysis tasks.",
		Instruction: `You are an AI research & analysis coordinator.
- Use 'web_researcher' when real-time browser navigation or web search is required.
- Use 'code_interpreter' when data calculations, script execution, file parsing, or statistical analysis are required.
- Synthesize responses from both specialists into a final markdown output.`,
		Model: model,
		SubAgents: []agent.Agent{
			researcherAgent,
			codeInterpreterAgent,
		}, // Or exposed via agent tool wrappers
	})
	if err != nil {
		return nil, err
	}

	return runner.New(runner.Config{
		AppName:           "hermes_harness",
		Agent:             rootAgent,
		SessionService:    session.InMemoryService(),
		AutoCreateSession: true,
	})
}
