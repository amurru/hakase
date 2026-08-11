package cli

import (
	"amurru/hakase/internal/session"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"
)

func RunSessionCLI(args []string) int {
	if len(args) == 0 {
		sessionCLIUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return runSessionList(args[1:])
	case "delete":
		return runSessionDelete(args[1:])
	case "archive":
		return runSessionArchive(args[1:])
	case "unarchive":
		return runSessionUnarchive(args[1:])
	case "help", "-h", "--help":
		sessionCLIUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown session subcommand %q\n\n", args[0])
		sessionCLIUsage()
		return 2
	}
}

func sessionCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase session <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list       list sessions (--format json|table, --max-count N, --archived)")
	fmt.Fprintln(os.Stderr, "  delete     delete a session by ID")
	fmt.Fprintln(os.Stderr, "  archive    archive a session by ID")
	fmt.Fprintln(os.Stderr, "  unarchive  unarchive a session by ID")
}

func runSessionList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var format string
	var maxCount int
	var archived bool
	fs.StringVar(&format, "format", "table", "output format (table or json)")
	fs.IntVar(&maxCount, "max-count", 0, "limit number of sessions shown")
	fs.IntVar(&maxCount, "n", 0, "limit number of sessions shown (short)")
	fs.BoolVar(&archived, "archived", false, "list archived sessions instead of active")
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

	store, err := session.NewSessionStore(session.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	var summaries []session.SessionSummary
	if archived {
		summaries, err = svc.ListArchivedSessions()
	} else {
		summaries, err = svc.ListSessions()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing sessions: %v\n", err)
		return 1
	}

	if maxCount > 0 && len(summaries) > maxCount {
		summaries = summaries[:maxCount]
	}

	switch format {
	case "json":
		data, err := json.MarshalIndent(summaries, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting output: %v\n", err)
			return 1
		}
		fmt.Println(string(data))
	case "table":
		fallthrough
	default:
		if len(summaries) == 0 {
			fmt.Println("No sessions found.")
			return 0
		}
		fmt.Printf("%-12s %-40s %s\n", "ID", "TITLE", "UPDATED")
		fmt.Println("──────────────────────────────────────────────────────────────────────────────────────────────────────")
		for _, s := range summaries {
			shortID := s.ID
			if len(shortID) > 12 {
				shortID = shortID[:12]
			}
			title := s.Title
			if len(title) > 40 {
				title = title[:37] + "..."
			}
			updated := s.UpdatedAt.Format("2006-01-02 15:04")
			archived := ""
			if s.Archived {
				archived = " [archived]"
			}
			fmt.Printf("%-12s %-40s %s%s\n", shortID, title, updated, archived)
		}
	}
	return 0
}

func runSessionDelete(args []string) int {
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: hakase session delete <sessionID>\n")
		return 2
	}
	id := fs.Arg(0)

	store, err := session.NewSessionStore(session.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	// Confirm deletion.
	fmt.Printf("Delete session %s? [y/N] ", id)
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		fmt.Println("Cancelled.")
		return 0
	}

	if err := svc.DeleteSession(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting session: %v\n", err)
		return 1
	}
	fmt.Printf("Session %s deleted.\n", id)
	return 0
}

func runSessionArchive(args []string) int {
	fs := flag.NewFlagSet("archive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: hakase session archive <sessionID>\n")
		return 2
	}
	id := fs.Arg(0)

	store, err := session.NewSessionStore(session.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	if err := svc.ArchiveSession(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error archiving session: %v\n", err)
		return 1
	}
	fmt.Printf("Session %s archived.\n", id)
	return 0
}

func runSessionUnarchive(args []string) int {
	fs := flag.NewFlagSet("unarchive", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: hakase session unarchive <sessionID>\n")
		return 2
	}
	id := fs.Arg(0)

	store, err := session.NewSessionStore(session.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	if err := svc.UnarchiveSession(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error unarchiving session: %v\n", err)
		return 1
	}
	fmt.Printf("Session %s unarchived.\n", id)
	return 0
}

// SessionCleanup removes stale sessions older than the given duration.
func SessionCleanup(maxAge time.Duration) int {
	store, err := session.NewSessionStore(session.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := session.NewSessionService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	removed, err := svc.CleanupStale(maxAge)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error cleaning up sessions: %v\n", err)
		return 1
	}
	fmt.Printf("Cleaned up %d stale session(s).\n", removed)
	return 0
}
