package agent

import (
	"errors"

	"amurru/hakase/internal/config"
	hctx "amurru/hakase/internal/context"
	"amurru/hakase/internal/memory"
	"amurru/hakase/internal/project"
	"amurru/hakase/internal/util"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// errMemoryUnavailable is returned when the notes store cannot be opened
// (no usable hakase home); the model sees it verbatim.
var errMemoryUnavailable = errors.New("memory store unavailable (no hakase home)")

// RememberInput is the schema for the remember tool as seen by the model.
// doc tags are injected into the inferred JSON schema by util.NewDocTool.
type RememberInput struct {
	Category string `json:"category" doc:"One of: user (who the user is, their preferences), feedback (corrections they gave you), project (facts about this codebase/workspace), lesson (what you learned the hard way)."`
	Content  string `json:"content" doc:"The note itself: one short, self-contained line worth carrying across sessions. No secrets, no transient state, nothing already recorded in AGENTS.md or the knowledge wiki."`
	ID       string `json:"id,omitempty" doc:"Optional id of an existing note (from the AUTO MEMORY block) to update instead of creating a new one."`
}

// RememberOutput is the tool result returned to the model.
type RememberOutput struct {
	ID      string `json:"id" doc:"The note's id; keep it if you may need to update or remove the note later."`
	Content string `json:"content" doc:"The stored note content."`
	Updated bool   `json:"updated,omitempty" doc:"True when an existing note was updated rather than a new note created."`
}

// ForgetMemoryInput is the schema for the forget_memory tool.
type ForgetMemoryInput struct {
	ID string `json:"id" doc:"The id of the note to delete (from the AUTO MEMORY block)."`
}

// ForgetMemoryOutput is the tool result returned to the model.
type ForgetMemoryOutput struct {
	Forgotten bool   `json:"forgotten" doc:"True when a note was deleted; false when no note had that id (harmless)."`
	Content   string `json:"content,omitempty" doc:"The deleted note's content, when one was found."`
}

// memoryStore resolves the note store for the tool handlers, failing closed
// with an actionable error when the hakase home is unavailable.
func memoryStore() (*memory.Store, error) {
	store, err := memory.OpenDefault()
	if err != nil {
		return nil, err
	}
	return store, nil
}

// wireMemory adds the agent-written auto-memory surface for the orchestrator
// (docs/auto-memory/spec.md D3/D7): the remember/forget_memory tools plus the
// session-start injection provider on the history builder. When memory is
// disabled it returns nil tools and leaves the builder untouched, so the
// model is never told about tools it lacks.
func wireMemory(cfg *config.Config, hb *hctx.HistoryBuilder) ([]tool.Tool, error) {
	if !config.MemoryEnabled(cfg) {
		return nil, nil
	}
	tools, err := createRememberTools(config.MemoryMaxNotes(cfg))
	if err != nil {
		return nil, err
	}
	hb.SetMemoryProvider(func(ctx agent.Context) string {
		store, err := memory.OpenDefault()
		if err != nil {
			return ""
		}
		// Same scoping as the remember tool's write path: context root
		// first, process root fallback, "" = global-only.
		notes := memory.SelectForProject(store.Get(), project.RootFrom(ctx))
		return memory.RenderBlock(notes, config.MemoryMaxPromptChars(cfg))
	})
	return tools, nil
}

// createRememberTools builds the agent-written auto-memory tools for the
// orchestrator (docs/auto-memory/spec.md D3/D7): remember (create-or-update)
// and forget_memory (delete). maxNotes is the store-size cap from config.
func createRememberTools(maxNotes int) ([]tool.Tool, error) {
	rememberT, err := util.NewDocTool(functiontool.Config{
		Name: "remember",
		Description: "Save a short durable fact for future sessions: a user preference, a correction the user made, a project convention, or a lesson learned. " +
			"Save sparingly - one line per note, only facts worth carrying across sessions. " +
			"Pass 'id' to update a note you already saved instead of stacking a duplicate.",
	}, func(ctx agent.Context, input RememberInput) (RememberOutput, error) {
		store, err := memoryStore()
		if err != nil {
			return RememberOutput{}, err
		}
		// Notes are scoped to the project they were written under
		// ("" = global when no root resolves); RootFrom already falls back
		// to the process root set at setup.
		root := project.RootFrom(ctx)
		if input.ID != "" {
			note, err := store.Touch(input.ID, input.Category, input.Content)
			if err != nil {
				return RememberOutput{}, err
			}
			return RememberOutput{ID: note.ID, Content: note.Content, Updated: true}, nil
		}
		note, err := store.Add(input.Category, input.Content, root, maxNotes)
		if err != nil {
			return RememberOutput{}, err
		}
		return RememberOutput{ID: note.ID, Content: note.Content}, nil
	})
	if err != nil {
		return nil, err
	}

	forgetT, err := util.NewDocTool(functiontool.Config{
		Name:        "forget_memory",
		Description: "Delete one of your saved memory notes by id (ids appear in the AUTO MEMORY block). Removing an outdated note keeps memory useful; unknown ids are a harmless no-op.",
	}, func(ctx agent.Context, input ForgetMemoryInput) (ForgetMemoryOutput, error) {
		store, err := memoryStore()
		if err != nil {
			return ForgetMemoryOutput{}, err
		}
		note, ok := store.FindByID(input.ID)
		if !ok {
			return ForgetMemoryOutput{Forgotten: false}, nil
		}
		if _, err := store.Remove(input.ID); err != nil {
			return ForgetMemoryOutput{}, err
		}
		return ForgetMemoryOutput{Forgotten: true, Content: note.Content}, nil
	})
	if err != nil {
		return nil, err
	}

	return []tool.Tool{rememberT, forgetT}, nil
}
