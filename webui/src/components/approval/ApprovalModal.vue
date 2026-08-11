<script setup lang="ts">
import { computed, watch } from 'vue'
import { useApprovalStore } from '@/stores/approval'
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription } from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Progress } from '@/components/ui/progress'
import { AlertTriangle, CheckCircle, XCircle, Shield } from '@lucide/vue'

const store = useApprovalStore()

const riskConfig = computed(() => {
  const risk = store.currentApproval?.risk?.toUpperCase() ?? ''
  switch (risk) {
    case 'HIGH':
      return { label: 'HIGH', class: 'bg-red-500/20 text-red-400 border-red-500/30' }
    case 'MEDIUM':
      return { label: 'MEDIUM', class: 'bg-amber-500/20 text-amber-400 border-amber-500/30' }
    case 'LOW':
      return { label: 'LOW', class: 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30' }
    default:
      return { label: risk || 'UNKNOWN', class: 'bg-muted text-muted-foreground border-border' }
  }
})

const progressColor = computed(() => {
  if (store.countdown > 30) return 'bg-emerald-500'
  if (store.countdown > 10) return 'bg-amber-500'
  return 'bg-red-500'
})

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && store.isOpen && store.status === 'pending') {
    e.preventDefault()
    e.stopPropagation()
  }
}

watch(
  () => store.isOpen,
  (open) => {
    if (open) {
      document.addEventListener('keydown', handleKeydown, true)
    } else {
      document.removeEventListener('keydown', handleKeydown, true)
    }
  },
)
</script>

<template>
  <Dialog :open="store.isOpen">
    <DialogContent
      class="sm:max-w-md"
      :show-close-button="false"
      @interact-outside.prevent
      @escape-outside.prevent
    >
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2">
          <Shield class="h-5 w-5 text-primary" />
          Tool Approval Required
        </DialogTitle>
        <DialogDescription>
          A tool is requesting permission to execute.
        </DialogDescription>
      </DialogHeader>

      <div v-if="store.currentApproval" class="space-y-4">
        <!-- Tool + Risk Row -->
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium text-muted-foreground">Tool:</span>
            <span class="font-mono text-sm font-semibold">{{ store.currentApproval.tool }}</span>
          </div>
          <Badge
            variant="outline"
            :class="riskConfig.class"
          >
            {{ riskConfig.label }}
          </Badge>
        </div>

        <!-- Reason -->
        <div class="space-y-1.5">
          <span class="text-xs font-medium text-muted-foreground">Reason</span>
          <p class="text-sm">{{ store.currentApproval.reason }}</p>
        </div>

        <!-- Command -->
        <div class="space-y-1.5">
          <span class="text-xs font-medium text-muted-foreground">Command</span>
          <pre class="max-h-40 overflow-auto rounded-md border border-border bg-muted/50 p-3 font-mono text-xs leading-relaxed text-foreground">{{ store.currentApproval.command }}</pre>
        </div>

        <!-- Countdown -->
        <div class="space-y-2">
          <div class="flex items-center justify-between text-xs text-muted-foreground">
            <span>Auto-deny in</span>
            <span class="tabular-nums font-mono font-semibold" :class="store.countdown <= 10 ? 'text-red-400' : ''">
              {{ store.countdown }}s
            </span>
          </div>
          <Progress
            :model-value="store.progressPercent"
            class="h-1.5"
            :class="progressColor"
          />
        </div>

        <!-- Auto-denied state -->
        <div
          v-if="store.status === 'timed_out'"
          class="flex items-center gap-2 rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400"
        >
          <AlertTriangle class="h-4 w-4 shrink-0" />
          <span>Auto-denied (no response within 60s)</span>
        </div>

        <!-- Action Buttons -->
        <div v-if="store.status === 'pending'" class="flex gap-3 pt-1">
          <Button
            variant="outline"
            class="flex-1 border-red-500/30 text-red-400 hover:bg-red-500/10 hover:text-red-300"
            :disabled="store.responding"
            @click="store.deny()"
          >
            <XCircle class="h-4 w-4" />
            Deny
          </Button>
          <Button
            class="flex-1 bg-emerald-600 text-white hover:bg-emerald-500"
            :disabled="store.responding"
            @click="store.approve()"
          >
            <CheckCircle class="h-4 w-4" />
            Approve
          </Button>
        </div>
      </div>
    </DialogContent>
  </Dialog>
</template>
