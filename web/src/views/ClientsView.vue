<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg md:text-xl font-bold">客户端管理</h2>
      <button @click="showCreate = true"
        class="px-3 py-1.5 md:px-4 md:py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
        + 添加
      </button>
    </div>

    <!-- 桌面端表格 -->
    <div class="hidden md:block bg-white rounded-lg border border-gray-200 overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-gray-50 text-gray-600">
          <tr>
            <th class="text-left px-4 py-3 font-medium">名称</th>
            <th class="text-left px-4 py-3 font-medium">状态</th>
            <th class="text-left px-4 py-3 font-medium">代理</th>
            <th class="text-left px-4 py-3 font-medium">应用数</th>
            <th class="text-left px-4 py-3 font-medium">UUID</th>
            <th class="text-left px-4 py-3 font-medium">版本</th>
            <th class="text-left px-4 py-3 font-medium">活跃连接数</th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <tr v-for="client in clients" :key="client.id" class="hover:bg-gray-50">
            <td class="px-4 py-3">
              <span v-if="client.id === '__host__' || editingId !== client.id">
                {{ client.name }}
              </span>
              <input v-else v-model="editName" @keyup.enter="saveEdit(client)" @keyup.escape="editingId = ''"
                class="px-2 py-1 border border-gray-300 rounded text-sm w-40" autofocus />
            </td>
            <td class="px-4 py-3">
              <span v-if="client.id === '__host__'" class="flex items-center gap-1.5 text-blue-600">
                <span class="w-2 h-2 rounded-full flex-shrink-0 bg-blue-500"></span>
                内置
              </span>
              <span v-else :class="client.online ? 'text-green-600' : 'text-gray-400'" class="flex items-center gap-1.5">
                <span class="w-2 h-2 rounded-full flex-shrink-0" :class="client.online ? 'bg-green-500' : 'bg-gray-300'"></span>
                {{ client.online ? '在线' : '离线' }}
                <span v-if="client.online" class="text-xs font-normal ml-0.5">
                  <template v-if="latencies[client.id] === undefined"><span class="text-gray-400">…</span></template>
                  <template v-else-if="latencies[client.id] < 0"><span class="text-gray-400">超时</span></template>
                  <template v-else><span :class="latencyColor(latencies[client.id])">{{ latencies[client.id] }}ms</span></template>
                </span>
              </span>
            </td>
            <td class="px-4 py-3">
              <span v-if="client.id !== '__host__' && client.proxy_enabled" class="text-blue-600 text-xs bg-blue-50 px-1.5 py-0.5 rounded">
                {{ client.proxy_listen || ':8080' }}
              </span>
              <span v-else class="text-gray-400 text-xs">-</span>
            </td>
            <td class="px-4 py-3 text-gray-500">{{ client.app_count }}</td>
            <td class="px-4 py-3">
              <span v-if="client.id === '__host__'" class="text-gray-400 text-xs">-</span>
              <code v-else class="text-xs bg-gray-100 px-2 py-1 rounded select-all">{{ client.id }}</code>
            </td>
            <td class="px-4 py-3 text-xs text-gray-500">{{ client.version || '-' }}</td>
            <td class="px-4 py-3 text-xs">
              <span v-if="client.id === '__host__' || !client.online" class="text-gray-400">-</span>
              <router-link v-else :to="{ name: 'client-conns', params: { id: client.id } }"
                class="text-blue-600 hover:text-blue-800 hover:underline">
                {{ client.active_streams }} / {{ client.conn_count }}
              </router-link>
            </td>
            <td class="px-4 py-3 text-right space-x-2">
              <template v-if="client.id !== '__host__'">
                <button @click="copyCommand(client.id)"
                  class="text-gray-500 hover:text-green-600 text-xs transition-colors">
                  {{ copiedId === client.id ? '✓ 已复制' : '复制命令' }}
                </button>
              </template>
              <button @click="openProxyManager(client.id)" class="text-purple-600 hover:text-purple-800 text-xs">代理</button>
              <template v-if="client.id !== '__host__'">
                <button @click="startEdit(client)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
                <button @click="handleDelete(client.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
              </template>
            </td>
          </tr>
          <tr v-if="loading">
            <td colspan="8"><LoadingSpinner /></td>
          </tr>
          <tr v-else-if="clients.length === 0">
            <td colspan="8" class="px-4 py-8 text-center text-gray-400">暂无客户端，点击右上角添加</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 移动端卡片列表 -->
    <div class="md:hidden space-y-3">
      <div v-if="loading" class="bg-white rounded-lg border border-gray-200">
        <LoadingSpinner />
      </div>
      <div v-else-if="clients.length === 0" class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        暂无客户端，点击右上角添加
      </div>
      <div v-for="client in clients" :key="client.id"
        class="bg-white rounded-lg border border-gray-200 p-4">
        <div class="flex items-start justify-between mb-2">
          <div class="flex-1 min-w-0">
            <div class="flex items-center gap-2 mb-1">
              <span v-if="client.id === '__host__' || editingId !== client.id" class="font-medium text-gray-900">
                {{ client.name }}
              </span>
              <input v-else v-model="editName" @keyup.enter="saveEdit(client)" @keyup.escape="editingId = ''"
                class="px-2 py-0.5 border border-gray-300 rounded text-sm w-full" autofocus />
              <span v-if="client.id === '__host__'" class="text-blue-600 bg-blue-50 inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded-full flex-shrink-0">
                <span class="w-1.5 h-1.5 rounded-full bg-blue-500"></span>
                内置
              </span>
              <span v-else :class="client.online ? 'text-green-600 bg-green-50' : 'text-gray-400 bg-gray-100'"
                class="inline-flex items-center gap-1 text-xs px-1.5 py-0.5 rounded-full flex-shrink-0">
                <span class="w-1.5 h-1.5 rounded-full" :class="client.online ? 'bg-green-500' : 'bg-gray-300'"></span>
                {{ client.online ? '在线' : '离线' }}
                <template v-if="client.online && latencies[client.id] !== undefined && latencies[client.id] >= 0">
                  <span :class="latencyColor(latencies[client.id])">{{ latencies[client.id] }}ms</span>
                </template>
              </span>
              <span v-if="client.id !== '__host__' && client.proxy_enabled" class="text-blue-600 text-xs bg-blue-50 px-1.5 py-0.5 rounded flex-shrink-0">
                {{ client.proxy_listen || ':8080' }}
              </span>
            </div>
            <code v-if="client.id !== '__host__'" class="text-xs text-gray-400 break-all">{{ client.id }}</code>
          </div>
          <span class="text-xs text-gray-400 ml-2 flex-shrink-0">
            {{ client.app_count }} 个应用
            <router-link v-if="client.id !== '__host__' && client.online" :to="{ name: 'client-conns', params: { id: client.id } }"
              class="ml-1 text-blue-600 hover:underline">
              {{ client.active_streams }}/{{ client.conn_count }}
            </router-link>
          </span>
        </div>
        <div class="flex gap-3 pt-2 border-t border-gray-100">
          <template v-if="client.id !== '__host__'">
            <button @click="startEdit(client)" class="text-blue-600 text-xs py-1">编辑</button>
          </template>
          <button @click="openProxyManager(client.id)" class="text-purple-600 text-xs py-1">代理</button>
          <template v-if="client.id !== '__host__'">
            <button @click="copyCommand(client.id)"
              class="text-xs py-1 transition-colors"
              :class="copiedId === client.id ? 'text-green-600' : 'text-gray-500'">
              {{ copiedId === client.id ? '✓ 已复制' : '复制命令' }}
            </button>
            <button @click="handleDelete(client.id)" class="text-red-600 text-xs py-1 ml-auto">删除</button>
          </template>
        </div>
      </div>
    </div>

    <!-- 创建弹窗 -->
    <div v-if="showCreate" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showCreate = false">
      <div class="bg-white w-full md:w-96 md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-4">添加客户端</h3>
        <form @submit.prevent="handleCreate">
          <div class="mb-4">
            <label class="block text-sm font-medium text-gray-700 mb-1">名称</label>
            <input v-model="createName" type="text" required autofocus
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div v-if="createError" class="text-sm text-red-600 mb-4">{{ createError }}</div>
          <div class="flex gap-2">
            <button type="button" @click="showCreate = false"
              class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
            <button type="submit"
              class="flex-1 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">创建</button>
          </div>
        </form>
      </div>
    </div>

    <!-- 编辑弹窗 -->
    <div v-if="editingId" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="editingId = ''">
      <div class="bg-white w-full md:w-96 md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-4">编辑客户端</h3>
        <form @submit.prevent="saveEdit(editingClient!)">
          <div class="mb-4">
            <label class="block text-sm font-medium text-gray-700 mb-1">名称</label>
            <input v-model="editName" type="text" required
              class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
          </div>
          <div v-if="editError" class="text-sm text-red-600 mb-4">{{ editError }}</div>
          <div class="flex gap-2">
            <button type="button" @click="editingId = ''"
              class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消</button>
            <button type="submit"
              class="flex-1 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">保存</button>
          </div>
        </form>
      </div>
    </div>

    <!-- 代理管理弹窗 -->
    <div v-if="showProxyModal" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showProxyModal = false">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="p-5">
          <div class="flex items-center justify-between mb-4">
            <h3 class="text-lg font-bold">代理管理</h3>
            <button @click="showProxyModal = false" class="text-gray-400 hover:text-gray-600 text-xl">&times;</button>
          </div>

          <!-- 代理列表 -->
          <div v-if="proxyLoading" class="text-center py-4 text-gray-400">加载中...</div>
          <div v-else-if="proxyList.length === 0" class="text-center py-8 text-gray-400 text-sm">
            暂无代理配置
          </div>
          <div v-else class="space-y-2 mb-4">
            <div v-for="proxy in proxyList" :key="proxy.id"
              class="border border-gray-200 rounded-lg p-3">
              <div class="flex items-start justify-between">
                <div class="flex-1">
                  <div class="font-medium text-sm">{{ proxy.name }}</div>
                  <div class="text-xs text-gray-500 mt-1">
                    <span class="bg-purple-50 text-purple-700 px-1.5 py-0.5 rounded">{{ proxy.proxy_type.toUpperCase() }}</span>
                    <template v-if="proxy.proxy_type === 'peer'">
                      <span class="ml-2 text-purple-600">{{ peerName(proxy.target_client_id) }}</span>
                    </template>
                    <template v-else>
                      <span class="ml-2">{{ proxy.proxy_address }}</span>
                    </template>
                  </div>
                </div>
                <div class="flex gap-2">
                  <button @click="openProxyEdit(proxy)" class="text-blue-600 text-xs">编辑</button>
                  <button @click="handleProxyDelete(proxy.id)" class="text-red-600 text-xs">删除</button>
                </div>
              </div>
            </div>
          </div>

          <!-- 新建/编辑表单 -->
          <div class="border-t border-gray-200 pt-4">
            <h4 class="text-sm font-medium text-gray-700 mb-3">
              {{ proxyEditMode === 'create' ? '添加代理' : '编辑代理' }}
            </h4>
            <form @submit.prevent="handleProxySubmit" class="space-y-3">
              <div>
                <label class="block text-xs text-gray-600 mb-1">代理名称</label>
                <input v-model="proxyForm.name" type="text" required
                  class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-1 focus:ring-purple-500"
                  placeholder="如：公司代理" />
              </div>
              <div>
                <label class="block text-xs text-gray-600 mb-1">代理类型</label>
                <BaseSelect v-model="proxyForm.proxy_type"
                  :options="[{ value: 'socks5', label: 'SOCKS5' }, { value: 'shadowsocks', label: 'Shadowsocks' }, { value: 'peer', label: '其他客户端' }]"
                  btn-class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-1 focus:ring-purple-500" />
              </div>
              <div v-if="proxyForm.proxy_type === 'peer'">
                <label class="block text-xs text-gray-600 mb-1">目标客户端</label>
                <BaseSelect v-model="proxyForm.target_client_id"
                  :options="peerOptions"
                  placeholder="请选择客户端"
                  btn-class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-1 focus:ring-purple-500" />
              </div>
              <div>
                <label class="block text-xs text-gray-600 mb-1">{{ proxyForm.proxy_type === 'peer' ? '连接地址' : '代理地址' }}</label>
                <input v-model="proxyForm.proxy_address" type="text" :required="proxyForm.proxy_type !== 'peer'" autocomplete="off"
                  class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-1 focus:ring-purple-500"
                  :placeholder="proxyForm.proxy_type === 'peer' ? '目标客户端的 IP:端口（如 192.168.1.100:8080）' : '如：127.0.0.1:1080'" />
              </div>
              <div v-if="proxyForm.proxy_type === 'peer'" class="text-xs text-gray-400">
                留空则通过服务端隧道转发，填写则客户端直连目标客户端的本地代理端口
              </div>
              <div v-if="proxyForm.proxy_type !== 'peer'">
                <label class="block text-xs text-gray-600 mb-1">代理地址</label>
                <input v-model="proxyForm.proxy_address" type="text" required autocomplete="off"
                  class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-1 focus:ring-purple-500"
                  placeholder="如：127.0.0.1:1080" />
              </div>
              <div v-if="proxyForm.proxy_type !== 'peer'">
                <label class="block text-xs text-gray-600 mb-1">认证密码（可选）</label>
                <input v-model="proxyForm.proxy_password" type="password" autocomplete="new-password"
                  class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-1 focus:ring-purple-500"
                  :placeholder="proxyForm.proxy_type === 'shadowsocks' ? '加密方法:密码（如 aes-256-gcm:mypassword）' : 'SOCKS5: 用户名:密码'" />
              </div>
              <div v-if="proxyError" class="text-xs text-red-600">{{ proxyError }}</div>
              <div class="flex gap-2">
                <button v-if="proxyEditMode === 'edit'" type="button" @click="openProxyCreate"
                  class="flex-1 py-2 text-sm text-gray-600 border border-gray-300 rounded-md">取消编辑</button>
                <button type="submit"
                  class="flex-1 py-2 bg-purple-600 text-white text-sm rounded-md hover:bg-purple-700">
                  {{ proxyEditMode === 'create' ? '添加' : '保存' }}
                </button>
              </div>
            </form>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { listClients, createClient, updateClient, deleteClient, pingClient, listAvailablePeers, type Client, type AvailablePeer } from '../api/clients'
import { getSettings } from '../api/settings'
import { listProxies, createProxy, updateProxy, deleteProxy, type ClientProxy } from '../api/proxies'
import BaseSelect from '../components/BaseSelect.vue'
import LoadingSpinner from '../components/LoadingSpinner.vue'

const clients = ref<Client[]>([])
const loading = ref(true)
const showCreate = ref(false)
const createName = ref('')
const createError = ref('')
const editingId = ref('')
const editingClient = ref<Client | null>(null)
const editName = ref('')
const editError = ref('')
const latencies = ref<Record<string, number>>({})
const adminDomain = ref('')
const copiedId = ref('')  // 刚刚复制的客户端 ID（用于短暂显示「已复制」）
let pingTimer: ReturnType<typeof setInterval> | null = null

// 代理管理相关
const showProxyModal = ref(false)
const proxyClientId = ref('')
const proxyList = ref<ClientProxy[]>([])
const proxyLoading = ref(false)
const proxyEditMode = ref<'create' | 'edit'>('create')
const proxyEditId = ref(0)
const proxyForm = ref({
  name: '',
  proxy_type: 'socks5',
  proxy_address: '',
  proxy_password: '',
  target_client_id: '',
})
const proxyError = ref('')
const availablePeers = ref<AvailablePeer[]>([])

const peerOptions = computed(() =>
  availablePeers.value.map(p => ({ value: p.id, label: `${p.name}（${p.proxy_listen}）` }))
)

// 根据当前协议推导 wss/ws
function connectCommand(clientId: string): string {
  const wsProto = window.location.protocol === 'https:' ? 'wss' : 'ws'
  const domain = adminDomain.value || window.location.host
  return `./hop-proxy-client --connect ${wsProto}://${domain}/ws/${clientId}`
}

async function copyCommand(clientId: string) {
  try {
    await navigator.clipboard.writeText(connectCommand(clientId))
    copiedId.value = clientId
    setTimeout(() => { copiedId.value = '' }, 2000)
  } catch {
    // 降级：选中 input
  }
}

async function loadClients() {
  const res = await listClients()
  if (res.data) clients.value = res.data
}

async function refreshLatencies() {
  const onlineClients = clients.value.filter(c => c.online && c.id !== '__host__')
  await Promise.all(onlineClients.map(async (c) => {
    const res = await pingClient(c.id)
    if (res.data !== undefined) latencies.value[c.id] = res.data.latency_ms
  }))
}

function latencyColor(ms: number): string {
  if (ms < 50) return 'text-green-500'
  if (ms < 150) return 'text-yellow-500'
  return 'text-red-500'
}

async function handleCreate() {
  createError.value = ''
  const res = await createClient(createName.value)
  if (res.error) { createError.value = res.error; return }
  showCreate.value = false
  createName.value = ''
  await loadClients()
}

function startEdit(client: Client) {
  editingId.value = client.id
  editingClient.value = client
  editName.value = client.name
  editError.value = ''
}

async function saveEdit(client: Client) {
  editError.value = ''
  const res = await updateClient(client.id, editName.value)
  if (res.error) { editError.value = res.error; return }
  editingId.value = ''
  editingClient.value = null
  await loadClients()
}

async function handleDelete(id: string) {
  if (!confirm('确定删除此客户端？关联的应用将变为未关联状态。')) return
  await deleteClient(id)
  await loadClients()
}

// 代理管理相关函数
async function openProxyManager(clientId: string) {
  proxyClientId.value = clientId
  showProxyModal.value = true
  proxyLoading.value = true
  proxyError.value = ''
  const [proxyRes, peerRes] = await Promise.all([
    listProxies(clientId),
    listAvailablePeers(),
  ])
  proxyLoading.value = false
  if (proxyRes.data) {
    proxyList.value = proxyRes.data
  } else {
    proxyList.value = []
  }
  if (peerRes.data) {
    availablePeers.value = peerRes.data
  } else {
    availablePeers.value = []
  }
}

function openProxyCreate() {
  proxyEditMode.value = 'create'
  proxyEditId.value = 0
  proxyForm.value = { name: '', proxy_type: 'socks5', proxy_address: '', proxy_password: '', target_client_id: '' }
  proxyError.value = ''
}

function openProxyEdit(proxy: ClientProxy) {
  proxyEditMode.value = 'edit'
  proxyEditId.value = proxy.id
  proxyForm.value = {
    name: proxy.name,
    proxy_type: proxy.proxy_type,
    proxy_address: proxy.proxy_address,
    proxy_password: '',  // 密码已脱敏，编辑时需要重新输入
    target_client_id: proxy.target_client_id || '',
  }
  proxyError.value = ''
}

async function handleProxySubmit() {
  if (!proxyForm.value.name) {
    proxyError.value = '代理名称不能为空'
    return
  }
  if (proxyForm.value.proxy_type !== 'peer' && !proxyForm.value.proxy_address) {
    proxyError.value = '代理地址不能为空'
    return
  }
  if (proxyForm.value.proxy_type === 'peer' && !proxyForm.value.target_client_id) {
    proxyError.value = '请选择目标客户端'
    return
  }
  proxyError.value = ''
  proxyError.value = ''

  const data = {
    name: proxyForm.value.name,
    proxy_type: proxyForm.value.proxy_type,
    ...(proxyForm.value.proxy_address ? { proxy_address: proxyForm.value.proxy_address } : {}),
    ...(proxyForm.value.proxy_password ? { proxy_password: proxyForm.value.proxy_password } : {}),
    ...(proxyForm.value.proxy_type === 'peer' ? { target_client_id: proxyForm.value.target_client_id } : {}),
  }

  if (proxyEditMode.value === 'create') {
    const res = await createProxy(proxyClientId.value, data)
    if (res.error) { proxyError.value = res.error; return }
  } else {
    const res = await updateProxy(proxyEditId.value, data)
    if (res.error) { proxyError.value = res.error; return }
  }
  // 刷新列表
  await openProxyManager(proxyClientId.value)
  proxyEditMode.value = 'create'
  proxyEditId.value = 0
  proxyForm.value = { name: '', proxy_type: 'socks5', proxy_address: '', proxy_password: '', target_client_id: '' }
}

async function handleProxyDelete(id: number) {
  if (!confirm('确定删除此代理？')) return
  const res = await deleteProxy(id)
  if (res.error) {
    proxyError.value = res.error
    return
  }
  // 刷新列表
  await openProxyManager(proxyClientId.value)
}

function peerName(clientId: string | null | undefined): string {
  if (!clientId) return '-'
  const peer = availablePeers.value.find(p => p.id === clientId)
  return peer ? peer.name : clientId
}

onMounted(async () => {
  const [, settingsRes] = await Promise.all([loadClients(), getSettings()])
  loading.value = false
  if (settingsRes.data) adminDomain.value = settingsRes.data.admin_domain || ''
  await refreshLatencies()
  pingTimer = setInterval(async () => {
    await loadClients()
    await refreshLatencies()
  }, 15000)
})

onUnmounted(() => {
  if (pingTimer) clearInterval(pingTimer)
})
</script>
