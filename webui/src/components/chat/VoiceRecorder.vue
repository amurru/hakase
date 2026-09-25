<script setup lang="ts">
import { ref, onUnmounted } from 'vue'
import { Mic, Square, Loader2, X } from '@lucide/vue'
import { Button } from '@/components/ui/button'
import { transcribeAudio } from '@/lib/api'
import { toast } from 'vue-sonner'

const emit = defineEmits<{
  transcription: [text: string]
}>()

defineProps<{
  disabled?: boolean
}>()

const isRecording = ref(false)
const isTranscribing = ref(false)
const recordSeconds = ref(0)

let mediaRecorder: MediaRecorder | null = null
let audioChunks: Blob[] = []
let timerInterval: ReturnType<typeof setInterval> | null = null

function formatTime(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${s < 10 ? '0' : ''}${s}`
}

async function startRecording() {
  if (isRecording.value || isTranscribing.value) return

  try {
    const stream = await navigator.mediaDevices.getUserMedia({ audio: true })
    audioChunks = []

    // Choose supported MIME type
    let mimeType = ''
    if (MediaRecorder.isTypeSupported('audio/webm;codecs=opus')) {
      mimeType = 'audio/webm;codecs=opus'
    } else if (MediaRecorder.isTypeSupported('audio/webm')) {
      mimeType = 'audio/webm'
    } else if (MediaRecorder.isTypeSupported('audio/ogg;codecs=opus')) {
      mimeType = 'audio/ogg;codecs=opus'
    } else if (MediaRecorder.isTypeSupported('audio/mp4')) {
      mimeType = 'audio/mp4'
    }

    mediaRecorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream)

    mediaRecorder.ondataavailable = (event: BlobEvent) => {
      if (event.data && event.data.size > 0) {
        audioChunks.push(event.data)
      }
    }

    mediaRecorder.onstop = async () => {
      // Stop all audio tracks to release microphone
      stream.getTracks().forEach((track) => track.stop())

      if (audioChunks.length === 0) return

      const blobType = mediaRecorder?.mimeType || 'audio/webm'
      const audioBlob = new Blob(audioChunks, { type: blobType })

      if (audioBlob.size === 0) return

      isTranscribing.value = true
      try {
        const result = await transcribeAudio(audioBlob)
        if (result.text && result.text.trim()) {
          emit('transcription', result.text.trim())
          toast.success('Voice transcribed successfully')
        } else {
          toast.info('No speech detected in audio')
        }
      } catch (err: unknown) {
        const msg = err instanceof Error ? err.message : 'Transcription failed'
        toast.error(msg)
      } finally {
        isTranscribing.value = false
      }
    }

    mediaRecorder.start(250) // Collect chunks every 250ms
    isRecording.value = true
    recordSeconds.value = 0

    timerInterval = setInterval(() => {
      recordSeconds.value++
    }, 1000)
  } catch (err: unknown) {
    toast.error('Microphone access denied or unavailable')
    console.error('Error starting voice recording:', err)
  }
}

function stopRecording() {
  if (!isRecording.value || !mediaRecorder) return
  isRecording.value = false
  if (timerInterval) {
    clearInterval(timerInterval)
    timerInterval = null
  }
  if (mediaRecorder.state !== 'inactive') {
    mediaRecorder.stop()
  }
}

function cancelRecording() {
  if (!isRecording.value || !mediaRecorder) return
  isRecording.value = false
  if (timerInterval) {
    clearInterval(timerInterval)
    timerInterval = null
  }
  // Clear chunks so onstop does nothing
  audioChunks = []
  if (mediaRecorder.state !== 'inactive') {
    mediaRecorder.stop()
  }
}

onUnmounted(() => {
  if (timerInterval) clearInterval(timerInterval)
  if (mediaRecorder && mediaRecorder.state !== 'inactive') {
    mediaRecorder.stop()
  }
})
</script>

<template>
  <div class="inline-flex items-center gap-1.5">
    <!-- Active Recording Control UI -->
    <div v-if="isRecording" class="flex items-center gap-2 rounded-xl bg-destructive/10 px-3 py-1.5 text-xs text-destructive animate-pulse">
      <span class="h-2 w-2 rounded-full bg-destructive animate-ping"></span>
      <span class="font-mono font-medium">{{ formatTime(recordSeconds) }}</span>
      <Button
        variant="ghost"
        size="icon"
        class="h-6 w-6 rounded-lg text-destructive hover:bg-destructive/20"
        title="Stop & transcribe"
        @click="stopRecording"
      >
        <Square class="h-3.5 w-3.5 fill-current" />
      </Button>
      <Button
        variant="ghost"
        size="icon"
        class="h-6 w-6 rounded-lg text-muted-foreground hover:text-foreground"
        title="Cancel recording"
        @click="cancelRecording"
      >
        <X class="h-3.5 w-3.5" />
      </Button>
    </div>

    <!-- Transcribing spinner UI -->
    <div v-else-if="isTranscribing" class="flex items-center gap-2 rounded-xl bg-muted px-3 py-1.5 text-xs text-muted-foreground">
      <Loader2 class="h-3.5 w-3.5 animate-spin text-primary" />
      <span>Transcribing...</span>
    </div>

    <!-- Normal Dictation Mic Button -->
    <Button
      v-else
      variant="ghost"
      size="icon"
      class="h-10 w-10 shrink-0 rounded-xl text-muted-foreground hover:text-foreground"
      :disabled="disabled"
      title="Dictate voice prompt"
      @click="startRecording"
    >
      <Mic class="h-4 w-4" />
    </Button>
  </div>
</template>
