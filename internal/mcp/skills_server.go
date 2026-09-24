// skills_server.go - serve hakase's local markdown skills over MCP
// (SEP-2640): every file of a discovered skill directory is exposed as an
// MCP resource under the `skill://` URI scheme, and a well-known
// `skill://index.json` resource enumerates them (index fields mirror the
// SEP: name/type/description/url; go-sdk v1.7 ships no skills helpers, so
// this is hand-built on plain resources - D2).
//
// Read-only by contract: handlers only read files beneath a discovered
// skill directory, through a root-confined open (see readSkillFile) so a
// symlink planted after discovery cannot redirect a read outside the skill;
// nothing writes to skill directories.
package mcp

import (
	"amurru/hakase/internal/interfaces"
	"amurru/hakase/internal/skill"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// indexSchema is the $schema URI of the Agent Skills discovery index format
// the SEP pins (0.2.0).
const indexSchema = "https://schemas.agentskills.io/discovery/0.2.0/schema.json"

// readSkillFile reads rel beneath the skill root dir through os.Root: the
// open is confined to the root and refuses to traverse symlinks that escape
// it, so neither a symlinked file registered by discovery (registering
// symlinks is skipped anyway) nor one swapped in afterwards can redirect a
// read outside the skill directory.
func readSkillFile(dir, rel string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// indexEntry is one record of skill://index.json.
type indexEntry struct {
	Name        string `json:"name,omitempty"`
	Type        string `json:"type"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// NewSkillsServer builds an MCP server exposing the markdown skills
// discovered from cwd (standard walk + project library + extraDirs + user
// level, mirroring DiscoverMarkdownSkills) as skill:// resources. Collisions
// follow discovery's first-wins; invalid skills were already skipped with a
// warning by discovery.
func NewSkillsServer(cwd string, extraDirs []string, log interfaces.LogFunc, version string) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "hakase-skills", Version: version}, nil)
	AddSkillResources(srv, cwd, extraDirs, log)
	return srv
}

// AddSkillResources registers the skill:// resources on an existing server,
// so a run-enabled server (`hakase mcp serve --agent`) can serve skills and
// runs over one connection. See NewSkillsServer for the discovery contract.
func AddSkillResources(srv *mcp.Server, cwd string, extraDirs []string, log interfaces.LogFunc) {
	skills := skill.DiscoverMarkdownSkills(cwd, extraDirs, log)
	entries := make([]indexEntry, 0, len(skills))
	for _, sk := range skills {
		name := sk.Frontmatter.Name
		base := "skill://" + name
		entries = append(entries, indexEntry{
			Name:        name,
			Type:        "skill-md",
			Description: sk.Frontmatter.Description,
			URL:         base + "/SKILL.md",
		})
		srv.AddResource(&mcp.Resource{
			URI:         base + "/SKILL.md",
			Name:        name,
			Title:       name,
			Description: sk.Frontmatter.Description,
			MIMEType:    "text/markdown",
		}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			// Read from disk so edits between discovery and read are
			// reflected, confined to the skill root (readSkillFile).
			data, err := readSkillFile(sk.Dir, "SKILL.md")
			if err != nil {
				return nil, fmt.Errorf("skill %q: %w", name, err)
			}
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: base + "/SKILL.md", MIMEType: "text/markdown", Text: string(data)},
			}}, nil
		})

		// Supporting files as sibling resources (SEP-2640: relative paths
		// resolve against the skill root). scripts/ is inventoried by
		// discovery; walk the whole directory for completeness. Only
		// regular files register: symlinks and other non-regular entries
		// are skipped, since a link could point outside the skill root.
		_ = filepath.WalkDir(sk.Dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil // ignore unreadable subtrees; never fail discovery
			}
			rel, rerr := filepath.Rel(sk.Dir, path)
			if rerr != nil || rel == "SKILL.md" {
				return nil
			}
			uri := base + "/" + filepath.ToSlash(rel)
			mt := mime.TypeByExtension(filepath.Ext(path))
			if mt == "" {
				// Windows resolves extension types via the registry, where
				// text types like .md/.go are often unregistered; keep them
				// portable so they are served as text, not base64 blobs.
				switch strings.ToLower(filepath.Ext(path)) {
				case ".md", ".markdown":
					mt = "text/markdown"
				case ".go":
					mt = "text/x-go"
				default:
					mt = "application/octet-stream"
				}
			}
			resourceURI, resourceMIME := uri, mt
			srv.AddResource(&mcp.Resource{
				URI:         resourceURI,
				Name:        name + "/" + filepath.ToSlash(rel),
				Title:       name + "/" + filepath.ToSlash(rel),
				Description: "Supporting file of skill " + name,
				MIMEType:    resourceMIME,
			}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				data, err := readSkillFile(sk.Dir, rel)
				if err != nil {
					return nil, fmt.Errorf("skill %q file %s: %w", name, rel, err)
				}
				if strings.HasPrefix(resourceMIME, "text/") || resourceMIME == "application/json" {
					return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
						{URI: resourceURI, MIMEType: resourceMIME, Text: string(data)},
					}}, nil
				}
				return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
					{URI: resourceURI, MIMEType: resourceMIME, Blob: data},
				}}, nil
			})
			return nil
		})
	}

	index, err := json.MarshalIndent(map[string]any{
		"$schema": indexSchema,
		"skills":  entries,
	}, "", "  ")
	if err == nil {
		srv.AddResource(&mcp.Resource{
			URI:         "skill://index.json",
			Name:        "index",
			Title:       "hakase skill index",
			Description: "Index of every skill this server exposes",
			MIMEType:    "application/json",
		}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
				{URI: "skill://index.json", MIMEType: "application/json", Text: string(index)},
			}}, nil
		})
	} else if log != nil {
		log(fmt.Sprintf("[skills] mcp: cannot render index.json: %v", err))
	}
}
