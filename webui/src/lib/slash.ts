// Slash-command palette shared by the chat input autocomplete popup and the
// ChatView dispatcher. Mirrors the TUI's registered commands minus the ones
// that only make sense in a terminal (/exit).

export interface SlashCommand {
  name: string
  description: string
  args?: string
}

export const SLASH_COMMANDS: SlashCommand[] = [
  { name: 'sidekick', description: 'Ask the sidekick model a direct question', args: '<question>' },
  { name: 'compact', description: 'Summarize history to free context', args: '[focus]' },
  { name: 'new', description: 'Start a fresh session' },
  { name: 'sessions', description: 'Open the session list' },
  { name: 'board', description: 'Open the task board' },
  { name: 'mcp', description: 'Open MCP servers' },
  { name: 'help', description: 'Show available commands' },
]

// suggestCommands returns the palette entries matching a value of the form
// "/<token>" (no whitespace yet). Empty token matches everything.
export function suggestCommands(value: string): SlashCommand[] {
  const m = /^\/([a-z]*)$/i.exec(value)
  if (!m) return []
  const token = m[1].toLowerCase()
  if (!token) return SLASH_COMMANDS
  return SLASH_COMMANDS.filter((c) => c.name.startsWith(token))
}

// parseSlashCommand recognizes an exact known command with optional args:
// "/name" or "/name rest...". Unknown "/xyz" tokens return null so they are
// delivered to the model as ordinary text (they may be paths, not commands).
export function parseSlashCommand(text: string): { name: string; args: string } | null {
  const t = text.trim()
  const m = /^\/([a-z]+)(?:\s([\s\S]*))?$/i.exec(t)
  if (!m) return null
  const name = m[1].toLowerCase()
  const known = SLASH_COMMANDS.some((c) => c.name === name)
  if (!known) return null
  return { name, args: (m[2] ?? '').trim() }
}
