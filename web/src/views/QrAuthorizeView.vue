<template>
  <div class="min-h-screen flex items-center justify-center bg-gray-50">
    <div class="w-full max-w-sm bg-white rounded-lg shadow-sm border border-gray-200 p-6 mx-4 sm:mx-0">

      <!-- 加载中 -->
      <div v-if="loading" class="text-center py-4 text-gray-400 text-sm">加载中...</div>

      <!-- 错误提示 -->
      <div v-else-if="loadError" class="text-center py-4">
        <div class="w-12 h-12 rounded-full bg-red-100 flex items-center justify-center mx-auto mb-3">
          <svg class="w-6 h-6 text-red-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
          </svg>
        </div>
        <div class="text-red-600 text-sm mb-3">{{ loadError }}</div>
        <button @click="handleCancel"
          class="py-2 px-4 bg-gray-100 text-gray-600 text-sm rounded-md hover:bg-gray-200">
          返回
        </button>
      </div>

      <!-- 授权确认 -->
      <template v-else>
        <div class="text-center mb-6">
          <div class="w-12 h-12 rounded-full bg-blue-100 flex items-center justify-center mx-auto mb-3">
            <svg class="w-6 h-6 text-blue-600" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v1m6 11h2m-6 0h-2v4m0-11v3m0 0h.01M12 12h4.01M16 20h4M4 12h4m-4 8h4m-4-4h4m4-4h.01" />
            </svg>
          </div>
          <h2 class="text-lg font-bold text-gray-900">扫码授权登录</h2>
          <p class="text-sm text-gray-500 mt-1">确认在另一台设备上临时访问以下地址？</p>
          <!-- 完整域名与应用名都展示：通配符应用也能看出具体授权哪个子域名 (#74) -->
          <p class="text-base font-semibold text-blue-700 mt-2 break-all">{{ fullDomain || appName || '该应用' }}</p>
          <p v-if="fullDomain && appName" class="text-sm text-gray-500 mt-1">应用：{{ appName }}</p>
        </div>

        <!-- 电脑侧信息（帮助识别发起方） -->
        <div class="bg-gray-50 rounded-md px-3 py-2 mb-4 text-xs text-gray-500 space-y-1">
          <p>发起设备 IP：{{ creatorIP || '未知' }}</p>
          <p v-if="creatorUA" class="truncate" :title="creatorUA">浏览器：{{ creatorUA }}</p>
        </div>

        <div v-if="error" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md mb-3">{{ error }}</div>

        <div class="space-y-3">
          <button @click="handleApprove" :disabled="approving || approved"
            class="w-full py-2.5 bg-blue-600 text-white text-sm font-medium rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ approved ? '已授权' : (approving ? '验证中...' : '确认授权') }}
          </button>
          <button @click="handleCancel" :disabled="approving"
            class="w-full py-2.5 bg-white text-gray-600 text-sm font-medium rounded-md border border-gray-300 hover:bg-gray-50 disabled:opacity-50">
            {{ approved ? '完成' : '取消' }}
          </button>
        </div>
        <p class="text-xs text-gray-400 text-center mt-4">授权后发起设备将获得该应用的临时访问权限，不会登录管理后台</p>
      </template>

    </div>
  </div>
</template>

<script setup lang="ts">
// 【设计原则】本页是扫码授权的安全核心：授权语义（目标应用、具体子域名、
// 发起方 IP/UA）由服务端按 sid 渲染，第三方无法伪造。passkey 在此只做
// 确认手势（用户在场 + 生物验证），不做授权主体——因此扫码端能确切知道
// 自己授权的是什么，避免直接用通行密钥跨设备扫码时的授权范围混淆
// （以为是单应用临时授权，实际可能是管理后台登录）。详见 qrlogin.go 注释。
import { ref, onMounted } from 'vue'
import { getMe } from '../api/auth'
import { getQRInfo, approveQRLogin } from '../api/webauthn'

const loading = ref(true)
const loadError = ref('')
const error = ref('')
const approving = ref(false)
const approved = ref(false)

const appName = ref('')
const fullDomain = ref('')
const creatorIP = ref('')
const creatorUA = ref('')

// 直接从 URL 读取参数（独立入口，无 router）
const urlParams = new URLSearchParams(window.location.search)
const sid = urlParams.get('sid') || ''

onMounted(async () => {
  if (!sid) {
    loadError.value = '缺少扫码参数'
    loading.value = false
    return
  }

  // 验证管理 session：未登录则跳登录页（授权回来后继续）
  try {
    const meRes = await getMe()
    if (!meRes.data) {
      window.location.href = '/login?next=' + encodeURIComponent(window.location.href)
      return
    }
  } catch {
    window.location.href = '/login?next=' + encodeURIComponent(window.location.href)
    return
  }

  // 加载扫码会话信息
  try {
    const res = await getQRInfo(sid)
    if (res.error || !res.data) {
      loadError.value = res.error || '二维码无效或已过期'
      loading.value = false
      return
    }
    appName.value = res.data.app_name
    fullDomain.value = res.data.full_domain || ''
    creatorIP.value = res.data.creator_ip
    creatorUA.value = res.data.creator_ua
  } catch {
    loadError.value = '加载扫码信息失败'
  } finally {
    loading.value = false
  }
})

// 确认授权：passkey 断言（Face ID）+ 1 分钟时效窗口
async function handleApprove() {
  error.value = ''
  approving.value = true
  try {
    const err = await approveQRLogin(sid)
    if (err) {
      error.value = err
      return
    }
    approved.value = true
  } catch {
    error.value = '授权失败，请重试'
  } finally {
    approving.value = false
  }
}

function handleCancel() {
  window.location.href = '/apps'
}
</script>
