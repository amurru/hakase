// projects.go - the `hakase projects` CLI: manage registered remote projects.
//
// Registering materializes the project into a managed checkout under the
// hakase home (registry store + git_clone/git_pull engine, see
// internal/registry/service.go), so a remote hakase host can work on code that
// was never on its filesystem. Like every subcommand here the handlers parse
// with ContinueOnError-style semantics and return an int exit code (0 =
// success/help, 1 = runtime failure, 2 = usage error); the caller (main)
// decides whether to exit.
package cli

import (
	"amurru/hakase/internal/registry"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// RunProjectCLI dispatches the `hakase projects` subcommand tree.
func RunProjectCLI(args []string) int {
	if len(args) == 0 {
		projectsCLIUsage()
		return 0
	}
	switch args[0] {
	case "list":
		return runProjectsList(args[1:])
	case "register":
		return runProjectsRegister(args[1:])
	case "sync":
		return runProjectsSync(args[1:])
	case "delete":
		return runProjectsDelete(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown projects subcommand %q\n\n", args[0])
		projectsCLIUsage()
		return 2
	}
}

// projectsCLIUsage prints the top-level `hakase projects` usage to stderr.
func projectsCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase projects <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list                 list registered projects")
	fmt.Fprintln(os.Stderr, "  register NAME URL    register a project and clone it [--ref BRANCH]")
	fmt.Fprintln(os.Stderr, "  sync NAME            fast-forward a project checkout from its remote")
	fmt.Fprintln(os.Stderr, "  delete NAME          unregister a project and remove its local checkout")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Sources: https://, http://, git://, ssh:// (file:// for a local bare remote).")
	fmt.Fprintln(os.Stderr, "Credentials are never stored: clone/pull use the host's git auth (DP-8).")
}

// projectCLILog forwards service progress lines to stderr.
func projectCLILog(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}

// humanErr renders an error for the terminal, removing <UNTRUSTED_DATA>
// framing that the git engine wraps around output for model consumption.
func humanErr(err error) string {
	if err == nil {
		return ""
	}
	lines := strings.Split(err.Error(), "\n")
	out := lines[:0]
	for _, l := range lines {
		switch strings.TrimSpace(l) {
		case "<UNTRUSTED_DATA>", "</UNTRUSTED_DATA>":
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// loadProjectsStore returns the registry store at the default location.
func loadProjectsStore() (*registry.Store, error) {
	return registry.NewStore(registry.DefaultPath())
}

// ------------------- list ----------------------------------------------------

func runProjectsList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "list takes no positional arguments\n\n")
		fs.Usage()
		return 2
	}

	st, err := loadProjectsStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading project registry: %s\n", humanErr(err))
		return 1
	}
	projects := st.List()
	if len(projects) == 0 {
		fmt.Println("No registered projects. Register one with: hakase projects register <name> <url>")
		return 0
	}

	fmt.Printf("%-28s %-12s %-10s %s\n", "NAME", "ID", "STATUS", "CHECKOUT")
	for _, p := range projects {
		checkout := p.Checkout
		if checkout == "" {
			checkout = "(not materialized)"
		}
		fmt.Printf("%-28s %-12s %-10s %s\n", p.Name, p.ID, p.Status, checkout)
	}
	fmt.Printf("\n%d project(s)\n", len(projects))
	return 0
}

// ------------------- register ------------------------------------------------

// registerUsage is the `hakase projects register` one-liner for error output.
func registerUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase projects register <name> <url> [--ref <branch>]")
}

func runProjectsRegister(args []string) int {
	var ref string
	var name, url string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case arg == "-h" || arg == "--help":
			registerUsage()
			return 0
		case arg == "--ref":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --ref")
				registerUsage()
				return 2
			}
			ref = v
		case strings.HasPrefix(arg, "--ref="):
			ref = strings.TrimPrefix(arg, "--ref=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			registerUsage()
			return 2
		default:
			if name == "" {
				name = arg
			} else if url == "" {
				url = arg
			} else {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				registerUsage()
				return 2
			}
		}
	}
	if name == "" || url == "" {
		fmt.Fprintln(os.Stderr, "register requires a name and a clone URL")
		registerUsage()
		return 2
	}
	if !registry.ValidName(name) {
		fmt.Fprintf(os.Stderr, "invalid project name %q (letters, digits, and . _ -; up to 64 chars)\n", name)
		registerUsage()
		return 2
	}
	if !registry.ValidSourceURL(url) {
		fmt.Fprintf(os.Stderr, "unsupported source URL %q (allowed: https://, http://, git://, ssh://, or file:// for a local bare remote)\n", url)
		registerUsage()
		return 2
	}

	st, err := loadProjectsStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading project registry: %s\n", humanErr(err))
		return 1
	}
	svc := registry.NewService(st, projectCLILog)
	p, err := svc.Register(context.Background(), name, url, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", humanErr(err))
		if p.ID != "" {
			fmt.Fprintf(os.Stderr,
				"entry %q was left in state %q - retry with 'hakase projects sync %s' or remove it with 'hakase projects delete %s'\n",
				p.Name, p.Status, p.Name, p.Name)
		}
		return 1
	}
	fmt.Printf("Registered %s (%s)\n", p.Name, p.ID)
	fmt.Printf("  source:  %s\n", p.SourceURL)
	if p.Ref != "" {
		fmt.Printf("  ref:     %s\n", p.Ref)
	}
	fmt.Printf("  status:  %s\n", p.Status)
	fmt.Printf("  checkout: %s\n", p.Checkout)
	return 0
}

// ------------------- sync ----------------------------------------------------

// syncUsage is the `hakase projects sync` one-liner for error output.
func syncUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase projects sync <name>")
}

func runProjectsSync(args []string) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		syncUsage()
		return 0
	}
	if len(args) != 1 {
		syncUsage()
		return 2
	}

	st, err := loadProjectsStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading project registry: %s\n", humanErr(err))
		return 1
	}
	svc := registry.NewService(st, projectCLILog)
	target, err := svc.Resolve(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", humanErr(err))
		return 1
	}
	p, err := svc.Sync(context.Background(), target.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", humanErr(err))
		fmt.Fprintf(os.Stderr, "entry %q is now in state %q\n", p.Name, p.Status)
		return 1
	}
	fmt.Printf("Synced %s (%s): %s\n", p.Name, p.ID, p.Status)
	fmt.Printf("  checkout: %s\n", p.Checkout)
	return 0
}

// ------------------- delete --------------------------------------------------

// deleteUsage is the `hakase projects delete` one-liner for error output.
func deleteUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase projects delete <name>")
}

func runProjectsDelete(args []string) int {
	if len(args) != 1 {
		deleteUsage()
		return 2
	}
	if args[0] == "-h" || args[0] == "--help" {
		deleteUsage()
		return 0
	}

	st, err := loadProjectsStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading project registry: %s\n", humanErr(err))
		return 1
	}
	svc := registry.NewService(st, projectCLILog)
	target, err := svc.Resolve(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", humanErr(err))
		return 1
	}
	checkout := st.CheckoutDir(target)
	p, err := svc.Delete(context.Background(), target.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", humanErr(err))
		return 1
	}
	fmt.Printf("Deleted %s (%s)\n", p.Name, p.ID)
	fmt.Printf("  checkout %s removed (the remote was left untouched)\n", checkout)
	return 0
}
