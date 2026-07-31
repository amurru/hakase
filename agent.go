package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
		cmd := exec.CommandContext(ctx, "python3", scriptPath)
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

	codeInterpreterAgent, err := llmagent.New(llmagent.Config{
		Name:        "code_interpreter",
		Description: "Specialist agent for executing Python code, data analysis, parsing JSON/CSV, and math computations.",
		Instruction: CodeInterpreterSystemInstruction,
		Model:       model,
		Tools:       []tool.Tool{pythonTool},
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
