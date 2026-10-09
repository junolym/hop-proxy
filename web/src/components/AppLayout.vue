<template>
  <div class="min-h-screen flex flex-col md:flex-row bg-gray-50">

    <!-- 移动端顶部导航栏 -->
    <header class="md:hidden flex items-center justify-between px-4 py-3 bg-white border-b border-gray-200 sticky top-0 z-30">
      <h1 class="text-base font-bold text-gray-800">HopProxy</h1>
      <span class="text-sm text-gray-500">{{ authStore.user?.username }}</span>
    </header>

    <!-- 桌面端侧边栏 -->
    <aside class="hidden md:flex w-56 bg-white border-r border-gray-200 flex-col flex-shrink-0 sticky top-0 h-screen">
      <div class="p-4 border-b border-gray-200">
        <h1 class="text-lg font-bold text-gray-800">HopProxy</h1>
      </div>
      <nav class="flex-1 p-3 space-y-1">
        <router-link v-for="item in visibleNavItems" :key="item.to" :to="item.to"
          class="flex items-center px-3 py-2 text-sm rounded-md transition-colors"
          :class="$route.path === item.to ? 'bg-blue-50 text-blue-700 font-medium' : 'text-gray-600 hover:bg-gray-50'">
          {{ item.label }}
        </router-link>
      </nav>
      <div class="p-3 border-t border-gray-200">
        <div class="flex items-center justify-between px-3 py-2">
          <span class="text-sm text-gray-500">{{ authStore.user?.username }}</span>
          <button @click="handleLogout" class="text-xs text-gray-400 hover:text-red-500">退出</button>
        </div>
      </div>
    </aside>

    <!-- 主内容区 -->
    <main class="flex-1 p-4 md:p-6 overflow-auto pb-20 md:pb-6">
      <router-view />
      <footer class="mt-8 pt-4 border-t border-gray-200 text-center text-xs text-gray-400">
        HopProxy v{{ serverVersion || 'dev' }}
      </footer>
    </main>

    <!-- 移动端底部 Tab 导航 -->
    <nav class="md:hidden fixed bottom-0 left-0 right-0 bg-white border-t border-gray-200 z-30 flex">
      <router-link v-for="item in visibleNavItems" :key="item.to" :to="item.to"
        class="flex-1 flex flex-col items-center py-2 text-xs transition-colors"
        :class="$route.path === item.to ? 'text-blue-600' : 'text-gray-400'">
        <span class="text-lg leading-none mb-0.5">{{ item.icon }}</span>
        {{ item.label }}
      </router-link>
      <button @click="handleLogout" class="flex-1 flex flex-col items-center py-2 text-xs text-gray-400">
        <span class="text-lg leading-none mb-0.5">⬚</span>
        退出
      </button>
    </nav>

  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useAuthStore } from '../stores/auth'
import { getServerVersion } from '../api/apps'

const authStore = useAuthStore()
const serverVersion = ref('')

const allNavItems = [
  { to: '/apps', label: '应用', icon: '◈', adminOnly: false },
  { to: '/share-codes', label: '分享码', icon: '↗', adminOnly: false },
  { to: '/clients', label: '客户端', icon: '⬡', adminOnly: false },
  { to: '/access-tokens', label: '访问票据', icon: '⊟', adminOnly: false },
  { to: '/agent-keys', label: '代理密钥', icon: '⛨', adminOnly: false },
  { to: '/settings', label: '用户设置', icon: '⚙', adminOnly: false },
  { to: '/admin', label: '系统管理', icon: '⚑', adminOnly: true },
  { to: '/sessions', label: '会话管理', icon: '◫', adminOnly: true },
  { to: '/logs', label: '系统日志', icon: '▤', adminOnly: true },
]

const visibleNavItems = computed(() =>
  allNavItems.filter(item => !item.adminOnly || authStore.user?.is_admin)
)

async function handleLogout() {
  await authStore.logout()
  // /login 是独立入口（login.html），不在 SPA 路由表内，必须整页跳转
  window.location.href = '/login'
}

onMounted(async () => {
  try {
    const res = await getServerVersion()
    if (res.data) {
      serverVersion.value = res.data.version || res.data.commit || ''
    }
  } catch {
    // 开发模式下版本 API 可能不可用
  }
})
</script>
