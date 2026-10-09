import { api } from './client'

// 电脑侧扫码登录（创建会话 + 轮询状态）

export interface QRCreateResponse {
  sid: string
}

export interface QRStatusResponse {
  state: 'pending' | 'scanned' | 'approved' | 'expired'
  redirect?: string
  expires_in?: number
}

// 创建扫码登录会话（登录页免认证调用，目标应用 URL 由 redirect 指定）
export function createQRLogin(redirect: string) {
  return api.post<QRCreateResponse>('/api/auth/qr/create', { redirect })
}

// 轮询扫码登录状态；approved 时服务端已在该响应中下发 SSO cookie
export function getQRStatus(sid: string) {
  return api.get<QRStatusResponse>(`/api/auth/qr/status?sid=${encodeURIComponent(sid)}`)
}
