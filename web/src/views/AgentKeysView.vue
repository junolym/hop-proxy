<template>
  <div>
    <div class="flex items-center justify-between mb-4">
      <h2 class="text-lg md:text-xl font-bold">代理密钥</h2>
      <button @click="openCreate"
        class="px-3 py-1.5 md:px-4 md:py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
        + 新增
      </button>
    </div>

    <p class="text-xs text-gray-400 mb-4">
      代理密钥用于安全代理（agent）。创建后获得 UUID，agent 启动时用此 UUID 连接服务端，应用选择密钥后通过加密隧道连接 agent。
    </p>

    <!-- 桌面端表格 -->
    <div class="hidden md:block bg-white rounded-lg border border-gray-200 overflow-hidden">
      <table class="w-full text-sm">
        <thead class="bg-gray-50 text-gray-600">
          <tr>
            <th class="text-left px-4 py-3 font-medium">名称</th>
            <th class="text-left px-4 py-3 font-medium">UUID</th>
            <th class="text-left px-4 py-3 font-medium">创建时间</th>
            <th class="text-right px-4 py-3 font-medium">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <tr v-for="k in keys" :key="k.id" class="hover:bg-gray-50">
            <td class="px-4 py-3 font-medium">{{ k.name }}</td>
            <td class="px-4 py-3">
              <code class="text-xs text-gray-600 select-all">{{ k.uuid }}</code>
            </td>
            <td class="px-4 py-3 text-gray-500 text-xs">{{ formatDateTime(k.created_at) }}</td>
            <td class="px-4 py-3 text-right space-x-2">
              <button @click="copyUUID(k.uuid)" class="text-gray-600 hover:text-gray-800 text-xs">复制UUID</button>
              <button @click="openEdit(k)" class="text-blue-600 hover:text-blue-800 text-xs">编辑</button>
              <button @click="handleDelete(k.id)" class="text-red-600 hover:text-red-800 text-xs">删除</button>
            </td>
          </tr>
          <tr v-if="loading">
            <td colspan="4"><LoadingSpinner /></td>
          </tr>
          <tr v-else-if="keys.length === 0">
            <td colspan="4" class="px-4 py-8 text-center text-gray-400">暂无代理密钥，点击右上角新增</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 移动端卡片列表 -->
    <div class="md:hidden space-y-3">
      <div v-if="loading" class="bg-white rounded-lg border border-gray-200">
        <LoadingSpinner />
      </div>
      <div v-else-if="keys.length === 0" class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
        暂无代理密钥，点击右上角新增
      </div>
      <div v-for="k in keys" :key="k.id" class="bg-white rounded-lg border border-gray-200 p-4">
        <div class="flex items-start justify-between mb-2">
          <span class="font-medium text-sm text-gray-900">{{ k.name }}</span>
        </div>
        <div class="text-xs text-gray-400 mb-2">
          UUID: <code class="select-all">{{ k.uuid }}</code>
        </div>
        <div class="flex items-center justify-between pt-2 border-t border-gray-100">
          <span class="text-xs text-gray-400">{{ formatDateTime(k.created_at) }}</span>
          <div class="flex gap-3">
            <button @click="copyUUID(k.uuid)" class="text-gray-600 text-xs py-1">复制UUID</button>
            <button @click="openEdit(k)" class="text-blue-600 text-xs py-1">编辑</button>
            <button @click="handleDelete(k.id)" class="text-red-600 text-xs py-1">删除</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showForm" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center"
      @click.self="showForm = false">
      <div class="bg-white w-full md:w-[32rem] md:rounded-lg rounded-t-2xl shadow-xl max-h-[90vh] overflow-y-auto">
        <div class="p-5">
          <h3 class="text-lg font-bold mb-4">{{ formMode === 'create' ? '新增代理密钥' : '编辑代理密钥' }}</h3>
          <form @submit.prevent="handleSubmit" class="space-y-4">
            <div>
              <label class="block text-sm font-medium text-gray-700 mb-1">名称</label>
              <input v-model="form.name" type="text" required
                class="w-full px-3 py-2 border border-gray-300 rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-blue-500" />
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

    <!-- 创建后展示 UUID 弹窗 -->
    <div v-if="newKey" class="fixed inset-0 bg-black/40 z-50 flex items-end md:items-center justify-center">
      <div class="bg-white w-full md:w-[30rem] md:rounded-lg rounded-t-2xl shadow-xl p-6">
        <h3 class="text-lg font-bold mb-2">代理密钥已创建</h3>
        <p class="text-sm text-gray-500 mb-4">请记录 UUID，agent 启动时需要此 UUID。</p>
        <div class="bg-gray-50 border border-gray-200 rounded p-3 mb-4">
          <p class="text-xs text-gray-500 mb-1">UUID</p>
          <code class="text-sm break-all select-all text-gray-800">{{ newKey.uuid }}</code>
        </div>
        <div class="text-xs text-gray-500 mb-4">
          agent 启动命令示例：<br>
          <code class="select-all">hop-proxy-agent --server=https://&lt;管理域名&gt; --uuid={{ newKey.uuid }} --listen=[ip]:port --forward=ip:port</code>
        </div>
        <button @click="copyUUID(newKey.uuid)"
          class="w-full mb-2 py-2 bg-blue-600 text-white text-sm rounded-md hover:bg-blue-700">
          {{ copyToast === '已复制 UUID' ? '✓ 已复制' : '复制 UUID' }}
        </button>
        <button @click="newKey = null"
          class="w-full py-2 text-sm text-gray-600 border border-gray-300 rounded-md">
          关闭
        </button>
      </div>
    </div>

    <!-- 复制提示 -->
    <div v-if="copyToast" class="fixed bottom-20 md:bottom-4 left-1/2 -translate-x-1/2 bg-gray-800 text-white text-sm px-4 py-2 rounded-md shadow-lg z-50">
      {{ copyToast }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { listAgentKeys, createAgentKey, updateAgentKey, deleteAgentKey, type AgentKey } from '../api/agent_keys'
import LoadingSpinner from '../components/LoadingSpinner.vue'

function formatDateTime(dateStr: string): string {
  return new Date(dateStr).toLocaleString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit',
  })
}

const keys = ref<AgentKey[]>([])
const loading = ref(true)
const showForm = ref(false)
const formMode = ref<'create' | 'edit'>('create')
const formError = ref('')
const editId = ref(0)
const newKey = ref<AgentKey | null>(null)
const copyToast = ref('')

const form = ref({ name: '' })

async function loadKeys() {
  const res = await listAgentKeys()
  if (res.data) keys.value = res.data
}

function openCreate() {
  formMode.value = 'create'
  form.value = { name: '' }
  formError.value = ''
  showForm.value = true
}

function openEdit(k: AgentKey) {
  formMode.value = 'edit'
  editId.value = k.id
  form.value = { name: k.name }
  formError.value = ''
  showForm.value = true
}

async function handleSubmit() {
  formError.value = ''
  if (!form.value.name.trim()) {
    formError.value = '名称不能为空'
    return
  }

  if (formMode.value === 'create') {
    const res = await createAgentKey({ name: form.value.name })
    if (res.error) { formError.value = res.error; return }
    showForm.value = false
    if (res.data) {
      newKey.value = res.data
    }
  } else {
    const res = await updateAgentKey(editId.value, { name: form.value.name })
    if (res.error) { formError.value = res.error; return }
    showForm.value = false
  }
  await loadKeys()
}

async function handleDelete(id: number) {
  if (!confirm('确定删除此代理密钥？关联的应用将无法通过 agent 连接。')) return
  await deleteAgentKey(id)
  await loadKeys()
}

async function copyUUID(uuid: string) {
  try {
    await navigator.clipboard.writeText(uuid)
    copyToast.value = '已复制 UUID'
    setTimeout(() => { copyToast.value = '' }, 2000)
  } catch {
    // ignore
  }
}

onMounted(async () => {
  await loadKeys()
  loading.value = false
})
</script>
