<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import {
  addUserHook,
  fetchHooks,
  removeUserHooks,
  setHooksMaster,
  setUserHooksEnabled,
  shortFingerprint,
  trustHooks,
  trustLabel,
  untrustHooks,
  updateUserHook,
  type HookSnapshot,
  type HooksProject,
} from '@/lib/hooks'
import { useSessionStore } from '@/stores/session'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { KeyRound, Loader2, PlugZap, RefreshCw, ShieldAlert, ShieldCheck } from '@lucide/vue'

const HOOK_EVENTS = ['PreToolUse', 'PostToolUse', 'SessionStart', 'UserPromptSubmit'] as const

const sessionStore = useSessionStore()

const enabled = ref(true)
const userHooks = ref<HookSnapshot[]>([])
const project = ref<HooksProject | null>(null)
const sessionId = ref('')
const loading = ref(false)
const error = ref('')
const acting = ref<string | null>(null)

// Add form state (command is one argv entry per line).
const showAdd = ref(false)
const addEvent = ref<string>('PreToolUse')
const addMatcher = ref('')
const addName = ref('')
const addCommand = ref('')
const addTimeout = ref('30')
const addOnFailure = ref('allow')

// Inline edit state, keyed by fingerprint (event is immutable: changing it
// is remove + add).
const editingFp = ref<string | null>(null)
const editMatcher = ref('')
const editName = ref('')
const editCommand = ref('')
const editTimeout = ref('')
const editOnFailure = ref('allow')

function applyList(list: { enabled: boolean; user: HookSnapshot[]; project?: HooksProject | null }) {
  enabled.value = list.enabled
  userHooks.value = list.user
  project.value = list.project ?? null
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const list = await fetchHooks(sessionId.value || undefined)
    applyList(list)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load hooks'
  } finally {
    loading.value = false
  }
}

async function setMaster(next: boolean) {
  try {
    applyList(await setHooksMaster(next))
    toast.success(next ? 'Hooks enabled' : 'Hooks disabled')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to toggle hooks')
  }
}

async function setHookEnabled(h: HookSnapshot, next: boolean) {
  acting.value = h.fingerprint
  try {
    applyList(await setUserHooksEnabled([h.fingerprint], next))
    toast.success(next ? 'Hook enabled' : 'Hook disabled (trust unaffected)')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to toggle hook')
  } finally {
    acting.value = null
  }
}

async function removeHook(h: HookSnapshot) {
  if (!confirm(`Remove hook "${h.name || shortFingerprint(h.fingerprint)}"?`)) return
  acting.value = h.fingerprint
  try {
    applyList(await removeUserHooks([h.fingerprint]))
    toast.success('Hook removed')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to remove hook')
  } finally {
    acting.value = null
  }
}

function parseCommand(text: string): string[] | null {
  const argv = text
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '')
  return argv.length > 0 ? argv : null
}

async function submitAdd() {
  const argv = parseCommand(addCommand.value)
  if (!argv) {
    toast.error('Command must be at least one argv entry (one per line)')
    return
  }
  const timeout = Number.parseInt(addTimeout.value, 10)
  try {
    applyList(
      await addUserHook({
        event: addEvent.value,
        matcher: addMatcher.value.trim(),
        name: addName.value.trim(),
        command: argv,
        timeout: Number.isFinite(timeout) ? timeout : 30,
        on_failure: addOnFailure.value,
      }),
    )
    toast.success('Hook added (live, no restart)')
    showAdd.value = false
    addMatcher.value = ''
    addName.value = ''
    addCommand.value = ''
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to add hook')
  }
}

function startEdit(h: HookSnapshot) {
  editingFp.value = h.fingerprint
  editMatcher.value = h.matcher
  editName.value = h.name
  editCommand.value = h.command.join('\n')
  editTimeout.value = String(h.timeout)
  editOnFailure.value = h.on_failure || 'allow'
}

async function submitEdit(h: HookSnapshot) {
  const argv = parseCommand(editCommand.value)
  if (!argv) {
    toast.error('Command must be at least one argv entry (one per line)')
    return
  }
  const timeout = Number.parseInt(editTimeout.value, 10)
  try {
    applyList(
      await updateUserHook({
        fingerprint: h.fingerprint,
        matcher: editMatcher.value,
        name: editName.value,
        command: argv,
        timeout: Number.isFinite(timeout) ? timeout : h.timeout,
        on_failure: editOnFailure.value,
      }),
    )
    toast.success('Hook updated (live, no restart)')
    editingFp.value = null
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to update hook')
  }
}

async function trust(fp: string) {
  acting.value = fp
  try {
    const res = await trustHooks([fp], sessionId.value || undefined)
    toast.success(`Trusted ${res.changed.length} hook(s)`)
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to trust hook')
  } finally {
    acting.value = null
  }
}

async function untrust(fp: string) {
  acting.value = fp
  try {
    await untrustHooks([fp])
    toast.success('Trust revoked')
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to revoke trust')
  } finally {
    acting.value = null
  }
}

function hookTitle(h: HookSnapshot): string {
  return h.name || shortFingerprint(h.fingerprint)
}

watch(sessionId, load)

onMounted(async () => {
  if (sessionStore.sessions.length === 0) {
    try {
      await sessionStore.fetchSessions()
    } catch {
      // sessions are optional context; the user layer still renders
    }
  }
  await load()
})
</script>

<template>
  <div class="space-y-6 p-6">
    <div class="flex items-center justify-between">
      <div class="flex items-center gap-2">
        <PlugZap class="h-6 w-6" />
        <h1 class="text-2xl font-semibold">Hooks</h1>
        <Badge v-if="!enabled" variant="destructive">disabled</Badge>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" @click="setMaster(!enabled)">
          {{ enabled ? 'Disable all' : 'Enable all' }}
        </Button>
        <Button variant="outline" size="sm" :disabled="loading" @click="load">
          <RefreshCw class="mr-1 h-4 w-4" :class="{ 'animate-spin': loading }" />
          Refresh
        </Button>
      </div>
    </div>

    <p class="text-sm text-muted-foreground">
      Tool-lifecycle hooks run your commands around tool calls. Your own hooks are editable
      below and apply live (no restart); project hooks
      (<code>.hakase/hooks.json</code>) execute only after per-hook trust.
      Trust is content-hash based — editing a script lapses trust until re-approved,
      while enabling/disabling never affects trust.
    </p>

    <div v-if="error" class="text-sm text-destructive">{{ error }}</div>

    <section class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-medium">Your hooks</h2>
        <Button variant="outline" size="sm" @click="showAdd = !showAdd">
          {{ showAdd ? 'Cancel' : 'Add hook' }}
        </Button>
      </div>
      <Card v-if="showAdd">
        <CardContent class="space-y-3 p-4">
          <div class="grid grid-cols-2 gap-3">
            <label class="space-y-1 text-sm">
              <span class="text-muted-foreground">Event</span>
              <select
                v-model="addEvent"
                class="w-full rounded-md border border-border bg-background px-2 py-1 text-sm"
              >
                <option v-for="e in HOOK_EVENTS" :key="e" :value="e">{{ e }}</option>
              </select>
            </label>
            <label class="space-y-1 text-sm">
              <span class="text-muted-foreground">Matcher (regex, empty = all tools)</span>
              <Input v-model="addMatcher" placeholder="^system_exec$" />
            </label>
            <label class="space-y-1 text-sm">
              <span class="text-muted-foreground">Name</span>
              <Input v-model="addName" placeholder="no-rm-rf" />
            </label>
            <label class="space-y-1 text-sm">
              <span class="text-muted-foreground">Timeout (s) / on failure</span>
              <span class="flex gap-2">
                <Input v-model="addTimeout" inputmode="numeric" class="w-20" />
                <select
                  v-model="addOnFailure"
                  class="rounded-md border border-border bg-background px-2 py-1 text-sm"
                >
                  <option value="allow">allow</option>
                  <option value="block">block</option>
                </select>
              </span>
            </label>
          </div>
          <label class="block space-y-1 text-sm">
            <span class="text-muted-foreground">Command (one argv entry per line, no shell)</span>
            <textarea
              v-model="addCommand"
              rows="3"
              placeholder="/home/you/.hakase/hooks/no-rm-rf.sh"
              class="w-full rounded-md border border-border bg-background px-2 py-1 font-mono text-sm"
            />
          </label>
          <Button size="sm" @click="submitAdd">Add hook</Button>
        </CardContent>
      </Card>
      <p v-if="userHooks.length === 0" class="text-sm text-muted-foreground">
        No user hooks configured.
      </p>
      <Card v-for="h in userHooks" :key="h.fingerprint" :class="{ 'opacity-60': !h.enabled }">
        <CardContent class="space-y-3 p-4">
          <div class="flex items-center justify-between gap-4">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <Badge variant="secondary">{{ h.event }}</Badge>
                <span class="font-medium">{{ hookTitle(h) }}</span>
                <Badge variant="outline">{{ trustLabel(h) }}</Badge>
                <Badge v-if="!h.enabled" variant="destructive">disabled</Badge>
              </div>
              <div class="mt-1 truncate font-mono text-xs text-muted-foreground">
                {{ h.command.join(' ') }}
              </div>
              <div class="mt-1 font-mono text-xs text-muted-foreground">
                {{ shortFingerprint(h.fingerprint) }}
              </div>
            </div>
            <div class="flex shrink-0 gap-2">
              <Button
                variant="outline"
                size="sm"
                :disabled="acting === h.fingerprint"
                @click="setHookEnabled(h, !h.enabled)"
              >
                {{ h.enabled ? 'Disable' : 'Enable' }}
              </Button>
              <Button
                variant="outline"
                size="sm"
                @click="editingFp === h.fingerprint ? (editingFp = null) : startEdit(h)"
              >
                Edit
              </Button>
              <Button
                variant="outline"
                size="sm"
                :disabled="acting === h.fingerprint"
                @click="removeHook(h)"
              >
                Remove
              </Button>
            </div>
          </div>
          <div v-if="editingFp === h.fingerprint" class="space-y-3 border-t border-border pt-3">
            <div class="grid grid-cols-2 gap-3">
              <label class="space-y-1 text-sm">
                <span class="text-muted-foreground">Matcher (empty = all tools)</span>
                <Input v-model="editMatcher" />
              </label>
              <label class="space-y-1 text-sm">
                <span class="text-muted-foreground">Name</span>
                <Input v-model="editName" />
              </label>
              <label class="space-y-1 text-sm">
                <span class="text-muted-foreground">Timeout (s) / on failure</span>
                <span class="flex gap-2">
                  <Input v-model="editTimeout" inputmode="numeric" class="w-20" />
                  <select
                    v-model="editOnFailure"
                    class="rounded-md border border-border bg-background px-2 py-1 text-sm"
                  >
                    <option value="allow">allow</option>
                    <option value="block">block</option>
                  </select>
                </span>
              </label>
            </div>
            <label class="block space-y-1 text-sm">
              <span class="text-muted-foreground">Command (one argv entry per line)</span>
              <textarea
                v-model="editCommand"
                rows="3"
                class="w-full rounded-md border border-border bg-background px-2 py-1 font-mono text-sm"
              />
            </label>
            <p class="text-xs text-muted-foreground">
              Event cannot change (remove + add to move events). Fingerprint follows the command.
            </p>
            <div class="flex gap-2">
              <Button size="sm" @click="submitEdit(h)">Save</Button>
              <Button variant="outline" size="sm" @click="editingFp = null">Cancel</Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </section>

    <section class="space-y-3">
      <div class="flex items-center justify-between">
        <h2 class="text-lg font-medium">Project hooks</h2>
        <select
          v-model="sessionId"
          class="rounded-md border border-border bg-background px-2 py-1 text-sm"
          title="Session scope for project resolution"
        >
          <option value="">Server project</option>
          <option v-for="s in sessionStore.sessions" :key="s.id" :value="s.id">
            {{ s.title || s.id }}
          </option>
        </select>
      </div>
      <p v-if="!project" class="text-sm text-muted-foreground">
        No project hooks file for this scope.
      </p>
      <template v-else>
        <p class="font-mono text-xs text-muted-foreground">{{ project.file }}</p>
        <Card v-for="h in project.hooks" :key="h.fingerprint">
          <CardContent class="flex items-center justify-between gap-4 p-4">
            <div class="min-w-0">
              <div class="flex items-center gap-2">
                <Badge variant="secondary">{{ h.event }}</Badge>
                <span class="font-medium">{{ hookTitle(h) }}</span>
                <Badge :variant="h.trusted ? 'default' : 'destructive'">
                  <ShieldCheck v-if="h.trusted" class="mr-1 h-3 w-3" />
                  <ShieldAlert v-else class="mr-1 h-3 w-3" />
                  {{ trustLabel(h) }}
                </Badge>
                <Badge v-if="!h.enabled" variant="destructive">disabled</Badge>
              </div>
              <div class="mt-1 truncate font-mono text-xs text-muted-foreground">
                {{ h.command.join(' ') }}
              </div>
              <div class="mt-1 font-mono text-xs text-muted-foreground">
                {{ shortFingerprint(h.fingerprint) }}
              </div>
            </div>
            <div class="shrink-0">
              <Button
                v-if="!h.trusted"
                size="sm"
                :disabled="acting === h.fingerprint"
                @click="trust(h.fingerprint)"
              >
                <Loader2 v-if="acting === h.fingerprint" class="mr-1 h-4 w-4 animate-spin" />
                <KeyRound v-else class="mr-1 h-4 w-4" />
                Trust
              </Button>
              <Button
                v-else
                variant="outline"
                size="sm"
                :disabled="acting === h.fingerprint"
                @click="untrust(h.fingerprint)"
              >
                Revoke
              </Button>
            </div>
          </CardContent>
        </Card>
      </template>
    </section>
  </div>
</template>
