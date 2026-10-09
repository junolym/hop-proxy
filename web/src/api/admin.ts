import { api } from './client'

export interface User {
  id: number
  username: string
  is_admin: boolean
  role: string
  created_at: string
  updated_at: string
}

export function listUsers() {
  return api.get<User[]>('/api/admin/users')
}

export function createUser(data: { username: string; password: string; role?: string }) {
  return api.post<User>('/api/admin/users', data)
}

export function updateUser(id: number, data: { username?: string; role?: string }) {
  return api.put(`/api/admin/users/${id}`, data)
}

export function deleteUser(id: number) {
  return api.del(`/api/admin/users/${id}`)
}

export function resetUserPassword(id: number, password: string) {
  return api.post(`/api/admin/users/${id}/reset-password`, { password })
}

export function updateAdminSettings(data: {
  site_name?: string
  admin_domain?: string
  proxy_domain?: string
  session_ttl?: string
  session_cookie_mode?: string // 管理端会话 Cookie 模式：persistent/session（#77）
  proxy_config?: string // 代理限制配置全局默认值（多行 key: value 文本，#47）
  show_temp_login?: boolean // 登录页显示「临时登录」入口（#71）
  show_qr_login?: boolean // 登录页显示「扫码登录」入口（#71）
  audit_retention_days?: number // 会话保留期（天，0=永久，#80）
}) {
  return api.put('/api/admin/settings', data)
}

export function resetJwtSecret() {
  return api.post('/api/admin/settings/reset-jwt')
}
