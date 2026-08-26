import { describe, it, expect } from 'vitest'
import { parseSlashCommand, suggestCommands, SLASH_COMMANDS } from './slash'

describe('parseSlashCommand', () => {
  it('recognizes known commands with and without args', () => {
    expect(parseSlashCommand('/new')).toEqual({ name: 'new', args: '' })
    expect(parseSlashCommand('  /compact focus on API docs ')).toEqual({
      name: 'compact',
      args: 'focus on API docs',
    })
    expect(parseSlashCommand('/sidekick why is the sky blue?')).toEqual({
      name: 'sidekick',
      args: 'why is the sky blue?',
    })
    // Case-insensitive command name.
    expect(parseSlashCommand('/BOARD')).toEqual({ name: 'board', args: '' })
  })

  it('returns null for unknown or non-command text', () => {
    expect(parseSlashCommand('hello world')).toBeNull()
    expect(parseSlashCommand('/etc/hosts is a path')).toBeNull()
    expect(parseSlashCommand('/unknowncmd')).toBeNull()
    expect(parseSlashCommand('/')).toBeNull()
    expect(parseSlashCommand('')).toBeNull()
  })
})

describe('suggestCommands', () => {
  it('shows all commands for a bare slash', () => {
    expect(suggestCommands('/')).toEqual(SLASH_COMMANDS)
  })

  it('filters by prefix', () => {
    const names = suggestCommands('/s').map((c) => c.name)
    expect(names).toContain('sidekick')
    expect(names).toContain('sessions')
    expect(names).not.toContain('board')

    expect(suggestCommands('/comp').map((c) => c.name)).toEqual(['compact'])
  })

  it('closes once the token is completed with a space', () => {
    // "/compact " contains a space -> no suggestions (next Enter sends).
    expect(suggestCommands('/compact ')).toEqual([])
  })

  it('returns nothing when the value does not start with a slash', () => {
    expect(suggestCommands('plain message')).toEqual([])
    expect(suggestCommands('')).toEqual([])
  })
})
