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

  // Live frames arriving while the session's backfill is still in flight.
  // Applied only after the backfill (merged in seq order) so a live frame
  // cannot advance lastSeq past still-undelivered retained frames.
  let pendingLive: GraphEventFrame[] = []
  // Bumped on every reset; a backfill response for a superseded session is
  // discarded instead of polluting the new session's graph.
  let resetGeneration = 0

  const sortedNodes = computed<CanvasNode[]>(() =>
    [...graph.nodes.values()].sort((a, b) => a.startedTs - b.startedTs),
  )
  const selectedNode = computed<CanvasNode | null>(() =>
    selectedNodeId.value ? (graph.nodes.get(selectedNodeId.value) ?? null) : null,
  )
  const roots = computed<CanvasNode[]>(() => rootNodes(graph.nodes))

  function handleGraphEvent(data: unknown): void {
    const frame = parseGraphFrame(data)
    if (!frame) return
    if (sessionId.value !== null && !backfillLoaded.value) {
      pendingLive.push(frame)
      return
    }
    applyEvent(graph, frame)
  }

  /** Apply buffered live frames in seq order (the seq guard drops duplicates). */
  function flushPendingLive(): void {
    if (pendingLive.length === 0) return
    const frames = pendingLive.slice().sort((a, b) => a.seq - b.seq)
    pendingLive = []
    for (const frame of frames) applyEvent(graph, frame)
  }

  /**
   * Apply a seq-ordered backfill batch. Events already seen live (or from a
   * previous backfill) are dropped by the reducer's seq guard; live frames
   * buffered during the fetch are merged afterwards.
   */
  function applyBackfill(frames: GraphEventFrame[]): void {
    for (const frame of frames) applyEvent(graph, frame)
  }

  /** Fetch the session's retained canvas events (best-effort, may be empty). */
  async function loadBackfill(sid: string): Promise<void> {
    const gen = resetGeneration
    try {
      const data = await apiFetch<{ events?: unknown[] }>(`/sessions/${sid}/graph`)
      if (gen !== resetGeneration) return // session switched while fetching
      const frames: GraphEventFrame[] = []
      for (const raw of data.events ?? []) {
        const frame = parseGraphFrame(raw)
        if (frame) frames.push(frame)
      }
      applyBackfill(frames)
    } catch {
      // No retained history (fresh session / server restart): empty canvas.
    } finally {
      if (gen === resetGeneration) {
        backfillLoaded.value = true
        flushPendingLive()
      }
    }
  }

  /** Switch sessions: drop local graph state, then rebuild from backfill. */
  function reset(sid: string | null): void {
    resetGeneration++
    graph.nodes.clear()
    graph.lastSeq = 0
    pendingLive = []
    selectedNodeId.value = null
    // Without a session there is no backfill to wait for: live frames apply
    // directly.
    backfillLoaded.value = sid === null
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
