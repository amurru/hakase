<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Button } from '@/components/ui/button'
import { Loader2 } from '@lucide/vue'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()

const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

async function handleSubmit() {
  if (!username.value || !password.value) {
    error.value = 'Please enter both username and password'
    return
  }

  loading.value = true
  error.value = ''

  const result = await authStore.login(username.value, password.value)

  if (result.ok) {
    const redirect = (route.query.redirect as string) || '/chat'
    router.push(redirect)
  } else {
    error.value = result.error || 'Login failed'
  }

  loading.value = false
}
</script>

<template>
  <div class="flex min-h-screen items-center justify-center bg-background px-4">
    <Card class="w-full max-w-sm">
      <CardHeader class="text-center">
        <CardTitle class="text-xl">Hakase</CardTitle>
        <CardDescription>Sign in to your account</CardDescription>
      </CardHeader>
      <CardContent>
        <form
          class="flex flex-col gap-4"
          @submit.prevent="handleSubmit"
        >
          <div
            v-if="error"
            class="rounded-md bg-destructive/15 px-4 py-3 text-sm text-destructive-foreground"
          >
            {{ error }}
          </div>

          <div class="flex flex-col gap-2">
            <Label for="username">Username</Label>
            <Input
              id="username"
              v-model="username"
              placeholder="Enter your username"
              autocomplete="username"
              :disabled="loading"
              @keyup.enter="handleSubmit"
            />
          </div>

          <div class="flex flex-col gap-2">
            <Label for="password">Password</Label>
            <Input
              id="password"
              v-model="password"
              type="password"
              placeholder="Enter your password"
              autocomplete="current-password"
              :disabled="loading"
              @keyup.enter="handleSubmit"
            />
          </div>

          <Button
            type="submit"
            class="w-full"
            :disabled="loading"
          >
            <Loader2
              v-if="loading"
              class="h-4 w-4 animate-spin"
            />
            {{ loading ? 'Signing in...' : 'Sign in' }}
          </Button>
        </form>
      </CardContent>
    </Card>
  </div>
</template>
