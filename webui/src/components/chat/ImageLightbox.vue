<script setup lang="ts">
import { ref } from 'vue'
import { Dialog, DialogContent } from '@/components/ui/dialog'

const open = ref(false)
const src = ref('')
const alt = ref('')

function show(imageSrc: string, imageAlt = '') {
  src.value = imageSrc
  alt.value = imageAlt
  open.value = true
}

function close() {
  open.value = false
}

defineExpose({ show, close })
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => (open = v)">
    <DialogContent
      class="w-auto max-w-[calc(100vw-2rem)] gap-0 border-border/60 p-2 shadow-2xl sm:max-w-[90vw] sm:p-3"
      :show-close-button="true"
    >
      <img
        :src="src"
        :alt="alt"
        class="mx-auto block max-h-[85vh] max-w-full rounded-md object-contain"
      />
    </DialogContent>
  </Dialog>
</template>
