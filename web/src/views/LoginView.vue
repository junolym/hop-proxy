<template>
  <div class="min-h-screen flex items-center justify-center bg-gray-50">
    <Transition name="fade" mode="out-in">
      <!-- 登录选项加载门控（#73）：临时/扫码登录按钮依赖异步配置，SSO 跳转场景下
           门控整张卡片（含标题）——选项就绪前仅显示居中转圈，避免标题先出、内容后蹦
           导致的高度跳动；普通管理登录无此依赖，直接渲染完整卡片 -->
      <div v-if="optionsLoading && tempLoginTarget" key="loading" class="w-full max-w-sm px-4">
        <LoadingSpinner />
      </div>

      <div v-else key="card" class="w-full max-w-sm bg-white rounded-lg shadow-sm border border-gray-200 p-6 mx-4 sm:mx-0">
        <h1 class="text-2xl font-bold text-center mb-6">登录 HopProxy</h1>

        <!-- 步骤1：用户名密码 -->
        <form v-if="step === 'password'" @submit.prevent="handleLogin" class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
            <input v-model="username" type="text" required autofocus
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
            <input v-model="password" type="password" required
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>

          <div v-if="error" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ error }}</div>

          <button type="submit" :disabled="loading"
            class="w-full py-2 bg-blue-600 text-white text-sm font-medium rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ loading ? '登录中...' : '登录' }}
          </button>

          <!-- 通行密钥登录（#71）：Face ID / Touch ID / iCloud 通行密钥 -->
          <button type="button" @click="handlePasskeyLogin" :disabled="loading"
            class="w-full py-2 bg-white text-gray-700 text-sm font-medium rounded-md border border-gray-300 hover:bg-gray-50 disabled:opacity-50">
            {{ passkeyLoading ? '验证中...' : '使用通行密钥登录' }}
          </button>

          <!-- 临时登录入口：仅在系统设置开启且 next=/sso?redirect=... 时显示 -->
          <button v-if="tempLoginTarget && showTempLogin" type="button" @click="step = 'temp'"
            class="w-full py-2 bg-white text-gray-700 text-sm font-medium rounded-md border border-gray-300 hover:bg-gray-50">
            临时登录 {{ tempLoginTarget }}
          </button>

          <!-- 扫码登录入口：仅在系统设置开启且 next=/sso?redirect=... 时显示 -->
          <button v-if="tempLoginTarget && showQRLogin" type="button" @click="startQRLogin"
            class="w-full py-2 bg-white text-gray-700 text-sm font-medium rounded-md border border-gray-300 hover:bg-gray-50">
            扫码登录 {{ tempLoginTarget }}
          </button>
        </form>

        <!-- 步骤2：TOTP 验证码 -->
        <form v-else-if="step === 'totp'" @submit.prevent="handleVerifyTOTP" class="space-y-4">
          <div class="text-center mb-4">
            <p class="text-sm text-gray-600">请输入二次验证码</p>
            <p class="text-xs text-gray-400 mt-1">用户: {{ totpUsername }}</p>
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">验证码</label>
            <input v-model="totpCode" type="text" maxlength="6" required autofocus
              placeholder="6位数字"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>

          <div v-if="error" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ error }}</div>

          <button type="submit" :disabled="loading || totpCode.length !== 6"
            class="w-full py-2 bg-blue-600 text-white text-sm font-medium rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ loading ? '验证中...' : '验证' }}
          </button>

          <button type="button" @click="backToPassword" class="w-full py-2 text-gray-600 text-sm hover:text-gray-800">
            返回重新登录
          </button>
        </form>

        <!-- 步骤3：临时登录（PIN + TOTP token） -->
        <form v-else-if="step === 'temp'" @submit.prevent="handleTempLogin" class="space-y-4">
          <div class="text-center mb-4">
            <p class="text-sm text-gray-600">临时登录 {{ tempLoginTarget }}</p>
            <p class="text-xs text-gray-400 mt-1">输入用户名与 PIN+验证码（连起来 12 位数字）</p>
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
            <input v-model="tempUsername" type="text" required autofocus
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">PIN + 验证码</label>
            <input v-model="tempPINToken" type="password" inputmode="numeric" maxlength="12" required
              placeholder="12位数字"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500 text-center tracking-widest" />
          </div>

          <div v-if="error" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ error }}</div>

          <button type="submit" :disabled="loading"
            class="w-full py-2 bg-blue-600 text-white text-sm font-medium rounded-md hover:bg-blue-700 disabled:opacity-50">
            {{ loading ? '验证中...' : '临时登录' }}
          </button>

          <button type="button" @click="backToPassword" class="w-full py-2 text-gray-600 text-sm hover:text-gray-800">
            返回密码登录
          </button>
        </form>

        <!-- 步骤4：扫码登录（扫码设备授权指定应用） -->
        <div v-else-if="step === 'qr'" class="space-y-4">
          <div class="text-center mb-4">
            <p class="text-sm font-medium text-gray-700">扫码登录 {{ tempLoginTarget }}</p>
            <p class="text-xs text-gray-400 mt-1">使用其他设备扫码，确认后完成登录</p>
          </div>

          <div v-if="qrError" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ qrError }}</div>

          <div v-if="qrDataUrl" class="flex flex-col items-center">
            <img :src="qrDataUrl" alt="扫码登录二维码" class="w-52 h-52 border border-gray-200 rounded-md" />
            <p class="text-xs mt-3" :class="qrStatus === 'scanned' ? 'text-green-600' : 'text-gray-400'">
              {{ qrStatus === 'scanned' ? '已扫码，请在扫码设备上确认' : '请使用其他设备扫描二维码' }}
            </p>
            <p v-if="qrExpiresIn > 0" class="text-xs text-gray-400 mt-1">{{ qrExpiresIn }} 秒后失效</p>
          </div>

          <button type="button" @click="stopQRPolling(); backToPassword()"
            class="w-full py-2 text-gray-600 text-sm hover:text-gray-800">
            返回密码登录
          </button>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import QRCode from 'qrcode'
import { login, tempLogin, getLoginOptions } from '../api/auth'
import { verifyTOTP } from '../api/totp'
import { loginWithPasskey } from '../api/webauthn'
import { createQRLogin, getQRStatus } from '../api/qrlogin'
import LoadingSpinner from '../components/LoadingSpinner.vue'

type Step = 'password' | 'totp' | 'temp' | 'qr'
const step = ref<Step>('password')
const username = ref('')
const password = ref('')
const error = ref('')
const loading = ref(false)

// TOTP 相关
const totpUsername = ref('')
const totpCode = ref('')

// 临时登录相关
const tempUsername = ref('')
const tempPINToken = ref('') // PIN(6) + token(6) 连在一起，共 12 位

// 通行密钥登录相关
const passkeyLoading = ref(false)

// 扫码登录相关
const qrDataUrl = ref('')
const qrStatus = ref('pending')
const qrError = ref('')
const qrExpiresIn = ref(0)
let qrPollTimer: ReturnType<typeof setInterval> | null = null

// 直接从 URL 读取参数（独立入口，无 router）
const urlParams = new URLSearchParams(window.location.search)
const nextUrl = urlParams.get('next') || ''

// 登录方式开关（系统设置控制「临时登录」「扫码登录」入口显示，默认关闭）
const showTempLogin = ref(false)
const showQRLogin = ref(false)
// 登录选项是否仍在拉取（#73：SSO 场景下门控表单，避免按钮先缺席后突然蹦出）
const optionsLoading = ref(true)

onMounted(async () => {
  try {
    const res = await getLoginOptions()
    if (res.data) {
      showTempLogin.value = !!res.data.show_temp_login
      showQRLogin.value = !!res.data.show_qr_login
    }
  } catch {
    // 拉取失败按默认（不显示）处理
  } finally {
    optionsLoading.value = false
  }
})

// 从 next 中提取 SSO 路径。
// next 可能是路径形式（/sso?redirect=...）或完整 URL（http://host/sso?redirect=...），
// 后者在 client.ts 的 401 自动跳转时产生（encodeURIComponent(window.location.href)）。
function extractSsoPath(url: string): string {
  if (!url) return ''
  // 完整 URL → 取 pathname + search
  if (url.startsWith('http')) {
    try {
      const u = new URL(url)
      return u.pathname + u.search
    } catch {
      return ''
    }
  }
  return url
}

// 从 next=/sso?redirect=https%3A%2F%2Fai.app.example.com%2F 提取临时登录目标 host
const tempLoginTarget = computed(() => {
  const ssoPath = extractSsoPath(nextUrl)
  if (!ssoPath.startsWith('/sso')) return ''
  // /sso?redirect=... 或 /sso?...&redirect=...
  const inner = new URLSearchParams(ssoPath.split('?')[1] || '')
  const redirect = inner.get('redirect') || ''
  if (!redirect) return ''
  try {
    const u = new URL(redirect)
    return u.host
  } catch {
    return ''
  }
})

// 临时登录后跳转的 redirect 参数（直接从 next 内提取）
const tempRedirect = computed(() => {
  const ssoPath = extractSsoPath(nextUrl)
  if (!ssoPath.startsWith('/sso')) return ''
  const inner = new URLSearchParams(ssoPath.split('?')[1] || '')
  return inner.get('redirect') || ''
})

async function handleLogin() {
  error.value = ''
  loading.value = true
  try {
    const res = await login(username.value, password.value)
    if (res.error) {
      error.value = res.error
      return
    }

    // 检查是否需要 TOTP 验证
    if (res.data?.totp_required) {
      totpUsername.value = res.data.username || username.value
      step.value = 'totp'
      return
    }

    // 无需 TOTP，直接登录成功
    redirectAfterLogin()
  } catch {
    error.value = '登录失败，请检查网络'
  } finally {
    loading.value = false
  }
}

async function handleVerifyTOTP() {
  error.value = ''
  if (totpCode.value.length !== 6) {
    error.value = '请输入6位验证码'
    return
  }

  loading.value = true
  try {
    const res = await verifyTOTP(totpCode.value)
    if (res.error) {
      error.value = res.error
      return
    }

    // TOTP 验证成功
    redirectAfterLogin()
  } catch {
    error.value = '验证失败，请检查网络'
  } finally {
    loading.value = false
  }
}

async function handleTempLogin() {
  error.value = ''
  // tempPINToken: 前 6 位是 PIN，后 6 位是 TOTP 验证码
  const pinToken = tempPINToken.value
  if (!tempUsername.value || pinToken.length !== 12) {
    error.value = '请输入用户名与 12 位 PIN+验证码'
    return
  }
  if (!tempRedirect.value) {
    error.value = '缺少跳转目标'
    return
  }

  const pin = pinToken.slice(0, 6)
  const token = pinToken.slice(6, 12)

  loading.value = true
  try {
    const res = await tempLogin(tempUsername.value, pin, token, tempRedirect.value)
    if (res.error) {
      error.value = res.error
      return
    }
    // 临时登录成功：跳转到目标应用（SSO cookie 已下发）
    if (res.data?.redirect) {
      window.location.href = res.data.redirect
    } else {
      window.location.href = tempRedirect.value
    }
  } catch {
    error.value = '临时登录失败，请检查网络'
  } finally {
    loading.value = false
  }
}

function backToPassword() {
  step.value = 'password'
  totpCode.value = ''
  tempPINToken.value = ''
  error.value = ''
}

// 通行密钥登录（#71）：Face ID / Touch ID，免用户名
async function handlePasskeyLogin() {
  error.value = ''
  passkeyLoading.value = true
  try {
    const err = await loginWithPasskey()
    if (err) {
      error.value = err
      return
    }
    redirectAfterLogin()
  } catch {
    error.value = '通行密钥验证失败，请重试'
  } finally {
    passkeyLoading.value = false
  }
}

// 扫码登录（#71）：创建会话 → 渲染二维码 → 轮询状态
async function startQRLogin() {
  error.value = ''
  qrError.value = ''
  qrDataUrl.value = ''
  qrStatus.value = 'pending'
  if (!tempRedirect.value) {
    qrError.value = '缺少跳转目标'
    step.value = 'qr'
    return
  }

  step.value = 'qr'
  try {
    const res = await createQRLogin(tempRedirect.value)
    if (res.error || !res.data?.sid) {
      qrError.value = res.error || '创建扫码会话失败'
      return
    }

    // 二维码内容：授权页地址（与登录页同域，iPhone 扫码后 Safari 打开）
    const authorizeURL = `${window.location.origin}/qr-authorize?sid=${res.data.sid}`
    qrDataUrl.value = await QRCode.toDataURL(authorizeURL, { width: 208, margin: 1 })

    startQRPolling(res.data.sid)
  } catch {
    qrError.value = '创建扫码会话失败，请检查网络'
  }
}

// 轮询扫码状态；approved 时 SSO cookie 已随该响应下发，直接跳转目标应用
function startQRPolling(sid: string) {
  stopQRPolling()
  qrPollTimer = setInterval(async () => {
    try {
      const res = await getQRStatus(sid)
      if (res.error) {
        stopQRPolling()
        qrError.value = res.error
        return
      }
      const st = res.data?.state || 'expired'
      qrStatus.value = st
      qrExpiresIn.value = res.data?.expires_in || 0
      if (st === 'scanned' || st === 'pending') return
      stopQRPolling()
      if (st === 'approved') {
        window.location.href = res.data?.redirect || tempRedirect.value
      } else {
        // expired：二维码失效
        qrDataUrl.value = ''
        qrError.value = '二维码已失效，请返回重试'
      }
    } catch {
      // 网络抖动忽略，下个周期重试
    }
  }, 2000)
}

function stopQRPolling() {
  if (qrPollTimer) {
    clearInterval(qrPollTimer)
    qrPollTimer = null
  }
}

onUnmounted(stopQRPolling)

function redirectAfterLogin() {
  // 登录后跳到 next 参数指定的地址（如 SSO 页面），否则到客户端列表
  if (nextUrl) {
    window.location.href = nextUrl
  } else {
    window.location.href = '/apps'
  }
}
</script>
