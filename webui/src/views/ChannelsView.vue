<script setup lang="ts">
import { computed, onMounted, ref, onUnmounted } from 'vue'
import { toast } from 'vue-sonner'
import {
  fetchChannelsStatus,
  requestPairingCode,
  revokeChannelUser,
  saveTelegramToken,
  clearTelegramToken,
  setTelegramEnabled,
  saveDiscordToken,
  clearDiscordToken,
  setDiscordEnabled,
  fetchSecretFlags,
  formatCountdown,
  secondsUntil,
  shortId,
  type ChannelsStatus,
  type ChannelStatus,
} from '@/lib/channels'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import EmptyState from '@/components/EmptyState.vue'
import { RefreshCw, KeyRound, Trash2, Copy, Send, Power, Loader2, AlertCircle, MessageCircle } from '@lucide/vue'

const allStatus = ref<ChannelsStatus | null>(null)
const hasTelegramToken = ref(false)
const hasDiscordToken = ref(false)
const loading = ref(false)
const error = ref('')

const pairingCode = ref('')
const pairingExpires = ref<string | undefined>(undefined)
const nowTick = ref(0)
let tickTimer: ReturnType<typeof setInterval> | null = null

const telegramTokenInput = ref('')
const discordTokenInput = ref('')
const tokenPending = ref<'telegram' | 'discord' | null>(null)
const enablePending = ref<'telegram' | 'discord' | null>(null)
const revoking = ref<{ channel: string; userId: number } | null>(null)

const telegram = computed<ChannelStatus | null>(() => allStatus.value?.telegram ?? null)
const discord = computed<ChannelStatus | null>(() => allStatus.value?.discord ?? null)
const pendingPairing = computed(
  () => telegram.value?.pending_pairing ?? discord.value?.pending_pairing ?? null,
)

const pairingCountdown = computed(() => {
  void nowTick.value
  return formatCountdown(secondsUntil(pairingExpires.value))
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    allStatus.value = await fetchChannelsStatus()
    const flags = await fetchSecretFlags()
    hasTelegramToken.value = flags.has_telegram_bot_token === true
    hasDiscordToken.value = flags.has_discord_bot_token === true
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

async function revoke(channel: string, userId: number) {
  revoking.value = { channel, userId }
  try {
    await revokeChannelUser(userId, channel)
    toast.success(`Unpaired user ${userId} (${channel})`)
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to revoke user')
  } finally {
    revoking.value = null
  }
}

function isRevoking(channel: string, userId: number) {
  return revoking.value?.channel === channel && revoking.value?.userId === userId
}

async function saveToken(channel: 'telegram' | 'discord') {
  const raw = channel === 'telegram' ? telegramTokenInput.value : discordTokenInput.value
  const token = raw.trim()
  if (!token) {
    toast.error('Enter a bot token first')
    return
  }
  tokenPending.value = channel
  try {
    if (channel === 'telegram') {
      await saveTelegramToken(token)
      telegramTokenInput.value = ''
      hasTelegramToken.value = true
    } else {
      await saveDiscordToken(token)
      discordTokenInput.value = ''
      hasDiscordToken.value = true
    }
    toast.success('Bot token saved. Restart the server to apply.')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to save token')
  } finally {
    tokenPending.value = null
  }
}

async function removeToken(channel: 'telegram' | 'discord') {
  tokenPending.value = channel
  try {
    if (channel === 'telegram') {
      await clearTelegramToken()
      hasTelegramToken.value = false
    } else {
      await clearDiscordToken()
      hasDiscordToken.value = false
    }
    toast.success('Bot token cleared. Restart the server to apply.')
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to clear token')
  } finally {
    tokenPending.value = null
  }
}

async function toggleEnabled(channel: 'telegram' | 'discord') {
  const st = channel === 'telegram' ? telegram.value : discord.value
  if (!st) return
  enablePending.value = channel
  try {
    if (channel === 'telegram') {
      await setTelegramEnabled(!st.enabled)
    } else {
      await setDiscordEnabled(!st.enabled)
    }
    const label = channel === 'telegram' ? 'Telegram' : 'Discord'
    toast.success(
      `${label} ${!st.enabled ? 'enabled' : 'disabled'} in config. Restart the server to apply.`,
    )
    await load()
  } catch (e: unknown) {
    toast.error(e instanceof Error ? e.message : 'Failed to update config')
  } finally {
    enablePending.value = null
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

    <!-- Shared pairing: one code pairs either transport -->
    <Card>
      <CardHeader>
        <div class="flex items-center gap-2">
          <KeyRound class="h-5 w-5" />
          <CardTitle class="text-base">Pairing</CardTitle>
        </div>
        <CardDescription>
          One code pairs either transport. Telegram: send
          <code class="font-mono">/start &lt;code&gt;</code> to your bot.
          Discord: DM <code class="font-mono">pair &lt;code&gt;</code> to your bot.
        </CardDescription>
      </CardHeader>
      <CardContent class="space-y-2">
        <div class="flex items-center justify-between">
          <span class="text-sm text-muted-foreground">New users pair with a one-time code.</span>
          <Button size="sm" variant="outline" @click="generateCode">
            <KeyRound class="h-4 w-4" />
            Generate pairing code
          </Button>
        </div>
        <div v-if="pairingCode" class="flex items-center gap-3 rounded-md border p-3">
          <span class="font-mono text-2xl font-bold tracking-widest">{{ pairingCode }}</span>
          <Badge variant="secondary">expires in {{ pairingCountdown }}</Badge>
          <Button size="sm" variant="ghost" @click="copyCode"><Copy class="h-4 w-4" /></Button>
          <Button size="sm" variant="secondary" class="ml-auto" @click="dismissCode">Done</Button>
        </div>
        <p v-else-if="pendingPairing" class="text-sm text-muted-foreground">
          A pairing code is pending (expires
          {{ new Date(pendingPairing.expires_at).toLocaleString() }}). Generate to see it again.
        </p>
        <p v-else class="text-sm text-muted-foreground">
          No pairing code outstanding.
        </p>
      </CardContent>
    </Card>

    <!-- Telegram transport -->
    <Card v-if="telegram">
      <CardHeader>
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <Send class="h-5 w-5" />
            <CardTitle>Telegram</CardTitle>
          </div>
          <div class="flex items-center gap-2">
            <Badge :variant="telegram.enabled ? 'default' : 'secondary'">
              {{ telegram.enabled ? 'enabled' : 'disabled' }}
            </Badge>
            <Badge :variant="telegram.running ? 'default' : 'outline'">
              {{ telegram.running ? 'running' : 'not running' }}
            </Badge>
          </div>
        </div>
        <CardDescription>
          Prompt hakase, watch progress, answer approvals, and manage tasks from your phone.
        </CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <p v-if="telegram.enabled && !telegram.running" class="flex items-center gap-2 rounded-md border border-yellow-500/30 bg-yellow-500/10 p-3 text-sm text-yellow-400">
          <AlertCircle class="h-4 w-4" />
          Enabled in config but not running in this process — restart the server (or fix the bot token).
        </p>

        <div class="space-y-2">
          <h3 class="text-sm font-medium">Paired users ({{ telegram.paired_users.length }})</h3>
          <div v-if="telegram.paired_users.length === 0" class="text-sm text-muted-foreground">
            Nobody paired yet — deny-by-default until a code is used.
          </div>
          <div
            v-for="u in telegram.paired_users"
            :key="'tg-' + u.user_id"
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
              :disabled="isRevoking('telegram', u.user_id)"
              @click="revoke('telegram', u.user_id)"
            >
              <Loader2 v-if="isRevoking('telegram', u.user_id)" class="h-4 w-4 animate-spin" />
              <Trash2 v-else class="h-4 w-4" />
              Revoke
            </Button>
          </div>
        </div>

        <div class="space-y-2">
          <h3 class="text-sm font-medium">Chats ({{ telegram.chats.length }})</h3>
          <EmptyState
            v-if="telegram.chats.length === 0"
            compact
            title="No chats yet"
            description="No chats have talked to the bot yet."
          >
            <template #icon>
              <Send class="h-6 w-6 opacity-50" />
            </template>
          </EmptyState>
          <div
            v-for="c in telegram.chats"
            :key="'tg-chat-' + c.chat_id"
            class="flex items-center justify-between rounded-md border p-2 text-sm"
          >
            <div class="font-mono text-xs">{{ c.chat_id }}</div>
            <div class="flex items-center gap-2">
              <Badge variant="outline">{{ c.notify ? 'notify on' : 'notify off' }}</Badge>
              <span class="text-xs text-muted-foreground">session {{ shortId(c.session_id) }}</span>
            </div>
          </div>
        </div>

        <div class="space-y-3 rounded-md border p-3">
          <p class="text-sm text-muted-foreground">
            Bot token from
            <a class="underline" href="https://t.me/BotFather" target="_blank" rel="noreferrer">@BotFather</a>.
            Token is write-only — it is never shown after saving. Changes apply on server restart.
          </p>
          <div class="flex items-center gap-2">
            <Input
              v-model="telegramTokenInput"
              type="password"
              placeholder="123456789:ABCdef…"
              class="max-w-md font-mono"
              @keyup.enter="saveToken('telegram')"
            />
            <Button size="sm" :disabled="tokenPending === 'telegram'" @click="saveToken('telegram')">
              <Loader2 v-if="tokenPending === 'telegram'" class="h-4 w-4 animate-spin" />
              Save token
            </Button>
            <Button v-if="hasTelegramToken" size="sm" variant="ghost" class="text-red-400 hover:text-red-300" :disabled="tokenPending === 'telegram'" @click="removeToken('telegram')">
              Clear
            </Button>
            <Badge v-if="hasTelegramToken" variant="secondary">token set</Badge>
          </div>
          <div class="flex items-center gap-2">
            <Button size="sm" variant="outline" :disabled="enablePending === 'telegram'" @click="toggleEnabled('telegram')">
              <Power class="h-4 w-4" />
              {{ telegram.enabled ? 'Disable channel' : 'Enable channel' }}
            </Button>
            <span v-if="!hasTelegramToken" class="text-xs text-muted-foreground">A bot token is required before enabling.</span>
          </div>
        </div>
      </CardContent>
    </Card>

    <!-- Discord transport -->
    <Card v-if="discord">
      <CardHeader>
        <div class="flex items-center justify-between">
          <div class="flex items-center gap-2">
            <MessageCircle class="h-5 w-5" />
            <CardTitle>Discord</CardTitle>
          </div>
          <div class="flex items-center gap-2">
            <Badge :variant="discord.enabled ? 'default' : 'secondary'">
              {{ discord.enabled ? 'enabled' : 'disabled' }}
            </Badge>
            <Badge :variant="discord.running ? 'default' : 'outline'">
              {{ discord.running ? 'running' : 'not running' }}
            </Badge>
          </div>
        </div>
        <CardDescription>
          DM-only bot over the gateway (no public endpoint needed). Pair with
          <code class="font-mono">pair &lt;code&gt;</code>, then chat in DMs.
        </CardDescription>
      </CardHeader>
      <CardContent class="space-y-4">
        <p v-if="discord.enabled && !discord.running" class="flex items-center gap-2 rounded-md border border-yellow-500/30 bg-yellow-500/10 p-3 text-sm text-yellow-400">
          <AlertCircle class="h-4 w-4" />
          Enabled in config but not running in this process — restart the server (or fix the bot token).
        </p>

        <div class="space-y-2">
          <h3 class="text-sm font-medium">Paired users ({{ discord.paired_users.length }})</h3>
          <div v-if="discord.paired_users.length === 0" class="text-sm text-muted-foreground">
            Nobody paired yet — deny-by-default until a code is used.
          </div>
          <div
            v-for="u in discord.paired_users"
            :key="'dc-' + u.user_id"
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
              :disabled="isRevoking('discord', u.user_id)"
              @click="revoke('discord', u.user_id)"
            >
              <Loader2 v-if="isRevoking('discord', u.user_id)" class="h-4 w-4 animate-spin" />
              <Trash2 v-else class="h-4 w-4" />
              Revoke
            </Button>
          </div>
        </div>

        <div class="space-y-2">
          <h3 class="text-sm font-medium">Chats ({{ discord.chats.length }})</h3>
          <EmptyState
            v-if="discord.chats.length === 0"
            compact
            title="No DMs yet"
            description="No Discord users have talked to the bot yet."
          >
            <template #icon>
              <MessageCircle class="h-6 w-6 opacity-50" />
            </template>
          </EmptyState>
          <div
            v-for="c in discord.chats"
            :key="'dc-chat-' + c.chat_id"
            class="flex items-center justify-between rounded-md border p-2 text-sm"
          >
            <div class="font-mono text-xs">{{ c.chat_id }}</div>
            <div class="flex items-center gap-2">
              <Badge variant="outline">{{ c.notify ? 'notify on' : 'notify off' }}</Badge>
              <span class="text-xs text-muted-foreground">session {{ shortId(c.session_id) }}</span>
            </div>
          </div>
        </div>

        <div class="space-y-3 rounded-md border p-3">
          <p class="text-sm text-muted-foreground">
            Bot token from the
            <a class="underline" href="https://discord.com/developers/applications" target="_blank" rel="noreferrer">Discord Developer Portal</a>
            (application - Bot - Token). Token is write-only — it is never shown after saving.
            Changes apply on server restart.
          </p>
          <div class="flex items-center gap-2">
            <Input
              v-model="discordTokenInput"
              type="password"
              placeholder="MT… (bot token)"
              class="max-w-md font-mono"
              @keyup.enter="saveToken('discord')"
            />
            <Button size="sm" :disabled="tokenPending === 'discord'" @click="saveToken('discord')">
              <Loader2 v-if="tokenPending === 'discord'" class="h-4 w-4 animate-spin" />
              Save token
            </Button>
            <Button v-if="hasDiscordToken" size="sm" variant="ghost" class="text-red-400 hover:text-red-300" :disabled="tokenPending === 'discord'" @click="removeToken('discord')">
              Clear
            </Button>
            <Badge v-if="hasDiscordToken" variant="secondary">token set</Badge>
          </div>
          <div class="flex items-center gap-2">
            <Button size="sm" variant="outline" :disabled="enablePending === 'discord'" @click="toggleEnabled('discord')">
              <Power class="h-4 w-4" />
              {{ discord.enabled ? 'Disable channel' : 'Enable channel' }}
            </Button>
            <span v-if="!hasDiscordToken" class="text-xs text-muted-foreground">A bot token is required before enabling.</span>
          </div>
        </div>
      </CardContent>
    </Card>
  </div>
</template>
