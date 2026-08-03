// knowledge_cli.go - the `hakase knowledge` CLI: list, read, search, lint,
// create, and link knowledge notes.
//
// Every subcommand parses with flag.ContinueOnError and returns an int exit
// code instead of calling os.Exit, which keeps the CLI testable: error paths
// map to codes (0 = success/help, 1 = runtime failure, 2 = usage error) and
// the caller (main) decides whether to exit.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// runKnowledgeCLI dispatches the `hakase knowledge` subcommand tree.
func runKnowledgeCLI(args []string) int {
	if len(args) == 0 {
		knowledgeCLIUsage()
		return 0
	}
	switch args[0] {
	case "list":
		return runKnowledgeList(args[1:])
	case "read":
		return runKnowledgeRead(args[1:])
	case "search":
		return runKnowledgeSearch(args[1:])
	case "lint":
		return runKnowledgeLint(args[1:])
	case "create":
		return runKnowledgeCreate(args[1:])
	case "link":
		return runKnowledgeLink(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown knowledge subcommand %q\n\n", args[0])
		knowledgeCLIUsage()
		return 2
	}
}

// knowledgeCLIUsage prints the top-level `hakase knowledge` usage to stderr.
func knowledgeCLIUsage() {
	fmt.Fprintln(os.Stderr, "Usage: hakase knowledge <subcommand>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Subcommands:")
	fmt.Fprintln(os.Stderr, "  list       list all knowledge notes")
	fmt.Fprintln(os.Stderr, "  read       read a note by slug, basename, or alias")
	fmt.Fprintln(os.Stderr, "  search     search notes by query")
	fmt.Fprintln(os.Stderr, "  lint       check knowledge base health (orphans, dangling links)")
	fmt.Fprintln(os.Stderr, "  create     scaffold a new knowledge note")
	fmt.Fprintln(os.Stderr, "  link       link two notes with a [[wikilink]]")
}

// loadKnowledgeDir returns the knowledge directory from config or the default.
// config errors are non-fatal: a warning is printed and the default is used.
func loadKnowledgeDir() string {
	cfg, err := loadConfig("config.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot load config.json: %v (using ./knowledge)\n", err)
		return "./knowledge"
	}
	return cfg.KnowledgeDir
}

// splitTags splits a comma-separated tag list, trimming spaces and dropping
// empty entries.
func splitTags(s string) []string {
	var tags []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// ------------------- list ----------------------------------------------------

func runKnowledgeList(args []string) int {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dirFlag string
	fs.StringVar(&dirFlag, "dir", "", "knowledge directory path")
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

	dir := dirFlag
	if dir == "" {
		dir = loadKnowledgeDir()
	}

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building index: %v\n", err)
		return 1
	}

	// Collect and sort by title.
	var notes []*KnowledgeNote
	for _, n := range idx.BySlug {
		notes = append(notes, n)
	}
	for i := 0; i < len(notes); i++ {
		for j := i + 1; j < len(notes); j++ {
			if notes[i].Frontmatter.Title > notes[j].Frontmatter.Title {
				notes[i], notes[j] = notes[j], notes[i]
			}
		}
	}

	if len(notes) == 0 {
		fmt.Println("No knowledge notes found.")
	} else {
		for _, n := range notes {
			status := n.Frontmatter.Status
			if status == "" {
				status = "draft"
			}
			fmt.Printf("  %s (%s) [%s] - %s\n", n.Frontmatter.Title, n.Slug, status, n.Frontmatter.Summary)
		}
		fmt.Printf("\nTotal: %d notes\n", len(notes))
	}
	return 0
}

// ------------------- read ----------------------------------------------------

func runKnowledgeRead(args []string) int {
	var dir string
	var name string
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
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge read <name> [--dir <path>]")
			return 0
		case arg == "--dir":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --dir")
				return 2
			}
			dir = v
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge read <name> [--dir <path>]")
			return 2
		default:
			if name != "" {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				fmt.Fprintln(os.Stderr, "Usage: hakase knowledge read <name> [--dir <path>]")
				return 2
			}
			name = arg
		}
	}
	if name == "" {
		fmt.Fprintln(os.Stderr, "read requires exactly one argument (note name)")
		fmt.Fprintln(os.Stderr, "Usage: hakase knowledge read <name> [--dir <path>]")
		return 2
	}

	if dir == "" {
		dir = loadKnowledgeDir()
	}

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building index: %v\n", err)
		return 1
	}

	note, ok := ResolveTarget(idx, name)
	if !ok {
		fmt.Fprintf(os.Stderr, "note %q not found\n", name)
		return 1
	}

	fmt.Printf("Title: %s\n", note.Frontmatter.Title)
	fmt.Printf("Slug: %s\n", note.Slug)
	fmt.Printf("Path: %s\n", note.Path)
	fmt.Printf("Status: %s\n", note.Frontmatter.Status)
	fmt.Printf("Updated: %s\n", note.Frontmatter.Updated)
	if len(note.Frontmatter.Tags) > 0 {
		fmt.Printf("Tags: %s\n", strings.Join(note.Frontmatter.Tags, ", "))
	}
	if len(note.Frontmatter.Aliases) > 0 {
		fmt.Printf("Aliases: %s\n", strings.Join(note.Frontmatter.Aliases, ", "))
	}
	if note.Frontmatter.Summary != "" {
		fmt.Printf("Summary: %s\n", note.Frontmatter.Summary)
	}
	fmt.Println()
	fmt.Print(note.Body)
	return 0
}

// ------------------- search --------------------------------------------------

func runKnowledgeSearch(args []string) int {
	var dir string
	var tags []string
	var query string
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
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge search <query> [--dir <path>] [--tags a,b]")
			return 0
		case arg == "--dir":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --dir")
				return 2
			}
			dir = v
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--tags":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --tags")
				return 2
			}
			tags = splitTags(v)
		case strings.HasPrefix(arg, "--tags="):
			tags = splitTags(strings.TrimPrefix(arg, "--tags="))
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge search <query> [--dir <path>] [--tags a,b]")
			return 2
		default:
			if query != "" {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				fmt.Fprintln(os.Stderr, "Usage: hakase knowledge search <query> [--dir <path>] [--tags a,b]")
				return 2
			}
			query = arg
		}
	}
	if query == "" {
		fmt.Fprintln(os.Stderr, "search requires a query")
		fmt.Fprintln(os.Stderr, "Usage: hakase knowledge search <query> [--dir <path>] [--tags a,b]")
		return 2
	}

	if dir == "" {
		dir = loadKnowledgeDir()
	}

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building index: %v\n", err)
		return 1
	}

	results := SearchKnowledge(idx, query, tags, false)
	if len(results) == 0 {
		fmt.Println("No results found.")
		return 0
	}

	for _, n := range results {
		snippet := firstSnippet(n.Body, query)
		fmt.Printf("  %s (%s) [%s] - %s\n    %s\n",
			n.Frontmatter.Title, n.Slug, n.Frontmatter.Status, n.Frontmatter.Summary, snippet)
	}
	fmt.Printf("\n%d results\n", len(results))
	return 0
}

// ------------------- lint ----------------------------------------------------

func runKnowledgeLint(args []string) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var dirFlag string
	fs.StringVar(&dirFlag, "dir", "", "knowledge directory path")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lint takes no positional arguments\n\n")
		fs.Usage()
		return 2
	}

	dir := dirFlag
	if dir == "" {
		dir = loadKnowledgeDir()
	}

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building index: %v\n", err)
		return 1
	}

	// Orphans: no backlinks AND no outlinks.
	var orphans []string
	for slug, note := range idx.BySlug {
		outlinks := ExtractWikilinks(note.Body)
		backlinks := idx.Backlinks[slug]
		if len(backlinks) == 0 && len(outlinks) == 0 {
			orphans = append(orphans, slug)
		}
	}

	// Dangling.
	var danglingTargets []string
	for target := range idx.Dangling {
		danglingTargets = append(danglingTargets, target)
	}
	for i := 0; i < len(danglingTargets); i++ {
		for j := i + 1; j < len(danglingTargets); j++ {
			if danglingTargets[i] > danglingTargets[j] {
				danglingTargets[i], danglingTargets[j] = danglingTargets[j], danglingTargets[i]
			}
		}
	}

	var archived []string
	for slug, note := range idx.BySlug {
		if note.Frontmatter.Status == "archived" {
			archived = append(archived, slug)
		}
	}

	brokenIndex := false
	resolvedDir := knowledgeDir(dir)
	if _, statErr := os.Stat(resolvedDir + "/index.md"); os.IsNotExist(statErr) {
		brokenIndex = true
	}

	fmt.Printf("Total notes: %d\n", len(idx.BySlug))
	fmt.Printf("Orphans: %d\n", len(orphans))
	for _, s := range orphans {
		fmt.Printf("  - %s\n", s)
	}
	fmt.Printf("Dangling links: %d\n", len(danglingTargets))
	for _, d := range danglingTargets {
		fmt.Printf("  - %s\n", d)
	}
	fmt.Printf("Archived: %d\n", len(archived))
	for _, a := range archived {
		fmt.Printf("  - %s\n", a)
	}
	if brokenIndex {
		fmt.Println("Broken index: yes (index.md missing)")
	} else {
		fmt.Println("Broken index: no")
	}
	return 0
}

// ------------------- create --------------------------------------------------

func runKnowledgeCreate(args []string) int {
	var dir string
	var content string
	var tags []string
	var title string
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
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge create <title> [--dir <path>] [--content <text>] [--tags a,b]")
			return 0
		case arg == "--dir":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --dir")
				return 2
			}
			dir = v
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--content":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --content")
				return 2
			}
			content = v
		case strings.HasPrefix(arg, "--content="):
			content = strings.TrimPrefix(arg, "--content=")
		case arg == "--tags":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --tags")
				return 2
			}
			tags = splitTags(v)
		case strings.HasPrefix(arg, "--tags="):
			tags = splitTags(strings.TrimPrefix(arg, "--tags="))
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge create <title> [--dir <path>] [--content <text>] [--tags a,b]")
			return 2
		default:
			if title != "" {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				fmt.Fprintln(os.Stderr, "Usage: hakase knowledge create <title> [--dir <path>] [--content <text>] [--tags a,b]")
				return 2
			}
			title = arg
		}
	}
	if title == "" {
		fmt.Fprintln(os.Stderr, "create requires a title")
		fmt.Fprintln(os.Stderr, "Usage: hakase knowledge create <title> [--dir <path>] [--content <text>] [--tags a,b]")
		return 2
	}

	if dir == "" {
		dir = loadKnowledgeDir()
	}

	slug := Slugify(title)
	if slug == "note" && title != "note" {
		fmt.Fprintf(os.Stderr, "error: title %q produced invalid slug\n", title)
		return 1
	}

	// Check if note already exists.
	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building index: %v\n", err)
		return 1
	}
	if _, ok := idx.BySlug[slug]; ok {
		fmt.Fprintf(os.Stderr, "error: note %q already exists\n", slug)
		return 1
	}

	// YAML single-quote escaping: double single quotes.
	yamlTitle := strings.ReplaceAll(title, "'", "''")

	today := time.Now().Format("2006-01-02")
	body := content
	if body == "" {
		body = fmt.Sprintf("# %s\n\n", title)
	}

	// Build the note content directly (avoiding serializeNote so we use
	// single-quoted YAML scalars as the spec requires).
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("title: '%s'\n", yamlTitle))
	b.WriteString("aliases: []\n")
	if len(tags) > 0 {
		b.WriteString("tags:\n")
		for _, t := range tags {
			b.WriteString(fmt.Sprintf("  - '%s'\n", strings.ReplaceAll(t, "'", "''")))
		}
	} else {
		b.WriteString("tags: []\n")
	}
	b.WriteString(fmt.Sprintf("created: %s\n", today))
	b.WriteString(fmt.Sprintf("updated: %s\n", today))
	b.WriteString("status: draft\n")
	b.WriteString("sources: []\n")
	b.WriteString("related: []\n")
	b.WriteString("---\n\n")
	b.WriteString(body)

	note := &KnowledgeNote{
		Slug:        slug,
		Path:        notePath(dir, slug),
		Raw:         b.String(),
		Frontmatter: KnowledgeFrontmatter{Title: title, Tags: tags, Created: today, Updated: today, Status: "draft"},
		Body:        body,
	}

	if err := SaveNote(dir, note); err != nil {
		fmt.Fprintf(os.Stderr, "error saving note: %v\n", err)
		return 1
	}

	// Rebuild the index and append to the log so index.md and log.md stay
	// in sync, matching the save_knowledge tool behavior.
	if newIdx, err := BuildKnowledgeIndex(dir); err == nil {
		_ = UpdateIndexFile(dir, newIdx)
	}
	_ = AppendLog(dir, "create", title)

	fmt.Printf("Created note %q at %s\n", slug, note.Path)
	return 0
}

// ------------------- link ----------------------------------------------------

func runKnowledgeLink(args []string) int {
	var dir string
	var from, to string
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
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge link <from> <to> [--dir <path>]")
			return 0
		case arg == "--dir":
			v, ok := next()
			if !ok {
				fmt.Fprintln(os.Stderr, "flag needs an argument: --dir")
				return 2
			}
			dir = v
		case strings.HasPrefix(arg, "--dir="):
			dir = strings.TrimPrefix(arg, "--dir=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "unknown flag %q\n\n", arg)
			fmt.Fprintln(os.Stderr, "Usage: hakase knowledge link <from> <to> [--dir <path>]")
			return 2
		default:
			if from == "" {
				from = arg
			} else if to == "" {
				to = arg
			} else {
				fmt.Fprintf(os.Stderr, "unexpected positional argument %q\n\n", arg)
				fmt.Fprintln(os.Stderr, "Usage: hakase knowledge link <from> <to> [--dir <path>]")
				return 2
			}
		}
	}
	if from == "" || to == "" {
		fmt.Fprintln(os.Stderr, "link requires two arguments (from and to)")
		fmt.Fprintln(os.Stderr, "Usage: hakase knowledge link <from> <to> [--dir <path>]")
		return 2
	}

	if dir == "" {
		dir = loadKnowledgeDir()
	}

	idx, err := BuildKnowledgeIndex(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error building index: %v\n", err)
		return 1
	}

	fromNote, ok := ResolveTarget(idx, from)
	if !ok {
		fmt.Fprintf(os.Stderr, "source note %q not found\n", from)
		return 1
	}

	targetNote, targetOK := ResolveTarget(idx, to)
	if !targetOK {
		fmt.Fprintf(os.Stderr, "target note %q not found\n", to)
		return 1
	}

	// Link by slug (the resolvable identifier), with the title as label.
	linkLine := fmt.Sprintf("- [[%s|%s]]", targetNote.Slug, targetNote.Frontmatter.Title)

	// Check if already linked.
	if strings.Contains(fromNote.Body, "[["+targetNote.Slug) ||
		strings.Contains(fromNote.Body, "[["+targetNote.Frontmatter.Title) {
		fmt.Printf("Link from %q to %q already exists.\n", fromNote.Slug, targetNote.Slug)
		return 0
	}

	if strings.Contains(fromNote.Body, "## Related") {
		fromNote.Body = appendAfterSection(fromNote.Body, "## Related", linkLine)
	} else {
		fromNote.Body += "\n\n## Related\n\n" + linkLine + "\n"
	}

	today := time.Now().Format("2006-01-02")
	fromNote.Frontmatter.Updated = today
	serializeNote(fromNote)

	if err := UpdateNote(dir, fromNote); err != nil {
		fmt.Fprintf(os.Stderr, "error updating note: %v\n", err)
		return 1
	}

	if newIdx, err := BuildKnowledgeIndex(dir); err == nil {
		_ = UpdateIndexFile(dir, newIdx)
	}
	_ = AppendLog(dir, "link", fromNote.Frontmatter.Title)

	fmt.Printf("Linked %q to %q.\n", fromNote.Slug, targetNote.Slug)
	return 0
}
