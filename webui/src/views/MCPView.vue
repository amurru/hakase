<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { apiFetch } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  RefreshCw,
  Power,
  PowerOff,
  RotateCcw,
  Server,
  AlertCircle,
  Plug,
  Wrench,
} from '@lucide/vue'

interface MCPServer {
  name: string
  type: string
  transport: string
  disabled: boolean
  toolCount: number
  status: string
  error?: string
}

const servers = ref<MCPServer[]>([])
const loading = ref(false)
const error = ref('')
const actionPending = ref<string | null>(null)

async function loadServers() {
  loading.value = true
  error.value = ''
  try {
    servers.value = await apiFetch<MCPServer[]>('/mcp/servers')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load servers'
  } finally {
    loading.value = false
  }
}

async function enableServer(name: string) {
  actionPending.value = name
  try {
    await apiFetch(`/mcp/servers/${encodeURIComponent(name)}/enable`, { method: 'POST' })
    await loadServers()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to enable server'
  } finally {
    actionPending.value = null
  }
}

async function disableServer(name: string) {
  actionPending.value = name
  try {
    await apiFetch(`/mcp/servers/${encodeURIComponent(name)}/disable`, { method: 'POST' })
    await loadServers()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to disable server'
  } finally {
    actionPending.value = null
  }
}

async function reconnectServer(name: string) {
  actionPending.value = name
  try {
    await apiFetch(`/mcp/servers/${encodeURIComponent(name)}/reconnect`, { method: 'POST' })
    await loadServers()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to reconnect server'
  } finally {
    actionPending.value = null
  }
}

function statusColor(status: string): string {
  switch (status) {
    case 'connected':
      return 'bg-green-500/20 text-green-400 border-green-500/30'
    case 'idle':
      return 'bg-blue-500/20 text-blue-400 border-blue-500/30'
    case 'failed':
      return 'bg-red-500/20 text-red-400 border-red-500/30'
    case 'disabled':
      return 'bg-gray-500/20 text-gray-400 border-gray-500/30'
    default:
      return 'bg-muted text-muted-foreground border-border'
  }
}

function typeColor(type: string): string {
  switch (type) {
    case 'http':
      return 'bg-purple-500/20 text-purple-400 border-purple-500/30'
    case 'stdio':
      return 'bg-amber-500/20 text-amber-400 border-amber-500/30'
    default:
      return 'bg-muted text-muted-foreground border-border'
  }
}

function statusLabel(status: string): string {
  return status || 'unknown'
}

function typeIcon(type: string) {
  return type === 'http' ? Plug : Server
}

onMounted(loadServers)
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <h1 class="text-lg font-semibold">MCP Servers</h1>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="loadServers">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        </Button>
      </div>
    </div>

    <!-- Content -->
    <div class="flex-1 overflow-auto p-4">
      <!-- Loading -->
      <div v-if="loading && servers.length === 0" class="flex h-40 items-center justify-center text-sm text-muted-foreground">
        Loading servers...
      </div>

      <!-- Error -->
      <div v-else-if="error && servers.length === 0" class="flex h-40 flex-col items-center justify-center gap-2">
        <div class="flex items-center gap-2 text-sm text-destructive">
          <AlertCircle class="h-4 w-4 shrink-0" />
          <span>{{ error }}</span>
        </div>
        <Button variant="ghost" size="sm" @click="loadServers">
          Retry
        </Button>
      </div>

      <!-- Empty state -->
      <div v-else-if="servers.length === 0" class="flex h-40 flex-col items-center justify-center text-muted-foreground">
        <Server class="mb-2 h-8 w-8 opacity-50" />
        <p class="text-sm">No MCP servers configured</p>
        <p class="mt-1 text-xs">Add servers in config.json under the "mcp" block</p>
      </div>

      <!-- Server list -->
      <div v-else class="flex flex-col gap-3">
        <div
          v-for="server in servers"
          :key="server.name"
          class="group"
        >
          <Card class="transition-colors hover:border-border/80">
            <CardContent class="p-4">
              <div class="flex items-start justify-between gap-4">
                <!-- Server info -->
                <div class="flex min-w-0 flex-1 flex-col gap-2">
                  <div class="flex items-center gap-2">
                    <!-- Type icon -->
                    <component
                      :is="typeIcon(server.type)"
                      class="h-4 w-4 shrink-0 text-muted-foreground"
                    />
                    <!-- Name -->
                    <span class="truncate text-sm font-medium">{{ server.name }}</span>
                    <!-- Type badge -->
                    <Badge
                      :class="typeColor(server.type)"
                      variant="outline"
                      class="shrink-0 text-[10px] uppercase"
                    >
                      {{ server.type }}
                    </Badge>
                    <!-- Status badge -->
                    <Badge
                      :class="statusColor(server.status)"
                      variant="outline"
                      class="shrink-0 text-[10px] uppercase"
                    >
                      {{ statusLabel(server.status) }}
                    </Badge>
                  </div>

                  <!-- Transport -->
                  <div class="flex items-center gap-2 text-xs text-muted-foreground">
                    <Wrench class="h-3 w-3 shrink-0" />
                    <span class="truncate font-mono">{{ server.transport }}</span>
                  </div>

                  <!-- Tool count and error -->
                  <div class="flex items-center gap-3 text-xs">
                    <span v-if="server.toolCount > 0" class="text-muted-foreground">
                      {{ server.toolCount }} tool{{ server.toolCount !== 1 ? 's' : '' }}
                    </span>
                    <span v-if="server.toolCount === 0 && server.status === 'connected'" class="text-muted-foreground">
                      0 tools
                    </span>
                    <span v-if="server.error" class="text-destructive line-clamp-1">
                      {{ server.error }}
                    </span>
                  </div>
                </div>

                <!-- Actions -->
                <div class="flex shrink-0 items-center gap-1">
                  <Button
                    v-if="!server.disabled"
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2"
                    :disabled="actionPending === server.name"
                    @click="disableServer(server.name)"
                  >
                    <PowerOff class="mr-1 h-3 w-3" />
                    Disable
                  </Button>
                  <Button
                    v-else
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2"
                    :disabled="actionPending === server.name"
                    @click="enableServer(server.name)"
                  >
                    <Power class="mr-1 h-3 w-3" />
                    Enable
                  </Button>
                  <Button
                    v-if="!server.disabled"
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2"
                    :disabled="actionPending === server.name"
                    @click="reconnectServer(server.name)"
                  >
                    <RotateCcw class="mr-1 h-3 w-3" />
                    Reconnect
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>

      <!-- Inline error (shown when servers are loaded but an action failed) -->
      <div
        v-if="error && servers.length > 0"
        class="mt-3 flex items-center gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive"
      >
        <AlertCircle class="h-3.5 w-3.5 shrink-0" />
        <span>{{ error }}</span>
        <Button
          variant="ghost"
          size="sm"
          class="ml-auto h-6 px-2"
          @click="error = ''"
        >
          Dismiss
        </Button>
      </div>
    </div>
  </div>
</template>
