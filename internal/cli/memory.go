// memory.go - the `hakase memory` management CLI. It touches only the notes
// file (internal/memory), never the agent runtime, so it works while the web
// server is running (cross-process flock keeps writes safe).
//
//	hakase memory list [--all]                              - notes for this project (+ global); --all adds other projects
//	hakase memory add --category <cat> [--project <path>] <text...>
//	hakase memory forget <id>                               - delete one note
package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amurru/hakase/internal/memory"
	"amurru/hakase/internal/project"
)

// cliProjectRoot derives the project root from the CLI's working directory
// (the agent runtime stamps notes with its process root; a CLI invocation
// has no SetupRunner, so the cwd walk is the equivalent).
func cliProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return project.FindRoot(cwd)
}

// RunMemoryCLI implements the memory subcommand.
func RunMemoryCLI(args []string) int {
	if len(args) == 0 {
		memoryUsage()
		return 2
	}
	store, err := memory.OpenDefault()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot open memory store: %v\n", err)
		return 1
	}

	switch args[0] {
	case "list":
		return runMemoryList(store, args[1:])
	case "add":
		return runMemoryAdd(store, args[1:])
	case "forget":
		return runMemoryForget(store, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "hakase: unknown memory subcommand %q\n\n", args[0])
		memoryUsage()
		return 2
	}
}

func memoryUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase memory <subcommand> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list [--all]            show notes for this project plus global ones (--all: every project)")
	fmt.Fprintln(os.Stderr, "  add                     add a note: --category <user|feedback|project|lesson> [--project <path>] <text...>")
	fmt.Fprintln(os.Stderr, "  forget <id>             delete one note by id")
}

func runMemoryList(store *memory.Store, args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	all := fs.Bool("all", false, "include notes from every project")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	st := store.Get()
	notes := memory.SelectForProject(st, cliProjectRoot())
	if *all {
		notes = st.Notes
	}
	if len(notes) == 0 {
		fmt.Printf("Memory notes: none (saved by the agent via the remember tool) - %s\n", store.Path())
		return 0
	}
	fmt.Printf("Memory notes (%d) - %s:\n", len(notes), store.Path())
	for _, n := range notes {
		scope := "global"
		if n.Project != "" {
			scope = n.Project
		}
		fmt.Printf("  [%s] (%s) %s\n    project: %s | updated: %s\n", n.Category, n.ID, n.Content, scope, n.UpdatedAt.Format(time.RFC3339))
	}
	return 0
}

func runMemoryAdd(store *memory.Store, args []string) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	category := fs.String("category", "", "note category: user | feedback | project | lesson")
	projectPath := fs.String("project", "", "project root to scope the note to (default: current project root; empty string = global)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	content := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if *category == "" || content == "" {
		fmt.Fprintln(os.Stderr, "Usage: hakase memory add --category <user|feedback|project|lesson> [--project <path>] <text...>")
		return 2
	}
	if *projectPath == "-" { // explicit global from the shell
		*projectPath = ""
	} else if *projectPath == "" {
		*projectPath = cliProjectRoot()
	} else {
		// Match the agent's write path: absolute, resolved to the enclosing
		// project root, so the note is visible to session injection (which
		// compares against absolute roots).
		abs, err := filepath.Abs(*projectPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hakase: cannot resolve --project %q: %v\n", *projectPath, err)
			return 2
		}
		*projectPath = project.FindRoot(abs)
	}
	note, err := store.Add(*category, content, *projectPath, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hakase: %v\n", err)
		return 1
	}
	fmt.Printf("Saved %s [%s] %s (project: %s)\n", note.ID, note.Category, note.Content, scopeLabel(note.Project))
	return 0
}

func runMemoryForget(store *memory.Store, args []string) int {
	fs := flag.NewFlagSet("forget", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "Usage: hakase memory forget <id>")
		return 2
	}
	id := strings.TrimSpace(fs.Arg(0))
	note, ok := store.FindByID(id)
	if !ok {
		fmt.Fprintf(os.Stderr, "hakase: no note with id %s\n", id)
		return 1
	}
	if _, err := store.Remove(id); err != nil {
		fmt.Fprintf(os.Stderr, "hakase: cannot write memory store: %v\n", err)
		return 1
	}
	fmt.Printf("Forgot %s [%s] %s\n", id, note.Category, note.Content)
	return 0
}

func scopeLabel(project string) string {
	if project == "" {
		return "global"
	}
	return project
}
