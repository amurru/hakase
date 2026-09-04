<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useProjectsStore, type Project, type ProjectStatus } from '@/stores/projects'
import { useSessionStore } from '@/stores/session'
import { useAppStore } from '@/stores/app'
import { toast } from 'vue-sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { ScrollArea } from '@/components/ui/scroll-area'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import {
  Plus,
  Loader2,
  RefreshCw,
  MessageSquarePlus,
  MoreVertical,
  Trash2,
  FolderGit2,
  Globe,
  CircleAlert,
  ArrowDown,
  ArrowUp,
} from '@lucide/vue'

const router = useRouter()
const projectsStore = useProjectsStore()
const sessionStore = useSessionStore()
const appStore = useAppStore()

// Live repo state keyed by project id for the ready rows (ahead/behind etc.).
// Recomputed with the list so removed entries drop their chips immediately.
const statusById = computed<Record<string, ProjectStatus | undefined>>(() => {
  const out: Record<string, ProjectStatus | undefined> = {}
  for (const p of projectsStore.projects) {
    out[p.id] = projectsStore.statusOf(p.id)
  }
  return out
})

// ---- Register dialog ----
const registerOpen = ref(false)
const regName = ref('')
const regURL = ref('')
const regRef = ref('')
const regError = ref('')
const registering = ref(false)

function openRegister() {
  regName.value = ''
  regURL.value = ''
  regRef.value = ''
  regError.value = ''
  registerOpen.value = true
}

async function submitRegister() {
  // Guard against a double click firing before the button's disabled state
  // re-renders.
  if (registering.value) return
  const name = regName.value.trim()
  const url = regURL.value.trim()
  if (!name || !url) {
    regError.value = 'Name and clone URL are required.'
    return
  }
  registering.value = true
  regError.value = ''
  try {
    const created = await projectsStore.registerProject({ name, url, ref: regRef.value })
    registerOpen.value = false
    if (created.status === 'ready') {
      toast.success(`Registered ${created.name} - cloned on this host`)
    } else {
      // sync_error row: the list entry is the retry surface (Sync now).
      toast.warning(`Registered ${created.name}, but the initial clone failed`)
    }
  } catch (e) {
    regError.value = e instanceof Error ? e.message : 'Registration failed.'
  } finally {
    registering.value = false
  }
}

// ---- Row actions ----
const chattingId = ref<string | null>(null)

function isReady(p: Project): boolean {
  return p.status === 'ready'
}

// Per the product research (docs/git-tools/project-ui.md), a conversation is
// never re-pointed at another project: "new chat on this project" starts a
// fresh bound session and jumps into it.
async function startChat(p: Project) {
  if (chattingId.value || !isReady(p)) return
  chattingId.value = p.id
  const created = await sessionStore.createSession('New Session', p.id)
  chattingId.value = null
  if (!created) {
    toast.error(`Could not start a session on ${p.name}`)
    return
  }
  appStore.setActiveSessionTitle(created.title)
  appStore.setActiveProjectName(created.project_name ?? p.name)
  router.push({ path: '/chat', query: { session: created.id } })
}

async function runSync(p: Project) {
  try {
    const updated = await projectsStore.syncProject(p.id)
    if (!updated) return
    if (updated.status === 'ready') {
      toast.success(`Synced ${updated.name}`)
    } else {
      const detail = updated.error ? `: ${updated.error}` : ''
      toast.error(`Sync of ${updated.name} failed${detail}`)
    }
  } catch (e) {
    // 409 refusals (dirty tree / active agent run / sync in progress) carry
    // the reason in the error message.
    toast.error(e instanceof Error ? e.message : 'Sync failed')
  }
}

// ---- Delete ----
const deleteTarget = ref<Project | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)

function confirmDelete(p: Project) {
  deleteTarget.value = p
  deleteOpen.value = true
}

async function handleDelete() {
  if (!deleteTarget.value) return
  const target = deleteTarget.value
  deleting.value = true
  try {
    await projectsStore.removeProject(target.id)
    toast.success(`Removed ${target.name} from this host`)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : 'Delete failed')
  } finally {
    deleting.value = false
    deleteTarget.value = null
    deleteOpen.value = false
  }
}

// ---- Status presentation ----
function relativeTime(dateStr?: string): string {
  if (!dateStr) return ''
  const then = new Date(dateStr).getTime()
  if (Number.isNaN(then)) return ''
  const diff = Date.now() - then
  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return 'just now'
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(dateStr).toLocaleDateString()
}

onMounted(async () => {
  await projectsStore.load()
  // project-ui.md "behind upstream" affordance: fetch fresh repo state for
  // every ready project when the page is opened, not on a timer.
  await projectsStore.loadReadyStatuses()
})
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-4">
      <div class="flex items-center gap-3">
        <h1 class="text-lg font-semibold">Projects</h1>
        <Badge variant="secondary" class="tabular-nums">
          {{ projectsStore.projects.length }}
        </Badge>
      </div>

      <Button size="sm" class="gap-2" @click="openRegister">
        <Plus class="h-4 w-4" />
        Register Project
      </Button>
    </div>

    <!-- Project list -->
    <ScrollArea class="flex-1">
      <!-- Loading -->
      <div
        v-if="projectsStore.loading && projectsStore.projects.length === 0"
        class="flex items-center justify-center py-16"
      >
        <Loader2 class="h-6 w-6 animate-spin text-muted-foreground" />
      </div>

      <!-- Load error (registry unreachable / not configured) -->
      <div
        v-else-if="projectsStore.error && projectsStore.projects.length === 0"
        class="flex flex-col items-center justify-center gap-3 px-6 py-16 text-muted-foreground"
      >
        <CircleAlert class="h-10 w-10 text-destructive/70" />
        <p class="text-sm">Projects are unavailable on this server</p>
        <p class="max-w-md text-center text-xs leading-relaxed">
          {{ projectsStore.error }}
        </p>
        <Button variant="outline" size="sm" @click="projectsStore.load">
          <RefreshCw class="mr-2 h-4 w-4" />
          Retry
        </Button>
      </div>

      <!-- Empty state -->
      <div
        v-else-if="projectsStore.projects.length === 0"
        class="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground"
      >
        <FolderGit2 class="h-10 w-10" />
        <p class="text-sm">No registered projects</p>
        <p class="max-w-sm text-center text-xs leading-relaxed">
          Register a git repository and hakase clones it into a managed checkout
          on this host. You can then start project-bound chat sessions against
          the latest code - the remote is never modified.
        </p>
        <Button variant="outline" size="sm" @click="openRegister">
          <Plus class="mr-2 h-4 w-4" />
          Register your first project
        </Button>
      </div>

      <!-- Project rows -->
      <div v-else class="divide-y divide-border">
        <div
          v-for="p in projectsStore.projects"
          :key="p.id"
          class="group flex items-start gap-4 px-6 py-3 transition-colors hover:bg-accent/50"
        >
          <!-- Identity + status -->
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <p class="truncate text-sm font-medium" :title="p.id">{{ p.name }}</p>
              <Badge
                v-if="p.status === 'ready'"
                variant="outline"
                class="shrink-0 border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-400"
              >
                ready
              </Badge>
              <Badge
                v-else-if="p.status === 'sync_error'"
                variant="destructive"
                class="shrink-0"
              >
                sync error
              </Badge>
              <Badge
                v-else
                variant="outline"
                class="shrink-0 gap-1 border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400"
              >
                <Loader2 class="h-3 w-3 animate-spin" />
                cloning
              </Badge>
            </div>

            <!-- Source -->
            <div class="mt-1 flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
              <Globe class="h-3 w-3 shrink-0" />
              <span class="truncate font-mono" :title="p.source_url">{{ p.source_url }}</span>
              <span v-if="p.ref" class="shrink-0 rounded bg-muted px-1 py-px font-mono text-[10px]">
                @{{ p.ref }}
              </span>
            </div>

            <!-- Live upstream state for ready rows (project-ui.md behind affordance) -->
            <div
              v-if="isReady(p)"
              class="mt-1 flex min-w-0 items-center gap-1.5 text-[11px]"
            >
              <template v-if="statusById[p.id]">
                <Badge
                  v-if="(statusById[p.id]?.behind ?? 0) > 0"
                  variant="outline"
                  class="shrink-0 border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400"
                >
                  <ArrowDown class="h-3 w-3" />
                  {{ statusById[p.id]?.behind }} behind
                </Badge>
                <Badge
                  v-if="(statusById[p.id]?.ahead ?? 0) > 0"
                  variant="outline"
                  class="shrink-0"
                >
                  <ArrowUp class="h-3 w-3" />
                  {{ statusById[p.id]?.ahead }} ahead
                </Badge>
                <span
                  v-if="statusById[p.id]?.error"
                  class="flex min-w-0 items-center gap-1 truncate text-amber-600/80 dark:text-amber-400/80"
                  :title="`Counts may be stale: ${statusById[p.id]?.error ?? ''}`"
                >
                  <CircleAlert class="h-3 w-3 shrink-0" />
                  <span class="truncate">counts may be stale (fetch failed)</span>
                </span>
              </template>
              <Loader2
                v-else-if="projectsStore.isStatusLoading(p.id)"
                class="h-3 w-3 animate-spin text-muted-foreground/60"
              />
            </div>

            <!-- Bounded error from a failed register/sync: the row is the retry surface -->
            <p
              v-if="p.status === 'sync_error' && p.error"
              class="mt-1 truncate text-xs text-destructive/90"
              :title="p.error"
            >
              {{ p.error }}
            </p>

            <!-- Checkout (secondary detail) + freshness -->
            <div class="mt-1 flex min-w-0 items-center gap-2 text-[11px] text-muted-foreground/70">
              <span
                v-if="p.checkout"
                class="truncate font-mono"
                :title="`Managed checkout on this host: ${p.checkout}`"
              >
                {{ p.checkout }}
              </span>
              <span v-if="p.updated_at" class="shrink-0">updated {{ relativeTime(p.updated_at) }}</span>
            </div>
          </div>

          <!-- Actions -->
          <div class="flex shrink-0 items-center gap-0.5 pt-0.5">
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label="Sync now"
              :title="`Sync ${p.name} from its remote (git pull --ff-only)`"
              :disabled="projectsStore.isSyncing(p.id)"
              @click="runSync(p)"
            >
              <Loader2
                v-if="projectsStore.isSyncing(p.id)"
                class="animate-spin"
              />
              <RefreshCw v-else class="h-3 w-3" />
            </Button>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label="New chat on this project"
              :title="isReady(p) ? `Start a new chat session bound to ${p.name}` : `${p.name} is not ready yet`"
              :disabled="!isReady(p) || chattingId === p.id"
              @click="startChat(p)"
            >
              <Loader2 v-if="chattingId === p.id" class="animate-spin" />
              <MessageSquarePlus v-else class="h-3 w-3" />
            </Button>

            <DropdownMenu @click.stop>
              <DropdownMenuTrigger as-child>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  aria-label="More actions"
                  class="transition-opacity"
                >
                  <MoreVertical class="h-3 w-3" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem
                  :disabled="!isReady(p)"
                  @click="startChat(p)"
                >
                  <MessageSquarePlus class="mr-2 h-4 w-4" />
                  New chat on this project
                </DropdownMenuItem>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  class="text-destructive focus:text-destructive"
                  @click="confirmDelete(p)"
                >
                  <Trash2 class="mr-2 h-4 w-4" />
                  Delete
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </div>
    </ScrollArea>

    <!-- Register dialog -->
    <Dialog v-model:open="registerOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Register a Project</DialogTitle>
          <DialogDescription>
            hakase clones the repository into a managed checkout on this host.
            Sessions bound to the project then work against that checkout. The
            remote is never modified.
          </DialogDescription>
        </DialogHeader>
        <div class="space-y-3 py-3">
          <div>
            <label for="project-name" class="mb-1 block text-xs font-medium text-muted-foreground">
              Name
            </label>
            <Input
              id="project-name"
              v-model="regName"
              placeholder="my-service"
              autofocus
            />
          </div>
          <div>
            <label for="project-url" class="mb-1 block text-xs font-medium text-muted-foreground">
              Clone URL
            </label>
            <Input
              id="project-url"
              v-model="regURL"
              placeholder="https://github.com/org/repo.git"
              @keydown.enter="submitRegister"
            />
          </div>
          <div>
            <label for="project-ref" class="mb-1 block text-xs font-medium text-muted-foreground">
              Branch / tag / commit <span class="text-muted-foreground/60">(optional)</span>
            </label>
            <Input
              id="project-ref"
              v-model="regRef"
              placeholder="main"
              @keydown.enter="submitRegister"
            />
          </div>
          <p class="text-[11px] leading-relaxed text-muted-foreground">
            Allowed: https://, git://, ssh:// (network remotes) or file:// for a
            local bare remote. Names must be unique.
          </p>
          <p v-if="regError" class="text-xs text-destructive">{{ regError }}</p>
        </div>
        <DialogFooter>
          <Button variant="outline" @click="registerOpen = false">
            Cancel
          </Button>
          <Button :disabled="registering" @click="submitRegister">
            <Loader2 v-if="registering" class="mr-2 h-4 w-4 animate-spin" />
            Register &amp; Clone
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- Delete confirmation dialog -->
    <Dialog v-model:open="deleteOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete Project?</DialogTitle>
          <DialogDescription>
            This removes
            <span class="font-medium text-foreground">"{{ deleteTarget?.name }}"</span>
            and its local checkout
            <span class="font-medium text-foreground">{{ deleteTarget?.checkout || 'on this host' }}</span>.
            The remote repository is never touched. Existing sessions keep their
            history but can no longer run against the checkout.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" @click="deleteOpen = false">
            Cancel
          </Button>
          <Button
            variant="destructive"
            :disabled="deleting"
            @click="handleDelete"
          >
            <Loader2 v-if="deleting" class="mr-2 h-4 w-4 animate-spin" />
            Delete Project
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
