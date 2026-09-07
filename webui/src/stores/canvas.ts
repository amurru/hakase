import { defineStore } from 'pinia'
import { computed, reactive, ref } from 'vue'
import { apiFetch } from '@/lib/api'
import {
  applyEvent,
  createGraphState,
  parseGraphFrame,
  rootNodes,
  type CanvasGraphState,
  type CanvasNode,
  type GraphEventFrame,
} from '@/lib/canvas'

/**
 * Execution-canvas state for the chat view's active session. The graph is
 * rebuilt from the server's retained ring (GET /sessions/{id}/graph) on
 * session switch and then extended live from "graph" SSE events; the store
 * itself stays a thin shell over the pure reducer in lib/canvas.
 */
export const useCanvasStore = defineStore('canvas', () => {
  const graph = reactive(createGraphState()) as CanvasGraphState
  const selectedNodeId = ref<string | null>(null)
  const panelOpen = ref(false)
  const sessionId = ref<string | null>(null)
  const backfillLoaded = ref(false)

  const sortedNodes = computed<CanvasNode[]>(() =>
    [...graph.nodes.values()].sort((a, b) => a.startedTs - b.startedTs),
  )
  const selectedNode = computed<CanvasNode | null>(() =>
    selectedNodeId.value ? (graph.nodes.get(selectedNodeId.value) ?? null) : null,
  )
  const roots = computed<CanvasNode[]>(() => rootNodes(graph.nodes))

  function handleGraphEvent(data: unknown): void {
    const frame = parseGraphFrame(data)
    if (frame) applyEvent(graph, frame)
  }

  /**
   * Apply a seq-ordered backfill batch. Events already seen live (or from a
   * previous backfill) are dropped by the reducer's seq guard; the live path
   * and backfill therefore interleave safely.
   */
  function applyBackfill(frames: GraphEventFrame[]): void {
    for (const frame of frames) applyEvent(graph, frame)
  }

  /** Fetch the session's retained canvas events (best-effort, may be empty). */
  async function loadBackfill(sid: string): Promise<void> {
    try {
      const data = await apiFetch<{ events?: unknown[] }>(`/sessions/${sid}/graph`)
      const frames: GraphEventFrame[] = []
      for (const raw of data.events ?? []) {
        const frame = parseGraphFrame(raw)
        if (frame) frames.push(frame)
      }
      applyBackfill(frames)
    } catch {
      // No retained history (fresh session / server restart): empty canvas.
    } finally {
      backfillLoaded.value = true
    }
  }

  /** Switch sessions: drop local graph state, then rebuild from backfill. */
  function reset(sid: string | null): void {
    graph.nodes.clear()
    graph.lastSeq = 0
    selectedNodeId.value = null
    backfillLoaded.value = false
    sessionId.value = sid
    if (sid) void loadBackfill(sid)
  }

  function select(id: string | null): void {
    selectedNodeId.value = id
  }

  return {
    graph,
    selectedNodeId,
    selectedNode,
    panelOpen,
    sessionId,
    backfillLoaded,
    sortedNodes,
    roots,
    handleGraphEvent,
    applyBackfill,
    loadBackfill,
    reset,
    select,
  }
})
