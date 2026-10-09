import { api } from './client'

export interface LogEntry {
  id: number
  ts: number // 毫秒时间戳
  level: string
  source: string
  client_id?: string
  type: string
  subdomain?: string
  request_id?: string // 全链路请求关联 ID（请求无关日志为 "-"）
  user_id?: number
  username?: string
  app_id?: number
  message: string
  fields?: { k: string; v: string }[]
}

export interface LogListResponse {
  logs: LogEntry[]
}

export interface LogFilters {
  date?: string          // 日志日期 YYYYMMDD（空 = 当天）
  level?: string[]      // 多选
  source?: string[]
  type?: string[]
  subdomain?: string[]
  request_id?: string[] // 全链路请求关联 ID（精确匹配）
  user_id?: string[]    // int64 字符串或 "__empty__"
  app_id?: string[]     // int64 字符串或 "__empty__"
  message?: string[]    // 消息类型筛选
  limit?: number
  offset?: number
  after_id?: number  // 增量查询：只返回 ID > after_id 的日志
}

function toMultiParam(arr?: string[] | number[]): string {
  if (!arr || arr.length === 0) return ''
  return arr.map(String).join(',')
}

function filtersToParams(filters: LogFilters = {}): string {
  const params = new URLSearchParams()
  if (filters.date) params.set('date', filters.date)
  const level = toMultiParam(filters.level)
  if (level) params.set('level', level)
  const source = toMultiParam(filters.source)
  if (source) params.set('source', source)
  const type = toMultiParam(filters.type)
  if (type) params.set('type', type)
  const subdomain = toMultiParam(filters.subdomain)
  if (subdomain) params.set('subdomain', subdomain)
  const requestId = toMultiParam(filters.request_id)
  if (requestId) params.set('request_id', requestId)
  const userId = toMultiParam(filters.user_id)
  if (userId) params.set('user_id', userId)
  const appId = toMultiParam(filters.app_id)
  if (appId) params.set('app_id', appId)
  const message = toMultiParam(filters.message)
  if (message) params.set('message', message)
  if (filters.limit !== undefined) params.set('limit', String(filters.limit))
  if (filters.offset !== undefined) params.set('offset', String(filters.offset))
  if (filters.after_id !== undefined) params.set('after_id', String(filters.after_id))
  return params.toString()
}

export function getLogs(filters: LogFilters = {}) {
  const query = filtersToParams(filters)
  return api.get<LogListResponse>(`/api/admin/logs${query ? '?' + query : ''}`)
}

// createLogStream 创建 SSE 订阅，实时推送新日志
export function createLogStream(filters: LogFilters = {}): EventSource {
  const query = filtersToParams(filters)
  return new EventSource(`/api/admin/logs/stream${query ? '?' + query : ''}`)
}

// getLogDates 返回日志目录中真实存在的日期列表（YYYYMMDD，从新到旧）
export function getLogDates() {
  return api.get<{ dates: string[] }>('/api/admin/logs/dates')
}

export function getLogSources(date?: string) {
  const q = date ? `?date=${date}` : ''
  return api.get<{ sources: string[] }>(`/api/admin/logs/sources${q}`)
}

export function getLogTypes(date?: string) {
  const q = date ? `?date=${date}` : ''
  return api.get<{ types: string[] }>(`/api/admin/logs/types${q}`)
}

export function getLogMsgTypes(date?: string) {
  const q = date ? `?date=${date}` : ''
  return api.get<{ messages: string[] }>(`/api/admin/logs/msg-types${q}`)
}

export function getLogUsers(date?: string) {
  const q = date ? `?date=${date}` : ''
  return api.get<{ users: { id: number; username: string }[] }>(`/api/admin/logs/users${q}`)
}

export const LOG_LEVELS = ['critical', 'error', 'warning', 'info', 'debug'] as const
