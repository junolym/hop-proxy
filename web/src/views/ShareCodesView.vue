<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg md:text-xl font-bold">分享码</h2>
      <button @click="openCreate"
        class="px-3 py-1.5 md:px-4 md:py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
        + 新增
      </button>
    </div>

    <!-- 桌面端表格 -->
    <div class="hidden md:block bg-white rounded-lg border border-gray-200 overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-gray-50 text-gray-600">
          <tr>
            <th class="text-left px-4 py-3 font-medium">应用</th>
            <th class="text-left px-4 py-3 font-medium">分享码</th>
            <th class="text-left px-4 py-3 font-medium">状态</th>
            <th class="text-left px-4 py-3 font-medium">跳转次数</th>
            <th class="text-left px-4 py-3 font-medium">创建时间</th>
            <th class="text-left px-4 py-3 font-medium">过期时间</th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <tr v-for="sc in sortedCodes" :key="sc.id" class="hover:bg-gray-50">
            <td class="px-4 py-3 font-medium">{{ sc.app_name }}
              <span class="text-xs text-gray-400 ml-1">{{ sc.subdomain }}</span>
              <div v-if="sc.concrete_subdomain" class="text-xs text-blue-500 mt-0.5">→ {{ sc.concrete_subdomain }}</div>
            </td>
            <td class="px-4 py-3">
              <a :href="shareURL(sc.code)" target="_blank"
                class="text-xs bg-gray-100 px-2 py-1 rounded hover:bg-blue-50 hover:text-blue-600 transition-colors select-all">
                {{ sc.code }}
              </a>
            </td>
            <td class="px-4 py-3">
              <span v-if="!sc.enabled" class="text-gray-400 text-xs">已禁用</span>
              <span v-else-if="isExpired(sc)" class="text-red-500 text-xs">已过期</span>
              <span v-else-if="sc.use_count >= sc.max_uses" class="text-orange-500 text-xs">已用完</span>
              <span v-else class="text-green-600 text-xs">有效</span>
            </td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ sc.use_count }} / {{ sc.max_uses }}</td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ formatDateTime(sc.created_at) }}</td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ formatDateTime(sc.expires_at) }}</td>
            <td class="px-4 py-3 text-right space-x-2">
              <button @click="openShareQR(sc)"
                class="text-blue-600 hover:text-blue-800 text-xs">二维码</button>
              <button @click="copyShareURL(sc)"
                class="text-xs transition-colors"
                :class="copiedShareId === sc.id ? 'text-green-600' : 'text-gray-600 hover:text-gray-800'">
                {{ copiedShareId === sc.id ? '✓ 已复制' : '复制链接' }}
              </button>
              <button @click="openEdit(sc)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
              <button @click="toggleEnabled(sc)" class="text-xs"
                :class="sc.enabled ? 'text-yellow-600 hover:text-yellow-800' : 'text-green-600 hover:text-green-800'">
                {{ sc.enabled ? '禁用' : '启用' }}
              </button>
              <button @click="handleDelete(sc.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
            </td>
          </tr>
          <tr v-if="loading">
            <td colspan="7"><LoadingSpinner /></td>
          </tr>
          <tr v-else-if="codes.length === 0">
            <td colspan="7" class="px-4 py-8 text-center text-gray-400">暂无分享码，点击右上角新增</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 移动端卡片列表 -->
    <div class="md:hidden space-y-3">
      <div v-if="loading" class="bg-white rounded-lg border border-gray-200">
        <LoadingSpinner />
      </div>
      <div v-else-if="codes.length === 0" class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        暂无分享码，点击右上角新增
      </div>
      <div v-for="sc in sortedCodes" :key="sc.id" class="bg-white rounded-lg border border-gray-200 p-4">
        <div class="flex items-start justify-between mb-2">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1 flex-wrap">
              <span class="font-medium text-sm text-gray-900">{{ sc.app_name }}</span>
              <span v-if="sc.concrete_subdomain" class="text-xs text-blue-500 bg-blue-50 px-1.5 py-0.5 rounded">{{ sc.concrete_subdomain }}</span>
              <span v-if="!sc.enabled" class="text-gray-400 bg-gray-100 text-xs px-1.5 py-0.5 rounded">已禁用</span>
              <span v-else-if="isExpired(sc)" class="text-red-500 bg-red-50 text-xs px-1.5 py-0.5 rounded">已过期</span>
              <span v-else-if="sc.use_count >= sc.max_uses" class="text-orange-500 bg-orange-50 text-xs px-1.5 py-0.5 rounded">已用完</span>
              <span v-else class="text-green-600 bg-green-50 text-xs px-1.5 py-0.5 rounded">有效</span>
            </div>
            <a :href="shareURL(sc.code)" target="_blank" class="text-xs text-blue-600 break-all">{{ shareURL(sc.code) }}</a>
          </div>
        </div>
        <div class="text-xs text-gray-400 mb-2">
          跳转次数：{{ sc.use_count }} / {{ sc.max_uses }} · 过期：{{ formatDateTime(sc.expires_at) }}
        </div>
        <div class="flex items-center justify-between pt-2 border-t border-gray-100">
          <span class="text-xs text-gray-400">{{ formatDateTime(sc.created_at) }}</span>
          <div class="flex gap-3">
            <button @click="openShareQR(sc)" class="text-blue-600 text-xs py-1">二维码</button>
            <button @click="copyShareURL(sc)"
              class="text-xs py-1 transition-colors"
              :class="copiedShareId === sc.id ? 'text-green-600' : 'text-gray-600'">
              {{ copiedShareId === sc.id ? '✓ 已复制' : '复制' }}
            </button>
            <button @click="openEdit(sc)" class="text-blue-600 text-xs py-1">编辑</button>
            <button @click="toggleEnabled(sc)" class="text-xs py-1"
              :class="sc.enabled ? 'text-yellow-600' : 'text-green-600'">
              {{ sc.enabled ? '禁用' : '启用' }}
            </button>
            <button @click="handleDelete(sc.id)" class="text-red-600 text-xs py-1">删除</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showForm = false">
      <div class="bg-white w-full md:w-[32rem] md:rounded-lg rounded-t-2xl shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="p-5">
          <h3 class="text-lg font-bold mb-4">{{ formMode === 'create' ? '新增分享码' : '编辑分享码' }}</h3>
          <form @submit.prevent="handleSubmit" class="space-y-4">

            <!-- 粘贴链接自动识别（仅创建模式） -->
            <div v-if="formMode === 'create'">
              <label class="block text-sm font-medium text-gray-700 mb-1">链接识别</label>
              <p class="text-xs text-gray-400 mb-2">粘贴应用访问链接（如 https://app1.proxy.example.com/docs?a=1），自动识别应用与跳转路径并填入下方表单</p>
              <div class="flex gap-2">
                <input v-model="linkInput" type="text" placeholder="https://app1.proxy.example.com/path"
                  @paste="onLinkPaste" @keyup.enter="recognizeLink"
                  class="flex-1 px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
                <button type="button" @click="recognizeLink"
                  class="px-4 py-2 bg-gray-100 text-gray-700 text-sm rounded-md hover:bg-gray-200 whitespace-nowrap">识别</button>
              </div>
              <p v-if="linkParseError" class="text-xs text-red-600 mt-1">{{ linkParseError }}</p>
              <p v-if="linkParsedInfo" class="text-xs text-green-600 mt-1">{{ linkParsedInfo }}</p>
            </div>

            <!-- 应用选择（仅创建模式） -->
            <div v-if="formMode === 'create'">
              <label class="block text-sm font-medium text-gray-700 mb-1">应用</label>
              <p class="text-xs text-gray-400 mb-2">仅支持 SSO 或 SSO+票据 认证方式的应用</p>
              <BaseSelect v-model="form.app_id" :options="appOptions" placeholder="请选择应用" />
            </div>

            <!-- 模糊匹配应用的具体子域名（创建模式且选中模糊应用时显示） -->
            <div v-if="formMode === 'create' && selectedAppIsFuzzy">
              <label class="block text-sm font-medium text-gray-700 mb-1">具体子域名</label>
              <p class="text-xs text-gray-400 mb-1">应用模式为 <code class="bg-gray-100 px-1 rounded">{{ selectedAppPattern }}</code>，请填写命中的完整子域名（仅小写字母、数字、连字符）</p>
              <input v-model="form.concrete_subdomain" type="text" required
                placeholder="如 abc-dev"
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>

            <!-- 模糊应用子域名只读展示（编辑模式） -->
            <div v-if="formMode === 'edit' && form.concrete_subdomain">
              <label class="block text-sm font-medium text-gray-700 mb-1">具体子域名</label>
              <input :value="form.concrete_subdomain" type="text" readonly
                class="w-full px-3 py-2 border border-gray-200 rounded-md text-sm bg-gray-50 text-gray-600" />
            </div>

            <!-- 分享码有效期 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">分享码有效期</label>
              <BaseSelect v-model.number="form.expires_in_secs" :options="expiresInSecsOptions" />
            </div>

            <!-- Cookie 有效期 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">Cookie 有效期</label>
              <p class="text-xs text-gray-400 mb-1">用户访问后获得的 SSO Cookie 有效期</p>
              <BaseSelect v-model.number="form.cookie_ttl" :options="cookieTTLOptionsForSelect" />
            </div>

            <!-- 跳转次数限制 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">跳转次数限制</label>
              <p class="text-xs text-gray-400 mb-1">每个新用户访问会消耗一次（已有有效 cookie 时不消耗）</p>
              <input v-model.number="form.max_uses" type="number" required min="1" max="1000"
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>

            <!-- 跳转路径 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">跳转路径</label>
              <p class="text-xs text-gray-400 mb-1">用户兑换后跳转到的应用内路径，留空表示 /，必须以 / 开头</p>
              <input v-model="form.redirect_path" type="text" placeholder="/"
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
            </div>

            <!-- 启用状态（仅编辑模式） -->
            <div v-if="formMode === 'edit'" class="flex items-center gap-2">
              <input type="checkbox" id="enabled" v-model="form.enabled"
                class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
              <label for="enabled" class="text-sm text-gray-700 cursor-pointer">启用</label>
            </div>

            <div v-if="formError" class="text-sm text-red-600 bg-red-50 px-3 py-2 rounded-md">{{ formError }}</div>

            <div class="flex gap-2">
              <button type="button" @click="showForm = false"
                class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
              <button type="submit"
                class="flex-1 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
                {{ formMode === 'create' ? '创建' : '保存' }}
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>

    <!-- 分享展示弹窗：二维码 + 链接（创建成功 / 列表点击二维码共用） -->
    <div v-if="shareModal" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="closeShareModal">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-2">{{ shareModal.title }}</h3>
        <p class="text-sm text-gray-500 mb-4">{{ shareModal.desc }}</p>
        <div class="flex justify-center mb-4">
          <img v-if="shareQR" :src="shareQR" alt="分享二维码"
            class="w-56 h-56 border border-gray-200 rounded-lg" />
          <div v-else class="w-56 h-56 flex items-center justify-center text-sm text-gray-400 border border-gray-200 rounded-lg">
            二维码生成中…
          </div>
        </div>
        <div class="bg-gray-50 border border-gray-200 rounded p-3 mb-4">
          <p class="text-xs text-gray-500 mb-1">分享链接</p>
          <code class="text-sm break-all select-all text-gray-800">{{ shareModal.url }}</code>
        </div>
        <button @click="copyModalURL"
          class="w-full mb-2 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
          {{ copied ? '✓ 已复制' : '复制链接' }}
        </button>
        <button @click="closeShareModal"
          class="w-full py-2 text-sm text-gray-600 border border-gray-300 rounded-md">
          关闭
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, nextTick } from 'vue'
import QRCode from 'qrcode'
import { listShareCodes, createShareCode, updateShareCode, deleteShareCode, type ShareCode } from '../api/share_codes'
import { listApps, type App } from '../api/apps'
import { getSettings } from '../api/settings'
import BaseSelect from '../components/BaseSelect.vue'
import LoadingSpinner from '../components/LoadingSpinner.vue'

function formatDateTime(dateStr: string): string {
  return new Date(dateStr).toLocaleString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  })
}

function isExpired(sc: ShareCode): boolean {
  return new Date(sc.expires_at) < new Date()
}

// 是否为「有效」状态（与列表状态列的判定一致：启用且未过期且未用完）
function isValid(sc: ShareCode): boolean {
  return sc.enabled && !isExpired(sc) && sc.use_count < sc.max_uses
}

// 有效置顶，其他状态（已禁用/已过期/已用完）沉底；同组内保持接口返回顺序
const sortedCodes = computed(() => {
  const valid = codes.value.filter(isValid)
  const rest = codes.value.filter(sc => !isValid(sc))
  return [...valid, ...rest]
})

function shareURL(code: string): string {
  return `${window.location.origin}/s/${code}`
}

const codes = ref<ShareCode[]>([])
const loading = ref(true)
const appList = ref<App[]>([])
const showForm = ref(false)
const formMode = ref<'create' | 'edit'>('create')
const formError = ref('')
const editId = ref(0)
const editingCode = ref<ShareCode | null>(null)  // 编辑中的原始分享码，用于回填展示字段（如当前过期时间）
const copied = ref(false)
// 分享展示弹窗（二维码 + 链接），创建成功与列表「二维码」按钮共用
const shareModal = ref<{ url: string; title: string; desc: string } | null>(null)
const shareQR = ref('')                                              // 弹窗内二维码的 dataURL
const copiedShareId = ref<number | null>(null)  // 刚刚复制链接的分享码 ID（用于短暂显示「已复制」）
const proxyDomain = ref('')                     // 代理父域名（用于从粘贴链接中提取子域名）
const linkInput = ref('')                       // 链接识别输入框内容
const linkParseError = ref('')                  // 链接识别失败提示
const linkParsedInfo = ref('')                  // 链接识别成功提示

const form = ref({
  app_id: '' as string | number,
  expires_in_secs: 86400,
  cookie_ttl: 604800,
  max_uses: 3,
  redirect_path: '',
  concrete_subdomain: '',
  enabled: true,
})

const expiryOptions = [
  { label: '1 小时', value: 3600 },
  { label: '24 小时（默认）', value: 86400 },
  { label: '3 天', value: 259200 },
  { label: '7 天', value: 604800 },
  { label: '30 天', value: 2592000 },
  { label: '一年', value: 31536000 },
  { label: '永久', value: 315360000 },
]

const cookieTTLOptions = [
  { label: '24 小时', value: 86400 },
  { label: '7 天（默认）', value: 604800 },
  { label: '30 天', value: 2592000 },
  { label: '一年', value: 31536000 },
  { label: '永久', value: 315360000 },
]

// 仅 SSO / SSO+票据 认证方式的应用可创建分享码
const selectableApps = computed(() =>
  appList.value.filter(a => a.auth_method === 'sso' || a.auth_method === 'sso_token')
)

const appOptions = computed(() =>
  selectableApps.value.map(a => ({ value: a.id, label: `${a.name} (${a.subdomain})` }))
)

// 编辑模式首位追加「保持原过期时间」选项
const expiresInSecsOptions = computed(() => {
  const opts = expiryOptions.map(o => ({ value: o.value, label: o.label }))
  if (formMode.value === 'edit') {
    opts.unshift({ value: 0, label: `保持原过期时间（${formatDateTime(editingCode.value?.expires_at || '')} 过期）` })
  }
  return opts
})

const cookieTTLOptionsForSelect = cookieTTLOptions.map(o => ({ value: o.value, label: o.label }))

// 当前选中应用是否为模糊匹配模式
const selectedAppIsFuzzy = computed(() => {
  if (form.value.app_id === '' || form.value.app_id === undefined) return false
  const app = appList.value.find(a => a.id === Number(form.value.app_id))
  return !!app && (app.subdomain.includes('*') || app.subdomain.includes('/'))
})

// 选中应用的模式（用于输入框提示）
const selectedAppPattern = computed(() => {
  if (form.value.app_id === '' || form.value.app_id === undefined) return ''
  const app = appList.value.find(a => a.id === Number(form.value.app_id))
  return app?.subdomain || ''
})

// subdomainPatternToRegExp 将子域名模式编译为正则，语义与后端 pkg/subdomain 一致：
// `*` → ([a-z0-9-]+)，`/.../` → (?:原始正则段)，其余字符按字面量转义。
// 返回编译后的正则与字面量字符数（用于模糊匹配优先级排序）；无效模式返回 null。
function subdomainPatternToRegExp(pattern: string): { re: RegExp; literalLen: number } | null {
  let src = '^'
  let literalLen = 0
  let i = 0
  while (i < pattern.length) {
    const c = pattern[i]
    if (c === '*') {
      src += '([a-z0-9-]+)'
      i++
    } else if (c === '/') {
      // 寻找下一个未转义的 '/' 作为正则段闭合（\/ → 字面 /，\\ → 字面 \）
      let j = i + 1
      let seg = ''
      let closed = false
      while (j < pattern.length) {
        if (pattern[j] === '\\' && j + 1 < pattern.length) {
          seg += pattern[j + 1]
          j += 2
          continue
        }
        if (pattern[j] === '/') { closed = true; break }
        seg += pattern[j]
        j++
      }
      if (!closed) return null
      src += '(?:' + seg + ')'
      i = j + 1
    } else {
      if ('.+?()[]{}^$|\\'.includes(c)) src += '\\'
      src += c
      literalLen++
      i++
    }
  }
  src += '$'
  try {
    return { re: new RegExp(src), literalLen }
  } catch {
    return null // 无效模式，跳过（与后端防御行为一致）
  }
}

// recognizeLink 解析粘贴的应用访问链接：提取子域名与路径，匹配应用后填入表单
function recognizeLink() {
  linkParseError.value = ''
  linkParsedInfo.value = ''
  const raw = linkInput.value.trim()
  if (!raw) return

  // 无协议时补 https:// 以便 URL 解析
  let u: URL
  try {
    u = new URL(/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(raw) ? raw : 'https://' + raw)
  } catch {
    linkParseError.value = '链接格式无法解析'
    return
  }

  if (!proxyDomain.value) {
    linkParseError.value = '服务端未配置代理域名，无法识别子域名'
    return
  }
  const host = u.hostname.toLowerCase()
  const suffix = '.' + proxyDomain.value.toLowerCase()
  if (!host.endsWith(suffix) || host.length === suffix.length) {
    linkParseError.value = `链接域名不属于代理域名 ${proxyDomain.value}`
    return
  }
  const sub = host.slice(0, -suffix.length)

  // 精确匹配优先；未命中再模糊匹配（字面量最多优先，并列取 id 最小，与后端一致）
  let app = appList.value.find(a => a.subdomain === sub)
  let concrete = ''
  if (!app) {
    let best: { app: App; litLen: number } | null = null
    for (const a of appList.value) {
      if (!(a.subdomain.includes('*') || a.subdomain.includes('/'))) continue
      const parsed = subdomainPatternToRegExp(a.subdomain)
      if (!parsed || !parsed.re.test(sub)) continue
      if (!best || parsed.literalLen > best.litLen || (parsed.literalLen === best.litLen && a.id < best.app.id)) {
        best = { app: a, litLen: parsed.literalLen }
      }
    }
    if (best) {
      app = best.app
      concrete = sub
    }
  }
  if (!app) {
    linkParseError.value = `未找到子域名「${sub}」对应的应用`
    return
  }
  if (app.auth_method !== 'sso' && app.auth_method !== 'sso_token') {
    linkParseError.value = `应用「${app.name}」的认证方式不支持分享码（仅支持 SSO / SSO+票据）`
    return
  }

  form.value.app_id = app.id
  form.value.concrete_subdomain = concrete
  const path = u.pathname + u.search
  form.value.redirect_path = path === '/' ? '' : path
  linkParsedInfo.value = `已识别应用「${app.name}」${concrete ? `（${concrete}）` : ''}，跳转路径 ${path === '/' ? '/' : path}`
}

// 粘贴后自动识别（等 v-model 同步再取值）
function onLinkPaste() {
  nextTick(() => recognizeLink())
}

async function loadCodes() {
  const res = await listShareCodes()
  if (res.data) codes.value = res.data
}

function openCreate() {
  formMode.value = 'create'
  editingCode.value = null
  form.value = { app_id: '', expires_in_secs: 86400, cookie_ttl: 604800, max_uses: 3, redirect_path: '', concrete_subdomain: '', enabled: true }
  formError.value = ''
  linkInput.value = ''
  linkParseError.value = ''
  linkParsedInfo.value = ''
  showForm.value = true
}

function openEdit(sc: ShareCode) {
  formMode.value = 'edit'
  editId.value = sc.id
  editingCode.value = sc
  form.value = {
    app_id: sc.app_id,
    expires_in_secs: 0, // 0 表示保持原过期时间（对应 select 中的「保持原过期时间」选项）
    cookie_ttl: sc.cookie_ttl,
    max_uses: sc.max_uses,
    redirect_path: sc.redirect_path,
    concrete_subdomain: sc.concrete_subdomain, // 编辑时只读展示，不提交
    enabled: sc.enabled,
  }
  formError.value = ''
  showForm.value = true
}

async function handleSubmit() {
  formError.value = ''

  if (form.value.max_uses < 1) {
    formError.value = '跳转次数至少为 1'
    return
  }

  // 跳转路径校验：留空表示 /；非空必须以 / 开头，且不能是 // 或 /\
  const path = (form.value.redirect_path || '').trim()
  if (path !== '' && !path.startsWith('/')) {
    formError.value = '跳转路径必须以 / 开头'
    return
  }
  if (path.startsWith('//') || path.startsWith('/\\')) {
    formError.value = '跳转路径格式非法'
    return
  }

  if (formMode.value === 'create') {
    if (!form.value.app_id) {
      formError.value = '请选择应用'
      return
    }
    // 模糊匹配应用必须填写具体子域名
    const concreteSub = (form.value.concrete_subdomain || '').trim().toLowerCase()
    if (selectedAppIsFuzzy.value && !concreteSub) {
      formError.value = '模糊匹配应用需填写具体子域名'
      return
    }
    const res = await createShareCode({
      app_id: Number(form.value.app_id),
      max_uses: form.value.max_uses,
      cookie_ttl: form.value.cookie_ttl,
      expires_in_secs: form.value.expires_in_secs,
      redirect_path: path,
      concrete_subdomain: concreteSub,
    })
    if (res.error) { formError.value = res.error; return }
    showForm.value = false
    if (res.data) {
      // 创建成功后立刻弹出二维码 + 分享链接
      openShareModal(shareURL(res.data.code), '分享码已创建', '将以下二维码或链接分享给他人，访问后将自动获得该应用的 SSO Cookie。')
    }
  } else {
    const payload: Parameters<typeof updateShareCode>[1] = {
      max_uses: form.value.max_uses,
      cookie_ttl: form.value.cookie_ttl,
      enabled: form.value.enabled,
      redirect_path: path,
    }
    // expires_in_secs=0 表示不修改过期时间
    if (form.value.expires_in_secs > 0) {
      payload.expires_in_secs = form.value.expires_in_secs
    }
    const res = await updateShareCode(editId.value, payload)
    if (res.error) { formError.value = res.error; return }
    showForm.value = false
  }
  await loadCodes()
}

async function toggleEnabled(sc: ShareCode) {
  await updateShareCode(sc.id, { enabled: !sc.enabled })
  await loadCodes()
}

async function handleDelete(id: number) {
  if (!confirm('确定删除此分享码？删除后分享链接立即失效，无法恢复。')) return
  await deleteShareCode(id)
  await loadCodes()
}

async function copyShareURL(sc: ShareCode) {
  try {
    await navigator.clipboard.writeText(shareURL(sc.code))
    copiedShareId.value = sc.id
    setTimeout(() => { copiedShareId.value = null }, 2000)
  } catch {
    // ignore
  }
}

// openShareModal 打开分享展示弹窗并生成二维码
async function openShareModal(url: string, title: string, desc: string) {
  shareModal.value = { url, title, desc }
  shareQR.value = ''
  copied.value = false
  try {
    shareQR.value = await QRCode.toDataURL(url, { width: 448, margin: 2 })
  } catch {
    // 二维码生成失败不阻塞弹窗，仍可复制链接
    shareQR.value = ''
  }
}

// 列表「二维码」按钮：展示已有分享码的二维码
function openShareQR(sc: ShareCode) {
  openShareModal(shareURL(sc.code), '分享二维码', '扫描二维码或复制链接访问该分享码对应的页面。')
}

function closeShareModal() {
  shareModal.value = null
  shareQR.value = ''
  copied.value = false
}

async function copyModalURL() {
  if (!shareModal.value) return
  try {
    await navigator.clipboard.writeText(shareModal.value.url)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch {
    // ignore
  }
}

onMounted(async () => {
  const [, appsRes, settingsRes] = await Promise.all([
    loadCodes(),
    listApps(),
    getSettings(),
  ])
  if (appsRes.data) appList.value = appsRes.data
  if (settingsRes.data) proxyDomain.value = settingsRes.data.proxy_domain || ''
  loading.value = false
})
</script>
