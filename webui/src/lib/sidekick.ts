export interface SidekickNote {
  id: string
  severity: string
  text: string
  timestamp: number
}

// Parses a sidekick SSE payload ({ severity, text }) into a note. Returns
// null on malformed input so a bad frame never breaks the stream.
export function parseSidekickEvent(raw: string): SidekickNote | null {
  try {
    const data = JSON.parse(raw)
    if (!data || typeof data !== 'object') return null
    const severity = typeof data.severity === 'string' ? data.severity : 'info'
    const text = typeof data.text === 'string' ? data.text : ''
    return {
      id: `sidekick-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
      severity,
      text,
      timestamp: Date.now(),
    }
  } catch {
    return null
  }
}

// Parses a "/sidekick" chat command typed in the web input. Returns null
// when the text is not a sidekick command, an empty string for a bare
// "/sidekick" (usage hint), or the trimmed question otherwise.
export function parseSidekickCommand(text: string): string | null {
  const t = text.trim()
  if (t !== '/sidekick' && !t.startsWith('/sidekick ')) return null
  return t.slice('/sidekick'.length).trim()
}

// Maps a sidekick severity to a quiet chip class (color + border only, no
// text label, no notification). Unknown severities fall back to info.
export function sidekickSeverityClass(severity: string): string {
  switch (severity) {
    case 'warning':
      return 'border-amber-500/40 bg-amber-500/10 text-amber-400'
    case 'critical':
    case 'error':
      return 'border-red-500/40 bg-red-500/10 text-red-400'
    case 'suggestion':
      return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-400'
    case 'info':
    default:
      return 'border-sky-500/40 bg-sky-500/10 text-sky-400'
  }
}
