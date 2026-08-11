<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useSessionStore, type SessionSummary } from '@/stores/session'
import { useAppStore } from '@/stores/app'
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
  DialogTrigger,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Plus, Search, MoreVertical, Archive, Trash2, MessageSquare, Loader2, FolderOpen } from '@lucide/vue'

const router = useRouter()
const sessionStore = useSessionStore()
const appStore = useAppStore()

const searchQuery = ref('')
const createDialogOpen = ref(false)
const newSessionTitle = ref('')
const deleteTarget = ref<SessionSummary | null>(null)
const deleteDialogOpen = ref(false)
const creating = ref(false)
const deleting = ref(false)
const archivingId = ref<string | null>(null)

const filteredSessions = computed(() => {
  if (!searchQuery.value.trim()) return sessionStore.sessions
  const q = searchQuery.value.toLowerCase()
  return sessionStore.sessions.filter((s) => s.title.toLowerCase().includes(q))
})

function relativeTime(dateStr: string): string {
  const now = Date.now()
  const then = new Date(dateStr).getTime()
  const diff = now - then
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

function isActiveSession(session: SessionSummary): boolean {
  return sessionStore.activeSession?.active === true && sessionStore.activeSession?.session_id === session.id
}

async function handleCreate() {
  const title = newSessionTitle.value.trim() || 'New Session'
  creating.value = true
  const created = await sessionStore.createSession(title)
  creating.value = false
  if (created) {
    newSessionTitle.value = ''
    createDialogOpen.value = false
  }
}

async function handleSwitch(session: SessionSummary) {
  await sessionStore.switchSession(session.id)
  appStore.setActiveSessionTitle(session.title)
  router.push('/chat')
}

async function handleArchive(session: SessionSummary) {
  archivingId.value = session.id
  await sessionStore.archiveSession(session.id)
  archivingId.value = null
}

function confirmDelete(session: SessionSummary) {
  deleteTarget.value = session
  deleteDialogOpen.value = true
}

async function handleDelete() {
  if (!deleteTarget.value) return
  deleting.value = true
  await sessionStore.deleteSession(deleteTarget.value.id)
  deleting.value = false
  deleteTarget.value = null
  deleteDialogOpen.value = false
}

onMounted(() => {
  sessionStore.fetchSessions()
  sessionStore.fetchActiveSession()
})
</script>

<template>
  <div class="flex h-full flex-col">
    <!-- Header -->
    <div class="flex shrink-0 items-center justify-between border-b border-border px-6 py-4">
      <div class="flex items-center gap-3">
        <h1 class="text-lg font-semibold">Sessions</h1>
        <Badge variant="secondary" class="tabular-nums">
          {{ sessionStore.sessions.length }}
        </Badge>
      </div>

      <Dialog v-model:open="createDialogOpen">
        <DialogTrigger as-child>
          <Button size="sm" class="gap-2">
            <Plus class="h-4 w-4" />
            New Session
          </Button>
        </DialogTrigger>
        <DialogContent class="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Create New Session</DialogTitle>
            <DialogDescription>
              Give your session a name to easily find it later.
            </DialogDescription>
          </DialogHeader>
          <div class="py-4">
            <Input
              v-model="newSessionTitle"
              placeholder="Session title (optional)"
              autofocus
              @keydown.enter="handleCreate"
            />
          </div>
          <DialogFooter>
            <Button variant="outline" @click="createDialogOpen = false">
              Cancel
            </Button>
            <Button :disabled="creating" @click="handleCreate">
              <Loader2 v-if="creating" class="mr-2 h-4 w-4 animate-spin" />
              Create
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>

    <!-- Search bar -->
    <div class="shrink-0 border-b border-border px-6 py-3">
      <div class="relative">
        <Search class="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          v-model="searchQuery"
          placeholder="Search sessions..."
          class="pl-9"
        />
      </div>
    </div>

    <!-- Session list -->
    <ScrollArea class="flex-1">
      <!-- Loading -->
      <div
        v-if="sessionStore.loading"
        class="flex items-center justify-center py-16"
      >
        <Loader2 class="h-6 w-6 animate-spin text-muted-foreground" />
      </div>

      <!-- Empty state -->
      <div
        v-else-if="filteredSessions.length === 0 && !searchQuery"
        class="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground"
      >
        <FolderOpen class="h-10 w-10" />
        <p class="text-sm">No sessions yet</p>
        <Button variant="outline" size="sm" @click="createDialogOpen = true">
          <Plus class="mr-2 h-4 w-4" />
          Create your first session
        </Button>
      </div>

      <!-- No search results -->
      <div
        v-else-if="filteredSessions.length === 0"
        class="flex flex-col items-center justify-center gap-3 py-16 text-muted-foreground"
      >
        <Search class="h-8 w-8" />
        <p class="text-sm">No sessions match "{{ searchQuery }}"</p>
      </div>

      <!-- Session items -->
      <div v-else class="divide-y divide-border">
        <div
          v-for="session in filteredSessions"
          :key="session.id"
          class="group flex items-center gap-4 px-6 py-3 transition-colors hover:bg-accent/50 cursor-pointer"
          @click="handleSwitch(session)"
        >
          <!-- Active indicator -->
          <div class="relative h-2 w-2 shrink-0">
            <span
              v-if="isActiveSession(session)"
              class="absolute inline-flex h-full w-full rounded-full bg-emerald-500"
            />
          </div>

          <!-- Content -->
          <div class="min-w-0 flex-1">
            <div class="flex items-center gap-2">
              <p class="truncate text-sm font-medium">{{ session.title }}</p>
              <Badge
                v-if="isActiveSession(session)"
                variant="default"
                class="shrink-0 bg-emerald-500/20 text-emerald-400 border-emerald-500/30"
              >
                Active
              </Badge>
            </div>
            <div class="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
              <span>{{ relativeTime(session.updated_at) }}</span>
              <span class="flex items-center gap-1">
                <MessageSquare class="h-3 w-3" />
                {{ session.message_count }}
              </span>
            </div>
          </div>

          <!-- Dropdown menu -->
          <DropdownMenu @click.stop>
            <DropdownMenuTrigger as-child>
              <Button
                variant="ghost"
                size="icon-xs"
                class="opacity-0 group-hover:opacity-100 transition-opacity"
                @click.stop
              >
                <MoreVertical class="h-4 w-4" />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                :disabled="archivingId === session.id"
                @click="handleArchive(session)"
              >
                <Archive class="mr-2 h-4 w-4" />
                Archive
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                class="text-destructive focus:text-destructive"
                @click="confirmDelete(session)"
              >
                <Trash2 class="mr-2 h-4 w-4" />
                Delete
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </ScrollArea>

    <!-- Delete confirmation dialog -->
    <Dialog v-model:open="deleteDialogOpen">
      <DialogContent class="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Delete Session?</DialogTitle>
          <DialogDescription>
            This will permanently delete
            <span class="font-medium text-foreground">"{{ deleteTarget?.title }}"</span>
            and all its messages. This cannot be undone.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" @click="deleteDialogOpen = false">
            Cancel
          </Button>
          <Button
            variant="destructive"
            :disabled="deleting"
            @click="handleDelete"
          >
            <Loader2 v-if="deleting" class="mr-2 h-4 w-4 animate-spin" />
            Delete
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
