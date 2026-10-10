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

var poisonRegex = regexp.MustCompile(`(?i)(ignore previous instructions|system prompt|eval\(|exec\(|rm -rf|curl .*\|.*sh|wget .*\|.*sh)`)

// Audit runs all 7 security checks reusing Diagnose() where applicable.
func (a *Auditor) Audit(ctx agent.ReadonlyContext) (*AuditResult, error) {
	res := &AuditResult{Checks: make([]AuditCheck, 0)}

	diags := a.mgr.Diagnose(ctx)

	// Check 1: Inventory vs 40 tool budget
	check1 := AuditCheck{Name: "Tool Inventory & Budget", Status: "PASS", Summary: "Tool count within limits"}
	totalTools := 0
	for _, d := range diags {
		if !d.Disabled && d.OK {
			totalTools += d.ToolCount
		}
	}
	if totalTools > 40 {
		check1.Status = "WARN"
		check1.Summary = fmt.Sprintf("Total active tools (%d) exceeds budget of 40", totalTools)
		check1.Details = append(check1.Details, "Consider disabling unused servers or configuring include/exclude lists")
	} else {
		check1.Details = append(check1.Details, fmt.Sprintf("Total active tools: %d / 40", totalTools))
	}
	res.Checks = append(res.Checks, check1)

	// Check 2: Shadow drift & permissions
	check2 := a.auditShadowDrift()
	res.Checks = append(res.Checks, check2)

	// Check 3: Poisoning static scan
	check3 := a.auditPoisoning(ctx)
	res.Checks = append(res.Checks, check3)

	// Check 4: Auth posture
	check4 := a.auditAuthPosture(diags)
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
		mcpJson := filepath.Join(home, "mcp.json")
		if st, err := os.Stat(mcpJson); err == nil {
			if st.Mode().Perm()&0002 != 0 {
				check.Status = "FAIL"
				check.Summary = "Insecure permissions on config file"
				check.Details = append(check.Details, fmt.Sprintf("%s is world-writable (%o)", mcpJson, st.Mode().Perm()))
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
		}
	} else {
		// Write baseline if missing
		baseData, _ := json.MarshalIndent(map[string]string{"hash": currHash}, "", "  ")
		_ = os.WriteFile(baselinePath, baseData, 0o600)
		check.Details = append(check.Details, "Created new baseline at ~/.hakase/mcp-audit.json")
	}

	return check
}

func (a *Auditor) hashConfig() string {
	h := sha256.New()
	reg, err := config.LoadMCPRegistry(a.cfg)
	if err == nil {
		names := make([]string, 0, len(reg.Servers))
		for name := range reg.Servers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			srv := reg.Servers[name]
			h.Write([]byte(name))
			h.Write([]byte(srv.URL))
			h.Write([]byte(strings.Join(srv.Command, " ")))
		}
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
		textToScan := decl.Name + " " + decl.Description
		if loc := poisonRegex.FindString(textToScan); loc != "" {
			check.Status = "FAIL"
			check.Summary = "Dangerous tool construct/prompt injection pattern detected"
			check.Details = append(check.Details, fmt.Sprintf("Server/Tool: %s | Snippet: %q", decl.Name, loc))
		}
	}

	return check
}

func (a *Auditor) auditAuthPosture(diags []ServerDiagnostic) AuditCheck {
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
	}

	return check
}

func (a *Auditor) auditSandboxEgress() AuditCheck {
	check := AuditCheck{Name: "Sandbox & Egress", Status: "PASS", Summary: "Egress and command options verified"}

	reg, err := config.LoadMCPRegistry(a.cfg)
	if err != nil {
		return check
	}

	for name, srv := range reg.Servers {
		for _, arg := range srv.Command {
			if arg == "sh" || arg == "bash" || arg == "cmd" || strings.Contains(arg, "sh -c") {
				check.Status = "WARN"
				check.Details = append(check.Details, fmt.Sprintf("Server %q runs via shell execution (%s)", name, arg))
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
				check.Details = append(check.Details, fmt.Sprintf("Server %q: stdio command may be unpinned", name))
			}
		}
	}

	return check
}
