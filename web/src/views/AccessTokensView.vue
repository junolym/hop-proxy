<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg md:text-xl font-bold">访问票据</h2>
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
            <th class="text-left px-4 py-3 font-medium">备注名称</th>
            <th class="text-left px-4 py-3 font-medium">票据</th>
            <th class="text-left px-4 py-3 font-medium">状态</th>
            <th class="text-left px-4 py-3 font-medium">过期时间</th>
            <th class="text-left px-4 py-3 font-medium">最近使用</th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <tr v-for="t in tokens" :key="t.id" class="hover:bg-gray-50">
            <td class="px-4 py-3 font-medium">{{ t.name }}</td>
            <td class="px-4 py-3">
              <code class="text-xs bg-gray-100 px-2 py-1 rounded select-all">{{ t.token }}</code>
            </td>
            <td class="px-4 py-3">
              <span v-if="tokenStatus(t) === 'valid'" class="text-green-600 text-xs">有效</span>
              <span v-else class="text-gray-400 text-xs">已失效</span>
            </td>
            <td class="px-4 py-3 text-gray-500 text-xs">
              {{ t.expires_at ? formatDate(t.expires_at) : '永不' }}
            </td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ timeAgo(t.last_used_at) }}</td>
            <td class="px-4 py-3 text-right space-x-2">
              <button @click="openEdit(t)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
              <button @click="handleDelete(t.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
            </td>
          </tr>
          <tr v-if="loading">
            <td colspan="6"><LoadingSpinner /></td>
          </tr>
          <tr v-else-if="tokens.length === 0">
            <td colspan="6" class="px-4 py-8 text-center text-gray-400">暂无访问票据，点击右上角新增</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 移动端卡片列表 -->
    <div class="md:hidden space-y-3">
      <div v-if="loading" class="bg-white rounded-lg border border-gray-200">
        <LoadingSpinner />
      </div>
      <div v-else-if="tokens.length === 0" class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        暂无访问票据，点击右上角新增
      </div>
      <div v-for="t in tokens" :key="t.id" class="bg-white rounded-lg border border-gray-200 p-4">
        <div class="flex items-start justify-between mb-2">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <span class="font-medium text-sm text-gray-900">{{ t.name }}</span>
              <span v-if="tokenStatus(t) === 'valid'" class="text-green-600 bg-green-50 text-xs px-1.5 py-0.5 rounded">有效</span>
              <span v-else class="text-gray-400 bg-gray-100 text-xs px-1.5 py-0.5 rounded">已失效</span>
            </div>
            <code class="text-xs text-gray-400 break-all">{{ t.token }}</code>
          </div>
        </div>
        <div class="text-xs text-gray-400 mb-2">
          过期：{{ t.expires_at ? formatDate(t.expires_at) : '永不' }}
        </div>
        <div class="flex items-center justify-between pt-2 border-t border-gray-100">
          <span class="text-xs text-gray-400">最近使用: {{ timeAgo(t.last_used_at) }}</span>
          <div class="flex gap-3">
            <button @click="openEdit(t)" class="text-blue-600 text-xs py-1">编辑</button>
            <button @click="handleDelete(t.id)" class="text-red-600 text-xs py-1">删除</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 新建后展示完整 token 的弹窗 -->
    <div v-if="newToken" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-2">票据已创建</h3>
        <p class="text-sm text-yellow-700 bg-yellow-50 border border-yellow-200 rounded p-3 mb-4">
          请立即复制保存，此票据值仅展示一次，关闭后将无法再查看完整内容。
        </p>
        <div class="bg-gray-50 border border-gray-200 rounded p-3 mb-4">
          <p class="text-xs text-gray-500 mb-1">票据值</p>
          <code class="text-sm break-all select-all text-gray-800">{{ newToken }}</code>
        </div>
        <p class="text-xs text-gray-400 mb-4">使用方式：<br>
          <code class="bg-gray-100 px-1 rounded">Authorization: Bearer {{ newToken }}</code><br>
          或 URL 方式：<code class="bg-gray-100 px-1 rounded">https://token:{{ newToken }}@your-app.domain</code>
        </p>
        <button @click="copyNewToken"
          class="w-full mb-2 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
          {{ copied ? '✓ 已复制' : '复制票据值' }}
        </button>
        <button @click="newToken = ''; copied = false"
          class="w-full py-2 text-sm text-gray-600 border border-gray-300 rounded-md">
          已复制，关闭
        </button>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showForm = false">
      <div class="bg-white w-full md:w-[32rem] md:rounded-lg rounded-t-2xl shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="p-5">
          <h3 class="text-lg font-bold mb-4">{{ formMode === 'create' ? '新增访问票据' : '编辑访问票据' }}</h3>
          <form @submit.prevent="handleSubmit" class="space-y-4">

            <!-- 备注名称 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">备注名称</label>
              <input v-model="form.name" type="text" required autofocus
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
                placeholder="例如：CI/CD 自动化" />
            </div>

            <!-- 授权入口 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-2">授权入口</label>
              <p class="text-xs text-gray-400 mb-2">勾选允许使用此票据的请求来源</p>
              <div class="space-y-1.5 border border-gray-200 rounded-md p-3 bg-gray-50">
                <label class="flex items-center gap-2 cursor-pointer hover:bg-white rounded px-2 py-1 transition-colors">
                  <input type="checkbox" v-model="form.allowedEntries.server"
                    class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
                  <span class="flex items-center gap-1.5 text-sm text-gray-800">
                    <span class="w-2 h-2 rounded-full bg-blue-500"></span>
                    服务端（直连访问代理域名）
                  </span>
                </label>
                <label v-for="c in clientList" :key="c.id"
                  class="flex items-center gap-2 cursor-pointer hover:bg-white rounded px-2 py-1 transition-colors">
                  <input type="checkbox" :value="c.id" v-model="form.allowedEntries.clients"
                    class="w-4 h-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
                  <span class="flex items-center gap-1.5 text-sm text-gray-800">
                    <span class="w-2 h-2 rounded-full flex-shrink-0"
                      :class="c.online ? 'bg-green-500' : 'bg-gray-300'"></span>
                    {{ c.name }}
                  </span>
                </label>
                <div v-if="clientList.length === 0" class="text-xs text-gray-400 text-center py-1">
                  暂无远程客户端
                </div>
              </div>
            </div>

            <!-- 授权应用 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">授权应用</label>
              <p class="text-xs text-gray-400 mb-2">不选则允许访问全部应用</p>
              <!-- 搜索框 -->
              <input v-model="appSearch" type="text" placeholder="输入关键词筛选应用..."
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm mb-2 focus:outline-none focus:ring-2 focus:ring-blue-500" />
              <!-- 搜索结果 -->
              <div class="border border-gray-200 rounded-md max-h-36 overflow-y-auto">
                <div v-for="app in filteredApps" :key="app.id"
                  class="flex items-center justify-between px-3 py-2 hover:bg-gray-50 cursor-pointer text-sm"
                  @click="toggleApp(app.id)">
                  <span>{{ app.name }}
                    <span class="text-xs text-gray-400 ml-1">{{ app.subdomain }}</span>
                  </span>
                  <span v-if="form.allowedAppIDs.includes(app.id)"
                    class="text-blue-600 text-xs">✓ 已选</span>
                </div>
                <div v-if="filteredApps.length === 0" class="px-3 py-4 text-center text-gray-400 text-xs">
                  {{ appSearch ? '无匹配应用' : '暂无应用' }}
                </div>
              </div>
              <!-- 已选应用 -->
              <div v-if="form.allowedAppIDs.length > 0" class="mt-2 flex flex-wrap gap-1.5">
                <span v-for="appId in form.allowedAppIDs" :key="appId"
                  class="inline-flex items-center gap-1 text-xs bg-blue-50 text-blue-700 px-2 py-0.5 rounded">
                  {{ appName(appId) }}
                  <button type="button" @click.stop="toggleApp(appId)" class="text-blue-400 hover:text-blue-700 ml-0.5">×</button>
                </span>
              </div>
              <p v-else class="mt-1 text-xs text-gray-400">未选择任何应用，将允许访问全部应用</p>
            </div>

            <!-- 过期时间 -->
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">过期时间</label>
              <BaseSelect v-model="form.expiresInSecs" :options="expiryOptions" />
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
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { listAccessTokens, createAccessToken, updateAccessToken, deleteAccessToken, type AccessToken } from '../api/access_tokens'
import { listClients, type Client } from '../api/clients'
import { listApps, type App } from '../api/apps'
import BaseSelect from '../components/BaseSelect.vue'
import LoadingSpinner from '../components/LoadingSpinner.vue'

function timeAgo(dateStr: string | null): string {
  if (!dateStr) return '从未'
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = now.getTime() - date.getTime()
  const diffSec = Math.floor(diffMs / 1000)
  const diffMin = Math.floor(diffSec / 60)
  const diffHour = Math.floor(diffMin / 60)
  const diffDay = Math.floor(diffHour / 24)
  const diffWeek = Math.floor(diffDay / 7)
  const diffMonth = Math.floor(diffDay / 30)
  const diffYear = Math.floor(diffDay / 365)
  if (diffSec < 60) return '刚刚'
  if (diffMin < 60) return `${diffMin} 分钟前`
  if (diffHour < 24) return `${diffHour} 小时前`
  if (diffDay < 7) return `${diffDay} 天前`
  if (diffWeek < 4) return `${diffWeek} 周前`
  if (diffMonth < 12) return `${diffMonth} 个月前`
  return `${diffYear} 年前`
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' })
}

function tokenStatus(t: AccessToken): 'valid' | 'expired' {
  if (!t.expires_at) return 'valid'
  return new Date(t.expires_at) > new Date() ? 'valid' : 'expired'
}

const tokens = ref<AccessToken[]>([])
const loading = ref(true)
const clientList = ref<Client[]>([])
const appList = ref<App[]>([])
const showForm = ref(false)
const formMode = ref<'create' | 'edit'>('create')
const formError = ref('')
const editId = ref(0)
const newToken = ref('')
const copied = ref(false)
const appSearch = ref('')

const form = ref({
  name: '',
  allowedEntries: { server: true, clients: [] as string[] },
  allowedAppIDs: [] as number[],
  expiresInSecs: 604800 as number | null,  // 默认 7 天
})

const expiryOptions = [
  { label: '1 天', value: 86400 },
  { label: '7 天（默认）', value: 604800 },
  { label: '30 天', value: 2592000 },
  { label: '一年', value: 31536000 },
  { label: '永不过期', value: null },
]

const filteredApps = computed(() => {
  if (!appSearch.value) return appList.value
  const q = appSearch.value.toLowerCase()
  return appList.value.filter(a => a.name.toLowerCase().includes(q) || a.subdomain.toLowerCase().includes(q))
})

function appName(id: number): string {
  return appList.value.find(a => a.id === id)?.name || String(id)
}

function toggleApp(id: number) {
  const idx = form.value.allowedAppIDs.indexOf(id)
  if (idx >= 0) {
    form.value.allowedAppIDs.splice(idx, 1)
  } else {
    form.value.allowedAppIDs.push(id)
  }
}

async function loadTokens() {
  const res = await listAccessTokens()
  if (res.data) tokens.value = res.data
}

function openCreate() {
  formMode.value = 'create'
  form.value = { name: '', allowedEntries: { server: true, clients: [] }, allowedAppIDs: [], expiresInSecs: 604800 }
  formError.value = ''
  appSearch.value = ''
  showForm.value = true
}

function openEdit(t: AccessToken) {
  formMode.value = 'edit'
  editId.value = t.id
  let entries = { server: true, clients: [] as string[] }
  try { entries = JSON.parse(t.allowed_entries) } catch {}
  let appIDs: number[] = []
  try { appIDs = JSON.parse(t.allowed_app_ids) } catch {}
  // 过期时间：编辑时不重置，设为 null（表示不修改）或保持原值
  // 这里简化为"编辑时过期时间重置为永不过期"
  form.value = { name: t.name, allowedEntries: entries, allowedAppIDs: appIDs, expiresInSecs: null }
  formError.value = ''
  appSearch.value = ''
  showForm.value = true
}

async function handleSubmit() {
  if (!form.value.name.trim()) {
    formError.value = '请输入备注名称'
    return
  }
  if (!form.value.allowedEntries.server && form.value.allowedEntries.clients.length === 0) {
    formError.value = '请至少选择一个授权入口'
    return
  }
  formError.value = ''

  const payload = {
    name: form.value.name.trim(),
    allowed_entries: form.value.allowedEntries,
    allowed_app_ids: form.value.allowedAppIDs,
    expires_in_secs: form.value.expiresInSecs,
  }

  if (formMode.value === 'create') {
    const res = await createAccessToken(payload)
    if (res.error) { formError.value = res.error; return }
    showForm.value = false
    if (res.data) {
      newToken.value = res.data.token
    }
  } else {
    const res = await updateAccessToken(editId.value, payload)
    if (res.error) { formError.value = res.error; return }
    showForm.value = false
  }
  await loadTokens()
}

async function handleDelete(id: number) {
  if (!confirm('确定删除此访问票据？删除后无法恢复，所有使用此票据的请求将被拒绝。')) return
  await deleteAccessToken(id)
  await loadTokens()
}

async function copyNewToken() {
  try {
    await navigator.clipboard.writeText(newToken.value)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  } catch {
    // ignore
  }
}

onMounted(async () => {
  const [, clientsRes, appsRes] = await Promise.all([
    loadTokens(),
    listClients(),
    listApps(),
  ])
  if (clientsRes.data) clientList.value = clientsRes.data.filter(c => c.id !== '__host__')
  if (appsRes.data) appList.value = appsRes.data
  loading.value = false
})
</script>
