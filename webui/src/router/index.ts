import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/login',
      name: 'login',
      component: () => import('@/views/LoginView.vue'),
      meta: { requiresAuth: false },
    },
    {
      path: '/',
      component: () => import('@/views/Layout.vue'),
      meta: { requiresAuth: true },
      children: [
        {
          path: '',
          redirect: '/chat',
        },
        {
          path: 'chat',
          name: 'chat',
          component: () => import('@/views/ChatView.vue'),
        },
        {
          path: 'sessions',
          name: 'sessions',
          component: () => import('@/views/SessionsView.vue'),
        },
        {
          path: 'tasks',
          name: 'tasks',
          component: () => import('@/views/TasksView.vue'),
        },
        {
          path: 'knowledge',
          name: 'knowledge',
          component: () => import('@/views/KnowledgeView.vue'),
        },
        {
          path: 'mcp',
          name: 'mcp',
          component: () => import('@/views/MCPView.vue'),
        },
        {
          path: 'cron',
          name: 'cron',
          component: () => import('@/views/CronView.vue'),
        },
        {
          path: 'files',
          name: 'files',
          component: () => import('@/views/FilesView.vue'),
        },
      ],
    },
  ],
})

router.beforeEach((to, _from, next) => {
  const authStore = useAuthStore()
  if (to.meta.requiresAuth && !authStore.isLoggedIn) {
    next({ path: '/login', query: { redirect: to.fullPath } })
  } else {
    next()
  }
})

export default router
