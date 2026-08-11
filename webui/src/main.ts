import { createApp } from 'vue'
import { createPinia } from 'pinia'
import router from './router'
import App from './App.vue'
import { useAuthStore } from './stores/auth'
import './assets/globals.css'

const app = createApp(App)
const pinia = createPinia()
app.use(pinia)
app.use(router)

// Verify session via /api/me before mounting so the router guard
// has accurate auth state on first navigation.
const authStore = useAuthStore()
await authStore.init()

app.mount('#app')
