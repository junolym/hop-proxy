import { api } from './client'

export function getSettings() {
  return api.get<Record<string, string>>('/api/settings')
}

// 代理限制配置项元数据（#47），驱动前端下拉与提示
export interface ProxyConfigKey {
  key: string
  desc: string
  nginx: string        // nginx 对应指令
  type: 'size' | 'time'
  default: string
  app_override: boolean // 是否允许应用级覆盖
  restart: boolean      // 是否需重启服务端生效（监听器级参数）
}

export function getProxyConfigKeys() {
  return api.get<ProxyConfigKey[]>('/api/proxy-config/keys')
}

// parseProxyConfigText 解析多行 "key: value" 文本为 map（前端展示用，不校验）
export function parseProxyConfigText(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const line of (text || '').split('\n')) {
    const t = line.trim()
    if (!t || t.startsWith('#')) continue
    const idx = t.indexOf(':')
    if (idx < 0) continue
    out[t.slice(0, idx).trim()] = t.slice(idx + 1).trim()
  }
  return out
}

// formatProxyConfigText 将 map 序列化为按 keys 顺序的多行 "key: value" 文本
export function formatProxyConfigText(vals: Record<string, string>, keys: ProxyConfigKey[]): string {
  return keys
    .filter(k => vals[k.key] !== undefined && vals[k.key] !== '')
    .map(k => `${k.key}: ${vals[k.key]}`)
    .join('\n')
}

// 用户设置
export interface UserSettings {
  auto_disable_days: number
  temp_login_enabled: boolean
  temp_login_pin_set: boolean
  totp_secret_set: boolean
  totp_enabled: boolean
  quick_login: boolean
  app_api_enabled: boolean
  app_api_token_set: boolean
  app_api_token: string
}

export function getUserSettings() {
  return api.get<UserSettings>('/api/user-settings')
}

export function updateUserSettings(data: {
  auto_disable_days?: number
  temp_login_enabled?: boolean
  temp_login_pin?: string
  quick_login?: boolean
  app_api_enabled?: boolean
  reset_app_api_token?: boolean
}) {
  return api.put<{ app_api_token?: string }>('/api/user-settings', data)
}
