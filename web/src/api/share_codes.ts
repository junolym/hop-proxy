import { api } from './client'

export interface ShareCode {
  id: number
  user_id: number
  app_id: number
  code: string
  max_uses: number
  use_count: number
  expires_at: string
  cookie_ttl: number
  redirect_path: string
  concrete_subdomain: string  // 模糊匹配应用的具体子域名；精确匹配应用为空
  enabled: boolean
  created_at: string
  updated_at: string
  // JOIN 填充
  app_name: string
  subdomain: string
}

export function listShareCodes() {
  return api.get<ShareCode[]>('/api/share-codes')
}

export function createShareCode(data: {
  app_id: number
  max_uses?: number
  cookie_ttl?: number
  expires_in_secs?: number
  redirect_path?: string
  concrete_subdomain?: string
}) {
  return api.post<ShareCode>('/api/share-codes', data)
}

export function updateShareCode(id: number, data: {
  max_uses?: number
  cookie_ttl?: number
  expires_in_secs?: number | null
  redirect_path?: string | null
  enabled?: boolean
}) {
  return api.put(`/api/share-codes/${id}`, data)
}

export function deleteShareCode(id: number) {
  return api.del(`/api/share-codes/${id}`)
}
