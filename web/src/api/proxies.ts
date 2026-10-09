import { api } from './client'

export interface ClientProxy {
  id: number
  client_id: string
  name: string
  proxy_type: string          // socks5 / shadowsocks / peer
  proxy_address: string       // 代理地址
  proxy_password: string      // 代理密码（脱敏）
  target_client_id: string | null // 目标客户端 ID（仅 peer 类型）
  created_at: string
}

// 获取客户端的代理列表
export function listProxies(clientId: string) {
  return api.get<ClientProxy[]>(`/api/clients/${clientId}/proxies`)
}

// 创建代理
export function createProxy(clientId: string, data: {
  name: string
  proxy_type: string          // socks5 / shadowsocks / peer
  proxy_address?: string
  proxy_password?: string
  target_client_id?: string
}) {
  return api.post<ClientProxy>(`/api/clients/${clientId}/proxies`, data)
}

// 更新代理
export function updateProxy(proxyId: number, data: {
  name: string
  proxy_type: string
  proxy_address?: string
  proxy_password?: string
  target_client_id?: string
}) {
  return api.put(`/api/proxies/${proxyId}`, data)
}

// 删除代理
export function deleteProxy(proxyId: number) {
  return api.del(`/api/proxies/${proxyId}`)
}
