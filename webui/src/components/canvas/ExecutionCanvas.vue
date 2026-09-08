<script setup lang="ts">
import { computed, markRaw, nextTick, watch } from 'vue'
import { VueFlow, useVueFlow, type Edge, type Node, type NodeTypesObject } from '@vue-flow/core'
import { Background } from '@vue-flow/background'
import { Controls } from '@vue-flow/controls'
import { MiniMap } from '@vue-flow/minimap'
import { Loader2, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import '@vue-flow/core/dist/style.css'
import '@vue-flow/core/dist/theme-default.css'
import '@vue-flow/controls/dist/style.css'
import '@vue-flow/minimap/dist/style.css'
import { useCanvasStore } from '@/stores/canvas'
import { computeLayout } from '@/lib/canvas'
import AgentNode from './AgentNode.vue'
import ToolNode from './ToolNode.vue'
import NodeDetailPanel from './NodeDetailPanel.vue'

const store = useCanvasStore()
const { fitView } = useVueFlow()

// markRaw: node type components are static; reactivity comes from node.data.
// The cast is the vue-flow idiom: custom nodes declare the props they use
// ({ data, selected }) rather than the full NodeProps surface.
const nodeTypes = markRaw({
  agent: AgentNode,
  tool: ToolNode,
}) as unknown as NodeTypesObject

const flowNodes = computed<Node[]>(() => {
  const layout = computeLayout(store.graph.nodes)
  return store.sortedNodes.map((n) => ({
    id: n.id,
    type: n.kind,
    position: layout.get(n.id) ?? { x: 0, y: 0 },
    data: { node: n },
  }))
})

const flowEdges = computed<Edge[]>(() => {
  const edges: Edge[] = []
  for (const n of store.sortedNodes) {
    const parent = n.visualParentId ?? n.parentId
    if (parent && store.graph.nodes.has(parent)) {
      edges.push({
        id: `e:${parent}->${n.id}`,
        source: parent,
        target: n.id,
        type: 'smoothstep',
        animated: n.status === 'running',
      })
    }
  }
  return edges
})

function onNodeClick({ node }: { node: Node }) {
  store.select(String(node.id))
}

// Recenter when a new run root appears (not on every node - constant zoom
// churn would fight the user's own panning).
watch(
  () => store.roots.length,
  async (count, old) => {
    if (old !== undefined && count > old) {
      await nextTick()
      void fitView({ padding: 0.15, duration: 200 })
    }
  },
)
</script>

<template>
  <div class="relative flex h-full min-w-0 flex-col bg-background">
    <div class="flex shrink-0 items-center gap-2 border-b px-3 py-1.5">
      <span class="text-xs font-medium text-muted-foreground">Execution Canvas</span>
      <span v-if="store.sortedNodes.length === 0" class="flex items-center gap-1.5 text-xs text-muted-foreground/70">
        <Loader2 v-if="!store.backfillLoaded" class="h-3 w-3 animate-spin" />
        <span v-else>waiting for agent activity…</span>
      </span>
      <span v-else class="text-xs text-muted-foreground/70">
        {{ store.sortedNodes.length }} nodes · {{ store.roots.length }} run{{ store.roots.length === 1 ? '' : 's' }}
      </span>
      <Button
        variant="ghost"
        size="icon-xs"
        class="ml-auto"
        aria-label="Close canvas"
        @click="store.panelOpen = false"
      >
        <X class="h-3.5 w-3.5" />
      </Button>
    </div>

    <div class="min-h-0 flex-1">
      <VueFlow
        :nodes="flowNodes"
        :edges="flowEdges"
        :node-types="nodeTypes"
        :min-zoom="0.1"
        :max-zoom="2"
        :nodes-connectable="false"
        fit-view-on-init
        @node-click="onNodeClick"
      >
        <Background :gap="20" />
        <Controls position="bottom-left" />
        <MiniMap position="bottom-right" pannable zoomable />
      </VueFlow>
    </div>

    <!-- Floating node inspector -->
    <div
      v-if="store.selectedNode"
      class="absolute bottom-3 right-3 top-14 z-10 w-96 max-w-[85%]"
    >
      <NodeDetailPanel />
    </div>
  </div>
</template>

<style>
/* Canvas chrome uses theme tokens: vue-flow's default stylesheet is
   light-oriented, so recolor the background dots and minimap per theme.
   Tokens are full oklch colors; alpha comes from color-mix. */
.vue-flow__background {
  color: color-mix(in oklab, var(--muted-foreground) 22%, transparent);
}

.vue-flow__minimap {
  background-color: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
}

.vue-flow__minimap-mask {
  fill: color-mix(in oklab, var(--foreground) 6%, transparent);
}

.vue-flow__controls {
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  box-shadow: none;
}

.vue-flow__controls-button {
  background: var(--card);
  border-bottom: 1px solid var(--border);
}

.vue-flow__controls-button svg {
  fill: var(--foreground);
}

.vue-flow__handle {
  width: 7px;
  height: 7px;
  background: color-mix(in oklab, var(--muted-foreground) 70%, transparent);
  border: none;
}

.vue-flow__edge-path {
  stroke: color-mix(in oklab, var(--muted-foreground) 50%, transparent);
}

.vue-flow__edge.animated .vue-flow__edge-path {
  stroke: var(--primary);
}
</style>
