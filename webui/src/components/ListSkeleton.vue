<script setup lang="ts">
// Shared loading skeleton for list rows (UX-09): the shape of the rows it
// replaces, so content swaps in without layout shift. Pulse is decorative -
// static bars remain under reduced motion.
withDefaults(
  defineProps<{
    rows?: number
  }>(),
  { rows: 6 },
)
</script>

<template>
  <div class="divide-y divide-border" aria-hidden="true">
    <div
      v-for="i in rows"
      :key="i"
      class="flex items-center gap-4 px-6 py-3"
    >
      <div class="h-2 w-2 shrink-0 rounded-full bg-muted" />
      <div class="min-w-0 flex-1 space-y-2">
        <div class="h-3.5 w-2/5 rounded bg-muted motion-safe:animate-pulse" />
        <div class="h-3 w-1/3 rounded bg-muted/70 motion-safe:animate-pulse" />
      </div>
    </div>
  </div>
</template>
