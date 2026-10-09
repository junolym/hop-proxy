<template>
  <div class="min-h-screen flex items-center justify-center bg-gray-50">
    <div class="w-full max-w-md bg-white rounded-lg shadow-sm border border-gray-200 p-6 mx-4 sm:mx-0">
      <h1 class="text-xl font-bold text-center mb-2">HopProxy 初始化</h1>
      <p class="text-sm text-gray-500 text-center mb-6">首次使用，请创建管理员账户并配置站点信息</p>

      <form @submit.prevent="handleSubmit" class="space-y-4">
        <div>
          <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
          <input v-model="form.username" type="text" required
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
        </div>
        <div>
          <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
          <input v-model="form.password" type="password" required minlength="6"
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
        </div>
        <div>
          <label class="block text-sm font-medium text-gray-700 mb-1">站点名称</label>
          <input v-model="form.site_name" type="text" placeholder="HopProxy"
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
        </div>
        <div>
          <label class="block text-sm font-medium text-gray-700 mb-1">管理域名</label>
          <input v-model="form.admin_domain" type="text" required placeholder="proxy.example.com"
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
        </div>
        <div>
          <label class="block text-sm font-medium text-gray-700 mb-1">代理域名</label>
          <input v-model="form.proxy_domain" type="text" required placeholder="proxy.example.com"
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          <p class="text-xs text-gray-400 mt-1">子域名的上级域名，如 *.proxy.example.com</p>
        </div>

        <div v-if="error" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ error }}</div>

        <button type="submit" :disabled="loading"
          class="w-full py-2 bg-blue-600 text-white text-sm font-medium rounded-md hover:bg-blue-700 disabled:opacity-50">
          {{ loading ? '初始化中...' : '完成初始化' }}
        </button>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { initSetup } from '../api/setup'
import { useSiteStore } from '../stores/site'

const siteStore = useSiteStore()

const form = ref({
  username: '',
  password: '',
  site_name: '',
  admin_domain: '',
  proxy_domain: '',
})
const error = ref('')
const loading = ref(false)

async function handleSubmit() {
  error.value = ''
  loading.value = true
  try {
    const res = await initSetup(form.value)
    if (res.error) {
      error.value = res.error
      return
    }
    siteStore.initialized = true
    // /login 是独立入口（login.html），不在 SPA 路由表内，必须整页跳转
    window.location.href = '/login'
  } catch {
    error.value = '初始化失败，请检查网络'
  } finally {
    loading.value = false
  }
}
</script>
