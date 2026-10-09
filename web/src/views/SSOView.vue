<template>
  <div class="min-h-screen flex items-center justify-center bg-gray-50">
    <div class="w-full max-w-sm bg-white rounded-lg shadow-sm border border-gray-200 p-6 mx-4 sm:mx-0">

      <!-- 加载中 -->
      <div v-if="loading" class="text-center py-4 text-gray-400 text-sm">加载中...</div>

      <!-- 错误提示 -->
      <div v-else-if="loadError" class="text-center py-4">
        <div class="text-red-600 text-sm mb-3">{{ loadError }}</div>
        <button @click="handleCancel"
          class="py-2 px-4 bg-gray-100 text-gray-600 text-sm rounded-md hover:bg-gray-200">
          返回
        </button>
      </div>

      <!-- 已登录：确认授权 -->
      <template v-else-if="authUser">
        <div class="text-center mb-6">
          <div class="w-12 h-12 rounded-full bg-blue-100 flex items-center justify-center mx-auto mb-3">
            <svg class="w-6 h-6 text-blue-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m5.618-4.016A11.955 11.955 0 0112 2.944a11.955 11.955 0 01-8.618 3.04A12.02 12.02 0 003 9c0 5.591 3.824 10.29 9 11.622 5.176-1.332 9-6.03 9-11.622 0-1.042-.133-2.052-.382-3.016" />
            </svg>
          </div>
          <h2 class="text-lg font-bold text-gray-900">授权访问</h2>
          <p class="text-sm text-gray-500 mt-1">
            以 <span class="font-semibold text-gray-800">{{ authUser.username }}</span> 的身份访问
          </p>
          <p class="text-base font-semibold text-blue-700 mt-2">{{ appName || '该应用' }}</p>
        </div>

        <div v-if="error" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md mb-3">{{ error }}</div>

        <!-- TOTP 二次验证输入 -->
        <div v-if="totpRequired" class="mb-4">
          <div class="text-center mb-3">
            <p class="text-sm text-gray-600">该应用要求二次验证</p>
            <p class="text-xs text-gray-400 mt-1">请输入 TOTP 验证码</p>
          </div>
          <input v-model="totpCode" type="text" maxlength="6" inputmode="numeric"
            placeholder="6位数字"
            class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
        </div>

        <!-- 通行密钥二次验证提示 (#76) -->
        <div v-else-if="secondFactor === 'passkey'" class="mb-4 text-center">
          <p class="text-sm text-gray-600">该应用要求二次验证</p>
          <p class="text-xs text-gray-400 mt-1">点击下方按钮，使用通行密钥完成验证（生物识别或设备密码等）</p>
        </div>

        <div class="space-y-3">
          <button @click="handleAuthorize" :disabled="authorizing || (totpRequired && totpCode.length !== 6)"
            class="w-full py-2.5 bg-blue-600 text-white text-sm font-medium rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ authorizing ? '验证中...' : (secondFactor === 'passkey' ? '验证并授权' : (totpRequired ? '验证并授权' : '确认访问')) }}
          </button>
          <button @click="handleCancel"
            class="w-full py-2.5 bg-white text-gray-600 text-sm font-medium rounded-md border border-gray-300 hover:bg-gray-50">
            取消
          </button>
        </div>
      </template>

    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { getMe } from '../api/auth'
import { getAppBySubdomain, ssoAuthorize } from '../api/sso'
import { performAssertion } from '../api/webauthn'

const loading = ref(true)
const authorizing = ref(false)
const error = ref('')
const loadError = ref('')
const authUser = ref<{ id: number; username: string } | null>(null)
const appName = ref('')

// 应用二次验证方式：''=无 / 'totp' / 'passkey'（加载应用信息时预判，#76）
const secondFactor = ref('')

// TOTP 二次验证状态
const totpRequired = ref(false)
const totpCode = ref('')

// 直接从 URL 读取参数（独立入口，无 router）
const urlParams = new URLSearchParams(window.location.search)
const redirectTarget = urlParams.get('redirect') || ''
const appId = ref(0)

onMounted(async () => {
  // 验证 redirect 参数
  if (!redirectTarget) {
    loadError.value = '缺少跳转地址'
    loading.value = false
    return
  }

  // 从 redirect URL 提取子域名
  let subdomain = ''
  try {
    const redirectURL = new URL(redirectTarget)
    const host = redirectURL.host.split(':')[0] // 移除端口
    const parts = host.split('.')
    if (parts.length >= 1) {
      subdomain = parts[0]
    }
  } catch {
    loadError.value = '无效的跳转地址'
    loading.value = false
    return
  }

  if (!subdomain) {
    loadError.value = '无法从跳转地址提取应用信息'
    loading.value = false
    return
  }

  // 查询应用信息
  try {
    const appRes = await getAppBySubdomain(subdomain)
    if (appRes.error) {
      // 根据错误类型显示不同提示
      if (appRes.extra?.error_code === 'app_not_found') {
        loadError.value = '应用不存在'
      } else if (appRes.extra?.error_code === 'app_disabled') {
        loadError.value = '应用已禁用'
      } else {
        loadError.value = appRes.error
      }
      loading.value = false
      return
    }
    if (appRes.data) {
      appId.value = appRes.data.id
      appName.value = appRes.data.name
      secondFactor.value = appRes.data.second_factor || ''
      if (!appRes.data.require_auth) {
        loadError.value = '该应用未启用 SSO 认证'
        loading.value = false
        return
      }
    }
  } catch {
    loadError.value = '获取应用信息失败'
    loading.value = false
    return
  }

  // 验证 Session Cookie 是否有效
  // 如果未登录，静默重定向到登录页（不显示错误信息）
  try {
    const res = await getMe()
    if (res.data) {
      authUser.value = res.data
    } else {
      // session 失效，跳登录
      const ssoURL = encodeURIComponent(window.location.href)
      window.location.href = `/login?next=${ssoURL}`
      return
    }
  } catch {
    // 未登录或 session 失效，静默重定向到登录页
    const ssoURL = encodeURIComponent(window.location.href)
    window.location.href = `/login?next=${ssoURL}`
    return
  } finally {
    loading.value = false
  }
})

// 确认授权：为此应用创建票据，然后跳转回目标地址
async function handleAuthorize() {
  if (!redirectTarget) {
    error.value = '缺少跳转地址'
    return
  }

  // 验证 redirect 地址不能指向当前管理域名（防止开放重定向）
  try {
    const targetURL = new URL(redirectTarget)
    if (targetURL.host === window.location.host) {
      error.value = '无效的跳转地址'
      return
    }
  } catch {
    error.value = '无效的跳转地址'
    return
  }

  // 通行密钥二次验证：断言流程闭环（第一阶段取参数 → 浏览器断言 → 第二阶段提交）(#76)
  if (secondFactor.value === 'passkey' && !totpRequired.value) {
    await runPasskeyAuthorize()
    return
  }

  authorizing.value = true
  error.value = ''
  try {
    // 应用要求 TOTP 时，第二次提交带验证码
    const code = totpRequired.value ? totpCode.value : undefined
    const res = await ssoAuthorize(redirectTarget, code)
    if (res.error) {
      error.value = res.error
      return
    }
    // 后端返回 second_factor='totp' 时，切换到 TOTP 输入 UI
    if (res.data?.second_factor === 'totp') {
      totpRequired.value = true
      totpCode.value = ''
      return
    }
    window.location.href = redirectTarget
  } catch {
    error.value = '授权失败，请重试'
  } finally {
    authorizing.value = false
  }
}

// 通行密钥二次验证（#76）：passkey 仅作确认手势，授权对象始终由服务端从 redirect 解析
async function runPasskeyAuthorize() {
  authorizing.value = true
  error.value = ''
  try {
    // 第一阶段：获取断言参数
    const beginRes = await ssoAuthorize(redirectTarget)
    if (beginRes.error) {
      error.value = beginRes.error
      return
    }
    // 应用二次验证方式可能在页面加载后变化，按服务端实际返回分流
    if (beginRes.data?.second_factor === 'totp') {
      totpRequired.value = true
      return
    }
    const opts = beginRes.data?.options
    if (!opts) {
      error.value = '获取验证参数失败'
      return
    }
    // 浏览器断言（生物识别或设备密码等，取消返回 null）
    const assertion = await performAssertion(opts)
    if (!assertion) {
      error.value = '已取消验证，请重试'
      return
    }
    // 第二阶段：提交断言完成授权
    const res = await ssoAuthorize(redirectTarget, undefined, assertion)
    if (res.error) {
      error.value = res.error
      return
    }
    window.location.href = redirectTarget
  } catch {
    error.value = '授权失败，请重试'
  } finally {
    authorizing.value = false
  }
}

// 取消：直接回到管理后台
function handleCancel() {
  window.location.href = '/apps'
}
</script>
