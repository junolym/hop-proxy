<template>
  <div>
    <div class="flex items-center gap-3 mb-4">
      <button @click="router.back()" class="text-gray-500 hover:text-gray-700 text-sm">&larr; 返回</button>
      <h2 class="text-lg md:text-xl font-bold">连接详情</h2>
      <span v-if="clientName" class="text-sm text-gray-500">{{ clientName }}</span>
    </div>

    <div v-if="loading" class="text-center py-12 text-gray-400">加载中...</div>
    <div v-else-if="conns.length === 0"
      class="text-center py-12 text-gray-400 text-sm bg-white rounded-lg border border-gray-200">
      暂无连接（客户端可能离线）
    </div>
    <div v-else class="bg-white rounded-lg border border-gray-200 overflow-hidden overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="bg-gray-50 text-gray-600">
          <tr>
            <th class="text-left px-4 py-3 font-medium">远程地址</th>
            <th class="text-left px-4 py-3 font-medium">创建时间</th>
            <th class="text-left px-4 py-3 font-medium">活跃流</th>
            <th class="text-left px-4 py-3 font-medium">延迟</th>
            <th class="text-left px-4 py-3 font-medium">状态</th>
            <th class="text-right px-4 py-3 font-medium">上传</th>
            <th class="text-right px-4 py-3 font-medium">下载</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100">
          <tr v-for="(conn, i) in conns" :key="i" class="hover:bg-gray-50">
            <td class="px-4 py-3 text-gray-700">{{ conn.remote_addr || '-' }}</td>
            <td class="px-4 py-3 text-xs text-gray-500">{{ formatTime(conn.created_at) }}</td>
            <td class="px-4 py-3 text-gray-500">{{ conn.num_streams }}</td>
            <td class="px-4 py-3 text-gray-500">
              <span v-if="conn.latency_ms < 0">-</span>
              <span v-else>{{ conn.latency_ms }}ms</span>
            </td>
            <td class="px-4 py-3">
              <span v-if="conn.draining"
                class="text-yellow-600 text-xs bg-yellow-50 px-1.5 py-0.5 rounded">draining</span>
              <span v-else class="text-green-600 text-xs bg-green-50 px-1.5 py-0.5 rounded">活跃</span>
            </td>
            <td class="px-4 py-3 text-right text-gray-500">{{ formatBytes(conn.bytes_recv) }}</td>
            <td class="px-4 py-3 text-right text-gray-500">{{ formatBytes(conn.bytes_sent) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { fetchClientConns, listClients, type ConnDetail } from '../api/clients'

const route = useRoute()
const router = useRouter()
const conns = ref<ConnDetail[]>([])
const loading = ref(true)
const clientName = ref('')
let timer: ReturnType<typeof setInterval> | null = null

function formatTime(iso: string): string {
  if (!iso || iso === '0001-01-01T00:00:00Z') return '-'
  const d = new Date(iso)
  return d.toLocaleString('zh-CN', { hour12: false })
}

function formatBytes(n: number): string {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

async function load() {
  const id = route.params.id as string
  const res = await fetchClientConns(id)
  if (res.data) conns.value = res.data
  else conns.value = []
  loading.value = false
}

onMounted(async () => {
  const id = route.params.id as string
  // 获取客户端名称用于标题展示
  const clientsRes = await listClients()
  if (clientsRes.data) {
    const c = clientsRes.data.find(c => c.id === id)
    if (c) clientName.value = c.name
  }
  await load()
  timer = setInterval(load, 5000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>
