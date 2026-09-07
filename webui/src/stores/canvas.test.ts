import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useCanvasStore } from './canvas'

function graphEvent(partial: Record<string, unknown>): Record<string, unknown> {
  return { seq: 1, ts: 1_000, type: 'agent_start', node_id: 'n', ...partial }
}

describe('useCanvasStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ events: [] }))))
  })

  it('routes live graph events into nodes', () => {
    const store = useCanvasStore()
    store.handleGraphEvent(graphEvent({ type: 'agent_start', node_id: 'run-1', agent: 'orchestrator' }))
    store.handleGraphEvent(
      graphEvent({ seq: 2, type: 'tool_start', node_id: 'run-1', call_id: 'c1', tool: 'download' }),
    )
    expect(store.sortedNodes.map((n) => n.id)).toEqual(['run-1', 'tool:run-1:c1'])
  })

  it('drops malformed events silently', () => {
    const store = useCanvasStore()
    store.handleGraphEvent({ garbage: true })
    store.handleGraphEvent(null)
    expect(store.sortedNodes).toHaveLength(0)
  })

  it('reset clears graph state and backfills for the session', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          events: [
            graphEvent({ seq: 1, type: 'agent_start', node_id: 'old-run', agent: 'orchestrator' }),
            graphEvent({ seq: 2, type: 'agent_end', node_id: 'old-run', status: 'completed' }),
          ],
        }),
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const store = useCanvasStore()
    store.handleGraphEvent(graphEvent({ type: 'agent_start', node_id: 'live-run' }))
    store.select('live-run')

    store.reset('sess-1')
    await vi.waitFor(() => expect(store.backfillLoaded).toBe(true))

    expect(store.sessionId).toBe('sess-1')
    expect(store.selectedNodeId).toBeNull()
    // Live graph wiped, backfill restored, seq guard keeps order coherent.
    expect(store.sortedNodes.map((n) => n.id)).toEqual(['old-run'])
    expect(store.sortedNodes[0]?.status).toBe('completed')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/sessions/sess-1/graph',
      expect.objectContaining({ headers: expect.objectContaining({ 'Content-Type': 'application/json' }) }),
    )
  })

  it('backfill tolerates a failed fetch', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    const store = useCanvasStore()
    store.reset('sess-1')
    await vi.waitFor(() => expect(store.backfillLoaded).toBe(true))
    expect(store.sortedNodes).toHaveLength(0)
  })

  it('live events after backfill are not duplicated', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ events: [graphEvent({ seq: 1, type: 'agent_start', node_id: 'run-1' })] })),
      ),
    )
    const store = useCanvasStore()
    store.reset('sess-1')
    await vi.waitFor(() => expect(store.backfillLoaded).toBe(true))
    store.handleGraphEvent(graphEvent({ seq: 1, type: 'agent_start', node_id: 'run-1' })) // replay dupe
    store.handleGraphEvent(graphEvent({ seq: 2, type: 'agent_end', node_id: 'run-1', status: 'completed' }))
    expect(store.sortedNodes).toHaveLength(1)
    expect(store.sortedNodes[0]?.status).toBe('completed')
  })

  it('select exposes the selected node', () => {
    const store = useCanvasStore()
    store.handleGraphEvent(graphEvent({ type: 'agent_start', node_id: 'run-1' }))
    store.select('run-1')
    expect(store.selectedNode?.id).toBe('run-1')
    store.select(null)
    expect(store.selectedNode).toBeNull()
  })
})
