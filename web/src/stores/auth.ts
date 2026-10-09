import { defineStore } from 'pinia'
import { ref } from 'vue'
import { getMe, logout as apiLogout } from '../api/auth'

export const useAuthStore = defineStore('auth', () => {
  const user = ref<{ id: number; username: string; is_admin: boolean; role: string } | null>(null)
  const isLoggedIn = ref(false)

  async function checkAuth() {
    try {
      const res = await getMe()
      if (res.data) {
        user.value = res.data
        isLoggedIn.value = true
        return true
      }
    } catch {
      // ignore
    }
    isLoggedIn.value = false
    user.value = null
    return false
  }

  async function logout() {
    // 通知服务端清除所有 SSO 会话和 Session Cookie
    try { await apiLogout() } catch { /* ignore */ }
    user.value = null
    isLoggedIn.value = false
  }

  return { user, isLoggedIn, checkAuth, logout }
})
