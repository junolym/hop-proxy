import { createRouter, createWebHistory } from 'vue-router'
import { useSiteStore } from '../stores/site'
import { useAuthStore } from '../stores/auth'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/setup',
      name: 'setup',
      component: () => import('../views/SetupView.vue'),
      meta: { requiresAuth: false },
    },
    {
      path: '/',
      component: () => import('../components/AppLayout.vue'),
      meta: { requiresAuth: true },
      children: [
        { path: '', redirect: '/apps' },
        { path: 'apps', name: 'apps', component: () => import('../views/AppsView.vue') },
        { path: 'clients', name: 'clients', component: () => import('../views/ClientsView.vue') },
        { path: 'clients/:id/conns', name: 'client-conns', component: () => import('../views/ClientConnsView.vue') },
        { path: 'access-tokens', name: 'access-tokens', component: () => import('../views/AccessTokensView.vue') },
        { path: 'share-codes', name: 'share-codes', component: () => import('../views/ShareCodesView.vue') },
        { path: 'agent-keys', name: 'agent-keys', component: () => import('../views/AgentKeysView.vue') },
        { path: 'settings', name: 'settings', component: () => import('../views/SettingsView.vue') },
        { path: 'admin', name: 'admin', component: () => import('../views/AdminView.vue'), meta: { requiresAdmin: true } },
        { path: 'sessions', name: 'sessions', component: () => import('../views/SessionsView.vue'), meta: { requiresAdmin: true } },
        { path: 'logs', name: 'logs', component: () => import('../views/LogsView.vue'), meta: { requiresAdmin: true } },
      ],
    },
    // /login 和 /sso 已拆分为独立入口，不再在 SPA 路由中定义
    // 后端直接返回 login.html 和 sso.html
  ],
})

router.beforeEach(async (to) => {
  const siteStore = useSiteStore()
  const authStore = useAuthStore()

  if (siteStore.initialized === null) {
    await siteStore.checkInitialized()
  }

  if (!siteStore.initialized) {
    if (to.name !== 'setup') return { name: 'setup' }
    return
  }

  if (to.name === 'setup') {
    return { name: 'apps' }
  }

  if (to.meta.requiresAuth || to.meta.requiresAdmin) {
    if (!authStore.isLoggedIn) {
      await authStore.checkAuth()
    }
    if (!authStore.isLoggedIn) {
      // 重定向到独立登录页
      window.location.href = '/login?next=' + encodeURIComponent(to.fullPath)
      return false
    }
  }

    // 访客用户拦截：禁止访问管理面板
  if (authStore.user?.role === 'guest') {
    if (to.meta.requiresAdmin) {
      window.location.href = '/guest'
      return false
    }
    // 访客访问 /apps、/clients、/settings 时跳转到独立提示页
    const guestBlocked = ['apps', 'clients', 'client-conns', 'access-tokens', 'share-codes', 'agent-keys', 'settings', 'logs']
    if (guestBlocked.includes(to.name as string)) {
      window.location.href = '/guest'
      return false
    }
  }

  // 管理员页面权限守卫
  if (to.meta.requiresAdmin && !authStore.user?.is_admin) {
    return { name: 'apps' }
  }
})

export default router
