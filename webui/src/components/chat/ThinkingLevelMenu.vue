<script setup lang="ts">
import { computed } from 'vue'
import { Brain } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { THINKING_LEVELS, useAppStore } from '@/stores/app'

const props = defineProps<{
  disabled?: boolean
}>()

const appStore = useAppStore()

// Radio-group binding: the store holds '' for the server default, which the
// group handles as a regular value via the matching item below.
const selected = computed({
  get: () => appStore.thinkingLevel,
  set: (v: string) => appStore.setThinkingLevel(v),
})

const buttonLabel = computed(() => appStore.thinkingLevelLabel(appStore.thinkingLevel))
const isOverride = computed(() => appStore.thinkingLevel !== '')
</script>

<template>
  <DropdownMenu>
    <DropdownMenuTrigger as-child>
      <Button
        variant="ghost"
        size="sm"
        class="h-10 shrink-0 gap-1.5 rounded-xl px-2 text-xs"
        :class="isOverride ? 'text-primary' : 'text-muted-foreground hover:text-foreground'"
        :disabled="props.disabled"
        :title="`Reasoning effort: ${buttonLabel} (click to change)`"
        aria-label="Reasoning effort"
      >
        <Brain class="h-4 w-4" />
        <span class="hidden max-w-16 truncate sm:inline">{{ buttonLabel }}</span>
      </Button>
    </DropdownMenuTrigger>
    <DropdownMenuContent align="start" class="w-56">
      <DropdownMenuLabel>Reasoning effort</DropdownMenuLabel>
      <DropdownMenuSeparator />
      <DropdownMenuRadioGroup v-model="selected">
        <DropdownMenuRadioItem
          v-for="opt in THINKING_LEVELS"
          :key="opt.value || 'default'"
          :value="opt.value"
        >
          <span class="flex w-full items-baseline gap-2">
            <span>{{ opt.label }}</span>
            <span class="ml-auto text-[11px] text-muted-foreground">{{ opt.hint }}</span>
          </span>
        </DropdownMenuRadioItem>
      </DropdownMenuRadioGroup>
    </DropdownMenuContent>
  </DropdownMenu>
</template>
