import { api } from './client'

// 登录会话（#80）：会话管理层的主体
export interface AuthSession {
  id: number
  short_id: string // 长 ID（sid）前 8 位：展示与关联搜索用
  user_id: number
  username: string
  source: string // password / passkey / temp_login / share_code / qr
  source_ref: string // 来源引用（分享码 ID / 扫码 sid 等）
  status: string // active / logged_out / revoked
  ip: string
  user_agent: string
  created_at: string
  last_active_at: string | null
  expires_at: string
  ended_at: string | null
  grant_count: number
  has_active_grants: boolean // 是否仍有生效中的应用授权（过期会话保护删除用）
}

// 会话下的应用授权（sso_sessions 审计视角）
export interface SessionGrant {
  grant_ref: number // 单条授权句柄（删除用）
  app_id: number
  app_name: string
  subdomain: string
  created_at: string
  expires_at: string
  last_request_at: string | null // 该授权最近一次被使用的时间
  revoked_at: string | null
}

// 会话筛选下拉项（拥有会话的用户，含管理员）
export interface SessionUser {
  user_id: number
  username: string
}

// 拥有登录会话的用户列表（用户管理接口不含管理员，此处专用）
export function listSessionUsers() {
  return api.get<SessionUser[]>('/api/admin/sessions/users')
}

export function listSessions(params?: { user_id?: number; source?: string; status?: string }) {
  const q = new URLSearchParams()
  if (params?.user_id) q.set('user_id', String(params.user_id))
  if (params?.source) q.set('source', params.source)
  if (params?.status) q.set('status', params.status)
  const qs = q.toString()
  return api.get<AuthSession[]>(`/api/admin/sessions${qs ? '?' + qs : ''}`)
}

export function getSession(id: number) {
  return api.get<{ session: AuthSession; grants: SessionGrant[] }>(`/api/admin/sessions/${id}`)
}

export function revokeSession(id: number) {
  return api.post(`/api/admin/sessions/${id}/revoke`)
}

// 删除已失效会话记录（有效会话需先强制下线；仍有生效授权时需先删除授权）
export function deleteSession(id: number) {
  return api.del(`/api/admin/sessions/${id}`)
}

// 删除会话下的单条应用授权（仍有效的授权删除即撤销）
export function deleteSessionGrant(sessionId: number, grantRef: number) {
  return api.del(`/api/admin/sessions/${sessionId}/grants/${grantRef}`)
}
