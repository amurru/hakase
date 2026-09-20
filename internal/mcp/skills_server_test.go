package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"amurru/hakase/internal/interfaces"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// skillsFixture writes two skills (one with a supporting file) into a temp
// project under .agents/skills and returns the project root. HOME and
// XDG_CONFIG_HOME are redirected so user-level skill directories from the
// real machine never leak into discovery.
func skillsFixture(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".agents/skills/deploy-check/SKILL.md", `---
name: deploy-check
description: Verifies a deployment before promoting it
---

Run the deploy checklist.
`)
	write(".agents/skills/deploy-check/references/rollback.md", "# Rollback\n\nPress the red button.\n")
	write(".agents/skills/greet/SKILL.md", `---
name: greet
description: Says hello properly
---

Hello, world.
`)
	return root
}

// TestSkillsServerRoundTrip: a foreign MCP client (in-memory transport) sees
// the index, reads a SKILL.md byte-identically, and reads a supporting file.
func TestSkillsServerRoundTrip(t *testing.T) {
	root := skillsFixture(t)
	srv := NewSkillsServer(root, nil, nil, "test")

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "foreign-host", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	defer cs.Close()

	// Index: both skills listed with skill-md entries.
	res, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "skill://index.json"})
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].Text == "" {
		t.Fatalf("index contents = %+v", res.Contents)
	}
	var index struct {
		Skills []struct {
			Name string `json:"name"`
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &index); err != nil {
		t.Fatalf("index json: %v", err)
	}
	if len(index.Skills) != 2 {
		t.Fatalf("index skills = %+v, want 2", index.Skills)
	}

	// SKILL.md byte-identical.
	want, err := os.ReadFile(filepath.Join(root, ".agents/skills/deploy-check/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	res, err = cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "skill://deploy-check/SKILL.md"})
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].Text != string(want) {
		t.Fatalf("SKILL.md round-trip mismatch: %+v", res.Contents)
	}

	// Supporting file.
	res, err = cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "skill://deploy-check/references/rollback.md"})
	if err != nil {
		t.Fatalf("read supporting file: %v", err)
	}
	if len(res.Contents) != 1 || !strings.Contains(res.Contents[0].Text, "red button") {
		t.Fatalf("supporting file = %+v", res.Contents)
	}

	// Resources list includes the index and every file.
	listed, err := cs.ListResources(ctx, &mcpsdk.ListResourcesParams{})
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	uris := map[string]bool{}
	for _, r := range listed.Resources {
		uris[r.URI] = true
	}
	for _, want := range []string{"skill://index.json", "skill://greet/SKILL.md", "skill://deploy-check/references/rollback.md"} {
		if !uris[want] {
			t.Errorf("resources list missing %q (got %v)", want, uris)
		}
	}
}

// TestSkillsServerCollisionsFirstWins: duplicate skill names resolve by
// discovery's first match, so the index holds one entry.
func TestSkillsServerCollisionsFirstWins(t *testing.T) {
	root := skillsFixture(t)
	// Nearest directory wins: add a same-named skill deeper (closer to cwd
	// semantics would need cwd inside; here extraDirs precedence via a second
	// source dir is enough to prove dedupe).
	dup := filepath.Join(root, "dup-source", "deploy-check")
	if err := os.MkdirAll(dup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dup, "SKILL.md"), []byte("---\nname: deploy-check\ndescription: duplicate\n---\n\ndup\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := NewSkillsServer(root, []string{"dup-source"}, func(string) {}, "test")
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "foreign-host", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ReadResource(ctx, &mcpsdk.ReadResourceParams{URI: "skill://index.json"})
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(res.Contents[0].Text, "\"deploy-check\"") {
		t.Fatalf("index missing deploy-check: %s", res.Contents[0].Text)
	}
	// First match wins: the .agents/skills copy, whose description differs.
	if strings.Contains(res.Contents[0].Text, "duplicate") {
		t.Fatalf("index took the later duplicate: %s", res.Contents[0].Text)
	}
	_ = interfaces.LogFunc(nil)
}
