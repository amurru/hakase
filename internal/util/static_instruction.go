// static_instruction.go - brace-safe agent instructions.
//
// ADK treats every {identifier} in an agent's Instruction string as
// session-state templating (InjectSessionState) and aborts the whole run
// with ErrStateKeyNotExist when the key is missing. Static prompt text is
// guarded by TestInstructionsHaveNoStatePlaceholders, but instructions also
// embed dynamic, partly user-controlled blocks - project context files,
// skill descriptions, git snapshots, delegation context, cron job names,
// skill bodies - where a brace pair is legitimate content (code examples,
// JSON snippets, prose like "search_skills{query}"). Any of those aborts
// the run at model-call time.
//
// StaticInstruction serves a pre-rendered instruction through ADK's
// InstructionProvider instead of the Instruction string. The provider path
// does not perform state substitution (braces stay literal), while the
// served bytes are identical on every call, so prompt-cache prefix
// stability is preserved. Use it for every production llmagent whose
// instruction embeds anything beyond static constants.
package util

import (
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
)

// StaticInstruction returns an InstructionProvider that serves s verbatim
// on every invocation. The content is never subjected to ADK's
// {variable} state templating, so embedded brace pairs from dynamic
// content (skill bodies, delegation context, job names) cannot abort
// the run.
func StaticInstruction(s string) llmagent.InstructionProvider {
	return func(agent.ReadonlyContext) (string, error) {
		return s, nil
	}
}
