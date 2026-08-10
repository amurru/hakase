// skill_cli.go - the `hakase skill` CLI: create, list and validate markdown
// skills (SKILL.md).
//
// Every subcommand parses with flag.ContinueOnError and returns an int exit
// code instead of calling os.Exit, which keeps the CLI testable: error paths
// map to codes (0 = success/help, 1 = runtime failure, 2 = usage error) and
// the caller (main) decides whether to exit.
package main

import (
	"amurru/hakase/internal/config"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// skillDescriptionPlaceholder is the default description written into newly
// created SKILL.md files. It is non-empty by construction so the created
// skill passes discovery validation (which rejects empty descriptions)
// immediately.
const skillDescriptionPlaceholder = "TODO: Describe what this skill does and when to use it. Be specific about trigger phrases and contexts."

// runSkillCLI dispatches the `hakase skill` subcommand tree.
func runSkillCLI(args []string) int {
	if len(args) == 0 {
		skillCLIUsage()
		return 2
	}
	switch args[0] {
	case "create":
		return runSkillCreate(args[1:])
	case "list":
		return runSkillList(args[1:])
	case "validate":
		return runSkillValidate(args[1:])
	case "evolve":
		return runSkillEvolve(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown skill subcommand %q\n\n", args[0])
		skillCLIUsage()
		return 2
	}
}

// skillCLIUsage prints the top-level `hakase skill` usage to stderr.
func skillCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase skill <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  create     create a new markdown skill (SKILL.md with scripts/ and references/)")
	fmt.Fprintln(os.Stderr, "  list       list discovered skills (Python + markdown) with source paths")
	fmt.Fprintln(os.Stderr, "  validate   validate a skill directory or SKILL.md file; exit non-zero on failure")
	fmt.Fprintln(os.Stderr, "  evolve     run one skill-evolution pass (evaluate + optional mutate); writes report to outputs/cron/")
}

// runSkillCreate scaffolds a new markdown skill at <dir>/<name>/SKILL.md.
// Arguments are parsed manually (not via flag.FlagSet) so the skill name can
// appear first, in any position relative to the flags.
func runSkillCreate(args []string) int {
	var (
		dir         string
		description string
		template    string
		force       bool
		name        string
	)

	usage := func() {
		fmt.Fprintln(os.Stderr, "Usage: hakase skill create <name> [--dir <path>] [--description <text>] [--template <name>] [--force]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  --dir <path>          directory in which to create the skill (default: <projectRoot>/.agents/skills)")
		fmt.Fprintln(os.Stderr, "  --description <text>  skill description (default: placeholder)")
		fmt.Fprintln(os.Stderr, "  --template <name>     scaffold template (currently: python)")
		fmt.Fprintln(os.Stderr, "  --force               overwrite an existing skill's SKILL.md")
		fmt.Fprintln(os.Stderr, "  -h, --help            show this help")
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		// next consumes the value token following a flag and advances i.
		next := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case arg == "-h" || arg == "--help":
			usage()
			return 0
		case arg == "--dir":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --dir")
				usage()
				return 2
			}
			dir = v
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--description":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --description")
				usage()
				return 2
			}
			description = v
		case strings.HasPrefix(arg, "--description="):
			description = strings.TrimPrefix(arg, "--description=")
		case arg == "--template":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --template")
				usage()
				return 2
			}
			template = v
		case strings.HasPrefix(arg, "--template="):
			template = strings.TrimPrefix(arg, "--template=")
		case arg == "--force":
			force = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			usage()
			return 2
		default:
			if name != "" {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				usage()
				return 2
			}
			name = arg
		}
	}

	if template != "" && template != "python" {
		fmt.Fprintf(os.Stderr, "unknown template %q (supported: python)\n", template)
		return 2
	}

	// Validate the name before touching the filesystem.
	if err := ValidateSkillName(name); err != nil {
		fmt.Fprintf(os.Stderr, "invalid skill name: %v\n", err)
		return 1
	}

	// A non-empty description is guaranteed so the created skill passes
	// discovery validation immediately. It is emitted as a single-quoted
	// YAML scalar so embedded colons (e.g. the placeholder's "TODO:")
	// cannot break the frontmatter; embedded single quotes are escaped by
	// doubling.
	if description == "" {
		description = skillDescriptionPlaceholder
	}
	yamlDescription := strings.ReplaceAll(description, "'", "''")

	// Resolve the destination directory: --dir if given (absolute, or
	// relative to cwd), else <projectRoot>/.agents/skills.
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot determine current directory: %v\n", err)
			return 1
		}
		dir = filepath.Join(FindProjectRoot(cwd), ".agents", "skills")
	} else {
		abs, err := filepath.Abs(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot resolve --dir %q: %v\n", dir, err)
			return 1
		}
		dir = abs
	}

	skillDir := filepath.Join(dir, name)
	if _, err := os.Stat(skillDir); err == nil {
		if !force {
			fmt.Fprintf(os.Stderr, "skill directory %s already exists (use --force to overwrite SKILL.md)\n", skillDir)
			return 1
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "cannot stat %s: %v\n", skillDir, err)
		return 1
	}

	// Standard skill layout: scripts/ and references/ subdirectories.
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create skill directories: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Join(skillDir, "references"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create skill directories: %v\n", err)
		return 1
	}

	content := fmt.Sprintf(`---
name: %s
description: '%s'
license: MIT
metadata:
  author: hakase
  version: 0.1.0
---

# %s

Skill scaffolded with 'hakase skill create'. Replace this body with a
description of what the skill does and when to use it. Add executable helpers
under scripts/ and deeper documentation under references/.
`, name, yamlDescription, name)

	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write SKILL.md: %v\n", err)
		return 1
	}

	if template == "python" {
		script := fmt.Sprintf(`"""Usage: this script implements the '%s' skill."""

def main():
    """Run the skill. Replace this body with the actual implementation."""
    print("TODO: implement %s")


if __name__ == "__main__":
    main()
`, name, name)
		if err := os.WriteFile(filepath.Join(skillDir, "scripts", name+".py"), []byte(script), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "cannot write template script: %v\n", err)
			return 1
		}
	}

	fmt.Printf("Created markdown skill %q at %s\n", name, skillPath)
	return 0
}

// runSkillList prints the discovered skills: Python entries from
// ./skills/skills.json plus markdown skills discovered from the project and
// user-level directories, each with a source path.
func runSkillList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "list takes no arguments\n\n")
		fs.Usage()
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine current directory: %v\n", err)
		return 1
	}

	// Custom skill dirs come from config.json. A config error only degrades
	// discovery to the standard locations; the command still succeeds.
	cfg, err := config.LoadConfig("config.json")
	var skillDirs []string
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot load config.json: %v (continuing with default skill dirs)\n", err)
	} else {
		skillDirs = cfg.SkillDirs
	}

	mdSkills := DiscoverMarkdownSkills(cwd, skillDirs, nil)

	// Python skills from the project library. A read or unmarshal error is
	// treated as an empty registry; the command still succeeds.
	var pySkills []SkillMeta
	if data, rerr := os.ReadFile(filepath.Join(cwd, "skills", "skills.json")); rerr == nil {
		var reg SkillRegistry
		if uerr := json.Unmarshal(data, &reg); uerr == nil {
			pySkills = reg.Skills
		}
	}

	if len(pySkills) == 0 && len(mdSkills) == 0 {
		fmt.Println("No skills found.")
		return 0
	}
	if len(pySkills) > 0 {
		fmt.Println("Python skills:")
		for _, s := range pySkills {
			fmt.Printf("  - %s: %s (./skills/%s)\n", s.Name, s.Description, s.FileName)
		}
	}
	if len(mdSkills) > 0 {
		fmt.Println("Markdown skills:")
		for _, s := range mdSkills {
			fmt.Printf("  - %s: %s (%s)\n", s.Frontmatter.Name, s.Frontmatter.Description, s.Path)
		}
	}
	return 0
}

// runSkillValidate parses and validates a single markdown skill given as a
// skill directory or a SKILL.md file path. It exits non-zero on failure so it
// can gate CI.
func runSkillValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "validate takes exactly one argument (a skill directory or SKILL.md file)\n\n")
		fs.Usage()
		return 2
	}

	target := fs.Arg(0)
	path := target
	if st, err := os.Stat(target); err == nil {
		if st.IsDir() {
			path = filepath.Join(target, "SKILL.md")
		}
	} else {
		fmt.Fprintf(os.Stderr, "cannot stat %s: %v\n", target, err)
		return 1
	}

	if _, err := ParseMarkdownSkill(path); err != nil {
		fmt.Fprintf(os.Stderr, "invalid skill: %v\n", err)
		return 1
	}
	fmt.Printf("OK %s\n", path)
	return 0
}

// runSkillEvolve runs one darwinian-evolver-style evolution pass over the
// Python skill library (plan Phase 3b/3c). Default mode is evaluation-only:
// every skill with an eval set is scored, skills below the deprecation
// threshold are marked deprecated, and an auditable report is written to
// outputs/cron/. --mutate enables the mutator step (requires a configured
// model); mutations that beat the incumbent by >=5% with zero holdout
// regressions are promoted, with the incumbent preserved as <name>.py.bak.
func runSkillEvolve(args []string) int {
	fs := flag.NewFlagSet("evolve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dirFlag, reportFlag string
	var mutate, noReport bool
	fs.StringVar(&dirFlag, "dir", "", "skill library directory (default ./skills)")
	fs.StringVar(&reportFlag, "report", "", "report path (default outputs/cron/evolve-<timestamp>.md)")
	fs.BoolVar(&mutate, "mutate", false, "enable the mutator step (requires a configured model)")
	fs.BoolVar(&noReport, "no-report", false, "do not write the report file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "evolve takes no positional arguments\n\n")
		fs.Usage()
		return 2
	}

	opts := EvolutionOptions{
		SkillsDir: dirFlag,
		Mutate:    mutate,
	}
	if !noReport {
		if reportFlag != "" {
			opts.ReportPath = reportFlag
		} else {
			opts.ReportPath = filepath.Join("outputs", "cron", fmt.Sprintf("evolve-%s.md", time.Now().Format("20060102-150405")))
		}
	}

	report, err := RunEvolutionPass(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evolution pass failed: %v\n", err)
		return 1
	}

	fmt.Println(report.Summary)
	if len(report.Promoted) > 0 {
		fmt.Printf("Promoted: %s\n", strings.Join(report.Promoted, ", "))
	}
	if len(report.Deprecated) > 0 {
		fmt.Printf("Deprecated: %s\n", strings.Join(report.Deprecated, ", "))
	}
	if opts.ReportPath != "" {
		fmt.Printf("Report: %s\n", opts.ReportPath)
	}
	return 0
}
