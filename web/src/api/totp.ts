import { api } from './client'

// TOTP 状态
export interface TOTPStatus {
  totp_enabled: boolean
}

// TOTP 设置信息
export interface TOTPSetup {
  secret: string
  qr_base64: string
  uri: string
}

// 获取 TOTP 状态
export function getTOTPStatus() {
  return api.get<TOTPStatus>('/api/totp/status')
}

// 生成 TOTP 密钥和二维码
export function setupTOTP() {
  return api.get<TOTPSetup>('/api/totp/setup')
}

// 启用 TOTP
export function enableTOTP(code: string) {
  return api.post('/api/totp/enable', { code })
}

// 禁用 TOTP（密码或验证码二选一）
export function disableTOTP(data: { password?: string; code?: string }) {
  return api.post('/api/totp/disable', data)
}

// 重置 TOTP 密钥（密码或验证码二选一）
export function resetTOTP(data: { password?: string; code?: string }) {
  return api.post('/api/totp/reset', data)
}

// 登录时验证 TOTP
export function verifyTOTP(code: string) {
  return api.post<{ ok: boolean }>('/api/auth/totp', { code })
}
