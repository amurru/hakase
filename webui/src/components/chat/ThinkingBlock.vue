<script setup lang="ts">
import { ref } from 'vue'
import { ChevronRight } from '@lucide/vue'

defineProps<{
  content: string
}>()

const isOpen = ref(false)
</script>

<template>
  <div v-if="content" class="my-2">
    <button
      class="flex items-center gap-1.5 text-xs text-muted-foreground/70 hover:text-muted-foreground transition-colors"
      @click="isOpen = !isOpen"
    >
      <ChevronRight
        class="h-3 w-3 transition-transform duration-200"
        :class="{ 'rotate-90': isOpen }"
      />
      <span>Thinking</span>
    </button>
    <Transition name="expand">
      <div
        v-show="isOpen"
        class="mt-2 ml-4 border-l-2 border-muted-foreground/20 pl-3 text-xs text-muted-foreground/60 font-mono whitespace-pre-wrap leading-relaxed max-h-64 overflow-y-auto"
      >
        {{ content }}
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.expand-enter-active,
.expand-leave-active {
  transition: all 0.2s ease;
  overflow: hidden;
}

.expand-enter-from,
.expand-leave-to {
  opacity: 0;
  max-height: 0;
  margin-top: 0;
  padding-top: 0;
}

.expand-enter-to,
.expand-leave-from {
  opacity: 1;
  max-height: 256px;
}
</style>
