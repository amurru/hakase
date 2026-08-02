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
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
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

// currentConfig holds the loaded configuration for checkpoint access
var currentConfig *Config

// taskBoardNotify is set by main and pushes TaskUpdateMsg to the TUI on task mutations.
var taskBoardNotify func(action string, task TaskMeta)

func notifyTaskBoard(action string, task TaskMeta) {
	if taskBoardNotify != nil {
		taskBoardNotify(action, task)
	}
}

type SkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	FileName    string `json:"file_name"`
	SavedAt     string `json:"saved_at"`
}

type SkillRegistry struct {
	Skills []SkillMeta `json:"skills"`
}

// Task management types

type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusFailed     TaskStatus = "failed"
	TaskStatusCancelled  TaskStatus = "cancelled"
	TaskStatusSkipped    TaskStatus = "skipped"
	TaskStatusBlocked    TaskStatus = "blocked"
)

var ValidTransitions = map[TaskStatus][]TaskStatus{
	TaskStatusPending:    {TaskStatusInProgress, TaskStatusCancelled, TaskStatusSkipped},
	TaskStatusInProgress: {TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled, TaskStatusBlocked},
	TaskStatusBlocked:    {TaskStatusInProgress, TaskStatusCancelled},
	TaskStatusCompleted:  {},
	TaskStatusFailed:     {},
	TaskStatusCancelled:  {},
	TaskStatusSkipped:    {},
}

type TaskPriority string

const (
	TaskPriorityCritical TaskPriority = "critical"
	TaskPriorityHigh     TaskPriority = "high"
	TaskPriorityMedium   TaskPriority = "medium"
	TaskPriorityLow      TaskPriority = "low"
)

type TaskMeta struct {
	ID           string                 `json:"id"`
	Version      int                    `json:"version"`
	Title        string                 `json:"title"`
	Description  string                 `json:"description,omitempty"`
	Status       TaskStatus             `json:"status"`
	Priority     TaskPriority           `json:"priority"`
	Owner        string                 `json:"owner,omitempty"`
	Assignee     string                 `json:"assignee,omitempty"`
	Dependencies []string               `json:"dependencies,omitempty"`
	BlockedBy    []string               `json:"blocked_by,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	StartedAt    *time.Time             `json:"started_at,omitempty"`
	CompletedAt  *time.Time             `json:"completed_at,omitempty"`
	DueAt        *time.Time             `json:"due_at,omitempty"`
	Attempts     int                    `json:"attempts"`
	MaxAttempts  int                    `json:"max_attempts"`
	LastError    string                 `json:"last_error,omitempty"`
	Result       interface{}            `json:"result,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
	ParentID     string                 `json:"parent_id,omitempty"`
	Tags         []string               `json:"tags,omitempty"`
}

type TaskRegistry struct {
	Tasks []TaskMeta `json:"tasks"`
}

// Task tool input/output types

type CreateTaskInput struct {
	Title        string       `json:"title" doc:"Task title"`
	Description  string       `json:"description,omitempty" doc:"Task description"`
	Priority     TaskPriority `json:"priority,omitempty" doc:"Task priority"`
	Dependencies []string     `json:"dependencies,omitempty" doc:"Task IDs that must complete first"`
	Assignee     string       `json:"assignee,omitempty" doc:"Agent to assign"`
	ParentID     string       `json:"parent_id,omitempty" doc:"Parent task ID for hierarchy"`
	Tags         []string     `json:"tags,omitempty" doc:"Task tags"`
}

type CreateTaskOutput struct {
	Task TaskMeta `json:"task" doc:"Created task"`
}

type UpdateTaskInput struct {
	ID          string       `json:"id" doc:"Task ID"`
	Title       string       `json:"title,omitempty" doc:"New title"`
	Description string       `json:"description,omitempty" doc:"New description"`
	Status      TaskStatus   `json:"status,omitempty" doc:"New status (transition validated)"`
	Priority    TaskPriority `json:"priority,omitempty" doc:"New priority"`
	Assignee    string       `json:"assignee,omitempty" doc:"New assignee"`
	Result      interface{}  `json:"result,omitempty" doc:"Execution result"`
	Error       string       `json:"error,omitempty" doc:"Error message if failed"`
}

type UpdateTaskOutput struct {
	Task TaskMeta `json:"task" doc:"Updated task"`
}

type ListTasksInput struct {
	Status   []TaskStatus `json:"status,omitempty" doc:"Filter by status"`
	Assignee string       `json:"assignee,omitempty" doc:"Filter by assignee"`
	Tags     []string     `json:"tags,omitempty" doc:"Filter by tags"`
	ParentID string       `json:"parent_id,omitempty" doc:"Filter by parent"`
}

type ListTasksOutput struct {
	Tasks []TaskMeta `json:"tasks" doc:"Matching tasks"`
}

type GetTaskInput struct {
	ID string `json:"id" doc:"Task ID"`
}

type GetTaskOutput struct {
	Task *TaskMeta `json:"task" doc:"Task or null if not found"`
}

type DeleteTaskInput struct {
	ID string `json:"id" doc:"Task ID"`
}

type DeleteTaskOutput struct {
	Success bool `json:"success" doc:"Whether deletion succeeded"`
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
	Skills         []SkillMeta         `json:"skills" doc:"List of all saved skills currently available in the registry"`
	MarkdownSkills []MarkdownSkillMeta `json:"markdown_skills" doc:"List of discovered markdown skills"`
}

// createListSkillsTool allows agents to inspect all previously learned skills
func createListSkillsTool(cwd string, extraDirs []string, log LogFunc) (tool.Tool, error) {
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

		mdSkills := DiscoverMarkdownSkills(cwd, extraDirs, log)
		mdMeta := make([]MarkdownSkillMeta, 0, len(mdSkills))
		for _, s := range mdSkills {
			mdMeta = append(mdMeta, MarkdownSkillMeta{
				Name:        s.Frontmatter.Name,
				Description: s.Frontmatter.Description,
				Path:        s.Path,
			})
		}

		return ListSkillsOutput{Skills: registry.Skills, MarkdownSkills: mdMeta}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "list_skills",
		Description: "Lists all previously saved Python skills and discovered markdown skills (SKILL.md) with their descriptions.",
	}, execHandler)
}

// getSkillsPrompt reads skills.json and builds a string for the system prompt.
// Python entries are rendered in the original format; markdown skills are
// appended under the same "AVAILABLE PRE-LEARNED SKILLS:" header. On a
// name collision the markdown skill wins: the Python entry is omitted and a
// warning is logged.
func getSkillsPrompt(mdSkills []MarkdownSkill, log LogFunc) string {
	registryPath := filepath.Join("./skills", "skills.json")
	data, err := os.ReadFile(registryPath)
	var registry SkillRegistry
	if err == nil {
		if jerr := json.Unmarshal(data, &registry); jerr != nil {
			registry = SkillRegistry{}
		}
	}

	// Markdown skill names take precedence; colliding Python entries are
	// omitted from the prompt (the .py file remains on disk and importable).
	mdNames := make(map[string]bool, len(mdSkills))
	for _, s := range mdSkills {
		mdNames[s.Frontmatter.Name] = true
	}

	pythonSkills := make([]SkillMeta, 0, len(registry.Skills))
	for _, s := range registry.Skills {
		if mdNames[s.Name] {
			if log != nil {
				log(fmt.Sprintf("[skills] Skipping Python skill '%s' in prompt: collides with markdown skill", s.Name))
			}
			continue
		}
		pythonSkills = append(pythonSkills, s)
	}

	if len(pythonSkills) == 0 && len(mdSkills) == 0 {
		return "No pre-existing skills currently saved."
	}

	var sb strings.Builder
	sb.WriteString("AVAILABLE PRE-LEARNED SKILLS:\n")
	for _, s := range pythonSkills {
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
	for _, s := range mdSkills {
		sb.WriteString(
			fmt.Sprintf(
				"- Skill: '%s' (markdown)\n  Description: %s\n  Load: call 'load_markdown_skill' with name '%s' to read full instructions\n\n",
				s.Frontmatter.Name,
				s.Frontmatter.Description,
				s.Frontmatter.Name,
			),
		)
	}
	return sb.String()
}

// LoadMarkdownSkillInput is the input for the load_markdown_skill tool.
type LoadMarkdownSkillInput struct {
	Name string `json:"name" doc:"Name of the markdown skill to load"`
}

// LoadMarkdownSkillOutput is the output of the load_markdown_skill tool.
type LoadMarkdownSkillOutput struct {
	Name        string   `json:"name"        doc:"Name of the markdown skill"`
	Description string   `json:"description" doc:"Short description of the skill from its SKILL.md frontmatter"`
	Content     string   `json:"content"     doc:"Full instructions (SKILL.md body) of the skill"`
	Location    string   `json:"location"    doc:"Absolute path to the skill's SKILL.md file"`
	Scripts     []string `json:"scripts"     doc:"Files under the skill's scripts/ directory, relative to the skill directory"`
}

// createLoadMarkdownSkillTool creates a tool that loads the full instructions
// and scripts of a markdown skill (SKILL.md) by name. The passed skills are
// indexed at construction time; an unknown name triggers one fresh
// re-discovery scan before failing.
func createLoadMarkdownSkillTool(skills []MarkdownSkill, cwd string, extraDirs []string, log LogFunc) (tool.Tool, error) {
	index := make(map[string]MarkdownSkill, len(skills))
	for _, s := range skills {
		index[s.Frontmatter.Name] = s
	}

	execHandler := func(ctx agent.Context, input LoadMarkdownSkillInput) (LoadMarkdownSkillOutput, error) {
		skill, ok := index[input.Name]
		if !ok {
			// The skill may have been created after startup; re-scan the
			// skill dirs once for freshness before giving up.
			fresh := DiscoverMarkdownSkills(cwd, extraDirs, log)
			index = make(map[string]MarkdownSkill, len(fresh))
			for _, s := range fresh {
				index[s.Frontmatter.Name] = s
			}
			skill, ok = index[input.Name]
		}
		if !ok {
			return LoadMarkdownSkillOutput{}, fmt.Errorf("skill not found: %s", input.Name)
		}
		if log != nil {
			log(fmt.Sprintf("[skills] Loaded markdown skill '%s'", input.Name))
		}
		return LoadMarkdownSkillOutput{
			Name:        skill.Frontmatter.Name,
			Description: skill.Frontmatter.Description,
			Content:     skill.Body,
			Location:    skill.Path,
			Scripts:     skill.Scripts,
		}, nil
	}

	return functiontool.New(functiontool.Config{
		Name:        "load_markdown_skill",
		Description: "Loads the full instructions and scripts of a markdown skill (SKILL.md) by name. Use this when the AVAILABLE PRE-LEARNED SKILLS list references a markdown skill.",
	}, execHandler)
}

// Task persistence functions

const tasksFile = "./tasks.json"

// taskRegistryMu serializes in-process access to tasks.json. Multiple task
// tool calls can run in the same turn (e.g. several create_task calls), and
// without this lock concurrent read-modify-write cycles corrupt the file.
var taskRegistryMu sync.Mutex

func loadTaskRegistryLocked() (TaskRegistry, error) {
	var registry TaskRegistry
	data, err := os.ReadFile(tasksFile)
	if err != nil {
		if os.IsNotExist(err) {
			return TaskRegistry{Tasks: []TaskMeta{}}, nil
		}
		return TaskRegistry{}, err
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		return TaskRegistry{}, err
	}
	return registry, nil
}

func loadTaskRegistry() (TaskRegistry, error) {
	taskRegistryMu.Lock()
	defer taskRegistryMu.Unlock()
	return loadTaskRegistryLocked()
}

func saveTaskRegistryLocked(registry TaskRegistry) error {
	registryBytes, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file and rename so readers never observe a torn file.
	tmp := tasksFile + ".tmp"
	if err := os.WriteFile(tmp, registryBytes, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, tasksFile)
}

func saveTaskRegistry(registry TaskRegistry) error {
	taskRegistryMu.Lock()
	defer taskRegistryMu.Unlock()
	return saveTaskRegistryLocked(registry)
}

func findTaskIndex(registry TaskRegistry, id string) int {
	for i, task := range registry.Tasks {
		if task.ID == id {
			return i
		}
	}
	return -1
}

func taskExists(registry TaskRegistry, id string) bool {
	return findTaskIndex(registry, id) != -1
}

func isValidTransition(from, to TaskStatus) bool {
	validNext, ok := ValidTransitions[from]
	if !ok {
		return false
	}
	for _, v := range validNext {
		if v == to {
			return true
		}
	}
	return false
}

func createTask(input CreateTaskInput) (TaskMeta, error) {
	taskRegistryMu.Lock()
	defer taskRegistryMu.Unlock()

	registry, err := loadTaskRegistryLocked()
	if err != nil {
		return TaskMeta{}, err
	}

	now := time.Now().UTC()

	// Check which dependencies are already completed
	blockedBy := []string{}
	for _, depID := range input.Dependencies {
		if taskExists(registry, depID) {
			depIdx := findTaskIndex(registry, depID)
			if depIdx != -1 && registry.Tasks[depIdx].Status != TaskStatusCompleted {
				blockedBy = append(blockedBy, depID)
			}
		}
	}

	status := TaskStatusPending
	if len(blockedBy) > 0 {
		status = TaskStatusBlocked
	}

	task := TaskMeta{
		ID:           ulid.Make().String(),
		Version:      1,
		Title:        input.Title,
		Description:  input.Description,
		Status:       status,
		Priority:     input.Priority,
		Owner:        "",
		Assignee:     input.Assignee,
		Dependencies: input.Dependencies,
		BlockedBy:    blockedBy,
		CreatedAt:    now,
		UpdatedAt:    now,
		Attempts:     0,
		MaxAttempts:  3,
		ParentID:     input.ParentID,
		Tags:         input.Tags,
		Metadata:     make(map[string]interface{}),
	}

	for _, depID := range input.Dependencies {
		if !taskExists(registry, depID) {
			return TaskMeta{}, fmt.Errorf("dependency task %s does not exist", depID)
		}
	}

	registry.Tasks = append(registry.Tasks, task)
	if err := saveTaskRegistryLocked(registry); err != nil {
		return TaskMeta{}, err
	}
	_ = writeTaskCheckpoint(&registry)
	return task, nil
}

func updateTask(input UpdateTaskInput) (TaskMeta, error) {
	taskRegistryMu.Lock()
	defer taskRegistryMu.Unlock()

	registry, err := loadTaskRegistryLocked()
	if err != nil {
		return TaskMeta{}, err
	}

	idx := findTaskIndex(registry, input.ID)
	if idx == -1 {
		return TaskMeta{}, fmt.Errorf("task not found: %s", input.ID)
	}

	task := registry.Tasks[idx]

	if input.Status != "" && !isValidTransition(task.Status, input.Status) {
		return TaskMeta{}, fmt.Errorf("invalid transition: %s -> %s", task.Status, input.Status)
	}

	if input.Title != "" {
		task.Title = input.Title
	}
	if input.Description != "" {
		task.Description = input.Description
	}
	if input.Status != "" {
		task.Status = input.Status
		now := time.Now().UTC()
		switch input.Status {
		case TaskStatusInProgress:
			task.StartedAt = &now
			task.Attempts++
		case TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
			task.CompletedAt = &now
		}
	}
	if input.Priority != "" {
		task.Priority = input.Priority
	}
	if input.Assignee != "" {
		task.Assignee = input.Assignee
	}
	if input.Result != nil {
		task.Result = input.Result
	}
	if input.Error != "" {
		task.LastError = input.Error
	}

	task.UpdatedAt = time.Now().UTC()
	registry.Tasks[idx] = task

	if task.Status == TaskStatusCompleted {
		unblockDependentTasks(&registry, task.ID)
	}

	if err := saveTaskRegistryLocked(registry); err != nil {
		return TaskMeta{}, err
	}
	_ = writeTaskCheckpoint(&registry)
	return task, nil
}

func listTasks(input ListTasksInput) ([]TaskMeta, error) {
	registry, err := loadTaskRegistry()
	if err != nil {
		return nil, err
	}

	var result []TaskMeta
	for _, task := range registry.Tasks {
		match := true

		if len(input.Status) > 0 {
			match = false
			for _, s := range input.Status {
				if task.Status == s {
					match = true
					break
				}
			}
		}

		if match && input.Assignee != "" {
			match = task.Assignee == input.Assignee
		}

		if match && len(input.Tags) > 0 {
			match = false
			for _, tag := range input.Tags {
				for _, ttag := range task.Tags {
					if tag == ttag {
						match = true
						break
					}
				}
				if !match {
					break
				}
			}
		}

		if match && input.ParentID != "" {
			match = task.ParentID == input.ParentID
		}

		if match {
			result = append(result, task)
		}
	}

	return result, nil
}

func getTask(id string) (*TaskMeta, error) {
	registry, err := loadTaskRegistry()
	if err != nil {
		return nil, err
	}

	idx := findTaskIndex(registry, id)
	if idx == -1 {
		return nil, nil
	}

	return &registry.Tasks[idx], nil
}

func deleteTask(id string) (bool, error) {
	taskRegistryMu.Lock()
	defer taskRegistryMu.Unlock()

	registry, err := loadTaskRegistryLocked()
	if err != nil {
		return false, err
	}

	idx := findTaskIndex(registry, id)
	if idx == -1 {
		return false, nil
	}

	registry.Tasks = append(registry.Tasks[:idx], registry.Tasks[idx+1:]...)

	for i := range registry.Tasks {
		newBlocked := registry.Tasks[i].BlockedBy[:0]
		for _, bid := range registry.Tasks[i].BlockedBy {
			if bid != id {
				newBlocked = append(newBlocked, bid)
			}
		}
		registry.Tasks[i].BlockedBy = newBlocked
	}

	if err := saveTaskRegistryLocked(registry); err != nil {
		return false, err
	}
	_ = writeTaskCheckpoint(&registry)
	return true, nil
}

func unblockDependentTasks(registry *TaskRegistry, completedID string) {
	now := time.Now().UTC()
	for i := range registry.Tasks {
		task := &registry.Tasks[i]
		newBlocked := task.BlockedBy[:0]
		for _, bid := range task.BlockedBy {
			if bid != completedID {
				newBlocked = append(newBlocked, bid)
			}
		}
		task.BlockedBy = newBlocked

		if task.Status == TaskStatusBlocked && len(task.BlockedBy) == 0 {
			task.Status = TaskStatusPending
			task.UpdatedAt = now
		}
	}
}

func writeTaskCheckpoint(registry *TaskRegistry) error {
	// Only write checkpoints if explicitly enabled in config
	if currentConfig == nil || !currentConfig.TaskCheckpoint {
		return nil
	}

	summary := map[string]int{
		"pending":     0,
		"in_progress": 0,
		"completed":   0,
		"failed":      0,
		"cancelled":   0,
		"skipped":     0,
		"blocked":     0,
	}

	for _, task := range registry.Tasks {
		summary[string(task.Status)]++
	}

	checkpointDir := "./.checkpoints"
	if err := os.MkdirAll(checkpointDir, 0755); err != nil {
		return err
	}

	checkpoint := map[string]interface{}{
		"timestamp":    time.Now().Format(time.RFC3339),
		"task_summary": summary,
		"total_tasks":  len(registry.Tasks),
	}

	checkpointBytes, err := json.MarshalIndent(checkpoint, "", "  ")
	if err != nil {
		return err
	}

	checkpointFile := filepath.Join(checkpointDir, fmt.Sprintf("checkpoint-%s.json", time.Now().Format("2006-01-02T15-04-05.000Z")))
	return os.WriteFile(checkpointFile, checkpointBytes, 0644)
}

// Task management tools

func createTaskTool(log LogFunc) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "create_task",
		Description: "Create a new task in the task board",
	}, func(ctx agent.Context, input CreateTaskInput) (CreateTaskOutput, error) {
		task, err := createTask(input)
		if err != nil {
			return CreateTaskOutput{}, err
		}
		if log != nil {
			log(fmt.Sprintf("📋 [tasks] Created task %s: %s", task.ID, task.Title))
		}
		notifyTaskBoard("created", task)
		return CreateTaskOutput{Task: task}, nil
	})
}

func updateTaskTool(log LogFunc) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "update_task",
		Description: "Update task status, assignee, or result",
	}, func(ctx agent.Context, input UpdateTaskInput) (UpdateTaskOutput, error) {
		task, err := updateTask(input)
		if err != nil {
			return UpdateTaskOutput{}, err
		}
		if log != nil {
			log(fmt.Sprintf("📋 [tasks] Updated task %s: %s (status: %s)", task.ID, task.Title, task.Status))
		}
		notifyTaskBoard("updated", task)
		return UpdateTaskOutput{Task: task}, nil
	})
}

func listTasksTool(log LogFunc) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_tasks",
		Description: "List tasks with optional filters",
	}, func(ctx agent.Context, input ListTasksInput) (ListTasksOutput, error) {
		tasks, err := listTasks(input)
		return ListTasksOutput{Tasks: tasks}, err
	})
}

func getTaskTool(log LogFunc) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "get_task",
		Description: "Get task details by ID",
	}, func(ctx agent.Context, input GetTaskInput) (GetTaskOutput, error) {
		task, err := getTask(input.ID)
		return GetTaskOutput{Task: task}, err
	})
}

func deleteTaskTool(log LogFunc) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "delete_task",
		Description: "Delete a task by ID",
	}, func(ctx agent.Context, input DeleteTaskInput) (DeleteTaskOutput, error) {
		success, err := deleteTask(input.ID)
		if err != nil {
			return DeleteTaskOutput{}, err
		}
		if success && log != nil {
			log(fmt.Sprintf("📋 [tasks] Deleted task %s", input.ID))
		}
		notifyTaskBoard("deleted", TaskMeta{ID: input.ID})
		return DeleteTaskOutput{Success: success}, nil
	})
}

// buildOrchestratorInstruction returns the system instruction for the root
// orchestrator agent, including the list of available skills so skill
// discovery happens at the orchestrator level, not only after delegation to
// the code_interpreter sub-agent.
func buildOrchestratorInstruction(installedSkills string) string {
	return `You are an AI research & analysis coordinator.
- Use 'web_researcher' when real-time browser navigation or web search is required.
- Use 'code_interpreter' when data calculations, script execution, file parsing, or statistical analysis are required.
- Use 'system_exec' tools when you need to run system commands, executables, or scripts directly on the host machine (not via the Python interpreter).
- Synthesize responses from the specialists into a final markdown output.

### TASK BOARD:
You have a task management system (persisted in tasks.json) for planning and tracking multi-step work. Available tools: 'create_task' (create), 'list_tasks' (list with optional status/assignee/tags/parent filters), 'get_task' (details by ID), 'update_task' (change status/priority/assignee/result), 'delete_task' (remove). For any multi-step request, break it into tasks, use 'list_tasks' to review your plan, and keep statuses current: mark a task 'in_progress' before executing it and 'completed' once done. Prefer the task tools over ad-hoc planning notes so progress is visible on the task board.

### SKILL REUSE:
Review the "AVAILABLE PRE-LEARNED SKILLS" list below. If a listed skill matches the user's request, load its full instructions with 'load_markdown_skill' and follow them, or delegate to the 'code_interpreter' sub-agent which can also reuse saved skills. Do not duplicate work that an existing skill already covers.

` + installedSkills + "\n\n" + buildTimeReminder()
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

	// Discover markdown skills BEFORE building the prompt and the load tool
	// so the prompt lists them and the tool index is fresh at startup.
	cwd, _ := os.Getwd()
	mdSkills := DiscoverMarkdownSkills(cwd, cfg.SkillDirs, log)
	loadMarkdownSkillTool, err := createLoadMarkdownSkillTool(mdSkills, cwd, cfg.SkillDirs, log)
	if err != nil {
		return nil, err
	}
	listSkillsTool, err := createListSkillsTool(cwd, cfg.SkillDirs, log)
	if err != nil {
		return nil, err
	}

	// Set global config for checkpoint access
	currentConfig = cfg

	// Load currently saved skills from disk
	installedSkills := getSkillsPrompt(mdSkills, log)

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
			loadMarkdownSkillTool,
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

	// Task management tools, attached to the orchestrator so the root agent can
	// plan and track multi-step work on the task board.
	createTaskT, err := createTaskTool(log)
	if err != nil {
		return nil, err
	}
	updateTaskT, err := updateTaskTool(log)
	if err != nil {
		return nil, err
	}
	listTasksT, err := listTasksTool(log)
	if err != nil {
		return nil, err
	}
	getTaskT, err := getTaskTool(log)
	if err != nil {
		return nil, err
	}
	deleteTaskT, err := deleteTaskTool(log)
	if err != nil {
		return nil, err
	}

	rootAgent, err := llmagent.New(llmagent.Config{
		Name:        "orchestrator",
		Description: "Main orchestrator agent that delegates research and analysis tasks.",
		Instruction: buildOrchestratorInstruction(installedSkills),
		Model:       model,
		Tools: append([]tool.Tool{
			listSkillsTool,
			loadMarkdownSkillTool,
			createTaskT,
			updateTaskT,
			listTasksT,
			getTaskT,
			deleteTaskT,
		}, systemExecTools...),
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
