<template>
  <div class="flex items-center justify-center min-h-[60vh]">
    <div class="text-center max-w-md">
      <div class="text-5xl mb-4">
        <svg class="mx-auto h-16 w-16 text-gray-400" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor">
          <path stroke-linecap="round" stroke-linejoin="round" d="M16.5 10.5V6.75a4.5 4.5 0 10-9 0v3.75m-.75 11.25h10.5a2.25 2.25 0 002.25-2.25v-6.75a2.25 2.25 0 00-2.25-2.25H6.75a2.25 2.25 0 00-2.25 2.25v6.75a2.25 2.25 0 002.25 2.25z" />
        </svg>
      </div>
      <h2 class="text-xl font-bold text-gray-900 mb-2">访客用户</h2>
      <p class="text-sm text-gray-600 leading-relaxed">
        您当前是访客用户，仅可通过 SSO 访问已授权的应用。<br />
        如需更多权限，请联系管理员。
      </p>
      <button
        @click="handleLogout"
        class="mt-6 px-4 py-2 bg-gray-600 text-white text-sm rounded-md hover:bg-gray-700"
      >
        退出登录
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted } from 'vue'
import { useAuthStore } from '../stores/auth'

const authStore = useAuthStore()

onMounted(async () => {
  const loggedIn = await authStore.checkAuth()
  if (!loggedIn) {
    window.location.href = '/login'
    return
  }
  if (authStore.user?.role !== 'guest') {
    window.location.href = '/apps'
  }
})

async function handleLogout() {
  await authStore.logout()
  window.location.href = '/login'
}
</script>
