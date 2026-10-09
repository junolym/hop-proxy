<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg md:text-xl font-bold">会话管理</h2>
      <button @click="load"
        class="px-3 py-1.5 md:px-4 md:py-2 text-sm text-gray-600 border border-gray-300 rounded-md hover:bg-gray-50">
        刷新
      </button>
    </div>

    <p class="text-xs text-gray-400 mb-4">
      登录会话与一次性授权的状态、来源与授权应用；可强制下线会话，其应用授权一并失效。
    </p>

    <!-- 筛选 -->
    <div class="flex flex-wrap gap-2 mb-4">
      <select v-model="filterSource" @change="load"
        class="px-3 py-1.5 border border-gray-300 rounded-md text-sm text-gray-700 bg-white focus:outline-none focus:ring-2 focus:ring-blue-500">
        <option value="">全部来源</option>
        <option v-for="(label, key) in sourceLabels" :key="key" :value="key">{{ label }}</option>
      </select>
      <select v-model="filterStatus" @change="load"
        class="px-3 py-1.5 border border-gray-300 rounded-md text-sm text-gray-700 bg-white focus:outline-none focus:ring-2 focus:ring-blue-500">
        <option value="">全部状态</option>
        <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
      </select>
      <select v-model.number="filterUser" @change="load"
        class="px-3 py-1.5 border border-gray-300 rounded-md text-sm text-gray-700 bg-white focus:outline-none focus:ring-2 focus:ring-blue-500">
        <option :value="0">全部用户</option>
        <option v-for="u in users" :key="u.user_id" :value="u.user_id">{{ u.username }}</option>
      </select>
      <input v-model="search" type="text" placeholder="搜索 ID / 用户 / 来源"
        class="px-3 py-1.5 border border-gray-300 rounded-md text-sm text-gray-700 bg-white focus:outline-none focus:ring-2 focus:ring-blue-500 w-full sm:w-56" />
    </div>

    <!-- 桌面端表格 -->
    <div class="hidden md:block bg-white rounded-lg border border-gray-200 overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-gray-50 text-gray-600">
          <tr>
            <th class="w-8"></th>
            <th class="text-left px-3 py-3 font-medium">ID</th>
            <th class="text-left px-3 py-3 font-medium">来源</th>
            <th class="text-left px-4 py-3 font-medium">用户</th>
            <th class="text-left px-4 py-3 font-medium">状态</th>
            <th class="text-left px-4 py-3 font-medium">开始时间</th>
            <th class="text-left px-4 py-3 font-medium" title="取「会话操作」与「授权应用使用」两者中较晚的时间">最后活跃</th>
            <th class="text-left px-4 py-3 font-medium">过期时间</th>
            <th class="text-left px-4 py-3 font-medium">授权应用</th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <template v-for="s in visibleSessions" :key="s.id">
            <tr class="hover:bg-gray-50 cursor-pointer" @click="toggleExpand(s)">
              <td class="pl-3 text-gray-400 text-xs">{{ expandedId === s.id ? '▾' : '▸' }}</td>
              <td class="px-3 py-3 text-gray-400 text-xs whitespace-nowrap">{{ s.short_id }}</td>
              <td class="px-3 py-3">
                <span class="text-xs px-2 py-0.5 rounded whitespace-nowrap" :class="sourceCls(s.source)">{{ sourceLabel(s.source) }}</span>
              </td>
              <td class="px-4 py-3">{{ s.username || `#${s.user_id}` }}</td>
              <td class="px-4 py-3">
                <span class="text-xs px-2 py-0.5 rounded whitespace-nowrap" :class="statusInfo(s).cls">{{ statusInfo(s).label }}</span>
              </td>
              <td class="px-4 py-3 text-gray-500 text-xs whitespace-nowrap">{{ formatDateTime(s.created_at) }}</td>
              <td class="px-4 py-3 text-gray-500 text-xs whitespace-nowrap">{{ formatDateTime(s.last_active_at) }}</td>
              <td class="px-4 py-3 text-gray-500 text-xs whitespace-nowrap">{{ formatDateTime(s.expires_at) }}</td>
              <td class="px-4 py-3 text-gray-600">{{ s.grant_count }}</td>
              <td class="px-4 py-3 text-right">
                <button v-if="isActive(s)" @click.stop="handleRevoke(s)"
                  class="text-red-600 hover:text-red-800 text-xs">强制下线</button>
                <button v-else-if="canDelete(s)" @click.stop="handleDelete(s)"
                  class="text-gray-500 hover:text-gray-700 text-xs">删除记录</button>
              </td>
            </tr>
            <tr v-if="expandedId === s.id">
              <td colspan="10" class="bg-gray-50 px-4 py-3">
                <div class="text-xs text-gray-500 mb-2 break-all">
                  {{ s.short_id }} · IP: {{ s.ip || '-' }} · UA: {{ s.user_agent || '-' }}
                  <span v-if="sourceRefInfo(s)"> · {{ sourceRefInfo(s) }}</span>
                  <span v-if="s.ended_at"> · 结束于: {{ formatDateTime(s.ended_at) }}</span>
                </div>
                <LoadingSpinner v-if="grantsLoading" />
                <table v-else-if="grants.length > 0" class="w-full text-xs bg-white rounded border border-gray-200">
                  <thead class="text-gray-500">
                    <tr>
                      <th class="text-left px-3 py-2 font-medium">应用</th>
                      <th class="text-left px-3 py-2 font-medium">子域名</th>
                      <th class="text-left px-3 py-2 font-medium">授权时间</th>
                      <th class="text-left px-3 py-2 font-medium">最近使用</th>
                      <th class="text-left px-3 py-2 font-medium">过期时间</th>
                      <th class="text-left px-3 py-2 font-medium">状态</th>
                      <th class="text-right px-3 py-2 font-medium">操作</th>
                    </tr>
                  </thead>
                  <tbody class="divide-y divide-gray-100">
                    <tr v-for="(g, i) in grants" :key="i">
                      <td class="px-3 py-2">{{ g.app_name || `#${g.app_id}` }}</td>
                      <td class="px-3 py-2 text-gray-500">{{ g.subdomain }}</td>
                      <td class="px-3 py-2 text-gray-500">{{ formatDateTime(g.created_at) }}</td>
                      <td class="px-3 py-2 text-gray-500">{{ formatDateTime(g.last_request_at) }}</td>
                      <td class="px-3 py-2 text-gray-500">{{ formatDateTime(g.expires_at) }}</td>
                      <td class="px-3 py-2">
                        <span class="px-1.5 py-0.5 rounded" :class="grantStatusInfo(g).cls">{{ grantStatusInfo(g).label }}</span>
                      </td>
                      <td class="px-3 py-2 text-right">
                        <button @click.stop="handleDeleteGrant(g)"
                          class="text-xs"
                          :class="grantStatusInfo(g).label === '有效' ? 'text-red-600 hover:text-red-800' : 'text-gray-500 hover:text-gray-700'">删除</button>
                      </td>
                    </tr>
                  </tbody>
                </table>
                <p v-else class="text-xs text-gray-400 py-2">该会话暂无应用授权</p>
              </td>
            </tr>
          </template>
          <tr v-if="loading">
            <td colspan="10"><LoadingSpinner /></td>
          </tr>
          <tr v-else-if="visibleSessions.length === 0">
            <td colspan="10" class="px-4 py-8 text-center text-gray-400">{{ search ? '无匹配的会话' : '暂无会话记录' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 移动端卡片列表 -->
    <div class="md:hidden space-y-3">
      <div v-if="loading" class="bg-white rounded-lg border border-gray-200">
        <LoadingSpinner />
      </div>
      <div v-else-if="visibleSessions.length === 0"
        class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        {{ search ? '无匹配的会话' : '暂无会话记录' }}
      </div>
      <div v-for="s in visibleSessions" :key="s.id" class="bg-white rounded-lg border border-gray-200 p-4">
        <div class="flex items-start justify-between mb-2">
          <div class="flex items-center gap-2 flex-wrap">
            <span class="text-xs px-2 py-0.5 rounded" :class="sourceCls(s.source)">{{ sourceLabel(s.source) }}</span>
            <span class="text-xs px-2 py-0.5 rounded" :class="statusInfo(s).cls">{{ statusInfo(s).label }}</span>
          </div>
          <span class="text-sm text-gray-700">{{ s.username || `#${s.user_id}` }} <span class="text-xs text-gray-400">{{ s.short_id }}</span></span>
        </div>
        <div class="text-xs text-gray-400 space-y-0.5 mb-2">
          <div>开始：{{ formatDateTime(s.created_at) }}</div>
          <div>最后活跃：{{ formatDateTime(s.last_active_at) }}</div>
          <div>过期：{{ formatDateTime(s.expires_at) }}</div>
          <div>授权应用 {{ s.grant_count }} 个</div>
        </div>
        <div class="flex items-center justify-between pt-2 border-t border-gray-100">
          <button @click="toggleExpand(s)" class="text-blue-600 text-xs py-1">
            {{ expandedId === s.id ? '收起详情' : '查看详情' }}
          </button>
          <button v-if="isActive(s)" @click="handleRevoke(s)" class="text-red-600 text-xs py-1">强制下线</button>
          <button v-else-if="canDelete(s)" @click="handleDelete(s)" class="text-gray-500 text-xs py-1">删除记录</button>
        </div>
        <div v-if="expandedId === s.id" class="mt-2 pt-2 border-t border-gray-100">
          <div class="text-xs text-gray-400 mb-2 break-all">
            {{ s.short_id }} · IP: {{ s.ip || '-' }} · UA: {{ s.user_agent || '-' }}
            <span v-if="sourceRefInfo(s)"><br>{{ sourceRefInfo(s) }}</span>
          </div>
          <LoadingSpinner v-if="grantsLoading" />
          <div v-else-if="grants.length > 0" class="space-y-2">
            <div v-for="(g, i) in grants" :key="i" class="border border-gray-100 rounded p-2">
              <div class="flex items-center justify-between text-xs mb-1">
                <span class="text-gray-700">{{ g.app_name || `#${g.app_id}` }}</span>
                <span class="flex items-center gap-2">
                  <span class="px-1.5 py-0.5 rounded" :class="grantStatusInfo(g).cls">{{ grantStatusInfo(g).label }}</span>
                  <button @click="handleDeleteGrant(g)" class="text-xs"
                    :class="grantStatusInfo(g).label === '有效' ? 'text-red-600' : 'text-gray-500'">删除</button>
                </span>
              </div>
              <div class="text-xs text-gray-400 space-y-0.5">
                <div>{{ g.subdomain }}</div>
                <div>授权于 {{ formatDateTime(g.created_at) }} · 最近使用 {{ formatDateTime(g.last_request_at) }}</div>
                <div>过期 {{ formatDateTime(g.expires_at) }}</div>
              </div>
            </div>
          </div>
          <p v-else class="text-xs text-gray-400 py-1">该会话暂无应用授权</p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { listSessions, listSessionUsers, getSession, revokeSession, deleteSession, deleteSessionGrant, type AuthSession, type SessionGrant, type SessionUser } from '../api/sessions'
import LoadingSpinner from '../components/LoadingSpinner.vue'

// 来源展示（#80）
const sourceLabels: Record<string, string> = {
  password: '密码登录',
  passkey: '通行密钥',
  temp_login: '临时登录',
  share_code: '分享码',
  qr: '扫码授权',
}

function sourceLabel(source: string): string {
  return sourceLabels[source] || source || '-'
}

function sourceCls(source: string): string {
  switch (source) {
    case 'password': return 'bg-blue-50 text-blue-600'
    case 'passkey': return 'bg-indigo-50 text-indigo-600'
    case 'temp_login': return 'bg-amber-50 text-amber-600'
    case 'share_code': return 'bg-purple-50 text-purple-600'
    case 'qr': return 'bg-emerald-50 text-emerald-600'
    default: return 'bg-gray-100 text-gray-600'
  }
}

// 会话有效 = 状态 active 且未过期（服务端只存状态，过期是否按时间判定）
function isActive(s: AuthSession): boolean {
  return s.status === 'active' && new Date(s.expires_at).getTime() > Date.now()
}

function statusInfo(s: AuthSession): { label: string; cls: string } {
  if (s.status === 'logged_out') return { label: '已退出', cls: 'bg-gray-100 text-gray-500' }
  if (s.status === 'revoked') return { label: '已下线', cls: 'bg-red-50 text-red-600' }
  if (!isActive(s)) {
    // 会话已过期但授权仍在生效：不可删除（应用访问继续有效），需先删除相关授权
    if (s.has_active_grants) return { label: '已过期（授权生效中）', cls: 'bg-orange-50 text-orange-600' }
    return { label: '已过期', cls: 'bg-amber-50 text-amber-600' }
  }
  // 有效但 7 天无任何活跃 → 不活跃
  const activeAt = s.last_active_at ? new Date(s.last_active_at).getTime() : 0
  if (Date.now() - activeAt > IDLE_AFTER_MS) return { label: '不活跃', cls: 'bg-teal-50 text-teal-600' }
  return { label: '有效', cls: 'bg-green-50 text-green-600' }
}

// 可删除：会话已失效且无生效中的授权（否则后端会拒绝）
function canDelete(s: AuthSession): boolean {
  return !isActive(s) && !s.has_active_grants
}

function grantStatusInfo(g: SessionGrant): { label: string; cls: string } {
  if (g.revoked_at) return { label: '已失效', cls: 'bg-red-50 text-red-600' }
  if (new Date(g.expires_at).getTime() < Date.now()) return { label: '已过期', cls: 'bg-amber-50 text-amber-600' }
  return { label: '有效', cls: 'bg-green-50 text-green-600' }
}

// 授权来源展示（#80）：扫码 → 批准方的登录会话（#短 ID，可在列表中对上）；
// 分享码 → 分享码本身（可在分享码页对上）；其余来源无引用
function sourceRefInfo(s: AuthSession): string {
  if (!s.source_ref) return ''
  if (s.source === 'qr') return `授权来源会话: ${s.source_ref}`
  if (s.source === 'share_code') return `分享码: ${s.source_ref}`
  return s.source_ref
}

function formatDateTime(dateStr: string | null): string {
  if (!dateStr) return '-'
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return '-'
  return d.toLocaleString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  })
}

const sessions = ref<AuthSession[]>([])
const users = ref<SessionUser[]>([])
const loading = ref(true)
const filterSource = ref('')
const filterStatus = ref('')
const filterUser = ref(0)
const search = ref('')

// 状态筛选项（与列表展示的状态列一一对应，服务端按派生状态过滤）
const statusOptions = [
  { value: 'valid', label: '有效' },
  { value: 'idle', label: '不活跃' },
  { value: 'expired_with_grants', label: '已过期（授权生效中）' },
  { value: 'expired', label: '已过期' },
  { value: 'logged_out', label: '已退出' },
  { value: 'revoked', label: '已下线' },
]

// 有效会话的「不活跃」阈值：7 天无任何活跃（会话操作或授权使用），服务端筛选同口径
const IDLE_AFTER_MS = 7 * 24 * 60 * 60 * 1000

// 本地搜索：按短 ID / 用户名 / 来源引用匹配（列表已按服务端筛选加载）
const visibleSessions = computed(() => {
  const q = search.value.trim().toLowerCase().replace(/^#/, '')
  if (!q) return sessions.value
  return sessions.value.filter(s =>
    s.short_id.toLowerCase().includes(q) ||
    s.username.toLowerCase().includes(q) ||
    s.source_ref.toLowerCase().includes(q)
  )
})
const expandedId = ref(0)
const grants = ref<SessionGrant[]>([])
const grantsLoading = ref(false)

async function load() {
  expandedId.value = 0
  grants.value = []
  const res = await listSessions({
    user_id: filterUser.value || undefined,
    source: filterSource.value || undefined,
    status: filterStatus.value || undefined,
  })
  if (res.data) sessions.value = res.data
}

async function toggleExpand(s: AuthSession) {
  if (expandedId.value === s.id) {
    expandedId.value = 0
    return
  }
  expandedId.value = s.id
  grants.value = []
  grantsLoading.value = true
  const res = await getSession(s.id)
  if (res.data && expandedId.value === s.id) {
    grants.value = res.data.grants || []
  }
  grantsLoading.value = false
}

// 删除记录：仅已失效会话（已退出/已下线/已过期）可删，有效会话需先强制下线
async function handleDelete(s: AuthSession) {
  if (!confirm(`确定删除该会话记录（${s.short_id}）？其应用授权记录一并删除。`)) return
  const res = await deleteSession(s.id)
  if (res.error) {
    alert(res.error)
    return
  }
  await load()
}

// 删除单条授权：仍有效的授权删除即撤销（应用访问立即失效）
async function handleDeleteGrant(g: SessionGrant) {
  const tip = grantStatusInfo(g).label === '有效'
    ? '该授权仍有效，删除将立即撤销该应用的访问。确定删除？'
    : '确定删除该授权记录？'
  if (!confirm(tip)) return
  const res = await deleteSessionGrant(expandedId.value, g.grant_ref)
  if (res.error) {
    alert(res.error)
    return
  }
  await refreshExpanded()
}

// 刷新当前展开会话的授权明细与列表行聚合（保持展开态）
async function refreshExpanded() {
  const id = expandedId.value
  const res = await getSession(id)
  if (res.data && expandedId.value === id) {
    grants.value = res.data.grants || []
    const updated = res.data.session
    sessions.value = sessions.value.map(s => s.id === id
      ? { ...s, grant_count: updated.grant_count, has_active_grants: updated.has_active_grants }
      : s)
  }
}

async function handleRevoke(s: AuthSession) {
  const who = s.username || `#${s.user_id}`
  if (!confirm(`确定强制下线该会话（${sourceLabel(s.source)} · ${who}）？其全部应用授权将一并失效。`)) return
  const res = await revokeSession(s.id)
  if (res.error) {
    alert(res.error)
    return
  }
  await load()
}

onMounted(async () => {
  const u = await listSessionUsers()
  if (u.data) users.value = u.data
  await load()
  loading.value = false
})
</script>
