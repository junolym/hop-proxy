import { api } from './client'

export interface ClientProxyConfig {
  proxy_type: string | null      // socks5 / shadowsocks / null
  proxy_address: string | null   // 代理地址
  proxy_password: string | null  // 代理密码（脱敏）
}

export interface AppClient {
  client_id: string
  client_name: string
  priority: number
  online: boolean
  target_url: string | null      // 客户端专属目标地址
  proxy_id: number | null        // 代理 ID
  proxy_name: string | null      // 代理名称
  proxy_type: string | null      // 代理类型
  proxy_address: string | null   // 代理地址
  proxy_password: string | null  // 代理密码（脱敏）
}

export interface AppProbeStatus {
  status: string               // available / error / failed / timeout / offline
  status_code?: number         // HTTP 状态码（available/error 时填充）
  detail?: string              // 失败原因（failed/timeout 时填充）
  checked_at: string           // 探测时间（ISO 8601）
}

export interface App {
  id: number
  user_id: number
  client_id: string | null  // 兼容字段，第一个客户端 ID
  client_ids: string[]
  client_infos: AppClient[]
  name: string
  subdomain: string
  target_url: string
  enabled: boolean
  require_auth: boolean       // 兼容字段，已废弃
  auth_method: string         // none、sso 或 token
  allowed_users: string       // owner 或 all
  sso_cookie_max_age: number  // SSO Cookie 过期时间（秒），-1 表示会话，315360000 表示永久
  load_balance: boolean
  inactive_days: number | null  // 不活跃天数阈值
  last_used_at: string | null   // 最近一次使用时间
  custom_headers: Record<string, string>  // 自定义 HTTP Header
  header_mode: string           // 请求头缺省处理模式：auto_xff（默认）/ auto_origin / none
  proxy_config: string          // 应用级代理配置覆盖（多行 key: value 文本，#47）
  exempt_paths: string[]       // 豁免 SSO 认证的路径列表
  agent_key_uuid: string       // 安全代理密钥 UUID（空=不使用 agent）
  probe_status?: AppProbeStatus  // 应用可用性探测状态（模糊匹配应用或未探测过时为空）
  second_factor: string          // SSO 授权二次验证方式：''=无 / 'totp' / 'passkey' (#76)
  client_name: string
  client_online: boolean
  created_at: string
  updated_at: string
}

export function listApps(userId?: number) {
  const params = new URLSearchParams()
  if (userId !== undefined) {
    params.set('user_id', String(userId))
  }
  const query = params.toString()
  return api.get<App[]>(query ? `/api/apps?${query}` : '/api/apps')
}

export interface ClientFullConfig {
  target_url?: string
  proxy_id?: number | null  // 代理 ID，null 表示不使用代理
}

export function createApp(data: {
  name: string
  subdomain: string
  target_url: string
  client_ids: string[]
  client_configs?: Record<string, ClientFullConfig>  // 客户端配置（目标地址、代理 ID）
  auth_method?: string            // none、sso 或 token
  allowed_users?: string          // owner 或 all
  sso_cookie_max_age?: number     // SSO Cookie 过期时间（秒），-1 表示会话，315360000 表示永久
  load_balance?: boolean
  custom_headers?: Record<string, string>  // 自定义 HTTP Header
  header_mode?: string             // 请求头缺省处理模式：auto_xff（默认）/ auto_origin / none
  proxy_config?: string            // 应用级代理配置覆盖（多行 key: value 文本，#47）
  exempt_paths?: string[]         // 豁免 SSO 认证的路径列表
  agent_key_uuid?: string         // 安全代理密钥 UUID
  second_factor?: string          // SSO 授权二次验证方式：''=无 / 'totp' / 'passkey' (#76)
}) {
  return api.post<App>('/api/apps', data)
}

export function updateApp(id: number, data: {
  name: string
  subdomain: string
  target_url: string
  client_ids: string[]
  client_configs?: Record<string, ClientFullConfig>  // 客户端配置（目标地址、代理 ID）
  enabled?: boolean
  auth_method?: string            // none、sso 或 token
  allowed_users?: string          // owner 或 all
  sso_cookie_max_age?: number     // SSO Cookie 过期时间（秒），-1 表示会话，315360000 表示永久
  load_balance?: boolean
  custom_headers?: Record<string, string>  // 自定义 HTTP Header
  header_mode?: string             // 请求头缺省处理模式：auto_xff（默认）/ auto_origin / none
  proxy_config?: string            // 应用级代理配置覆盖（多行 key: value 文本，#47）
  exempt_paths?: string[]         // 豁免 SSO 认证的路径列表
  agent_key_uuid?: string         // 安全代理密钥 UUID
  second_factor?: string          // SSO 授权二次验证方式：''=无 / 'totp' / 'passkey' (#76)
}) {
  return api.put(`/api/apps/${id}`, data)
}

export function deleteApp(id: number) {
  return api.del(`/api/apps/${id}`)
}

// 克隆应用：后端一次性复制全部配置（含路由规则/跳转路径），名称与子域名自动推导为 <原值>-N
export function duplicateApp(id: number) {
  return api.post<App>(`/api/apps/${id}/duplicate`)
}

// ===== 路由规则（App Routes）=====

export interface AppRoute {
  id: number
  app_id: number
  client_id: string      // 空字符串=任意
  method: string         // 空字符串=任意，"*"=通配
  path_pattern: string   // 路径模式
  auth_method: string    // 空=不覆盖，"none"/"sso"/"token"
  target_url: string     // 空=不覆盖
  path_rewrite: string   // 空=不改写（转发到后端的路径按前缀替换语义改写）
  priority: number
  enabled: boolean
}

export function listAppRoutes(appId: number) {
  return api.get<AppRoute[]>(`/api/apps/${appId}/routes`)
}

export function createAppRoute(appId: number, data: Partial<AppRoute>) {
  return api.post<AppRoute>(`/api/apps/${appId}/routes`, data)
}

export function updateAppRoute(routeId: number, data: Partial<AppRoute>) {
  return api.put(`/api/routes/${routeId}`, data)
}

export function deleteAppRoute(routeId: number) {
  return api.del(`/api/routes/${routeId}`)
}

// ===== 跳转路径规则（App Redirects）=====

export interface AppRedirect {
  id: number
  app_id: number
  match_type: string      // exact | regex
  match_path: string      // 精确路径或正则
  match_include_query: boolean // true 时匹配 path?query，用于避免同 path 不同 query 的死循环
  redirect_target: string // /开头=站内，http(s)://开头=站外；正则支持 $1 ${1}
  status_code: number     // 301 永久 | 302 临时
  priority: number
  enabled: boolean
}

export function listAppRedirects(appId: number) {
  return api.get<AppRedirect[]>(`/api/apps/${appId}/redirects`)
}

export function createAppRedirect(appId: number, data: Partial<AppRedirect>) {
  return api.post<AppRedirect>(`/api/apps/${appId}/redirects`, data)
}

export function updateAppRedirect(id: number, data: Partial<AppRedirect>) {
  return api.put(`/api/redirects/${id}`, data)
}

export function deleteAppRedirect(id: number) {
  return api.del(`/api/redirects/${id}`)
}

// ===== 服务端版本信息 =====

export interface ServerInfo {
  version: string
  commit: string
  go_version: string
}

export function getServerVersion() {
  return api.get<ServerInfo>('/api/version')
}
