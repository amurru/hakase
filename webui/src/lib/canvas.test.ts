import { describe, expect, it } from 'vitest'
import {
  applyEvent,
  computeLayout,
  createGraphState,
  formatDuration,
  parseGraphFrame,
  rootNodes,
  TEXT_ACCUM_CAP,
  toolNodeId,
  type GraphEventFrame,
} from './canvas'

function frame(partial: Partial<GraphEventFrame>): GraphEventFrame {
  return {
    seq: 1,
    ts: 1_000,
    type: 'agent_start',
    node_id: 'n',
    ...partial,
  }
}

describe('parseGraphFrame', () => {
  it('accepts a valid frame', () => {
    const parsed = parseGraphFrame({ seq: 3, ts: 5, type: 'tool_start', node_id: 'a', call_id: 'c1' })
    expect(parsed).not.toBeNull()
    expect(parsed?.seq).toBe(3)
    expect(parsed?.call_id).toBe('c1')
  })

  it('rejects malformed payloads', () => {
    expect(parseGraphFrame(null)).toBeNull()
    expect(parseGraphFrame('nope')).toBeNull()
    expect(parseGraphFrame({ type: 'bogus', node_id: 'a' })).toBeNull()
    expect(parseGraphFrame({ type: 'agent_start' })).toBeNull()
  })
})

describe('applyEvent', () => {
  it('builds a root agent node from agent_start', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'run-1', agent: 'orchestrator', goal: 'hello' }))
    const node = state.nodes.get('run-1')
    expect(node?.kind).toBe('agent')
    expect(node?.status).toBe('running')
    expect(node?.label).toBe('orchestrator')
    expect(node?.goal).toBe('hello')
    expect(node?.parentId).toBeNull()
  })

  it('pairs tool_start/tool_end via call ids and records duration/result', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'run-1' }))
    applyEvent(state, frame({ seq: 2, type: 'tool_start', node_id: 'run-1', call_id: 'c1', tool: 'download', args: { url: 'https://x' } }))
    applyEvent(state, frame({ seq: 3, ts: 4_000, type: 'tool_end', node_id: 'run-1', call_id: 'c1', ok: true, duration_ms: 3_000, result: '{"ok":true}' }))

    const tool = state.nodes.get(toolNodeId('run-1', 'c1'))
    expect(tool?.kind).toBe('tool')
    expect(tool?.tool).toBe('download')
    expect(tool?.status).toBe('completed')
    expect(tool?.durationMs).toBe(3_000)
    expect(tool?.result).toBe('{"ok":true}')
    expect(tool?.parentId).toBe('run-1')
  })

  it('marks failed tool calls when ok is false', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'tool_start', node_id: 'a', call_id: 'c1', tool: 't' }))
    applyEvent(state, frame({ seq: 2, type: 'tool_end', node_id: 'a', call_id: 'c1', ok: false, error: 'boom' }))
    expect(state.nodes.get(toolNodeId('a', 'c1'))?.status).toBe('failed')
  })

  it('synthesizes tool nodes for tool_end without a start (evicted ring)', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'tool_end', node_id: 'a', call_id: 'c9', tool: 'read_file' }))
    expect(state.nodes.get(toolNodeId('a', 'c9'))?.status).toBe('completed')
  })

  it('accumulates text/thought deltas and terminates agents', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'sub' }))
    applyEvent(state, frame({ seq: 2, type: 'agent_thought', node_id: 'sub', text: 'think ' }))
    applyEvent(state, frame({ seq: 3, type: 'agent_thought', node_id: 'sub', text: 'hard' }))
    applyEvent(state, frame({ seq: 4, type: 'agent_text', node_id: 'sub', text: 'answer' }))
    applyEvent(state, frame({ seq: 5, type: 'agent_end', node_id: 'sub', status: 'completed', summary: 'done', duration_ms: 9 }))

    const node = state.nodes.get('sub')
    expect(node?.thought).toBe('think hard')
    expect(node?.text).toBe('answer')
    expect(node?.status).toBe('completed')
    expect(node?.summary).toBe('done')
    expect(node?.durationMs).toBe(9)
  })

  it('caps accumulated text at TEXT_ACCUM_CAP', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'a' }))
    applyEvent(state, frame({ seq: 2, type: 'agent_text', node_id: 'a', text: 'x'.repeat(TEXT_ACCUM_CAP + 10) }))
    const node = state.nodes.get('a')
    expect(node?.text.length).toBeLessThanOrEqual(TEXT_ACCUM_CAP + 20)
    expect(node?.text.endsWith('…[truncated]')).toBe(true)
  })

  it('links delegation children under an open delegate_task tool node', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'root' }))
    applyEvent(state, frame({ seq: 2, type: 'tool_start', node_id: 'root', call_id: 'c1', tool: 'delegate_task' }))
    applyEvent(state, frame({ seq: 3, type: 'agent_start', node_id: 'sub', parent_id: 'root', agent: 'web_researcher' }))

    const sub = state.nodes.get('sub')
    expect(sub?.parentId).toBe('root')
    expect(sub?.visualParentId).toBe(toolNodeId('root', 'c1'))

    // Closing the delegation call leaves existing visual links stable.
    applyEvent(state, frame({ seq: 4, type: 'tool_end', node_id: 'root', call_id: 'c1', tool: 'delegate_task', ok: true }))
    expect(state.nodes.get(toolNodeId('root', 'c1'))?.status).toBe('completed')
  })

  it('falls back to the agent parent when no delegate_task call is open', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'root' }))
    applyEvent(state, frame({ seq: 2, type: 'agent_start', node_id: 'sub', parent_id: 'root' }))
    expect(state.nodes.get('sub')?.visualParentId).toBe('root')
  })

  it('creates transfer nodes parented to the transferring node', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'transfer', node_id: 'root', target: 'web_researcher' }))
    const node = state.nodes.get('transfer:web_researcher')
    expect(node?.kind).toBe('agent')
    expect(node?.parentId).toBe('root')
  })

  it('drops duplicate or out-of-order frames by seq', () => {
    const state = createGraphState()
    applyEvent(state, frame({ seq: 5, type: 'agent_start', node_id: 'a' }))
    applyEvent(state, frame({ seq: 4, type: 'agent_start', node_id: 'b' })) // stale
    applyEvent(state, frame({ seq: 5, type: 'agent_start', node_id: 'c' })) // duplicate
    expect(state.nodes.has('a')).toBe(true)
    expect(state.nodes.has('b')).toBe(false)
    expect(state.nodes.has('c')).toBe(false)
  })
})

describe('computeLayout', () => {
  it('returns no positions for an empty graph', () => {
    expect(computeLayout(createGraphState().nodes).size).toBe(0)
  })

  it('stacks children below their parent and siblings side by side', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'root' }))
    applyEvent(state, frame({ seq: 2, ts: 2_000, type: 'tool_start', node_id: 'root', call_id: 'c1', tool: 'a' }))
    applyEvent(state, frame({ seq: 3, ts: 3_000, type: 'tool_start', node_id: 'root', call_id: 'c2', tool: 'b' }))

    const layout = computeLayout(state.nodes)
    const root = layout.get('root')!
    const a = layout.get(toolNodeId('root', 'c1'))!
    const b = layout.get(toolNodeId('root', 'c2'))!

    expect(a.y).toBeGreaterThan(root.y)
    expect(b.y).toBe(a.y)
    expect(b.x).toBeGreaterThan(a.x)
  })

  it('lays out multiple roots (sequential runs) left to right', () => {
    const state = createGraphState()
    applyEvent(state, frame({ seq: 1, ts: 1_000, type: 'agent_start', node_id: 'run-1' }))
    applyEvent(state, frame({ seq: 2, ts: 2_000, type: 'agent_start', node_id: 'run-2' }))

    const layout = computeLayout(state.nodes)
    expect(layout.get('run-2')!.x).toBeGreaterThan(layout.get('run-1')!.x)
  })

  it('treats orphaned nodes (evicted parents) as roots', () => {
    const state = createGraphState()
    applyEvent(state, frame({ type: 'agent_start', node_id: 'orphan', parent_id: 'ghost' }))
    expect(rootNodes(state.nodes).map((n) => n.id)).toEqual(['orphan'])
    expect(computeLayout(state.nodes).get('orphan')).toBeDefined()
  })
})

describe('formatDuration', () => {
  it('formats sub-second and multi-second durations', () => {
    expect(formatDuration(undefined)).toBe('')
    expect(formatDuration(850)).toBe('850ms')
    expect(formatDuration(3_200)).toBe('3.2s')
  })
})
