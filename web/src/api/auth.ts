import { api } from './client'

// 登录响应
interface LoginResponse {
  ok?: boolean
  totp_required?: boolean
  username?: string
}

// 临时登录响应
interface TempLoginResponse {
  ok?: boolean
  redirect?: string
}

export async function login(username: string, password: string) {
  // 后端会通过 Set-Cookie 设置 hopproxy_session httpOnly Cookie
  // 或 hopproxy_totp_pending 临时 Cookie（需要 TOTP 验证）
  return api.post<LoginResponse>('/api/auth/login', { username, password })
}

// 临时登录：用户名 + PIN + TOTP token 换取指定应用的 SSO cookie
export function tempLogin(username: string, pin: string, token: string, redirect: string) {
  return api.post<TempLoginResponse>('/api/auth/temp-login', { username, pin, token, redirect })
}

export function getMe() {
  return api.get<{ id: number; username: string; is_admin: boolean; role: string }>('/api/auth/me')
}

// 登录页登录方式开关（公开端点，#71）
export interface LoginOptions {
  show_temp_login: boolean
  show_qr_login: boolean
}

export function getLoginOptions() {
  return api.get<LoginOptions>('/api/auth/login-options')
}

export function changePassword(oldPassword: string, newPassword: string) {
  return api.post('/api/auth/password', { old_password: oldPassword, new_password: newPassword })
}

export function logout() {
  return api.post('/api/auth/logout')
}
