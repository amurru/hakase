// Execution-canvas domain model: the client side of the backend's structured
// "graph" SSE events (internal/interfaces/graph.go). This module is pure and
// framework-free so the reducer, grouping heuristics, and layout stay
// unit-testable; the Pinia store and Vue components wrap it.

export type GraphEventType =
  | 'agent_start'
  | 'agent_end'
  | 'tool_start'
  | 'tool_end'
  | 'agent_text'
  | 'agent_thought'
  | 'transfer'

export type NodeKind = 'agent' | 'tool'
export type NodeStatus = 'running' | 'completed' | 'failed' | 'timed_out'

/** One "graph" SSE frame: the backend GraphEvent plus seq/ts assigned by the bridge. */
export interface GraphEventFrame {
  seq: number
  ts: number
  type: GraphEventType
  node_id: string
  parent_id?: string
  agent?: string
  goal?: string
  status?: string
  summary?: string
  error?: string
  call_id?: string
  tool?: string
  args?: unknown
  result?: string
  ok?: boolean
  duration_ms?: number
  text?: string
  target?: string
}

/** One rendered graph node: an agent (root or delegation) or a tool call. */
export interface CanvasNode {
  id: string
  /** Data parent: the agent node that owns/created this node. */
  parentId: string | null
  /**
   * Render parent: usually the data parent, but a delegation spawned by an
   * open delegate_task call renders under that tool node (grouping heuristic
   * in resolveVisualParent). Edges are built from this.
   */
  visualParentId: string | null
  kind: NodeKind
  label: string
  agent?: string
  status: NodeStatus
  startedTs: number
  durationMs?: number
  goal?: string
  summary?: string
  error?: string
  tool?: string
  callId?: string
  args?: unknown
  result?: string
  /** Accumulated agent_text deltas. */
  text: string
  /** Accumulated agent_thought deltas. */
  thought: string
}

export interface CanvasGraphState {
  nodes: Map<string, CanvasNode>
  lastSeq: number
}

/** Server caps each text delta (4KB); the client caps the accumulated total. */
export const TEXT_ACCUM_CAP = 128 * 1024

/** Parse/coerce one SSE payload into a frame; null for anything malformed. */
export function parseGraphFrame(data: unknown): GraphEventFrame | null {
  if (typeof data !== 'object' || data === null) return null
  const raw = data as Record<string, unknown>
  const validTypes: readonly string[] = [
    'agent_start',
    'agent_end',
    'tool_start',
    'tool_end',
    'agent_text',
    'agent_thought',
    'transfer',
  ]
  if (typeof raw.type !== 'string' || !validTypes.includes(raw.type)) return null
  if (typeof raw.node_id !== 'string' || raw.node_id === '') return null
  return {
    seq: typeof raw.seq === 'number' ? raw.seq : 0,
    ts: typeof raw.ts === 'number' ? raw.ts : Date.now(),
    type: raw.type as GraphEventType,
    node_id: raw.node_id,
    parent_id: typeof raw.parent_id === 'string' ? raw.parent_id : undefined,
    agent: typeof raw.agent === 'string' ? raw.agent : undefined,
    goal: typeof raw.goal === 'string' ? raw.goal : undefined,
    status: typeof raw.status === 'string' ? raw.status : undefined,
    summary: typeof raw.summary === 'string' ? raw.summary : undefined,
    error: typeof raw.error === 'string' ? raw.error : undefined,
    call_id: typeof raw.call_id === 'string' ? raw.call_id : undefined,
    tool: typeof raw.tool === 'string' ? raw.tool : undefined,
    args: raw.args,
    result: typeof raw.result === 'string' ? raw.result : undefined,
    ok: typeof raw.ok === 'boolean' ? raw.ok : undefined,
    duration_ms: typeof raw.duration_ms === 'number' ? raw.duration_ms : undefined,
    text: typeof raw.text === 'string' ? raw.text : undefined,
    target: typeof raw.target === 'string' ? raw.target : undefined,
  }
}

export function createGraphState(): CanvasGraphState {
  return { nodes: new Map(), lastSeq: 0 }
}

/** Deterministic tool-node id: tool calls are namespaced under their agent node. */
export function toolNodeId(agentNodeId: string, callId: string): string {
  return `tool:${agentNodeId}:${callId}`
}

function appendCapped(current: string, delta: string | undefined): string {
  if (!delta) return current
  const merged = current + delta
  if (merged.length <= TEXT_ACCUM_CAP) return merged
  return merged.slice(0, TEXT_ACCUM_CAP) + '\n…[truncated]'
}

/**
 * A delegation child renders under the open delegate_task tool call of its
 * parent agent (when one exists) instead of dangling off the agent node:
 * delegate_task is synchronous, so an agent_start arriving while a
 * delegate_task call is still open belongs to it.
 */
function resolveVisualParent(state: CanvasGraphState, node: CanvasNode): void {
  node.visualParentId = node.parentId
  if (!node.parentId) return
  let open: CanvasNode | null = null
  for (const other of state.nodes.values()) {
    if (
      other.kind === 'tool' &&
      other.parentId === node.parentId &&
      other.tool === 'delegate_task' &&
      other.status === 'running'
    ) {
      open = other // keep last match: most recent open call
    }
  }
  if (open) node.visualParentId = open.id
}

/**
 * Apply one frame to the graph state. Out-of-order/duplicate frames are
 * dropped by seq; unknown node references are tolerated (the ring buffer may
 * have evicted their creation events).
 */
export function applyEvent(state: CanvasGraphState, frame: GraphEventFrame): void {
  if (Number.isFinite(frame.seq) && frame.seq > 0) {
    if (frame.seq <= state.lastSeq) return
    state.lastSeq = frame.seq
  }

  switch (frame.type) {
    case 'agent_start': {
      let node = state.nodes.get(frame.node_id)
      if (!node) {
        node = {
          id: frame.node_id,
          parentId: frame.parent_id || null,
          visualParentId: null,
          kind: 'agent',
          label: frame.agent || frame.node_id,
          agent: frame.agent,
          status: 'running',
          startedTs: frame.ts,
          goal: frame.goal,
          text: '',
          thought: '',
        }
        state.nodes.set(frame.node_id, node)
      } else {
        node.status = 'running'
        node.goal = frame.goal ?? node.goal
      }
      resolveVisualParent(state, node)
      return
    }
    case 'agent_end': {
      let node = state.nodes.get(frame.node_id)
      if (!node) {
        // The retained ring may evict the start but keep the end; synthesize
        // so the terminal state (and its summary) still shows.
        node = {
          id: frame.node_id,
          parentId: frame.parent_id || null,
          visualParentId: null,
          kind: 'agent',
          label: frame.agent || frame.node_id,
          agent: frame.agent,
          status: 'running',
          startedTs: frame.ts,
          text: '',
          thought: '',
        }
        state.nodes.set(frame.node_id, node)
        resolveVisualParent(state, node)
      }
      node.status = isNodeStatus(frame.status) ? frame.status : 'completed'
      node.summary = frame.summary ?? node.summary
      node.error = frame.error
      if (typeof frame.duration_ms === 'number') node.durationMs = frame.duration_ms
      return
    }
    case 'tool_start': {
      const id = toolNodeId(frame.node_id, frame.call_id ?? '?')
      let node = state.nodes.get(id)
      if (!node) {
        node = {
          id,
          parentId: frame.node_id,
          visualParentId: null,
          kind: 'tool',
          label: frame.tool || frame.call_id || 'tool',
          status: 'running',
          startedTs: frame.ts,
          tool: frame.tool,
          callId: frame.call_id,
          args: frame.args,
          text: '',
          thought: '',
        }
        state.nodes.set(id, node)
      } else {
        node.status = 'running'
        node.args = frame.args ?? node.args
      }
      return
    }
    case 'tool_end': {
      const id = toolNodeId(frame.node_id, frame.call_id ?? '?')
      let node = state.nodes.get(id)
      if (!node) {
        // Result without an observed start (evicted from backfill): synthesize.
        node = {
          id,
          parentId: frame.node_id,
          visualParentId: null,
          kind: 'tool',
          label: frame.tool || frame.call_id || 'tool',
          status: 'running',
          startedTs: frame.ts,
          tool: frame.tool,
          callId: frame.call_id,
          text: '',
          thought: '',
        }
        state.nodes.set(id, node)
      }
      node.status = frame.ok === false ? 'failed' : 'completed'
      node.result = frame.result
      node.error = frame.error
      if (typeof frame.duration_ms === 'number') node.durationMs = frame.duration_ms
      return
    }
    case 'agent_text': {
      const node = state.nodes.get(frame.node_id)
      if (node) node.text = appendCapped(node.text, frame.text)
      return
    }
    case 'agent_thought': {
      const node = state.nodes.get(frame.node_id)
      if (node) node.thought = appendCapped(node.thought, frame.text)
      return
    }
    case 'transfer': {
      // Informational only: the backend announces each transfer with an
      // agent_start for a uniquely-id'd node right after this frame, and
      // synthesizing a node here would duplicate it.
      return
    }
  }
}

function isNodeStatus(v: string | undefined): v is NodeStatus {
  return v === 'running' || v === 'completed' || v === 'failed' || v === 'timed_out'
}

// ---------------------------------------------------------------------------
// Layout: deterministic tidy tree over the visual-parent forest. Node counts
// are small (tens), so a two-pass post-order walk beats pulling in dagre.
// ---------------------------------------------------------------------------

export interface LayoutPosition {
  x: number
  y: number
}

export const NODE_SIZE: Record<NodeKind, { width: number; height: number }> = {
  agent: { width: 208, height: 76 },
  tool: { width: 176, height: 46 },
}

const SIBLING_GAP = 24
const ROOT_GAP = 48
const LEVEL_HEIGHT = 120

/**
 * Compute positions for every node. Roots (nodes whose visual/data parent is
 * missing from the map — including multiple sequential runs) lay out left to
 * right; children center under their subtree.
 */
export function computeLayout(nodes: Map<string, CanvasNode>): Map<string, LayoutPosition> {
  const positions = new Map<string, LayoutPosition>()
  if (nodes.size === 0) return positions

  const childrenOf = new Map<string, CanvasNode[]>()
  const roots: CanvasNode[] = []
  for (const node of nodes.values()) {
    const parentId = node.visualParentId ?? node.parentId
    if (parentId && nodes.has(parentId)) {
      const list = childrenOf.get(parentId)
      if (list) list.push(node)
      else childrenOf.set(parentId, [node])
    } else {
      roots.push(node)
    }
  }
  const byStart = (a: CanvasNode, b: CanvasNode) => a.startedTs - b.startedTs
  for (const list of childrenOf.values()) list.sort(byStart)
  roots.sort(byStart)

  // subtreeWidth(id) = max(node width, sum of child subtree widths + gaps).
  const subtreeWidth = new Map<string, number>()
  function measure(node: CanvasNode): number {
    const kids = childrenOf.get(node.id) ?? []
    let total = 0
    for (const kid of kids) total += measure(kid) + SIBLING_GAP
    total = kids.length > 0 ? total - SIBLING_GAP : 0
    const width = Math.max(NODE_SIZE[node.kind].width, total)
    subtreeWidth.set(node.id, width)
    return width
  }

  let cursor = 0
  function place(node: CanvasNode, depth: number, left: number): void {
    const width = subtreeWidth.get(node.id) ?? NODE_SIZE[node.kind].width
    const size = NODE_SIZE[node.kind].width
    positions.set(node.id, {
      x: left + (width - size) / 2,
      y: depth * LEVEL_HEIGHT,
    })
    const kids = childrenOf.get(node.id) ?? []
    let kidLeft = left
    for (const kid of kids) {
      const kidWidth = subtreeWidth.get(kid.id) ?? NODE_SIZE[kid.kind].width
      place(kid, depth + 1, kidLeft)
      kidLeft += kidWidth + SIBLING_GAP
    }
  }

  for (const root of roots) {
    const width = measure(root)
    place(root, 0, cursor)
    cursor += width + ROOT_GAP
  }
  return positions
}

/** Root agent nodes (data parent null/missing), in start order. */
export function rootNodes(nodes: Map<string, CanvasNode>): CanvasNode[] {
  const roots: CanvasNode[] = []
  for (const node of nodes.values()) {
    const parentId = node.visualParentId ?? node.parentId
    if (!parentId || !nodes.has(parentId)) roots.push(node)
  }
  return roots.sort((a, b) => a.startedTs - b.startedTs)
}

/** "12.3s" / "850ms" formatting for node chips and detail panels. */
export function formatDuration(ms: number | undefined): string {
  if (ms === undefined || ms < 0) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  return `${(ms / 1000).toFixed(1)}s`
}
