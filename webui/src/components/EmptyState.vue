<script setup lang="ts">
// Shared empty state (UX-09): icon + title + optional description + optional
// action. One pattern for every list/board view so empty screens read as one
// system instead of per-view copy.
defineProps<{
  title: string
  description?: string
  // Compact fits narrow panes (file tree, viewer) instead of full views.
  compact?: boolean
}>()
</script>

<template>
  <div
    class="flex flex-col items-center justify-center gap-2 px-6 text-center text-muted-foreground"
    :class="compact ? 'py-6' : 'py-16'"
  >
    <div class="mb-1 text-muted-foreground/70">
      <slot name="icon" />
    </div>
    <p class="text-sm font-medium text-foreground/80">{{ title }}</p>
    <p v-if="description" class="max-w-sm text-xs text-muted-foreground/80">
      {{ description }}
    </p>
    <div v-if="$slots.action" class="mt-2">
      <slot name="action" />
    </div>
  </div>
</template>
