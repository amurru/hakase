<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { apiFetch } from '@/lib/api'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import {
  RefreshCw,
  Power,
  PowerOff,
  Wand2,
  Sparkles,
  AlertCircle,
  Loader2,
  FileCode2,
} from '@lucide/vue'

interface Skill {
  name: string
  type: string
  description: string
  enabled: boolean
  path?: string
  fileName?: string
  source?: string
  savedAt?: string
  deprecated?: boolean
  evalScore?: number
}

const skills = ref<Skill[]>([])
const loading = ref(false)
const error = ref('')
const actionPending = ref<string | null>(null)

async function loadSkills() {
  loading.value = true
  error.value = ''
  try {
    skills.value = await apiFetch<Skill[]>('/skills')
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load skills'
  } finally {
    loading.value = false
  }
}

async function enableSkill(skill: Skill) {
  actionPending.value = skill.name
  try {
    await apiFetch(`/skills/${encodeURIComponent(skill.name)}/enable?type=${skill.type}`, { method: 'POST' })
    toast.success(`Skill ${skill.name} enabled`)
    await loadSkills()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to enable skill'
    toast.error(error.value)
  } finally {
    actionPending.value = null
  }
}

async function disableSkill(skill: Skill) {
  actionPending.value = skill.name
  try {
    await apiFetch(`/skills/${encodeURIComponent(skill.name)}/disable?type=${skill.type}`, { method: 'POST' })
    toast.success(`Skill ${skill.name} disabled`)
    await loadSkills()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to disable skill'
    toast.error(error.value)
  } finally {
    actionPending.value = null
  }
}

const enabledCount = () => skills.value.filter((s) => s.enabled).length

function typeColor(type: string): string {
  switch (type) {
    case 'python':
      return 'bg-blue-500/20 text-blue-400 border-blue-500/30'
    case 'markdown':
      return 'bg-purple-500/20 text-purple-400 border-purple-500/30'
    default:
      return 'bg-muted text-muted-foreground border-border'
  }
}

onMounted(loadSkills)
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-3">
      <h1 class="text-lg font-semibold">Skills</h1>
      <div class="flex items-center gap-2">
        <Button variant="ghost" size="sm" @click="loadSkills">
          <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        </Button>
      </div>
    </div>

    <!-- Content -->
    <div class="flex-1 overflow-auto p-4">
      <!-- Loading -->
      <div v-if="loading && skills.length === 0" class="flex h-40 items-center justify-center text-sm text-muted-foreground">
        Loading skills...
      </div>
      <div v-else-if="error && skills.length === 0" class="flex h-40 flex-col items-center justify-center gap-2">
        <div class="flex items-center gap-2 text-sm text-destructive">
          <AlertCircle class="h-4 w-4 shrink-0" />
          <span>{{ error }}</span>
        </div>
        <Button variant="ghost" size="sm" @click="loadSkills">
          Retry
        </Button>
      </div>
      <div v-else-if="skills.length === 0" class="flex h-40 flex-col items-center justify-center text-muted-foreground">
        <Sparkles class="mb-2 h-8 w-8 opacity-50" />
        <p class="text-sm">No skills found</p>
        <p class="mt-1 text-xs">Python skills live in ./skills/skills.json; markdown skills are discovered from .agents/skills/ and similar directories</p>
      </div>
      <div v-else>
        <div class="mb-3 flex items-center gap-2 text-xs text-muted-foreground">
          <span>{{ enabledCount() }} of {{ skills.length }} skills enabled</span>
          <span class="text-border">|</span>
          <span>Disabled skills are hidden from the agent; state is stored in ~/.hakase/skill-state.json</span>
        </div>

        <div class="flex flex-col gap-3">
        <div v-for="skill in skills" :key="skill.type + ':' + skill.name" class="group">
          <Card class="transition-colors hover:border-border/80" :class="{ 'opacity-60': !skill.enabled }">
            <CardContent class="p-4">
              <div class="flex items-start justify-between gap-4">
                <!-- Skill info -->
                <div class="flex min-w-0 flex-1 flex-col gap-2">
                  <div class="flex items-center gap-2">
                    <component
                      :is="skill.type === 'python' ? FileCode2 : Wand2"
                      class="h-4 w-4 shrink-0 text-muted-foreground"
                    />
                    <span class="truncate text-sm font-medium">{{ skill.name }}</span>
                    <Badge
                      :class="typeColor(skill.type)"
                      variant="outline"
                      class="shrink-0 text-[10px] uppercase"
                    >
                      {{ skill.type }}
                    </Badge>
                    <Badge
                      variant="outline"
                      :class="skill.enabled
                        ? 'shrink-0 text-[10px] uppercase bg-green-500/20 text-green-400 border-green-500/30'
                        : 'shrink-0 text-[10px] uppercase bg-gray-500/20 text-gray-400 border-gray-500/30'"
                    >
                      {{ skill.enabled ? 'enabled' : 'disabled' }}
                    </Badge>
                    <Badge
                      v-if="skill.deprecated"
                      variant="outline"
                      class="shrink-0 text-[10px] uppercase bg-red-500/20 text-red-400 border-red-500/30"
                    >
                      deprecated
                    </Badge>
                  </div>

                  <p class="line-clamp-2 text-xs text-muted-foreground">
                    {{ skill.description }}
                  </p>

                  <!-- Source -->
                  <div class="flex items-center gap-2 text-xs text-muted-foreground">
                    <Wand2 class="h-3 w-3 shrink-0 opacity-70" />
                    <span class="truncate font-mono">
                      {{ skill.source || skill.path || skill.fileName }}
                    </span>
                  </div>
                </div>

                <!-- Actions -->
                <div class="flex shrink-0 items-center gap-1">
                  <Button
                    v-if="skill.enabled"
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2"
                    :disabled="actionPending === skill.name"
                    @click="disableSkill(skill)"
                  >
                    <Loader2 v-if="actionPending === skill.name" class="mr-1 h-3 w-3 animate-spin" />
                    <PowerOff v-else class="mr-1 h-3 w-3" />
                    Disable
                  </Button>
                  <Button
                    v-else
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2"
                    :disabled="actionPending === skill.name"
                    @click="enableSkill(skill)"
                  >
                    <Loader2 v-if="actionPending === skill.name" class="mr-1 h-3 w-3 animate-spin" />
                    <Power v-else class="mr-1 h-3 w-3" />
                    Enable
                  </Button>
                </div>
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
      </div>

      <div
        v-if="error && skills.length > 0"
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