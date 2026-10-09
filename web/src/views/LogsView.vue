<template>
  <div>
    <h2 class="text-lg md:text-xl font-bold mb-4">系统日志</h2>

    <!-- 工具栏 -->
    <div class="bg-white rounded-lg border border-gray-200 p-3 mb-3 flex flex-wrap items-center gap-3">
      <div class="flex items-center gap-2">
        <label class="text-xs text-gray-500">日期</label>
        <select v-model="selectedDate" @change="onChangeDate"
          class="px-2 py-1 border border-gray-300 rounded text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
          <option v-for="d in dates" :key="d" :value="d">{{ dateLabel(d) }}</option>
        </select>
      </div>
      <div class="flex items-center gap-2">
        <label class="text-xs text-gray-500">条数</label>
        <select v-model.number="filters.limit"
          class="px-2 py-1 border border-gray-300 rounded text-sm focus:outline-none focus:ring-2 focus:ring-blue-500">
          <option :value="50">50</option>
          <option :value="100">100</option>
          <option :value="200">200</option>
          <option :value="500">500</option>
        </select>
      </div>
      <button @click="fetchLogs()"
        class="px-3 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">
        查询
      </button>
      <button @click="resetAllFilters"
        class="px-3 py-1.5 bg-gray-100 text-gray-700 text-sm rounded hover:bg-gray-200">
        重置筛选
      </button>
      <button @click="copyLogs"
        class="px-3 py-1.5 bg-gray-100 text-gray-700 text-sm rounded hover:bg-gray-200"
        :disabled="logs.length === 0"
        :class="{ 'opacity-50 cursor-not-allowed': logs.length === 0 }">
        {{ copyBtnText }}
      </button>
      <button @click="downloadLogs"
        class="px-3 py-1.5 bg-gray-100 text-gray-700 text-sm rounded hover:bg-gray-200"
        :disabled="logs.length === 0"
        :class="{ 'opacity-50 cursor-not-allowed': logs.length === 0 }">
        下载
      </button>
      <button @click="togglePause"
        :class="paused ? 'bg-green-600 text-white' : 'bg-gray-100 text-gray-700'"
        class="px-3 py-1.5 text-sm rounded hover:opacity-90">
        {{ paused ? '继续' : '暂停' }}
      </button>
      <span v-if="!paused" class="flex items-center gap-1 text-xs text-green-600">
        <span class="w-2 h-2 bg-green-500 rounded-full inline-block animate-pulse"></span>
        实时
      </span>
      <span v-else class="flex items-center gap-1 text-xs text-gray-400">
        <span class="w-2 h-2 bg-gray-400 rounded-full inline-block"></span>
        已暂停
      </span>
      <div v-if="activeFilterCount > 0" class="ml-auto text-xs text-blue-600">
        {{ activeFilterCount }} 项筛选激活
      </div>
    </div>

    <!-- 日志表格（表头常驻：加载中/空结果时筛选按钮不消失） -->
    <div class="bg-white rounded-lg border border-gray-200 overflow-hidden">
      <div class="overflow-x-auto">
        <table ref="tableRef" class="w-full text-sm">
          <thead class="bg-gray-50 border-b border-gray-200">
            <tr>
              <th class="px-3 py-2 text-left text-xs text-gray-500 font-medium whitespace-nowrap w-44">时间</th>
              <th v-for="col in filterableCols" :key="col.field"
                class="px-3 py-2 text-left text-xs text-gray-500 font-medium whitespace-nowrap">
                <div class="flex items-center gap-1">
                  <span>{{ col.label }}</span>
                  <button
                    :class="isFilterActive(col.field) ? 'bg-blue-500 text-white' : 'bg-gray-200 text-gray-600 hover:bg-gray-300'"
                    class="ml-1 w-4 h-4 inline-flex items-center justify-center text-xs rounded"
                    @click="openFilter($event, col.field)"
                    title="筛选">▼</button>
                </div>
              </th>
              <th class="px-3 py-2 text-left text-xs text-gray-500 font-medium whitespace-nowrap">补充信息</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100">
            <!-- 占位行必须跨全部 10 列，保证表头（含筛选条）始终可见 -->
            <tr v-if="loading">
              <td colspan="10" class="p-8 text-center text-gray-500 text-sm">加载中...</td>
            </tr>
            <tr v-else-if="error">
              <td colspan="10" class="p-8 text-center text-red-600 text-sm">{{ error }}</td>
            </tr>
            <tr v-else-if="logs.length === 0">
              <td colspan="10" class="p-8 text-center text-gray-400 text-sm">暂无日志</td>
            </tr>
            <template v-else>
              <tr v-for="log in logs" :key="log.id" class="hover:bg-gray-50"
                @contextmenu.prevent="onCellContextmenu($event, log)">
                <td class="px-3 py-2 text-xs text-gray-600 whitespace-nowrap font-mono">
                  {{ formatTime(log.ts) }}
                </td>
                <td class="px-3 py-2 whitespace-nowrap">
                  <span :class="levelClass(log.level)" class="px-2 py-0.5 text-xs rounded font-medium">
                    {{ log.level }}
                  </span>
                </td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap">{{ log.source || '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap">{{ log.type || '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap">
                  {{ log.username || (log.user_id ? `#${log.user_id}` : '-') }}
                </td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap">{{ log.subdomain || '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap">{{ log.app_id ?? '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap font-mono">{{ log.request_id || '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-700 whitespace-nowrap">{{ log.message || '-' }}</td>
                <td class="px-3 py-2 text-xs text-gray-800 min-w-[300px] max-w-[600px]">
                  <div v-if="log.fields && log.fields.length" class="flex flex-wrap gap-x-3 gap-y-0.5">
                    <span v-for="f in log.fields" :key="f.k">
                      <span class="text-gray-400">{{ f.k }}=</span><span class="font-mono">{{ f.v }}</span>
                    </span>
                  </div>
                  <span v-else class="text-gray-300">-</span>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 筛选弹层 -->
    <div v-if="filterPopover.open" class="fixed inset-0 z-40" @click="closeFilter">
      <div class="absolute bg-white border border-gray-200 rounded shadow-lg min-w-[200px] max-h-[320px] flex flex-col"
        :style="popoverStyle"
        @click.stop>
        <div class="p-2 border-b border-gray-100">
          <input v-model="filterSearch" type="text" placeholder="搜索..."
            class="w-full px-2 py-1 text-xs border border-gray-300 rounded focus:outline-none focus:ring-1 focus:ring-blue-500" />
        </div>
        <div class="flex-1 overflow-y-auto py-1">
          <div v-if="filteredFilterOptions.length === 0" class="px-3 py-2 text-xs text-gray-400">无可选项</div>
          <label v-for="o in filteredFilterOptions" :key="o.value"
            class="flex items-center px-3 py-1 text-xs hover:bg-gray-50 cursor-pointer">
            <input type="checkbox" :checked="localSelection.includes(o.value)" @change="toggleSelect(o.value)"
              class="mr-2" />
            <span>{{ o.label }}</span>
          </label>
        </div>
        <div class="flex items-center justify-between p-2 border-t border-gray-100">
          <button @click="clearFilter" class="px-2 py-1 text-xs text-gray-500 hover:text-red-600">清空</button>
          <div class="flex gap-2">
            <button @click="closeFilter"
              class="px-2 py-1 text-xs bg-gray-100 text-gray-700 rounded hover:bg-gray-200">取消</button>
            <button @click="applyFilter"
              class="px-2 py-1 text-xs bg-blue-600 text-white rounded hover:bg-blue-700">应用</button>
          </div>
        </div>
      </div>
    </div>

    <!-- 右键菜单 -->
    <div v-if="ctxMenu.open"
      class="fixed z-50 bg-white border border-gray-200 rounded shadow-lg py-1 text-sm min-w-[140px]"
      :style="{ top: ctxMenu.y + 'px', left: ctxMenu.x + 'px' }"
      @click.stop>
      <button @click="ctxOnlyThis" class="block w-full text-left px-3 py-1.5 hover:bg-gray-100">
        只看该{{ ctxMenu.fieldLabel }}
      </button>
      <button v-if="ctxMenu.field === 'level'" @click="ctxLevelAndHigher" class="block w-full text-left px-3 py-1.5 hover:bg-gray-100">
        只看该等级及更高
      </button>
      <button @click="ctxExcludeThis" class="block w-full text-left px-3 py-1.5 hover:bg-gray-100">
        排除该{{ ctxMenu.fieldLabel }}
      </button>
      <div class="border-t border-gray-100 my-1"></div>
      <button @click="ctxCopyValue" class="block w-full text-left px-3 py-1.5 hover:bg-gray-100 text-gray-600">
        复制值
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import {
  getLogs, getLogSources, getLogTypes, getLogMsgTypes, getLogUsers, getLogDates, createLogStream, LOG_LEVELS,
  type LogEntry, type LogFilters,
} from '../api/logs'

// —— 表格列定义 ——
type FilterableField = 'level' | 'source' | 'type' | 'subdomain' | 'user_id' | 'app_id' | 'request_id' | 'message'

const filterableCols: { field: FilterableField; label: string }[] = [
  { field: 'level', label: '等级' },
  { field: 'source', label: '来源' },
  { field: 'type', label: '类型' },
  { field: 'user_id', label: '用户' },
  { field: 'subdomain', label: '子域名' },
  { field: 'app_id', label: '应用ID' },
  { field: 'request_id', label: '请求ID' },
  { field: 'message', label: '消息类型' },
]

// —— 状态 ——
const tableRef = ref<HTMLTableElement | null>(null)
const logs = ref<LogEntry[]>([])
const loading = ref(false)
const error = ref('')
const sources = ref<string[]>([])
const types = ref<string[]>([])
const msgTypes = ref<string[]>([])
const users = ref<{ id: number; username: string }[]>([])
let eventSource: EventSource | null = null
const paused = ref(false)

// —— 日期选择（按天滚动的日志文件）——
const dates = ref<string[]>([])
const selectedDate = ref('')

// todayStr 返回本地今天的 YYYYMMDD
function todayStr(): string {
  const d = new Date()
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}`
}

// dateLabel 把 YYYYMMDD 转成展示格式 YYYY-MM-DD（今天加标注）
function dateLabel(d: string): string {
  const label = `${d.slice(0, 4)}-${d.slice(4, 6)}-${d.slice(6, 8)}`
  return d === todayStr() ? `${label}（今天）` : label
}

// isToday 当前选中的是否今天（只有今天才连 SSE 实时流）
function isToday(): boolean {
  return selectedDate.value === todayStr()
}

// onChangeDate 切换日期：重新拉取日志和筛选元数据
function onChangeDate() {
  logs.value = []
  fetchLogs()
  fetchMeta()
}

const filters = reactive<{
  level: string[]
  source: string[]
  type: string[]
  subdomain: string[]
  user_id: string[]
  app_id: string[]
  request_id: string[]
  message: string[]
  limit: number
  offset: number
}>({
  // 默认筛选 info 及更高（排除 debug）
  level: ['info', 'warning', 'error', 'critical'],
  source: [],
  type: [],
  subdomain: [],
  user_id: [],
  app_id: [],
  request_id: [],
  message: [],
  limit: 100,
  offset: 0,
})

// —— 筛选弹层 ——
const filterPopover = reactive<{
  open: boolean
  field: FilterableField
  x: number
  y: number
}>({
  open: false,
  field: 'level',
  x: 0,
  y: 0,
})

const filterSearch = ref('')
const localSelection = ref<string[]>([])

const popoverStyle = computed(() => {
  const x = Math.min(filterPopover.x, window.innerWidth - 220)
  const y = Math.min(filterPopover.y, window.innerHeight - 320)
  return { left: x + 'px', top: y + 'px' }
})

const filterOptions = computed<{ value: string; label: string }[]>(() => {
  switch (filterPopover.field) {
    case 'level':
      return LOG_LEVELS.map(v => ({ value: v, label: v }))
    case 'source':
      return sources.value.map(v => ({ value: v, label: v }))
    case 'type':
      return types.value.map(v => ({ value: v, label: v }))
    case 'subdomain':
      return uniqueFromLogs('subdomain').map(v => ({ value: v, label: v }))
    case 'user_id':
      return users.value.map(u => ({ value: String(u.id), label: u.username || `#${u.id}` }))
    case 'app_id':
      return uniqueFromLogs('app_id').map(v => ({ value: v, label: v }))
    case 'request_id':
      return uniqueFromLogs('request_id').map(v => ({ value: v, label: v }))
    case 'message':
      return msgTypes.value.map(v => ({ value: v, label: v }))
  }
})

const filteredFilterOptions = computed(() => {
  if (!filterSearch.value) return filterOptions.value
  const q = filterSearch.value.toLowerCase()
  return filterOptions.value.filter(o => o.label.toLowerCase().includes(q))
})

function uniqueFromLogs(field: 'subdomain' | 'app_id' | 'request_id'): string[] {
  const seen = new Set<string>()
  for (const l of logs.value) {
    const v = (l as any)[field]
    if (v !== undefined && v !== null && v !== '') {
      seen.add(String(v))
    }
  }
  return Array.from(seen).sort()
}

function isFilterActive(field: FilterableField): boolean {
  return (filters as any)[field].length > 0
}

function openFilter(evt: MouseEvent, field: FilterableField) {
  filterPopover.field = field
  filterPopover.x = evt.clientX
  filterPopover.y = evt.clientY + 20
  // 复制当前已选到本地
  const cur = (filters as any)[field] as (string | number)[]
  localSelection.value = cur.map(String)
  filterSearch.value = ''
  filterPopover.open = true
}

function closeFilter() {
  filterPopover.open = false
}

function toggleSelect(v: string) {
  const i = localSelection.value.indexOf(v)
  if (i >= 0) localSelection.value.splice(i, 1)
  else localSelection.value.push(v)
}

function applyFilter() {
  const field = filterPopover.field
  const selected = localSelection.value
  // user_id/app_id 用 string 类型（支持 "__empty__"）
  ;(filters as any)[field] = [...selected]
  filterPopover.open = false
  fetchLogs()
}

function clearFilter() {
  const field = filterPopover.field
  ;(filters as any)[field] = []
  filterPopover.open = false
  fetchLogs()
}

// —— 右键菜单 ——
const ctxMenu = reactive<{
  open: boolean
  x: number
  y: number
  field: FilterableField
  fieldLabel: string
  value: string
}>({
  open: false,
  x: 0,
  y: 0,
  field: 'level',
  fieldLabel: '',
  value: '',
})

function onCellContextmenu(evt: MouseEvent, log: LogEntry) {
  const tds = (evt.target as HTMLElement)?.closest('tr')?.querySelectorAll('td')
  if (!tds) return
  let idx = -1
  for (let i = 0; i < tds.length; i++) {
    if (tds[i].contains(evt.target as Node)) {
      idx = i
      break
    }
  }
  if (idx <= 0) return
  // 列顺序：0=time, 1=level, 2=source, 3=type, 4=user_id, 5=subdomain, 6=app_id, 7=request_id, 8=message, 9=extra
  const fields: (FilterableField | null)[] = [null, 'level', 'source', 'type', 'user_id', 'subdomain', 'app_id', 'request_id', 'message', null]
  const field = fields[idx]
  if (!field) return

  // 取值：空值统一为空字符串，用于"只看空值"筛选
  let value = ''
  let displayValue = ''
  if (field === 'user_id') {
    value = log.user_id != null ? String(log.user_id) : ''
    displayValue = log.username || value || '(空)'
  } else if (field === 'app_id') {
    value = log.app_id != null ? String(log.app_id) : ''
    displayValue = value || '(空)'
  } else {
    value = String((log as any)[field] ?? '')
    displayValue = value || '(空)'
  }

  const labels: Record<FilterableField, string> = {
    level: '等级',
    source: '来源',
    type: '类型',
    subdomain: '子域名',
    user_id: '用户',
    app_id: '应用ID',
    request_id: '请求ID',
    message: '消息类型',
  }

  ctxMenu.open = true
  ctxMenu.x = evt.clientX
  ctxMenu.y = evt.clientY
  ctxMenu.field = field
  ctxMenu.fieldLabel = labels[field]
  ctxMenu.value = value
  // 用于复制
  ;(ctxMenu as any).displayValue = displayValue
}

function ctxOnlyThis() {
  const { field, value } = ctxMenu
  // user_id/app_id 用 string 类型（空值用 "__empty__"）
  ;(filters as any)[field] = [value || '__empty__']
  closeCtxMenu()
  fetchLogs()
}

// ctxLevelAndHigher：等级右键"只看该等级及更高"
// LOG_LEVELS 顺序为 critical > error > warning > info > debug
// 索引 0 是最高（critical），选中当前等级及所有更高等级（索引更小）
function ctxLevelAndHigher() {
  const level = ctxMenu.value
  const idx = LOG_LEVELS.indexOf(level as any)
  if (idx < 0) return
  // idx 及更小（更高等级）
  filters.level = [...LOG_LEVELS.slice(0, idx + 1)]
  closeCtxMenu()
  fetchLogs()
}

function ctxExcludeThis() {
  const { field, value } = ctxMenu
  // 排除：把除当前外的所有可选项都选中
  // 先收集当前字段所有可选值
  let allOptions: string[] = []
  switch (field) {
    case 'level':
      allOptions = [...LOG_LEVELS]
      break
    case 'source':
      allOptions = [...sources.value]
      break
    case 'type':
      allOptions = [...types.value]
      break
    case 'subdomain':
      allOptions = uniqueFromLogs('subdomain')
      break
    case 'user_id':
      allOptions = users.value.map(u => String(u.id))
      break
    case 'app_id':
      allOptions = uniqueFromLogs('app_id')
      break
    case 'request_id':
      allOptions = uniqueFromLogs('request_id')
      break
    case 'message':
      allOptions = [...msgTypes.value]
      break
  }
  // 排除当前值：选中所有非当前值的选项
  const excludeVal = value || '__empty__'
  const selected = allOptions.filter(v => v !== value && v !== excludeVal)
  ;(filters as any)[field] = selected
  closeCtxMenu()
  fetchLogs()
}

function ctxCopyValue() {
  navigator.clipboard?.writeText((ctxMenu as any).displayValue || ctxMenu.value)
  closeCtxMenu()
}

function closeCtxMenu() {
  ctxMenu.open = false
}

// —— 数据拉取 ——
// fetchLogs 全量拉取初始日志（时间倒序）。SSE 连接建立后增量 prepend。
async function fetchLogs() {
  loading.value = true
  error.value = ''
  const params: LogFilters = { ...filters, date: selectedDate.value || undefined }
  const res = await getLogs(params)
  loading.value = false
  if (res.error) {
    error.value = res.error
    return
  }
  if (res.data) {
    logs.value = res.data.logs || []
  }
  // 拉取全量后建立 SSE 订阅（仅当天视图有实时流；历史日期无新日志）
  if (isToday()) {
    connectSSE()
  } else {
    disconnectSSE()
  }
}

// connectSSE 建立 SSE 订阅，新日志按时间戳有序插入列表头部，超出 limit 截断尾部
// 仅当天视图连接（历史日期无新日志）
function connectSSE() {
  if (paused.value) return // 暂停状态不重连
  if (!isToday()) return // 历史日期无实时流
  disconnectSSE()
  const sseParams: LogFilters = { ...filters }
  delete sseParams.limit
  delete sseParams.offset
  eventSource = createLogStream(sseParams)
  eventSource.onmessage = (ev: MessageEvent) => {
    try {
      const log: LogEntry = JSON.parse(ev.data)
      // 按时间戳有序插入：找到第一条 ts <= 当前 ts 的位置，插入其前面
      let insertIdx = 0
      for (let i = 0; i < logs.value.length; i++) {
        if (logs.value[i].ts <= log.ts) {
          insertIdx = i
          break
        }
        insertIdx = i + 1
      }
      logs.value.splice(insertIdx, 0, log)
      // 按 limit 截断尾部
      const maxLen = filters.limit || 100
      if (logs.value.length > maxLen) {
        logs.value = logs.value.slice(0, maxLen)
      }
    } catch {
      // 忽略解析失败
    }
  }
  eventSource.onerror = () => {
    // 浏览器会自动重连；不额外处理
  }
}

function disconnectSSE() {
  if (eventSource) {
    eventSource.close()
    eventSource = null
  }
}

// togglePause 暂停/继续 SSE 实时更新
function togglePause() {
  if (paused.value) {
    // 继续：重新建立 SSE 连接
    paused.value = false
    connectSSE()
  } else {
    // 暂停：断开 SSE，保留当前列表
    paused.value = true
    disconnectSSE()
  }
}

async function fetchMeta() {
  const date = selectedDate.value || undefined
  const [srcRes, typeRes, msgTypeRes, usersRes] = await Promise.all([
    getLogSources(date),
    getLogTypes(date),
    getLogMsgTypes(date),
    getLogUsers(date),
  ])
  if (srcRes.data) sources.value = srcRes.data.sources || []
  if (typeRes.data) types.value = typeRes.data.types || []
  if (msgTypeRes.data) msgTypes.value = msgTypeRes.data.messages || []
  if (usersRes.data) users.value = usersRes.data.users || []
}

function resetAllFilters() {
  filters.level = ['info', 'warning', 'error', 'critical']
  filters.source = []
  filters.type = []
  filters.subdomain = []
  filters.user_id = []
  filters.app_id = []
  filters.request_id = []
  filters.message = []
  filters.offset = 0
  fetchLogs()
}

// —— 导出当前日志（筛选+截断后）为可读文本 ——
function logsToText(): string {
  const header = ['时间', '等级', '来源', '类型', '用户', '子域名', '应用ID', '请求ID', '消息类型', '补充信息']
  const lines = [header.join('\t')]
  for (const log of logs.value) {
    const row = [
      formatTime(log.ts),
      log.level,
      log.source || '-',
      log.type || '-',
      log.username || (log.user_id ? `#${log.user_id}` : '-'),
      log.subdomain || '-',
      log.app_id != null ? String(log.app_id) : '-',
      log.request_id || '-',
      log.message || '-',
      log.fields && log.fields.length
        ? log.fields.map(f => `${f.k}=${f.v}`).join(' ')
        : '',
    ]
    lines.push(row.join('\t'))
  }
  return lines.join('\n')
}

function downloadLogs() {
  if (logs.value.length === 0) return
  const text = logsToText()
  const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  const ts = new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)
  a.download = `hopproxy-logs-${ts}.txt`
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
}

const copyBtnText = ref('复制')
async function copyLogs() {
  if (logs.value.length === 0) return
  const text = logsToText()
  try {
    await navigator.clipboard.writeText(text)
    copyBtnText.value = '已复制'
  } catch {
    // fallback：用 textarea + execCommand
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    try {
      document.execCommand('copy')
      copyBtnText.value = '已复制'
    } catch {
      copyBtnText.value = '复制失败'
    }
    document.body.removeChild(ta)
  }
  setTimeout(() => { copyBtnText.value = '复制' }, 2000)
}

function formatTime(ts: number): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, '0')}`
}

function levelClass(level: string): string {
  switch (level) {
    case 'critical': return 'bg-red-200 text-red-900'
    case 'error': return 'bg-red-100 text-red-700'
    case 'warning': return 'bg-amber-100 text-amber-700'
    case 'info': return 'bg-blue-50 text-blue-700'
    case 'debug': return 'bg-gray-100 text-gray-600'
    default: return 'bg-gray-100 text-gray-600'
  }
}

const activeFilterCount = computed(() => {
  let count = 0
  if (filters.level.length > 0) count++
  if (filters.source.length > 0) count++
  if (filters.type.length > 0) count++
  if (filters.subdomain.length > 0) count++
  if (filters.user_id.length > 0) count++
  if (filters.app_id.length > 0) count++
  if (filters.request_id.length > 0) count++
  if (filters.message.length > 0) count++
  return count
})

// 全局点击关闭右键菜单
function onDocClick() {
  if (ctxMenu.open) closeCtxMenu()
}

onMounted(async () => {
  // 先取日期列表（默认选最近一天），再拉数据和元数据
  const datesRes = await getLogDates()
  if (datesRes.data && datesRes.data.dates.length > 0) {
    dates.value = datesRes.data.dates
  }
  if (dates.value.length === 0) {
    dates.value = [todayStr()]
  }
  selectedDate.value = dates.value[0]
  fetchMeta()
  fetchLogs()
  document.addEventListener('click', onDocClick)
})

onUnmounted(() => {
  disconnectSSE()
  document.removeEventListener('click', onDocClick)
})
</script>
