package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

func runSessionCLI(args []string) int {
	if len(args) < 1 {
		fmt.Println("Usage: hakase session <list|delete|archive|unarchive> [options]")
		return 1
	}

	store, err := NewSessionStore(sessionsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := NewSessionService(store)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	switch args[0] {
	case "list":
		return sessionList(svc, args)
	case "delete":
		return sessionDelete(svc, args)
	case "archive":
		return sessionArchive(svc, args)
	case "unarchive":
		return sessionUnarchive(svc, args)
	default:
		fmt.Fprintf(os.Stderr, "Unknown session command: %s\n", args[0])
		return 1
	}
}

func sessionList(svc *SessionService, args []string) int {
	format := "table"
	maxCount := 0
	includeArchived := false

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case "--max-count", "-n":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &maxCount)
				i++
			}
		case "--archived":
			includeArchived = true
		}
	}

	var summaries []SessionSummary
	var err error
	if includeArchived {
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

func sessionDelete(svc *SessionService, args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: hakase session delete <sessionID>\n")
		return 1
	}
	id := args[1]

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

func sessionArchive(svc *SessionService, args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: hakase session archive <sessionID>\n")
		return 1
	}
	id := args[1]

	if err := svc.ArchiveSession(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error archiving session: %v\n", err)
		return 1
	}
	fmt.Printf("Session %s archived.\n", id)
	return 0
}

func sessionUnarchive(svc *SessionService, args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: hakase session unarchive <sessionID>\n")
		return 1
	}
	id := args[1]

	if err := svc.UnarchiveSession(id); err != nil {
		fmt.Fprintf(os.Stderr, "Error unarchiving session: %v\n", err)
		return 1
	}
	fmt.Printf("Session %s unarchived.\n", id)
	return 0
}

// sessionCleanup removes stale sessions older than the given duration.
func sessionCleanup(maxAge time.Duration) int {
	store, err := NewSessionStore(sessionsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	svc, err := NewSessionService(store)
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
