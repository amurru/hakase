<script setup lang="ts">
import { computed, onMounted, ref, onUnmounted } from 'vue'
import { toast } from 'vue-sonner'
import {
  fetchChannelsStatus,
  requestPairingCode,
  revokeTelegramUser,
  saveTelegramToken,
  clearTelegramToken,
  setTelegramEnabled,
  fetchSecretFlags,
  formatCountdown,
  secondsUntil,
  shortId,
  type TelegramChannelStatus,
} from '@/lib/channels'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { RefreshCw, KeyRound, Trash2, Copy, Send, Power, Loader2, AlertCircle } from '@lucide/vue'

const status = ref<TelegramChannelStatus | null>(null)
const hasToken = ref(false)
const loading = ref(false)
const error = ref('')

const pairingCode = ref('')
const pairingExpires = ref<string | undefined>(undefined)
const nowTick = ref(0)
let tickTimer: ReturnType<typeof setInterval> | null = null

const tokenInput = ref('')
const tokenPending = ref(false)
const enablePending = ref(false)
const revokingUser = ref<number | null>(null)

const pairingCountdown = computed(() => {
  void nowTick.value
  return formatCountdown(secondsUntil(pairingExpires.value))
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const all = await fetchChannelsStatus()
    status.value = all.telegram ?? null
    const flags = await fetchSecretFlags()
    hasToken.value = flags.has_telegram_bot_token === true
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : 'Failed to load channel status'
  } finally {
    loading.value = false
  }
}

async function generateCode() {
  try {
    const resp = await requestPairingCode()
    pairingCode.value = resp.code
    pairingExpires.value = resp.expires_at
    if (!tickTimer) {
      tickTimer = setInterval(() => (nowTick.value += 1), 1000)
    }
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to generate pairing code')
  }
}

function dismissCode() {
  pairingCode.value = ''
  pairingExpires.value = undefined
  if (tickTimer) {
    clearInterval(tickTimer)
    tickTimer = null
  }
}

async function copyCode() {
  try {
    await navigator.clipboard.writeText(pairingCode.value)
    toast.success('Pairing code copied')
  } catch {
    toast.error('Clipboard unavailable')
  }
}

async function revoke(userId: number) {
  revokingUser.value = userId
  try {
    await revokeTelegramUser(userId)
    toast.success(`Unpaired user ${userId}`)
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to revoke user')
  } finally {
    revokingUser.value = null
  }
}

async function saveToken() {
  const token = tokenInput.value.trim()
  if (!token) {
    toast.error('Enter a bot token first')
    return
  }
  tokenPending.value = true
  try {
    await saveTelegramToken(token)
    tokenInput.value = ''
    hasToken.value = true
    toast.success('Bot token saved. Restart the server to apply.')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to save token')
  } finally {
    tokenPending.value = false
  }
}

async function removeToken() {
  tokenPending.value = true
  try {
    await clearTelegramToken()
    hasToken.value = false
    toast.success('Bot token cleared. Restart the server to apply.')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to clear token')
  } finally {
    tokenPending.value = false
  }
}

async function toggleEnabled() {
  if (!status.value) return
  enablePending.value = true
  try {
    await setTelegramEnabled(!status.value.enabled)
    toast.success(
      `Telegram ${!status.value.enabled ? 'enabled' : 'disabled'} in config. Restart the server to apply.`,
    )
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to update config')
  } finally {
    enablePending.value = false
  }
}

onMounted(load)
onUnmounted(() => {
  if (tickTimer) clearInterval(tickTimer)
})
</script>

<template>
  <div class="mx-auto max-w-4xl space-y-4 p-4">
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-semibold">Channels</h1>
        <p class="text-sm text-muted-foreground">
          Chat transports that can talk to hakase remotely.
        </p>
      </div>
      <Button variant="outline" size="sm" :disabled="loading" @click="load">
        <RefreshCw class="h-4 w-4" :class="{ 'animate-spin': loading }" />
        Refresh
      </Button>
    </div>

    <div v-if="error" class="flex items-center gap-2 rounded-md border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-400">
      <AlertCircle class="h-4 w-4" />
      {{ error }}
    </div>

    <Card v-if="status">
      <CardHeader>
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <Send class="h-5 w-5" />
            <CardTitle>Telegram</CardTitle>
          </div>
          <div class="flex items-center gap-2">
            <Badge :variant="status.enabled ? 'default' : 'secondary'">
              {{ status.enabled ? 'enabled' : 'disabled' }}
            </Badge>
            <Badge :variant="status.running ? 'default' : 'outline'">
              {{ status.running ? 'running' : 'not running' }}
            </Badge>
          </div>
        </div>
        <CardDescription>
          Prompt hakase, watch progress, answer approvals, and manage tasks from your phone.
        </CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <p v-if="status.enabled && !status.running" class="flex items-center gap-2 rounded-md border border-yellow-500/30 bg-yellow-500/10 p-3 text-sm text-yellow-400">
          <AlertCircle class="h-4 w-4" />
          Enabled in config but not running in this process — restart the server (or fix the bot token).
        </p>

        <!-- Pairing -->
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <h3 class="text-sm font-medium">Pairing</h3>
            <Button size="sm" variant="outline" @click="generateCode">
              <KeyRound class="h-4 w-4" />
              Generate pairing code
            </Button>
          </div>
          <div v-if="pairingCode" class="flex items-center gap-3 rounded-md border p-3">
            <span class="font-mono text-2xl font-bold tracking-widest">{{ pairingCode }}</span>
            <Badge variant="secondary">expires in {{ pairingCountdown }}</Badge>
            <Button size="sm" variant="ghost" @click="copyCode"><Copy class="h-4 w-4" /></Button>
            <div class="ml-auto text-xs text-muted-foreground">
              Send <code class="font-mono">/start {{ pairingCode }}</code> to your bot, then dismiss.
            </div>
            <Button size="sm" variant="secondary" @click="dismissCode">Done</Button>
          </div>
          <p v-else-if="status.pending_pairing" class="text-sm text-muted-foreground">
            A pairing code is pending (expires
            {{ new Date(status.pending_pairing.expires_at).toLocaleString() }}). Generate to see it again.
          </p>
          <p v-else class="text-sm text-muted-foreground">
            No pairing code outstanding. New users send <code class="font-mono">/start &lt;code&gt;</code> to pair.
          </p>
        </div>

        <!-- Paired users -->
        <div class="space-y-2">
          <h3 class="text-sm font-medium">Paired users ({{ status.paired_users.length }})</h3>
          <div v-if="status.paired_users.length === 0" class="text-sm text-muted-foreground">
            Nobody paired yet — deny-by-default until a code is used.
          </div>
          <div
            v-for="u in status.paired_users"
            :key="u.user_id"
            class="flex items-center justify-between rounded-md border p-2 text-sm"
          >
            <div>
              <span class="font-medium">{{ u.username || 'unnamed' }}</span>
              <span class="ml-2 font-mono text-xs text-muted-foreground">{{ u.user_id }}</span>
              <span class="ml-2 text-xs text-muted-foreground">paired {{ new Date(u.paired_at).toLocaleString() }}</span>
            </div>
            <Button
              size="sm"
              variant="ghost"
              class="text-red-400 hover:text-red-300"
              :disabled="revokingUser === u.user_id"
              @click="revoke(u.user_id)"
            >
              <Loader2 v-if="revokingUser === u.user_id" class="h-4 w-4 animate-spin" />
              <Trash2 v-else class="h-4 w-4" />
              Revoke
            </Button>
          </div>
        </div>

        <!-- Chat bindings -->
        <div class="space-y-2">
          <h3 class="text-sm font-medium">Chats ({{ status.chats.length }})</h3>
          <div v-if="status.chats.length === 0" class="text-sm text-muted-foreground">
            No chats have talked to the bot yet.
          </div>
          <div
            v-for="c in status.chats"
            :key="c.chat_id"
            class="flex items-center justify-between rounded-md border p-2 text-sm"
          >
            <div class="font-mono text-xs">{{ c.chat_id }}</div>
            <div class="flex items-center gap-2">
              <Badge variant="outline">{{ c.notify ? 'notify on' : 'notify off' }}</Badge>
              <span class="text-xs text-muted-foreground">session {{ shortId(c.session_id) }}</span>
            </div>
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- Setup: token + enablement (config API, applies on restart) -->
    <Card>
      <CardHeader>
        <CardTitle class="text-base">Setup</CardTitle>
        <CardDescription>
          Bot token from
          <a class="underline" href="https://t.me/BotFather" target="_blank" rel="noreferrer">@BotFather</a>.
          Token is write-only — it is never shown after saving. Changes apply on server restart.
        </CardDescription>
      </CardHeader>
      <CardContent class="space-y-3">
        <div class="flex items-center gap-2">
          <Input
            v-model="tokenInput"
            type="password"
            placeholder="123456789:ABCdef…"
            class="max-w-md font-mono"
            @keyup.enter="saveToken"
          />
          <Button size="sm" :disabled="tokenPending" @click="saveToken">
            <Loader2 v-if="tokenPending" class="h-4 w-4 animate-spin" />
            Save token
          </Button>
          <Button v-if="hasToken" size="sm" variant="ghost" class="text-red-400 hover:text-red-300" :disabled="tokenPending" @click="removeToken">
            Clear
          </Button>
          <Badge v-if="hasToken" variant="secondary">token set</Badge>
        </div>
        <div class="flex items-center gap-2">
          <Button size="sm" variant="outline" :disabled="enablePending || !status" @click="toggleEnabled">
            <Power class="h-4 w-4" />
            {{ status?.enabled ? 'Disable channel' : 'Enable channel' }}
          </Button>
          <span v-if="!hasToken" class="text-xs text-muted-foreground">A bot token is required before enabling.</span>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
