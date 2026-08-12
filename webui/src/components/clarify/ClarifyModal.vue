<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useClarifyStore } from '@/stores/clarify'
import { Button } from '@/components/ui/button'
import { Progress } from '@/components/ui/progress'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { AlertCircle, Clock } from '@lucide/vue'

const store = useClarifyStore()

// Local selection state
const selectedChoices = ref<string[]>([])
const freeText = ref('')

// Reset selection when a new clarify arrives
watch(
  () => store.currentClarify?.id,
  () => {
    selectedChoices.value = []
    freeText.value = ''
  },
)

const hasChoices = computed(() => {
  const c = store.currentClarify
  return c !== null && c.choices.length > 0
})

const canSubmit = computed(() => {
  if (hasChoices.value) {
    return selectedChoices.value.length > 0
  }
  return freeText.value.trim().length > 0
})

const countdownPercent = computed(() => {
  return (store.countdown / 120) * 100
})

const countdownLabel = computed(() => {
  const mins = Math.floor(store.countdown / 60)
  const secs = store.countdown % 60
  return `${mins}:${secs.toString().padStart(2, '0')}`
})

const isTimedOut = computed(() => store.status === 'timed_out')
const isAnswered = computed(() => store.status === 'answered')
const isCancled = computed(() => store.status === 'canceled')
const isTerminal = computed(() => isTimedOut.value || isAnswered.value || isCancled.value)

function toggleChoice(choice: string) {
  const c = store.currentClarify
  if (!c) return

  if (c.multiSelect) {
    const idx = selectedChoices.value.indexOf(choice)
    if (idx >= 0) {
      selectedChoices.value.splice(idx, 1)
    } else {
      selectedChoices.value.push(choice)
    }
  } else {
    // Single-select: radio behavior - replace.
    selectedChoices.value = [choice]
  }
}

function handleSubmit() {
  if (!canSubmit.value) return
  if (hasChoices.value) {
    store.respond(selectedChoices.value)
  } else {
    store.respond([], freeText.value.trim())
  }
}

function handleCancel() {
  store.cancel()
}

// Prevent DialogContent's default close behavior.
// The modal must only close via submit/cancel.
function onOpenChange(open: boolean) {
  if (!open && !isTerminal.value) {
    // Re-open - we don't allow dismiss without action.
    // We'll prevent the close by not calling store.dismissCurrent().
  }
}
</script>

<template>
  <Dialog :open="store.isOpen" @update:open="onOpenChange">
    <DialogContent
      class="sm:max-w-md border-blue-500/30 dark:border-blue-400/20"
      :show-close-button="false"
    >
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2 text-blue-400">
          <AlertCircle class="h-5 w-5" />
          Clarification Needed
        </DialogTitle>
        <DialogDescription v-if="store.currentClarify">
          {{ store.currentClarify.question }}
        </DialogDescription>
      </DialogHeader>

      <!-- Terminal state overlay -->
      <div
        v-if="isTerminal"
        class="flex flex-col items-center gap-3 py-4"
      >
        <div
          v-if="isTimedOut"
          class="text-sm text-amber-400 font-medium"
        >
          Timed out - no response received
        </div>
        <div
          v-else-if="isAnswered"
          class="text-sm text-emerald-400 font-medium"
        >
          Response submitted
        </div>
        <div
          v-else-if="isCancled"
          class="text-sm text-muted-foreground font-medium"
        >
          Clarification dismissed
        </div>
      </div>

      <!-- Active form -->
      <div v-else class="flex flex-col gap-4">
        <!-- Choice mode -->
        <div v-if="hasChoices" class="flex flex-col gap-2">
          <div
            v-for="choice in store.currentClarify!.choices"
            :key="choice"
            class="flex items-center gap-3 rounded-md border border-border bg-muted/30 px-3 py-2.5 cursor-pointer transition-colors hover:bg-muted/60"
            :class="{
              'border-blue-500/50 bg-blue-500/10': selectedChoices.includes(choice),
            }"
            @click="toggleChoice(choice)"
          >
            <!-- Radio (single-select) -->
            <span
              v-if="!store.currentClarify!.multiSelect"
              class="flex h-4 w-4 shrink-0 items-center justify-center rounded-full border-2"
              :class="{
                'border-blue-400': selectedChoices.includes(choice),
                'border-muted-foreground/40': !selectedChoices.includes(choice),
              }"
            >
              <span
                v-if="selectedChoices.includes(choice)"
                class="h-2 w-2 rounded-full bg-blue-400"
              />
            </span>
            <!-- Checkbox (multi-select) -->
            <span
              v-else
              class="flex h-4 w-4 shrink-0 items-center justify-center rounded border-2"
              :class="{
                'border-blue-400 bg-blue-400': selectedChoices.includes(choice),
                'border-muted-foreground/40': !selectedChoices.includes(choice),
              }"
            >
              <svg
                v-if="selectedChoices.includes(choice)"
                class="h-3 w-3 text-white"
                viewBox="0 0 12 12"
                fill="none"
              >
                <path
                  d="M2 6l3 3 5-5"
                  stroke="currentColor"
                  stroke-width="2"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                />
              </svg>
            </span>
            <span class="text-sm">{{ choice }}</span>
          </div>
        </div>

        <!-- Free-text mode -->
        <div v-else>
          <textarea
            v-model="freeText"
            rows="3"
            class="w-full rounded-md border border-border bg-muted/30 px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-blue-500/50 focus:border-blue-500/50 resize-none"
            placeholder="Type your answer..."
          />
        </div>

        <!-- Buttons -->
        <DialogFooter class="flex-row justify-end gap-2 sm:gap-2">
          <Button
            variant="outline"
            size="sm"
            class="border-border text-muted-foreground hover:text-foreground"
            @click="handleCancel"
          >
            Cancel
          </Button>
          <Button
            size="sm"
            :disabled="!canSubmit"
            class="bg-blue-600 text-white hover:bg-blue-500 disabled:opacity-40"
            @click="handleSubmit"
          >
            Submit
          </Button>
        </DialogFooter>
      </div>

      <!-- Countdown timer -->
      <div
        v-if="!isTerminal"
        class="flex items-center gap-2 border-t border-border pt-3"
      >
        <Clock class="h-3.5 w-3.5 text-muted-foreground" />
        <Progress
          :model-value="countdownPercent"
          class="h-1.5 flex-1"
          :class="{
            'bg-amber-500/20': countdownPercent < 25,
            'bg-blue-500/20': countdownPercent >= 25,
          }"
        />
        <span
          class="text-xs tabular-nums min-w-[3ch] text-right"
          :class="{
            'text-amber-400 font-medium': countdownPercent < 25,
            'text-muted-foreground': countdownPercent >= 25,
          }"
        >
          {{ countdownLabel }}
        </span>
      </div>
    </DialogContent>
  </Dialog>
</template>
