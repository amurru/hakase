<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import {
  fetchHooks,
  shortFingerprint,
  trustHooks,
  trustLabel,
  untrustHooks,
  type HookSnapshot,
  type HooksProject,
} from '@/lib/hooks'
import { useSessionStore } from '@/stores/session'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { KeyRound, Loader2, PlugZap, RefreshCw, ShieldAlert, ShieldCheck } from '@lucide/vue'

const sessionStore = useSessionStore()

const enabled = ref(true)
const userHooks = ref<HookSnapshot[]>([])
const project = ref<HooksProject | null>(null)
const sessionId = ref('')
const loading = ref(false)
const error = ref('')
const acting = ref<string | null>(null)

async function load() {
  loading.value = true
  error.value = ''
  try {
    const list = await fetchHooks(sessionId.value || undefined)
    enabled.value = list.enabled
    userHooks.value = list.user
    project.value = list.project ?? null
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load hooks'
  } finally {
    loading.value = false
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
      <Button variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw class="mr-1 h-4 w-4" :class="{ 'animate-spin': loading }" />
        Refresh
      </Button>
    </div>

    <p class="text-sm text-muted-foreground">
      Tool-lifecycle hooks run your commands around tool calls. Your own config always runs;
      project hooks (<code>.hakase/hooks.json</code>) execute only after per-hook trust.
      Trust is content-hash based — editing a script lapses trust until re-approved.
    </p>

    <div v-if="error" class="text-sm text-destructive">{{ error }}</div>

    <section class="space-y-3">
      <h2 class="text-lg font-medium">Your hooks</h2>
      <p v-if="userHooks.length === 0" class="text-sm text-muted-foreground">
        No user hooks configured.
      </p>
      <Card v-for="h in userHooks" :key="h.fingerprint">
        <CardContent class="flex items-center justify-between gap-4 p-4">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <Badge variant="secondary">{{ h.event }}</Badge>
              <span class="font-medium">{{ hookTitle(h) }}</span>
              <Badge variant="outline">{{ trustLabel(h) }}</Badge>
            </div>
            <div class="mt-1 truncate font-mono text-xs text-muted-foreground">
              {{ h.command.join(' ') }}
            </div>
            <div class="mt-1 font-mono text-xs text-muted-foreground">
              {{ shortFingerprint(h.fingerprint) }}
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
