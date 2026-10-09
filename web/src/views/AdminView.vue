<template>
  <div>
    <h2 class="text-lg md:text-xl font-bold mb-4">系统管理</h2>

    <div class="max-w-2xl space-y-4">
      <!-- 站点设置 -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-4">站点设置</h3>
        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="loading" />
        <div v-else>
        <form @submit.prevent="handleSaveSettings" class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">站点名称</label>
            <input v-model="settings.site_name" type="text"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">管理域名</label>
            <input v-model="settings.admin_domain" type="text"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">代理域名</label>
            <input v-model="settings.proxy_domain" type="text"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">会话有效期</label>
            <input v-model="settings.session_ttl" type="text" placeholder="24h"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            <p class="mt-1.5 text-xs text-gray-400">
              登录状态的最长时限；会话级 Cookie 模式下同样生效（浏览器常开超时需重新登录）。
            </p>
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">会话保留期（天）</label>
            <input v-model="settings.audit_retention_days" type="text" placeholder="90"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            <p class="mt-1.5 text-xs text-gray-400">
              已失效的登录会话与应用授权的保留天数，超出后由每日清理任务删除（0 = 永久保留）；有效会话不受影响。
            </p>
          </div>
          <div>
            <label class="inline-flex items-center cursor-pointer">
              <input type="checkbox" v-model="sessionCookieSession" class="sr-only peer" />
              <div class="w-11 h-6 bg-gray-200 rounded-full peer peer-checked:bg-blue-600 peer-focus:ring-2 peer-focus:ring-blue-500 transition-colors relative after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-transform peer-checked:after:translate-x-5"></div>
              <span class="ml-3 text-sm text-gray-700">会话级 Cookie（关闭浏览器后失效）</span>
            </label>
            <p class="mt-1.5 text-xs text-gray-400">
              开启后登录 Cookie 不写入磁盘，关闭浏览器即失效，重新打开需重新登录；
              浏览器开启「恢复上次会话」时可能一并恢复会话 Cookie，属浏览器行为。
            </p>
          </div>

          <!-- 登录页登录方式（#71）：控制应用 SSO 跳登录页时的入口显示，默认关闭 -->
          <div class="pt-3 border-t border-gray-100 space-y-4">
            <p class="text-sm font-medium text-gray-700">登录页登录方式</p>
            <p class="text-xs text-gray-400">控制应用 SSO 跳转到登录页时是否显示以下入口，默认关闭。</p>

            <div>
              <label class="inline-flex items-center cursor-pointer">
                <input type="checkbox" v-model="settings.show_temp_login" class="sr-only peer" />
                <div class="w-11 h-6 bg-gray-200 rounded-full peer peer-checked:bg-blue-600 peer-focus:ring-2 peer-focus:ring-blue-500 transition-colors relative after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-transform peer-checked:after:translate-x-5"></div>
                <span class="ml-3 text-sm text-gray-700">显示「临时登录」入口</span>
              </label>
              <div v-if="settings.show_temp_login" class="mt-2 flex items-center gap-3 flex-wrap">
                <p class="text-xs text-amber-600 bg-amber-50 px-3 py-2 rounded-md flex-1 min-w-fit">
                  使用前需先在 个人设置 → 临时登录 配置 PIN（依赖 TOTP 密钥）。
                </p>
                <button type="button" @click="router.push('/settings')"
                  class="px-3 py-1.5 bg-white text-blue-600 text-xs font-medium rounded-md border border-blue-300 hover:bg-blue-50">
                  前往配置
                </button>
              </div>
            </div>

            <div>
              <label class="inline-flex items-center cursor-pointer">
                <input type="checkbox" v-model="settings.show_qr_login" class="sr-only peer" />
                <div class="w-11 h-6 bg-gray-200 rounded-full peer peer-checked:bg-blue-600 peer-focus:ring-2 peer-focus:ring-blue-500 transition-colors relative after:content-[''] after:absolute after:top-0.5 after:left-0.5 after:bg-white after:rounded-full after:h-5 after:w-5 after:transition-transform peer-checked:after:translate-x-5"></div>
                <span class="ml-3 text-sm text-gray-700">显示「扫码登录」入口</span>
              </label>
              <div v-if="settings.show_qr_login" class="mt-2 flex items-center gap-3 flex-wrap">
                <p class="text-xs text-amber-600 bg-amber-50 px-3 py-2 rounded-md flex-1 min-w-fit">
                  使用前需先在 个人设置 → 通行密钥 注册通行密钥（授权确认依赖生物识别）。
                </p>
                <button type="button" @click="router.push('/settings')"
                  class="px-3 py-1.5 bg-white text-blue-600 text-xs font-medium rounded-md border border-blue-300 hover:bg-blue-50">
                  前往配置
                </button>
              </div>
            </div>
          </div>

          <div v-if="settingsMsg" class="text-sm px-3 py-2 rounded-md"
            :class="settingsError ? 'text-red-600 bg-red-50' : 'text-green-600 bg-green-50'">
            {{ settingsMsg }}
          </div>
          <button type="submit" class="px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
            保存设置
          </button>
        </form>
        <hr class="my-4 border-gray-200" />
        <div>
          <p class="text-sm text-gray-600 mb-2">重置 JWT 密钥将使所有登录会话失效。</p>
          <button @click="handleResetJwt" class="px-4 py-2 bg-red-600 text-white text-sm rounded-md hover:bg-red-700">
            重置 JWT 密钥
          </button>
        </div>
        </div>
        </Transition>
      </div>

      <!-- 代理配置（#47，key 对齐 nginx） -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <h3 class="text-base font-medium mb-1">代理配置</h3>
        <p class="text-sm text-gray-600 mb-4">
          代理路径的限制参数全局默认值（应用未设置覆盖时生效）。key 与 nginx 指令对齐；
          大小支持 k/m/g 后缀，时长支持 s/m/h（0 表示不限）。
        </p>
        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="loading" />
        <div v-else class="space-y-2">
          <div v-for="k in proxyKeys" :key="k.key" class="flex flex-col sm:flex-row sm:items-center gap-1 sm:gap-3">
            <label class="w-64 text-sm font-mono text-gray-700 flex-shrink-0" :title="k.desc">
              {{ k.key }}
              <span v-if="k.restart" class="ml-1 text-xs text-amber-600">重启后生效</span>
            </label>
            <input v-model="proxyCfgVals[k.key]" type="text" :placeholder="k.default"
              class="flex-1 max-w-xs px-3 py-1.5 border border-gray-300 rounded-md text-sm font-mono focus:outline-none focus:ring-2 focus:ring-blue-500" />
            <span class="text-xs text-gray-400 flex-1 hidden md:inline">{{ k.desc }}</span>
          </div>
        </div>
        </Transition>
        <div v-if="proxyCfgMsg" class="text-sm mt-3 px-3 py-2 rounded-md"
          :class="proxyCfgError ? 'text-red-600 bg-red-50' : 'text-green-600 bg-green-50'">
          {{ proxyCfgMsg }}
        </div>
        <button @click="handleSaveProxyConfig" :disabled="proxyCfgLoading"
          class="mt-4 px-4 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700 disabled:opacity-50">
          {{ proxyCfgLoading ? '保存中...' : '保存代理配置' }}
        </button>
      </div>

      <!-- 用户管理 -->
      <div class="bg-white rounded-lg border border-gray-200 p-4 md:p-6">
        <div class="flex items-center justify-between mb-4">
          <h3 class="text-base font-medium">用户管理</h3>
          <button @click="openCreateUser" class="px-3 py-1.5 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
            + 添加用户
          </button>
        </div>
        <Transition name="fade" mode="out-in">
        <LoadingSpinner v-if="loading" />
        <div v-else class="space-y-2">
          <div v-for="u in users" :key="u.id"
            class="flex items-center justify-between px-3 py-2 rounded-md hover:bg-gray-50 border border-gray-100">
            <div class="flex items-center gap-2">
              <span class="text-sm font-medium text-gray-900">{{ u.username }}</span>
              <span v-if="u.is_admin" class="text-xs text-orange-600 bg-orange-50 px-1.5 py-0.5 rounded">管理员</span>
              <span v-else-if="u.role === 'guest'" class="text-xs text-purple-600 bg-purple-50 px-1.5 py-0.5 rounded">访客</span>
              <span v-else class="text-xs text-blue-600 bg-blue-50 px-1.5 py-0.5 rounded">用户</span>
            </div>
            <div class="flex items-center gap-2">
              <button @click="openEditUser(u)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
              <button @click="openResetPw(u)" class="text-yellow-600 hover:text-yellow-800 text-xs">重置密码</button>
              <button v-if="u.id !== currentUserId" @click="viewUserApps(u.id)"
                class="text-green-600 hover:text-green-800 text-xs">查看应用</button>
              <button v-if="u.id !== currentUserId" @click="handleDeleteUser(u.id)"
                class="text-red-600 hover:text-red-800 text-xs">删除</button>
            </div>
          </div>
          <div v-if="users.length === 0" class="text-center py-6 text-gray-400 text-sm">暂无用户</div>
        </div>
        </Transition>
      </div>
    </div>

    <!-- 创建/编辑用户弹窗 -->
    <div v-if="showUserForm" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showUserForm = false">
      <div class="bg-white w-full md:w-96 md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-4">{{ userFormMode === 'create' ? '添加用户' : '编辑用户' }}</h3>
        <form @submit.prevent="handleUserSubmit" class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">用户名</label>
            <input v-model="userForm.username" type="text" required
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">角色</label>
            <BaseSelect v-model="userForm.role"
              :options="[{ value: 'user', label: '用户' }, { value: 'guest', label: '访客' }]" />
          </div>
          <div v-if="userFormMode === 'create'">
            <label class="block text-sm font-medium text-gray-700 mb-1">密码</label>
            <input v-model="userForm.password" type="password" required minlength="6"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div v-if="userFormError" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ userFormError }}</div>
          <div class="flex gap-2">
            <button type="button" @click="showUserForm = false"
              class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
            <button type="submit"
              class="flex-1 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
              {{ userFormMode === 'create' ? '创建' : '保存' }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- 重置密码弹窗 -->
    <div v-if="showResetPw" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showResetPw = false">
      <div class="bg-white w-full md:w-96 md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-4">重置密码 - {{ resetPwUser }}</h3>
        <form @submit.prevent="handleResetPw" class="space-y-4">
          <div>
            <label class="block text-sm font-medium text-gray-700 mb-1">新密码</label>
            <input v-model="resetPwPassword" type="password" required minlength="6"
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div v-if="resetPwError" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ resetPwError }}</div>
          <div class="flex gap-2">
            <button type="button" @click="showResetPw = false"
              class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
            <button type="submit"
              class="flex-1 py-2 bg-yellow-600 text-white text-sm rounded-md hover:bg-yellow-700">重置密码</button>
          </div>
        </form>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { getSettings, getProxyConfigKeys, parseProxyConfigText, type ProxyConfigKey } from '../api/settings'
import { listUsers, createUser, updateUser, deleteUser, resetUserPassword, updateAdminSettings, resetJwtSecret, type User } from '../api/admin'
import BaseSelect from '../components/BaseSelect.vue'
import LoadingSpinner from '../components/LoadingSpinner.vue'

const router = useRouter()
const authStore = useAuthStore()
const currentUserId = authStore.user?.id
const loading = ref(true)

// 站点设置（含登录页登录方式开关，#71；会话 Cookie 模式，#77；会话保留期，#80）
const settings = ref({
  site_name: '',
  admin_domain: '',
  proxy_domain: '',
  session_ttl: '',
  session_cookie_mode: 'persistent',
  audit_retention_days: '90',
  show_temp_login: false,
  show_qr_login: false,
})
const settingsMsg = ref('')
const settingsError = ref(false)

// 会话级 Cookie 开关（#77）：checkbox 绑定布尔值，落库映射回 persistent/session 字符串
const sessionCookieSession = computed({
  get: () => settings.value.session_cookie_mode === 'session',
  set: (v: boolean) => { settings.value.session_cookie_mode = v ? 'session' : 'persistent' },
})

// 代理配置（#47）
const proxyKeys = ref<ProxyConfigKey[]>([])
const proxyCfgVals = ref<Record<string, string>>({})
const proxyCfgMsg = ref('')
const proxyCfgError = ref(false)
const proxyCfgLoading = ref(false)

// 用户管理
const users = ref<User[]>([])
const showUserForm = ref(false)
const userFormMode = ref<'create' | 'edit'>('create')
const userFormError = ref('')
const editUserId = ref(0)
const userForm = ref({ username: '', password: '', role: 'user' })

// 重置密码
const showResetPw = ref(false)
const resetPwUserId = ref(0)
const resetPwUser = ref('')
const resetPwPassword = ref('')
const resetPwError = ref('')

async function loadSettings() {
  const res = await getSettings()
  if (res.data) {
    settings.value = {
      site_name: res.data.site_name || '',
      admin_domain: res.data.admin_domain || '',
      proxy_domain: res.data.proxy_domain || '',
      session_ttl: res.data.session_ttl || '',
      session_cookie_mode: res.data.session_cookie_mode === 'session' ? 'session' : 'persistent',
      audit_retention_days: res.data.audit_retention_days || '90',
      show_temp_login: res.data.show_temp_login === 'true',
      show_qr_login: res.data.show_qr_login === 'true',
    }
  }
}

// 加载代理配置元数据与当前值（缺省自动填默认值，#47）
async function loadProxyConfig() {
  const [keysRes, settingsRes] = await Promise.all([getProxyConfigKeys(), getSettings()])
  if (keysRes.data) {
    proxyKeys.value = keysRes.data
    const stored = parseProxyConfigText(settingsRes.data?.proxy_config || '')
    const vals: Record<string, string> = {}
    for (const k of keysRes.data) {
      vals[k.key] = stored[k.key] ?? k.default
    }
    proxyCfgVals.value = vals
  }
}

async function handleSaveProxyConfig() {
  proxyCfgMsg.value = ''
  proxyCfgLoading.value = true
  // 只提交与默认值不同的项，其余走内置默认
  const lines: string[] = []
  for (const k of proxyKeys.value) {
    const v = (proxyCfgVals.value[k.key] || '').trim()
    if (v && v !== k.default) lines.push(`${k.key}: ${v}`)
  }
  const res = await updateAdminSettings({ proxy_config: lines.join('\n') })
  proxyCfgLoading.value = false
  if (res.error) {
    proxyCfgMsg.value = res.error
    proxyCfgError.value = true
  } else {
    proxyCfgMsg.value = '保存成功。监听器级参数（标注"重启后生效"）需重启服务端后生效'
    proxyCfgError.value = false
  }
}

async function loadUsers() {
  const res = await listUsers()
  if (res.data) users.value = res.data
}

async function handleSaveSettings() {
  settingsMsg.value = ''
  // 会话保留期：表单为文本，落库为整数天（0=永久，#80）
  const retentionDays = parseInt(settings.value.audit_retention_days, 10)
  if (isNaN(retentionDays) || retentionDays < 0) {
    settingsMsg.value = '会话保留期必须为 ≥ 0 的整数（天，0 = 永久保留）'
    settingsError.value = true
    return
  }
  const res = await updateAdminSettings({ ...settings.value, audit_retention_days: retentionDays })
  if (res.error) { settingsMsg.value = res.error; settingsError.value = true }
  else { settingsMsg.value = '保存成功'; settingsError.value = false }
}

async function handleResetJwt() {
  if (!confirm('重置 JWT 密钥将使所有登录会话失效，确定继续？')) return
  await resetJwtSecret()
  await authStore.logout()
  // /login 是独立入口（login.html），不在 SPA 路由表内，必须整页跳转
  window.location.href = '/login'
}

function openCreateUser() {
  userFormMode.value = 'create'
  userForm.value = { username: '', password: '', role: 'user' }
  userFormError.value = ''
  showUserForm.value = true
}

function openEditUser(u: User) {
  userFormMode.value = 'edit'
  editUserId.value = u.id
  userForm.value = { username: u.username, password: '', role: u.role || 'user' }
  userFormError.value = ''
  showUserForm.value = true
}

async function handleUserSubmit() {
  userFormError.value = ''
  if (userFormMode.value === 'create') {
    const res = await createUser({ username: userForm.value.username, password: userForm.value.password, role: userForm.value.role })
    if (res.error) { userFormError.value = res.error; return }
  } else {
    const res = await updateUser(editUserId.value, { username: userForm.value.username, role: userForm.value.role })
    if (res.error) { userFormError.value = res.error; return }
  }
  showUserForm.value = false
  await loadUsers()
}

async function handleDeleteUser(id: number) {
  if (!confirm('确定删除此用户？其关联数据也将被清理。')) return
  await deleteUser(id)
  await loadUsers()
}

function openResetPw(u: User) {
  resetPwUserId.value = u.id
  resetPwUser.value = u.username
  resetPwPassword.value = ''
  resetPwError.value = ''
  showResetPw.value = true
}

async function handleResetPw() {
  resetPwError.value = ''
  const res = await resetUserPassword(resetPwUserId.value, resetPwPassword.value)
  if (res.error) { resetPwError.value = res.error; return }
  showResetPw.value = false
}

function viewUserApps(userId: number) {
  router.push(`/apps?user_id=${userId}`)
}

onMounted(async () => {
  await Promise.all([loadSettings(), loadUsers(), loadProxyConfig()])
  loading.value = false
})
</script>
