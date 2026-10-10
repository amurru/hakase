package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"amurru/hakase/internal/config"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

// AuditCheck represents one executed check in the audit report.
type AuditCheck struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"` // "PASS", "WARN", "FAIL"
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

// AuditResult summarizes the overall audit status.
type AuditResult struct {
	Failed bool         `json:"failed"`
	Checks []AuditCheck `json:"checks"`
}

// Auditor manages the 7 security checks for `mcp audit`.
type Auditor struct {
	mgr *MCPServerManager
	cfg *config.Config
}

// NewAuditor creates a new Auditor instance.
func NewAuditor(mgr *MCPServerManager, cfg *config.Config) *Auditor {
	return &Auditor{mgr: mgr, cfg: cfg}
}

var poisonRegex = regexp.MustCompile(`(?i)(ignore (previous|prior) instructions|disregard (prior|previous) (instructions|directives)|system[ _-]prompt|eval\s*\(|exec\s*\(|rm\s+-rf|curl[^\n]*\|[^\n]*sh|wget[^\n]*\|[^\n]*sh|powershell(\.exe)?\s+(-enc|-encodedcommand)|python[0-9]?\s+-c|base64\s+(-d|--decode)|nc(\.exe)?\s+-[a-z]*e)`)

// Audit runs all 7 security checks reusing Diagnose() where applicable.
func (a *Auditor) Audit(ctx agent.ReadonlyContext) (*AuditResult, error) {
	res := &AuditResult{Checks: make([]AuditCheck, 0)}

	diags := a.mgr.Diagnose(ctx)

	// Check 1: Inventory vs configured gateway budget (default 40)
	check1 := AuditCheck{Name: "Tool Inventory & Budget", Status: "PASS", Summary: "Tool count within limits"}
	totalTools := 0
	for _, d := range diags {
		if !d.Disabled && d.OK {
			totalTools += d.ToolCount
		}
	}
	budget := 40
	if a.cfg != nil && a.cfg.MCPServers.Gateway.Budget > 0 {
		budget = a.cfg.MCPServers.Gateway.Budget
	}
	if totalTools > budget {
		check1.Status = "WARN"
		check1.Summary = fmt.Sprintf("Total active tools (%d) exceeds budget of %d", totalTools, budget)
		check1.Details = append(check1.Details, "Consider disabling unused servers or configuring include/exclude lists")
	} else {
		check1.Details = append(check1.Details, fmt.Sprintf("Total active tools: %d / %d", totalTools, budget))
	}
	res.Checks = append(res.Checks, check1)

	// Check 2: Shadow drift & permissions
	check2 := a.auditShadowDrift()
	res.Checks = append(res.Checks, check2)

	// Check 3: Poisoning static scan
	check3 := a.auditPoisoning(ctx)
	res.Checks = append(res.Checks, check3)

	// Check 4: Auth posture
	check4 := a.auditAuthPosture()
	res.Checks = append(res.Checks, check4)

	// Check 5: Sandbox & Egress
	check5 := a.auditSandboxEgress()
	res.Checks = append(res.Checks, check5)

	// Check 6: Provenance & Pinning
	check6 := a.auditProvenance()
	res.Checks = append(res.Checks, check6)

	// Check 7: Elicitation audit log
	check7 := AuditCheck{Name: "Elicitation Audit Log", Status: "PASS", Summary: "Elicitation audit log accessible"}
	logPath := filepath.Join(config.HakaseHome(), "logs", "elicitation.jsonl")
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		check7.Details = append(check7.Details, "No elicitation log present yet")
	} else {
		check7.Details = append(check7.Details, fmt.Sprintf("Elicitation log: %s", logPath))
	}
	res.Checks = append(res.Checks, check7)

	for _, c := range res.Checks {
		if c.Status == "FAIL" {
			res.Failed = true
			break
		}
	}

	return res, nil
}

func (a *Auditor) auditShadowDrift() AuditCheck {
	check := AuditCheck{Name: "Shadow & Permissions", Status: "PASS", Summary: "No drift or insecure permissions detected"}

	home := config.HakaseHome()
	if home != "" {
		for _, f := range []string{
			filepath.Join(home, "mcp.json"),
			filepath.Join(home, "mcp-tokens.json"),
			filepath.Join(home, "mcp-audit.json"),
		} {
			if st, err := os.Stat(f); err == nil {
				if st.Mode().Perm()&0o022 != 0 {
					check.Status = "FAIL"
					check.Summary = "Insecure permissions on config file"
					check.Details = append(check.Details, fmt.Sprintf("%s is group/world-writable (%o)", f, st.Mode().Perm()))
				}
			}
		}
	}

	// Shadow baseline file
	baselinePath := filepath.Join(home, "mcp-audit.json")
	currHash := a.hashConfig()

	if data, err := os.ReadFile(baselinePath); err == nil {
		var stored map[string]string
		if err := json.Unmarshal(data, &stored); err == nil {
			if stored["hash"] != currHash {
				if check.Status != "FAIL" {
					check.Status = "WARN"
					check.Summary = "Configuration drift detected since baseline"
				}
				check.Details = append(check.Details, "Server configuration hash differs from ~/.hakase/mcp-audit.json")
			}
		} else {
			check.Status = "WARN"
			check.Summary = "Unreadable audit baseline"
			check.Details = append(check.Details, fmt.Sprintf("could not parse %s, treating as drift", baselinePath))
		}
	} else {
		// Write baseline if missing
		if err := os.MkdirAll(filepath.Dir(baselinePath), 0o700); err != nil {
			check.Status = "WARN"
			check.Summary = "Could not create audit baseline directory"
			check.Details = append(check.Details, err.Error())
			return check
		}
		baseData, _ := json.MarshalIndent(map[string]string{"hash": currHash}, "", "  ")
		if err := os.WriteFile(baselinePath, baseData, 0o600); err != nil {
			check.Status = "WARN"
			check.Summary = "Could not write audit baseline"
			check.Details = append(check.Details, err.Error())
		} else {
			check.Details = append(check.Details, "Created new baseline at ~/.hakase/mcp-audit.json")
		}
	}

	return check
}

func (a *Auditor) hashConfig() string {
	reg, err := config.LoadMCPRegistry(a.cfg)
	if err != nil {
		return "unreadable:" + err.Error()
	}
	names := make([]string, 0, len(reg.Servers))
	for name := range reg.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		srv := reg.Servers[name]
		h.Write([]byte(name))
		h.Write([]byte{0})
		// Canonical full-config hash: privilege changes (env, headers,
		// oauth, type, disabled) must trigger drift, not just command/url.
		if data, err := json.Marshal(srv); err == nil {
			h.Write(data)
		} else {
			h.Write([]byte(srv.URL))
			h.Write([]byte(strings.Join(srv.Command, " ")))
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

type declarer interface {
	Declaration() *genai.FunctionDeclaration
}

func (a *Auditor) auditPoisoning(ctx agent.ReadonlyContext) AuditCheck {
	check := AuditCheck{Name: "Poisoning Static Scan", Status: "PASS", Summary: "No prompt injection or dangerous constructs detected"}

	tools, err := a.mgr.Tools(ctx)
	if err != nil {
		check.Status = "WARN"
		check.Summary = "Unable to fetch tools for poisoning scan"
		check.Details = append(check.Details, err.Error())
		return check
	}

	for _, t := range tools {
		var decl *genai.FunctionDeclaration
		if d, ok := t.(declarer); ok {
			decl = d.Declaration()
		}
		if decl == nil {
			continue
		}
		// Scan the full declaration (name, description, parameter schema),
		// not just name+description: payloads usually hide in parameters.
		var textToScan string
		if data, err := json.Marshal(decl); err == nil {
			textToScan = string(data)
		} else {
			textToScan = decl.Name + " " + decl.Description
		}
		if loc := poisonRegex.FindString(textToScan); loc != "" {
			check.Status = "FAIL"
			check.Summary = "Dangerous tool construct/prompt injection pattern detected"
			toolName := decl.Name
			if server := serverFromToolName(toolName); server != "" {
				toolName = server + "/" + toolName
			}
			check.Details = append(check.Details, fmt.Sprintf("Server/Tool: %s | Snippet: %q", toolName, truncateSnippet(loc)))
		}
	}

	// Enabled but unreachable servers are skipped by Tools(): surface them so
	// a poisoned-but-down server cannot silently pass.
	for _, d := range a.mgr.Diagnose(ctx) {
		if !d.Disabled && !d.OK {
			if check.Status == "PASS" {
				check.Status = "WARN"
				check.Summary = "Poisoning scan incomplete: some servers unreachable"
			}
			msg := fmt.Sprintf("Server %q unreachable, excluded from scan", d.Name)
			if d.Error != "" {
				msg += ": " + d.Error
			}
			check.Details = append(check.Details, msg)
		}
	}

	return check
}

// serverFromToolName extracts the server from hakase tool names
// (mcp_<server>_<tool>).
func serverFromToolName(tool string) string {
	if !strings.HasPrefix(tool, "mcp_") {
		return ""
	}
	rest := strings.TrimPrefix(tool, "mcp_")
	if i := strings.LastIndex(rest, "_"); i > 0 {
		return rest[:i]
	}
	return ""
}

// truncateSnippet caps a matched snippet for the report.
func truncateSnippet(s string) string {
	const max = 160
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func (a *Auditor) auditAuthPosture() AuditCheck {
	check := AuditCheck{Name: "Authentication Posture", Status: "PASS", Summary: "Auth configuration verified"}

	reg, err := config.LoadMCPRegistry(a.cfg)
	if err != nil {
		check.Status = "WARN"
		check.Summary = "Could not load MCP registry for auth check"
		return check
	}

	for name, srv := range reg.Servers {
		if srv.Type == "http" && srv.OAuth == nil {
			check.Status = "WARN"
			check.Details = append(check.Details, fmt.Sprintf("Server %q is an HTTP transport with no OAuth config (anonymous remote)", name))
		}
		if srv.OAuth != nil && len(srv.OAuth.Scopes) > 5 {
			check.Status = "WARN"
			check.Details = append(check.Details, fmt.Sprintf("Server %q requests broad scopes (%d scopes)", name, len(srv.OAuth.Scopes)))
		}
		for _, arg := range srv.Command {
			if strings.Contains(arg, "npx") && !strings.Contains(arg, "@") {
				check.Status = "WARN"
				check.Details = append(check.Details, fmt.Sprintf("Server %q uses unpinned npx command: %s", name, arg))
			}
		}
		for k, v := range srv.Env {
			if looksLikeRawSecret(v) {
				if check.Status == "PASS" {
					check.Status = "WARN"
				}
				check.Details = append(check.Details, fmt.Sprintf("Server %q env %q may contain a raw secret (use ${%s} placeholder)", name, k, k))
			}
		}
		for k, v := range srv.Headers {
			if looksLikeRawSecret(v) {
				if check.Status == "PASS" {
					check.Status = "WARN"
				}
				check.Details = append(check.Details, fmt.Sprintf("Server %q header %q may contain a raw secret", name, k))
			}
		}
	}

	return check
}

// looksLikeRawSecret flags values that are not ${VAR} placeholders but look
// like pasted credentials.
func looksLikeRawSecret(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || strings.Contains(v, "${") {
		return false
	}
	if len(v) >= 16 && !strings.Contains(v, " ") {
		return true
	}
	lower := strings.ToLower(v)
	return strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "token ")
}

func (a *Auditor) auditSandboxEgress() AuditCheck {
	check := AuditCheck{Name: "Sandbox & Egress", Status: "PASS", Summary: "Egress and command options verified"}

	reg, err := config.LoadMCPRegistry(a.cfg)
	if err != nil {
		return check
	}

	for name, srv := range reg.Servers {
		shell := false
		for _, arg := range srv.Command {
			if arg == "sh" || arg == "bash" || arg == "cmd" || strings.Contains(arg, "sh -c") {
				shell = true
				check.Details = append(check.Details, fmt.Sprintf("Server %q runs via shell execution (%s)", name, arg))
			}
		}
		if shell {
			cmdStr := strings.Join(srv.Command, " ")
			lower := strings.ToLower(cmdStr)
			// Shell fetching remote code is a FAIL, plain shell wrapping is WARN.
			if strings.Contains(lower, "curl") || strings.Contains(lower, "wget") {
				check.Status = "FAIL"
				check.Summary = "Shell execution fetching remote code detected"
			} else if check.Status == "PASS" {
				check.Status = "WARN"
			}
		}
	}

	return check
}

func (a *Auditor) auditProvenance() AuditCheck {
	check := AuditCheck{Name: "Provenance & Version Pinning", Status: "PASS", Summary: "Server references checked"}

	reg, err := config.LoadMCPRegistry(a.cfg)
	if err != nil {
		return check
	}

	for name, srv := range reg.Servers {
		if len(srv.Command) > 0 {
			cmdStr := strings.Join(srv.Command, " ")
			if !strings.Contains(cmdStr, "@") && !strings.Contains(cmdStr, "==") {
				if check.Status == "PASS" {
					check.Status = "WARN"
					check.Summary = "Unpinned server references detected"
				}
				check.Details = append(check.Details, fmt.Sprintf("Server %q: stdio command may be unpinned", name))
			}
		}
		if srv.Type == "http" && srv.URL != "" && srv.OAuth == nil {
			if check.Status == "PASS" {
				check.Status = "WARN"
				check.Summary = "Unpinned or anonymous references detected"
			}
			check.Details = append(check.Details, fmt.Sprintf("Server %q: http endpoint without OAuth (anonymous remote)", name))
		}
	}

	return check
}
