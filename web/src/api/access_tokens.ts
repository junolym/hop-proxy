import { api } from './client'

export interface AllowedEntries {
  server: boolean
  clients: string[]
}

export interface AccessToken {
  id: number
  user_id: number
  name: string
  token: string          // 列表中打码，新建时返回完整值
  allowed_entries: string  // JSON 字符串
  allowed_app_ids: string  // JSON 字符串
  expires_at: string | null
  last_used_at: string | null
  created_at: string
  updated_at: string
}

export function listAccessTokens() {
  return api.get<AccessToken[]>('/api/access-tokens')
}

export function createAccessToken(data: {
  name: string
  allowed_entries: AllowedEntries
  allowed_app_ids: number[]
  expires_in_secs?: number | null
}) {
  return api.post<AccessToken>('/api/access-tokens', data)
}

export function updateAccessToken(id: number, data: {
  name: string
  allowed_entries: AllowedEntries
  allowed_app_ids: number[]
  expires_in_secs?: number | null
}) {
  return api.put(`/api/access-tokens/${id}`, data)
}

export function deleteAccessToken(id: number) {
  return api.del(`/api/access-tokens/${id}`)
}
