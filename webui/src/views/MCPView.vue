<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { apiFetch } from '@/lib/api'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from '@/components/ui/dialog'
import {
  RefreshCw,
  Power,
  PowerOff,
  RotateCcw,
  Server,
  AlertCircle,
  Plug,
  Wrench,
  Plus,
  Pencil,
  Trash2,
  Loader2,
} from '@lucide/vue'

interface MCPServerConfig {
  type?: string
  url?: string
  command?: string[]
  env?: Record<string, string>
  headers?: Record<string, string>
  disabled?: boolean
  timeout_ms?: number
}

interface MCPServer {
  name: string
  type: string
  transport: string
  disabled: boolean
  toolCount: number
  status: string
  error?: string
  config?: MCPServerConfig
}

// Editable server form
interface ServerForm {
  name: string
  type: string
  url: string
  command: string
  env: string
  headers: string
  disabled: boolean
}

const servers = ref<MCPServer[]>([])
const loading = ref(false)
const error = ref('')
const actionPending = ref<string | null>(null)
const savingServer = ref(false)

// Add/Edit dialog state
const dialogOpen = ref(false)
const editingName = ref('') // empty = add mode
const form = ref<ServerForm>({
  name: '',
  type: 'http',
  url: '',
  command: '',
  env: '',
  headers: '',
  disabled: false,
})
const formError = ref('')

const dialogTitle = computed(() => (editingName.value ? `Edit server ${editingName.value}` : 'Add MCP server'))

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

// -- add/edit dialog ------------------------------------------------------

function openAdd() {
  editingName.value = ''
  form.value = { name: '', type: 'http', url: '', command: '', env: '', headers: '', disabled: false }
  formError.value = ''
  dialogOpen.value = true
}

function openEdit(server: MCPServer) {
  editingName.value = server.name
  const c = server.config || {}
  form.value = {
    name: server.name,
    type: c.type || server.type || 'http',
    url: c.url || '',
    command: (c.command || []).join(' '),
    env: mapToLines(c.env),
    headers: mapToLines(c.headers),
    disabled: c.disabled || server.disabled,
  }
  formError.value = ''
  dialogOpen.value = true
}

function mapToLines(m?: Record<string, string>): string {
  if (!m) return ''
  return Object.entries(m)
    .map(([k, v]) => `${k}=${v}`)
    .join('\n')
}

function linesToMap(s: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of s.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed) continue
    const idx = trimmed.indexOf('=')
    if (idx <= 0) {
      throw new Error(`invalid key=value line: "${trimmed}"`)
    }
    out[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim()
  }
  return Object.keys(out).length > 0 ? out : undefined
}

async function saveServer() {
  formError.value = ''
  const name = form.value.name.trim()
  if (!name) {
    formError.value = 'Server name is required'
    return
  }
  if (!/^[a-zA-Z0-9_-]+$/.test(name)) {
    formError.value = 'Server name may only contain letters, numbers, _ and -'
    return
  }

  let env: Record<string, string> | undefined
  let headers: Record<string, string> | undefined
  try {
    env = linesToMap(form.value.env)
    headers = linesToMap(form.value.headers)
  } catch (e: unknown) {
    formError.value = e instanceof Error ? e.message : 'Invalid env/headers format'
    return
  }

  const body = {
    type: form.value.type,
    url: form.value.url.trim(),
    command: form.value.command.trim() ? form.value.command.trim().split(/\s+/) : undefined,
    env,
    headers,
    disabled: form.value.disabled,
  }

  savingServer.value = true
  try {
    if (editingName.value) {
      await apiFetch(`/mcp/servers/${encodeURIComponent(editingName.value)}`, { method: 'PUT', body })
      toast.success(`Server ${editingName.value} updated`)
    } else {
      await apiFetch('/mcp/servers', { method: 'POST', body: { ...body, name } })
      toast.success(`Server ${name} added`)
    }
    dialogOpen.value = false
    await loadServers()
  } catch (e: unknown) {
    formError.value = e instanceof Error ? e.message : 'Failed to save server'
    toast.error(formError.value)
  } finally {
    savingServer.value = false
  }
}

async function removeServer(server: MCPServer) {
  if (!confirm(`Remove MCP server "${server.name}"?`)) return
  actionPending.value = server.name
  try {
    await apiFetch(`/mcp/servers/${encodeURIComponent(server.name)}`, { method: 'DELETE' })
    toast.success(`Server ${server.name} removed`)
    await loadServers()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to remove server'
    toast.error(error.value)
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
        <Button size="sm" @click="openAdd">
          <Plus class="mr-1 h-4 w-4" />
          Add Server
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
        <p class="mt-1 text-xs">Add a server with the button above, or configure the "mcp" block in config.json</p>
        <Button size="sm" class="mt-3" @click="openAdd">
          <Plus class="mr-1 h-4 w-4" />
          Add Server
        </Button>
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
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2"
                    :disabled="actionPending === server.name"
                    @click="openEdit(server)"
                  >
                    <Pencil class="mr-1 h-3 w-3" />
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2 text-destructive"
                    :disabled="actionPending === server.name"
                    @click="removeServer(server)"
                  >
                    <Trash2 class="mr-1 h-3 w-3" />
                    Remove
                  </Button>
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

    <!-- Add/Edit dialog -->
    <Dialog v-model:open="dialogOpen">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{{ dialogTitle }}</DialogTitle>
        </DialogHeader>

        <div class="grid gap-4 py-2">
          <div v-if="formError" class="flex items-center gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive">
            <AlertCircle class="h-3.5 w-3.5 shrink-0" />
            <span>{{ formError }}</span>
          </div>

          <div class="grid gap-2">
            <Label for="srv-name">Name</Label>
            <Input
              id="srv-name"
              v-model="form.name"
              :disabled="!!editingName"
              placeholder="e.g. lightpanda"
            />
            <p v-if="editingName" class="text-xs text-muted-foreground">Name cannot be changed after creation.</p>
          </div>

          <div class="grid gap-2">
            <Label for="srv-type">Type</Label>
            <select
              id="srv-type"
              v-model="form.type"
              class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
            >
              <option value="http">http (streamable endpoint)</option>
              <option value="stdio">stdio (local process)</option>
            </select>
          </div>

          <div v-if="form.type === 'http'" class="grid gap-2">
            <Label for="srv-url">URL</Label>
            <Input id="srv-url" v-model="form.url" placeholder="http://localhost:9223/mcp" />
          </div>

          <div v-else class="grid gap-2">
            <Label for="srv-command">Command</Label>
            <Input id="srv-command" v-model="form.command" placeholder="npx -y @github/mcp-server" />
          </div>

          <div class="grid gap-2">
            <Label for="srv-env">Environment (key=value per line)</Label>
            <textarea
              id="srv-env"
              v-model="form.env"
              rows="2"
              class="w-full resize-none rounded-md border border-input bg-background px-3 py-2 font-mono text-xs ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              placeholder="GITHUB_PAT=${GITHUB_PAT}"
            />
          </div>

          <div class="grid gap-2">
            <Label for="srv-headers">HTTP headers (key=value per line)</Label>
            <textarea
              id="srv-headers"
              v-model="form.headers"
              rows="2"
              class="w-full resize-none rounded-md border border-input bg-background px-3 py-2 font-mono text-xs ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
              placeholder="Authorization=Bearer ${MCP_TOKEN}"
            />
          </div>

          <label class="flex items-center gap-2 text-sm">
            <input v-model="form.disabled" type="checkbox" class="h-4 w-4 rounded border-input" />
            Disabled
          </label>
        </div>

        <DialogFooter class="flex-row justify-end gap-2">
          <Button variant="ghost" size="sm" @click="dialogOpen = false">Cancel</Button>
          <Button size="sm" :disabled="savingServer" @click="saveServer">
            <Loader2 v-if="savingServer" class="mr-1 h-4 w-4 animate-spin" />
            {{ editingName ? 'Save' : 'Add' }}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
