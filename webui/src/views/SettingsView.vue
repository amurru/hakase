<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { apiFetch } from '@/lib/api'
import { toast } from 'vue-sonner'
import { useAppStore } from '@/stores/app'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { AlertCircle, CheckCircle2, KeyRound, Save, RefreshCw, Loader2, Info } from '@lucide/vue'

interface ConfigResponse {
  path: string
  writable: boolean
  has_api_key: boolean
  has_vision_api_key: boolean
  config: Record<string, unknown>
}

interface SettingsForm {
  provider: string
  model_name: string
  base_url: string
  summary_model: string
  fallback_providers: string
  thinking_level: string
  chat_buffer_size: number
  task_checkpoint: boolean
  search_expansion: boolean

  instruction: string
  instruction_files: string
  knowledge_dir: string
  skill_dirs: string

  vision_model: string
  vision_provider: string
  vision_base_url: string
  model_vision: string

  context_files_max_chars: number
  context_files_apply_to: string
  system_env_enabled: boolean
  system_env_max_chars: number
  system_env_apply_to: string

  units_system: string

  sandbox_mode: string
  sandbox_workspace_roots: string
  sandbox_read_roots: string
  sandbox_deny_roots: string
  sandbox_allow_network: boolean
  sandbox_allow_pip_install: boolean
  sandbox_allowed_commands: string

  loop_guard_max_output_tokens: number
  loop_guard_repetition_limit: number
  loop_guard_max_text_without_tool: number

  approval_mode: string
  approval_expiry_seconds: number
  clarify_expiry_seconds: number
}

const cfgPath = ref('')
const appStore = useAppStore()
const writable = ref(true)
const hasAPIKey = ref(false)
const hasVisionAPIKey = ref(false)
const loading = ref(false)
const saving = ref(false)
const error = ref('')

// Write-only API key state: the stored value is never shown. The user can
// only provide a brand-new key (replace) or remove it.
const newAPIKey = ref('')
const newVisionAPIKey = ref('')
const showAPIKeyInput = ref(false)
const showVisionAPIKeyInput = ref(false)

const original = ref<ConfigResponse['config']>({})
const form = ref<SettingsForm>({
  provider: '',
  model_name: '',
  base_url: '',
  summary_model: '',
  fallback_providers: '',
  thinking_level: '',
  chat_buffer_size: 1000,
  task_checkpoint: false,
  search_expansion: false,
  instruction: '',
  instruction_files: '',
  knowledge_dir: '',
  skill_dirs: '',
  vision_model: '',
  vision_provider: '',
  vision_base_url: '',
  model_vision: 'auto',
  context_files_max_chars: 20000,
  context_files_apply_to: '',
    system_env_enabled: true,
    system_env_max_chars: 800,
    system_env_apply_to: '',

    units_system: 'metric',

  sandbox_mode: 'paths',
  sandbox_workspace_roots: '.',
  sandbox_read_roots: '',
  sandbox_deny_roots: '',
  sandbox_allow_network: false,
  sandbox_allow_pip_install: true,
  sandbox_allowed_commands: '',
  loop_guard_max_output_tokens: 8192,
  loop_guard_repetition_limit: 8,
  loop_guard_max_text_without_tool: 20000,
  approval_mode: 'interactive',
  approval_expiry_seconds: 60,
  clarify_expiry_seconds: 120,
})

// -- helpers -------------------------------------------------------------

function asStr(v: unknown): string {
  return typeof v === 'string' ? v : ''
}
function asNum(v: unknown): number {
  return typeof v === 'number' ? v : 0
}
function asBool(v: unknown, dflt = false): boolean {
  return typeof v === 'boolean' ? v : dflt
}
function asList(v: unknown): string {
  if (Array.isArray(v)) return v.join(', ')
  return ''
}
function asObj(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === 'object' ? (v as Record<string, unknown>) : undefined
}

function fillForm(cfg: ConfigResponse['config']) {
  original.value = cfg
  const sb = asObj(cfg.sandbox)
  const lg = asObj(cfg.loop_guard)
  const ap = asObj(cfg.approval)
  const cl = asObj(cfg.clarify)
  const cf = asObj(cfg.context_files)
  const se = asObj(cfg.system_env)
  const un = asObj(cfg.units)

  form.value = {
    provider: asStr(cfg.provider),
    model_name: asStr(cfg.model_name),
    base_url: asStr(cfg.base_url),
    summary_model: asStr(cfg.summary_model),
    fallback_providers: asList(cfg.fallback_providers),
    thinking_level: asStr(cfg.thinking_level),
    chat_buffer_size: asNum(cfg.chat_buffer_size) || 1000,
    task_checkpoint: asBool(cfg.task_checkpoint),
    search_expansion: asBool(cfg.search_expansion),

    instruction: asStr(cfg.instruction),
    instruction_files: asList(cfg.instruction_files),
    knowledge_dir: asStr(cfg.knowledge_dir),
    skill_dirs: asList(cfg.skill_dirs),

    vision_model: asStr(cfg.vision_model),
    vision_provider: asStr(cfg.vision_provider),
    vision_base_url: asStr(cfg.vision_base_url),
    model_vision: asStr(cfg.model_vision) || 'auto',

    context_files_max_chars: asNum(cf?.max_chars) || 20000,
    context_files_apply_to: asList(cf?.apply_to),
    system_env_enabled: asBool(se?.enabled, true),
    system_env_max_chars: asNum(se?.max_chars) || 800,
    system_env_apply_to: asList(se?.apply_to),

    units_system: asStr(un?.system) || 'metric',

    sandbox_mode: asStr(sb?.mode) || 'paths',
    sandbox_workspace_roots: asList(sb?.workspace_roots),
    sandbox_read_roots: asList(sb?.read_roots),
    sandbox_deny_roots: asList(sb?.deny_roots),
    sandbox_allow_network: asBool(sb?.allow_network),
    sandbox_allow_pip_install: asBool(sb?.allow_pip_install, true),
    sandbox_allowed_commands: asList(sb?.allowed_commands),

    loop_guard_max_output_tokens: asNum(lg?.max_output_tokens) || 8192,
    loop_guard_repetition_limit: asNum(lg?.repetition_limit) || 8,
    loop_guard_max_text_without_tool: asNum(lg?.max_text_without_tool) || 20000,

    approval_mode: asStr(ap?.mode) || 'interactive',
    approval_expiry_seconds: asNum(ap?.expiry_seconds) || 60,
    clarify_expiry_seconds: asNum(cl?.expiry_seconds) || 120,
  }
}

function toList(s: string): string[] {
  return s
    .split(',')
    .map((x) => x.trim())
    .filter(Boolean)
}

// -- load / save ---------------------------------------------------------

async function loadConfig() {
  loading.value = true
  error.value = ''
  try {
    const resp = await apiFetch<ConfigResponse>('/config')
    cfgPath.value = resp.path
    writable.value = resp.writable
    hasAPIKey.value = resp.has_api_key
    hasVisionAPIKey.value = resp.has_vision_api_key
    fillForm(resp.config)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load config'
  } finally {
    loading.value = false
  }
}

// Build a partial payload containing only the keys that changed vs. the
// loaded config. Nested blocks (sandbox, loop_guard, ...) are included only
// when a sub-field changed, and only with the changed sub-fields.
function buildPayload(): Record<string, unknown> {
  const o = original.value
  const p: Record<string, unknown> = {}

  const scalars: Array<[keyof SettingsForm, string]> = [
    ['provider', 'provider'],
    ['model_name', 'model_name'],
    ['base_url', 'base_url'],
    ['summary_model', 'summary_model'],
    ['thinking_level', 'thinking_level'],
    ['instruction', 'instruction'],
    ['knowledge_dir', 'knowledge_dir'],
    ['vision_model', 'vision_model'],
    ['vision_provider', 'vision_provider'],
    ['vision_base_url', 'vision_base_url'],
    ['model_vision', 'model_vision'],
  ]
  for (const [f, k] of scalars) {
    if (form.value[f] !== asStr(o[k])) p[k] = form.value[f]
  }

  const bools: Array<[keyof SettingsForm, string]> = [
    ['task_checkpoint', 'task_checkpoint'],
    ['search_expansion', 'search_expansion'],
  ]
  for (const [f, k] of bools) {
    if (form.value[f] !== asBool(o[k])) p[k] = form.value[f]
  }

  const nums: Array<[keyof SettingsForm, string]> = [
    ['chat_buffer_size', 'chat_buffer_size'],
  ]
  for (const [f, k] of nums) {
    if (form.value[f] !== asNum(o[k])) p[k] = form.value[f]
  }

  // Array fields (comma-string in the form).
  const listFields: Array<[keyof SettingsForm, string]> = [
    ['fallback_providers', 'fallback_providers'],
    ['instruction_files', 'instruction_files'],
    ['skill_dirs', 'skill_dirs'],
  ]
  for (const [f, k] of listFields) {
    if (form.value[f] !== asList(o[k])) p[k] = toList(form.value[f] as string)
  }

  // context_files block
  const cf = asObj(o.context_files) || {}
  const cfPatch: Record<string, unknown> = {}
  if (form.value.context_files_max_chars !== (asNum(cf.max_chars) || 20000)) {
    cfPatch.max_chars = form.value.context_files_max_chars
  }
  if (form.value.context_files_apply_to !== asList(cf.apply_to)) {
    cfPatch.apply_to = toList(form.value.context_files_apply_to)
  }
  if (Object.keys(cfPatch).length > 0) p.context_files = cfPatch

  // system_env block
  const se = asObj(o.system_env) || {}
  const sePatch: Record<string, unknown> = {}
  const seEnabledDefault = se.enabled === undefined ? true : asBool(se.enabled)
  if (form.value.system_env_enabled !== seEnabledDefault) {
    sePatch.enabled = form.value.system_env_enabled
  }
  if (form.value.system_env_max_chars !== (asNum(se.max_chars) || 800)) {
    sePatch.max_chars = form.value.system_env_max_chars
  }
  if (form.value.system_env_apply_to !== asList(se.apply_to)) {
    sePatch.apply_to = toList(form.value.system_env_apply_to)
  }
  if (Object.keys(sePatch).length > 0) p.system_env = sePatch

  // units block
  const un = asObj(o.units) || {}
  const unitsSystemDefault = un.system === undefined ? 'metric' : asStr(un.system)
  if (form.value.units_system !== unitsSystemDefault) {
    p.units = { system: form.value.units_system }
  }

  // sandbox block
  const sb = asObj(o.sandbox) || {}
  const sbPatch: Record<string, unknown> = {}
  const sandboxKeys: Array<[keyof SettingsForm, string, 'str' | 'bool' | 'list']> = [
    ['sandbox_mode', 'mode', 'str'],
    ['sandbox_allow_network', 'allow_network', 'bool'],
    ['sandbox_allow_pip_install', 'allow_pip_install', 'bool'],
    ['sandbox_workspace_roots', 'workspace_roots', 'list'],
    ['sandbox_read_roots', 'read_roots', 'list'],
    ['sandbox_deny_roots', 'deny_roots', 'list'],
    ['sandbox_allowed_commands', 'allowed_commands', 'list'],
  ]
  for (const [f, k, type] of sandboxKeys) {
    const cur = form.value[f]
    const origVal = sb[k]
    let same = false
    if (type === 'str') same = cur === asStr(origVal)
    else if (type === 'bool') same = cur === asBool(origVal)
    else same = cur === asList(origVal)
    if (!same) {
      sbPatch[k] = type === 'list' ? toList(cur as string) : cur
    }
  }
  if (Object.keys(sbPatch).length > 0) p.sandbox = sbPatch

  // loop_guard block
  const lg = asObj(o.loop_guard) || {}
  const lgPatch: Record<string, unknown> = {}
  if (form.value.loop_guard_max_output_tokens !== (asNum(lg.max_output_tokens) || 8192)) {
    lgPatch.max_output_tokens = form.value.loop_guard_max_output_tokens
  }
  if (form.value.loop_guard_repetition_limit !== (asNum(lg.repetition_limit) || 8)) {
    lgPatch.repetition_limit = form.value.loop_guard_repetition_limit
  }
  if (form.value.loop_guard_max_text_without_tool !== (asNum(lg.max_text_without_tool) || 20000)) {
    lgPatch.max_text_without_tool = form.value.loop_guard_max_text_without_tool
  }
  if (Object.keys(lgPatch).length > 0) p.loop_guard = lgPatch

  // approval block
  const ap = asObj(o.approval) || {}
  const apPatch: Record<string, unknown> = {}
  if (form.value.approval_mode !== (asStr(ap.mode) || 'interactive')) {
    apPatch.mode = form.value.approval_mode
  }
  if (form.value.approval_expiry_seconds !== (asNum(ap.expiry_seconds) || 60)) {
    apPatch.expiry_seconds = form.value.approval_expiry_seconds
  }
  if (Object.keys(apPatch).length > 0) p.approval = apPatch

  // clarify block
  const cl = asObj(o.clarify) || {}
  if (form.value.clarify_expiry_seconds !== (asNum(cl.expiry_seconds) || 120)) {
    p.clarify = { expiry_seconds: form.value.clarify_expiry_seconds }
  }

  // Write-only API key management: only include a key when the user typed a
  // brand-new value or asked to remove it. The stored value is never read.
  if (newAPIKey.value.trim()) {
    p.api_key = newAPIKey.value.trim()
  }
  if (newVisionAPIKey.value.trim()) {
    p.vision_api_key = newVisionAPIKey.value.trim()
  }

  return p
}

async function saveConfig() {
  saving.value = true
  error.value = ''
  try {
    const payload = buildPayload()
    if (Object.keys(payload).length === 0) {
      toast.info('No changes to save')
      return
    }
    await apiFetch('/config', { method: 'PUT', body: payload })
    toast.success('Configuration saved')
    // Reset the write-only key inputs - the new key was persisted, and it is
    // never echoed back by GET.
    newAPIKey.value = ''
    newVisionAPIKey.value = ''
    showAPIKeyInput.value = false
    showVisionAPIKeyInput.value = false
    await loadConfig()
    // Keep the navbar model label in sync with the new configuration.
    appStore.loadModelName()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to save config'
    toast.error(error.value)
  } finally {
    saving.value = false
  }
}

// Remove an API key entirely. Removal is immediate (clear_* flag) - the key
// value is never read or shown.
async function removeAPIKey() {
  saving.value = true
  error.value = ''
  try {
    await apiFetch('/config', {
      method: 'PUT',
      body: { clear_api_key: true },
    })
    toast.success('API key removed')
    await loadConfig()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to remove API key'
    toast.error(error.value)
  } finally {
    saving.value = false
  }
}

async function removeVisionAPIKey() {
  saving.value = true
  error.value = ''
  try {
    await apiFetch('/config', {
      method: 'PUT',
      body: { clear_vision_api_key: true },
    })
    toast.success('Vision API key removed')
    await loadConfig()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to remove vision API key'
    toast.error(error.value)
  } finally {
    saving.value = false
  }
}

onMounted(loadConfig)
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <div class="flex items-center gap-3">
        <h1 class="text-lg font-semibold">Settings</h1>
        <span v-if="cfgPath" class="max-w-96 truncate font-mono text-xs text-muted-foreground">
          {{ cfgPath }}
        </span>
        <Badge
          v-if="!writable"
          variant="outline"
          class="shrink-0 text-[10px] uppercase"
        >
          Config from env (read-only)
        </Badge>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="loadConfig">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        </Button>
        <Button size="sm" :disabled="saving || !writable || loading" @click="saveConfig">
          <Loader2 v-if="saving" class="mr-1 h-4 w-4 animate-spin" />
          <Save v-else class="mr-1 h-4 w-4" />
          Save
        </Button>
      </div>
    </div>

    <!-- Content -->
    <div class="flex-1 overflow-auto p-6">
      <!-- Loading -->
      <div v-if="loading" class="flex h-40 items-center justify-center text-sm text-muted-foreground">
        Loading configuration...
      </div>

      <!-- Error (initial load) -->
      <div v-else-if="error && !cfgPath" class="flex h-40 flex-col items-center justify-center gap-2">
        <div class="flex items-center gap-2 text-sm text-destructive">
          <AlertCircle class="h-4 w-4 shrink-0" />
          <span>{{ error }}</span>
        </div>
        <Button variant="ghost" size="sm" @click="loadConfig">Retry</Button>
      </div>

      <template v-else>
        <!-- API key notice -->
        <div
          v-if="!writable"
          class="mb-6 flex items-start gap-3 rounded-md border border-amber-500/30 bg-amber-500/10 px-4 py-3 text-sm"
        >
          <AlertCircle class="mt-0.5 h-4 w-4 shrink-0 text-amber-400" />
          <div>
            <p class="font-medium text-amber-400">Configuration is loaded from environment variables</p>
            <p class="mt-0.5 text-muted-foreground">
              No config.json file was found. The settings below reflect effective values; they cannot be saved here.
            </p>
          </div>
        </div>

        <div class="grid max-w-3xl gap-6">
          <!-- Provider & Model -->
          <Card>
            <CardHeader>
              <CardTitle>Provider &amp; Model</CardTitle>
              <CardDescription>
                Primary LLM provider and model. API keys can be added or removed here but are never displayed.
              </CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="flex items-start gap-3 rounded-md border border-primary/20 bg-primary/5 px-4 py-3 text-sm">
                <Info class="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                <div>
                  <p class="font-medium">Gemini works out of the box</p>
                  <p class="mt-0.5 text-muted-foreground">
                    Gemini is the default provider - leave Provider and Base URL empty and just set the
                    API key. Base URL is only needed for OpenAI-compatible endpoints, and an empty model
                    name falls back to the provider's default.
                  </p>
                </div>
              </div>

              <div class="grid gap-2 rounded-md border border-border p-3">
                <div class="flex items-center justify-between gap-2">
                  <div class="flex items-center gap-2">
                    <KeyRound class="h-4 w-4 text-muted-foreground" />
                    <span class="text-sm font-medium">API key</span>
                    <template v-if="hasAPIKey">
                      <CheckCircle2 class="h-4 w-4 text-emerald-500" />
                      <span class="text-xs text-emerald-500">configured</span>
                    </template>
                    <template v-else>
                      <AlertCircle class="h-4 w-4 text-amber-500" />
                      <span class="text-xs text-amber-500">not set</span>
                    </template>
                  </div>
                  <div class="flex items-center gap-1">
                    <template v-if="hasAPIKey && !showAPIKeyInput">
                      <Button variant="ghost" size="sm" class="h-7 px-2" :disabled="!writable" @click="showAPIKeyInput = true">
                        Replace
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        class="h-7 px-2 text-destructive"
                        :disabled="!writable || saving"
                        @click="removeAPIKey"
                      >
                        Remove
                      </Button>
                    </template>
                    <Button
                      v-if="!hasAPIKey && !showAPIKeyInput"
                      size="sm"
                      class="h-7 px-2"
                      :disabled="!writable || saving"
                      @click="showAPIKeyInput = true"
                    >
                      Add
                    </Button>
                  </div>
                </div>
                <div v-if="showAPIKeyInput" class="grid gap-2">
                  <div class="flex gap-2">
                    <Input
                      v-model="newAPIKey"
                      type="password"
                      autocomplete="off"
                      :disabled="!writable"
                      placeholder="Paste a new API key (never displayed)"
                      class="h-8 text-sm"
                      @keydown.enter="saveConfig"
                    />
                    <Button size="sm" class="h-8 shrink-0" :disabled="!writable || saving || !newAPIKey.trim()" @click="saveConfig">
                      Save
                    </Button>
                    <Button variant="ghost" size="sm" class="h-8 shrink-0" @click="showAPIKeyInput = false; newAPIKey = ''">
                      Cancel
                    </Button>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    The stored key is never shown or returned. Saving replaces it with what you paste; use Remove to delete it.
                  </p>
                </div>
              </div>

              <div class="grid gap-2">
                <Label for="provider">Provider</Label>
                <select
                  id="provider"
                  v-model="form.provider"
                  :disabled="!writable"
                  class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                >
                  <option value="">(default)</option>
                  <option value="gemini">gemini</option>
                  <option value="openai">openai</option>
                  <option value="openai-compatible">openai-compatible</option>
                </select>
              </div>

              <div class="grid gap-2">
                <Label for="model_name">Model name</Label>
                <Input id="model_name" v-model="form.model_name" :disabled="!writable" placeholder="gemini-3.6-flash" />
              </div>

              <div class="grid gap-2">
                <Label for="base_url">Base URL (openai-compatible)</Label>
                <Input id="base_url" v-model="form.base_url" :disabled="!writable" placeholder="http://localhost:11434/v1" />
              </div>

              <div class="grid gap-2">
                <Label for="summary_model">Summary model (context compaction)</Label>
                <Input id="summary_model" v-model="form.summary_model" :disabled="!writable" placeholder="gemini-2.5-flash-lite" />
              </div>

              <div class="grid gap-2">
                <Label for="fallback_providers">Fallback providers (comma-separated)</Label>
                <Input id="fallback_providers" v-model="form.fallback_providers" :disabled="!writable" placeholder="openai" />
              </div>

              <div class="grid gap-2">
                <Label for="thinking_level">Thinking level</Label>
                <select
                  id="thinking_level"
                  v-model="form.thinking_level"
                  :disabled="!writable"
                  class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                >
                  <option value="">(provider default)</option>
                  <option value="off">off</option>
                  <option value="low">low</option>
                  <option value="medium">medium</option>
                  <option value="high">high</option>
                  <option value="maximum">maximum</option>
                  <option value="xhigh">xhigh</option>
                </select>
              </div>

              <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div class="grid gap-2">
                  <Label for="chat_buffer_size">Chat buffer size</Label>
                  <Input id="chat_buffer_size" v-model.number="form.chat_buffer_size" :disabled="!writable" type="number" />
                </div>
                <div class="flex items-end gap-6 pb-1">
                  <label class="flex items-center gap-2 text-sm">
                    <input v-model="form.task_checkpoint" type="checkbox" :disabled="!writable"
                           class="h-4 w-4 rounded border-input" />
                    Task checkpoint
                  </label>
                  <label class="flex items-center gap-2 text-sm">
                    <input v-model="form.search_expansion" type="checkbox" :disabled="!writable"
                           class="h-4 w-4 rounded border-input" />
                    Search expansion
                  </label>
                </div>
              </div>
            </CardContent>
          </Card>

          <!-- Agent -->
          <Card>
            <CardHeader>
              <CardTitle>Agent</CardTitle>
              <CardDescription>Instructions, context files, and knowledge base location.</CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="grid gap-2">
                <Label for="instruction">Instruction</Label>
                <textarea
                  id="instruction"
                  v-model="form.instruction"
                  :disabled="!writable"
                  rows="3"
                  class="w-full resize-none rounded-md border border-input bg-background px-3 py-2 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
                  placeholder="Additional instruction injected into agent prompts"
                />
              </div>
              <div class="grid gap-2">
                <Label for="instruction_files">Instruction files (comma-separated paths or URLs)</Label>
                <Input id="instruction_files" v-model="form.instruction_files" :disabled="!writable" placeholder="docs/rules.md, https://example.com/agents.md" />
              </div>
              <div class="grid gap-2">
                <Label for="knowledge_dir">Knowledge directory</Label>
                <Input id="knowledge_dir" v-model="form.knowledge_dir" :disabled="!writable" placeholder="./knowledge" />
              </div>
              <div class="grid gap-2">
                <Label for="skill_dirs">Skill directories (comma-separated)</Label>
                <Input id="skill_dirs" v-model="form.skill_dirs" :disabled="!writable" placeholder="./skills" />
              </div>
            </CardContent>
          </Card>

          <!-- Vision -->
          <Card>
            <CardHeader>
              <CardTitle>Vision</CardTitle>
              <CardDescription>
                Multimodal model used to describe images when the main model lacks vision.
              </CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="grid gap-2 rounded-md border border-border p-3">
                <div class="flex items-center justify-between gap-2">
                  <div class="flex items-center gap-2">
                    <KeyRound class="h-4 w-4 text-muted-foreground" />
                    <span class="text-sm font-medium">Vision API key</span>
                    <template v-if="hasVisionAPIKey">
                      <CheckCircle2 class="h-4 w-4 text-emerald-500" />
                      <span class="text-xs text-emerald-500">configured</span>
                    </template>
                    <template v-else>
                      <AlertCircle class="h-4 w-4 text-amber-500" />
                      <span class="text-xs text-amber-500">not set</span>
                    </template>
                  </div>
                  <div class="flex items-center gap-1">
                    <template v-if="hasVisionAPIKey && !showVisionAPIKeyInput">
                      <Button variant="ghost" size="sm" class="h-7 px-2" :disabled="!writable" @click="showVisionAPIKeyInput = true">
                        Replace
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        class="h-7 px-2 text-destructive"
                        :disabled="!writable || saving"
                        @click="removeVisionAPIKey"
                      >
                        Remove
                      </Button>
                    </template>
                    <Button
                      v-if="!hasVisionAPIKey && !showVisionAPIKeyInput"
                      size="sm"
                      class="h-7 px-2"
                      :disabled="!writable || saving"
                      @click="showVisionAPIKeyInput = true"
                    >
                      Add
                    </Button>
                  </div>
                </div>
                <div v-if="showVisionAPIKeyInput" class="grid gap-2">
                  <div class="flex gap-2">
                    <Input
                      v-model="newVisionAPIKey"
                      type="password"
                      autocomplete="off"
                      :disabled="!writable"
                      placeholder="Paste a new vision API key (never displayed)"
                      class="h-8 text-sm"
                      @keydown.enter="saveConfig"
                    />
                    <Button size="sm" class="h-8 shrink-0" :disabled="!writable || saving || !newVisionAPIKey.trim()" @click="saveConfig">
                      Save
                    </Button>
                    <Button variant="ghost" size="sm" class="h-8 shrink-0" @click="showVisionAPIKeyInput = false; newVisionAPIKey = ''">
                      Cancel
                    </Button>
                  </div>
                  <p class="text-xs text-muted-foreground">
                    The stored key is never shown or returned. Saving replaces it with what you paste; use Remove to delete it.
                  </p>
                </div>
              </div>

              <div class="grid gap-2">
                <Label for="vision_model">Vision model</Label>
                <Input id="vision_model" v-model="form.vision_model" :disabled="!writable" placeholder="gemini-2.5-flash" />
              </div>
              <div class="grid gap-2">
                <Label for="vision_provider">Vision provider</Label>
                <select
                  id="vision_provider"
                  v-model="form.vision_provider"
                  :disabled="!writable"
                  class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                >
                  <option value="">(primary provider)</option>
                  <option value="gemini">gemini</option>
                  <option value="openai">openai</option>
                  <option value="openai-compatible">openai-compatible</option>
                </select>
              </div>
              <div class="grid gap-2">
                <Label for="vision_base_url">Vision base URL</Label>
                <Input id="vision_base_url" v-model="form.vision_base_url" :disabled="!writable" />
              </div>
              <div class="grid gap-2">
                <Label for="model_vision">Model vision override</Label>
                <select
                  id="model_vision"
                  v-model="form.model_vision"
                  :disabled="!writable"
                  class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                >
                  <option value="auto">auto</option>
                  <option value="yes">yes</option>
                  <option value="no">no</option>
                </select>
              </div>
            </CardContent>
          </Card>

          <!-- Context & Environment -->
          <Card>
            <CardHeader>
              <CardTitle>Context &amp; Environment</CardTitle>
              <CardDescription>Project context files (AGENTS.md) and runtime environment block.</CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
                <div class="grid gap-2">
                  <Label for="context_files_max_chars">Context file max chars</Label>
                  <Input id="context_files_max_chars" v-model.number="form.context_files_max_chars" :disabled="!writable" type="number" />
                </div>
                <div class="grid gap-2">
                  <Label for="context_files_apply_to">Context files apply to (comma-separated)</Label>
                  <Input id="context_files_apply_to" v-model="form.context_files_apply_to" :disabled="!writable" placeholder="orchestrator, general_purpose" />
                </div>
                <div class="grid gap-2">
                  <Label for="system_env_max_chars">Environment block max chars</Label>
                  <Input id="system_env_max_chars" v-model.number="form.system_env_max_chars" :disabled="!writable" type="number" />
                </div>
                <div class="grid gap-2">
                  <Label for="system_env_apply_to">Environment apply to (comma-separated)</Label>
                  <Input id="system_env_apply_to" v-model="form.system_env_apply_to" :disabled="!writable" />
                </div>
              </div>
              <label class="flex items-center gap-2 text-sm">
                <input v-model="form.system_env_enabled" type="checkbox" :disabled="!writable"
                       class="h-4 w-4 rounded border-input" />
                Inject runtime environment block
              </label>
            </CardContent>
          </Card>

          <!-- Preferred Units -->
          <Card>
            <CardHeader>
              <CardTitle>Preferred Measurement Units</CardTitle>
              <CardDescription>
                The agent reports physical quantities (distance, mass, volume, temperature, speed, area)
                in your preferred system. Defaults to metric (SI / ISO) when unset.
              </CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="grid gap-2">
                <Label for="units_system">Measurement system</Label>
                <select
                  id="units_system"
                  v-model="form.units_system"
                  :disabled="!writable"
                  class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                >
                  <option value="metric">Metric (meters, kg, liters, °C)</option>
                  <option value="imperial">Imperial (miles, lb, gallons, °F)</option>
                </select>
              </div>
            </CardContent>
          </Card>

          <!-- Sandbox -->
          <Card>
            <CardHeader>
              <CardTitle>Sandbox</CardTitle>
              <CardDescription>Workspace confinement and subprocess isolation.</CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="grid gap-2">
                <Label for="sandbox_mode">Mode</Label>
                <select
                  id="sandbox_mode"
                  v-model="form.sandbox_mode"
                  :disabled="!writable"
                  class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                >
                  <option value="paths">paths</option>
                  <option value="bubblewrap">bubblewrap</option>
                  <option value="landlock">landlock</option>
                  <option value="off">off</option>
                </select>
              </div>
              <div class="grid gap-2">
                <Label for="sandbox_workspace_roots">Workspace roots (comma-separated)</Label>
                <Input id="sandbox_workspace_roots" v-model="form.sandbox_workspace_roots" :disabled="!writable" placeholder="." />
              </div>
              <div class="grid gap-2">
                <Label for="sandbox_read_roots">Read roots (comma-separated)</Label>
                <Input id="sandbox_read_roots" v-model="form.sandbox_read_roots" :disabled="!writable" />
              </div>
              <div class="grid gap-2">
                <Label for="sandbox_deny_roots">Deny roots (comma-separated)</Label>
                <Input id="sandbox_deny_roots" v-model="form.sandbox_deny_roots" :disabled="!writable" />
              </div>
              <div class="grid gap-2">
                <Label for="sandbox_allowed_commands">Allowed commands (comma-separated)</Label>
                <Input id="sandbox_allowed_commands" v-model="form.sandbox_allowed_commands" :disabled="!writable" placeholder="ls, cat, git" />
              </div>
              <div class="flex items-end gap-6 pb-1">
                <label class="flex items-center gap-2 text-sm">
                  <input v-model="form.sandbox_allow_network" type="checkbox" :disabled="!writable"
                         class="h-4 w-4 rounded border-input" />
                  Allow network
                </label>
                <label class="flex items-center gap-2 text-sm">
                  <input v-model="form.sandbox_allow_pip_install" type="checkbox" :disabled="!writable"
                         class="h-4 w-4 rounded border-input" />
                  Allow pip install
                </label>
              </div>
            </CardContent>
          </Card>

          <!-- Guardrails -->
          <Card>
            <CardHeader>
              <CardTitle>Guardrails &amp; Gates</CardTitle>
              <CardDescription>Anti-degeneration loop guards and interactive approval/clarify gates.</CardDescription>
            </CardHeader>
            <CardContent class="grid gap-4">
              <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
                <div class="grid gap-2">
                  <Label for="loop_guard_max_output_tokens">Max output tokens</Label>
                  <Input id="loop_guard_max_output_tokens" v-model.number="form.loop_guard_max_output_tokens" :disabled="!writable" type="number" />
                </div>
                <div class="grid gap-2">
                  <Label for="loop_guard_repetition_limit">Repetition limit</Label>
                  <Input id="loop_guard_repetition_limit" v-model.number="form.loop_guard_repetition_limit" :disabled="!writable" type="number" />
                </div>
                <div class="grid gap-2">
                  <Label for="loop_guard_max_text_without_tool">Max text without tool</Label>
                  <Input id="loop_guard_max_text_without_tool" v-model.number="form.loop_guard_max_text_without_tool" :disabled="!writable" type="number" />
                </div>
              </div>
              <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
                <div class="grid gap-2">
                  <Label for="approval_mode">Approval mode</Label>
                  <select
                    id="approval_mode"
                    v-model="form.approval_mode"
                    :disabled="!writable"
                    class="h-9 w-full rounded-md border border-input bg-transparent px-3 py-1 text-sm focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
                  >
                    <option value="interactive">interactive</option>
                    <option value="deny">deny</option>
                    <option value="allow">allow</option>
                  </select>
                </div>
                <div class="grid gap-2">
                  <Label for="approval_expiry_seconds">Approval expiry (s)</Label>
                  <Input id="approval_expiry_seconds" v-model.number="form.approval_expiry_seconds" :disabled="!writable" type="number" />
                </div>
                <div class="grid gap-2">
                  <Label for="clarify_expiry_seconds">Clarify expiry (s)</Label>
                  <Input id="clarify_expiry_seconds" v-model.number="form.clarify_expiry_seconds" :disabled="!writable" type="number" />
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      </template>
    </div>
  </div>
</template>
