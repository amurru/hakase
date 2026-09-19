<script setup lang="ts">
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import type { RailTickVM } from '@/lib/messageRail'

const props = defineProps<{
  ticks: RailTickVM[]
  activeId: string | null
}>()

const emit = defineEmits<{ select: [id: string] }>()

function tickClass(tick: RailTickVM): string {
  if (tick.id === props.activeId) {
    return 'w-6 bg-foreground'
  }
  return 'w-2.5 bg-muted-foreground/40 group-hover/tick:w-6 group-hover/tick:bg-foreground/80 group-focus-visible/tick:w-6 group-focus-visible/tick:bg-foreground/80'
}

function formatTime(ts: number): string {
  if (!ts) return ''
  return new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

function tickLabel(tick: RailTickVM): string {
  const preview = tick.preview.length > 80 ? tick.preview.slice(0, 80) + '…' : tick.preview
  return preview ? `Jump to message: ${preview}` : 'Jump to message'
}
</script>

<template>
  <!-- Overview rail over the transcript's left gutter. The lane itself never
       captures the pointer - only tick buttons do - so text selection and
       scrolling next to it are unaffected. Hidden on touch-sized viewports,
       where hover previews don't exist. -->
  <TooltipProvider :delay-duration="150">
    <nav
      v-if="ticks.length"
      class="pointer-events-none absolute inset-y-0 left-0 z-10 hidden w-4 md:block"
      aria-label="Jump to message"
    >
      <Tooltip v-for="tick in ticks" :key="tick.id">
        <TooltipTrigger as-child>
          <button
            type="button"
            class="group/tick pointer-events-auto absolute left-1 flex h-3 w-5 -translate-y-1/2 cursor-pointer items-center rounded-sm outline-none focus-visible:border-ring focus-visible:ring-ring/50 focus-visible:ring-3"
            :style="{ top: `${tick.y}px` }"
            :aria-label="tickLabel(tick)"
            @click="emit('select', tick.id)"
          >
            <span
              class="block h-0.5 rounded-full transition-[width,background-color] duration-200 ease-out motion-reduce:transition-none"
              :class="tickClass(tick)"
            />
          </button>
        </TooltipTrigger>
        <TooltipContent
          side="right"
          :side-offset="6"
          class="max-w-72 rounded-lg px-3 py-2 text-left"
        >
          <p class="whitespace-pre-wrap break-words text-xs leading-snug">
            {{ tick.preview }}
          </p>
          <p v-if="formatTime(tick.timestamp)" class="mt-1 text-[10px] opacity-60">
            {{ formatTime(tick.timestamp) }}
          </p>
        </TooltipContent>
      </Tooltip>
    </nav>
  </TooltipProvider>
</template>
