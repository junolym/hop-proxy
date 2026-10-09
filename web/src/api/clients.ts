import { api } from './client'

export interface Client {
  id: string
  user_id: number
  name: string
  online: boolean
  app_count: number
  proxy_enabled: boolean
  proxy_listen: string
  version?: string  // 客户端构建版本
  active_streams: number  // 活跃 yamux stream 数
  conn_count: number      // 当前 WS 连接数
  created_at: string
  updated_at: string
}

export function listClients() {
  return api.get<Client[]>('/api/clients')
}

export function createClient(name: string) {
  return api.post<Client>('/api/clients', { name })
}

export function updateClient(id: string, name: string) {
  return api.put(`/api/clients/${id}`, { name })
}

export function deleteClient(id: string) {
  return api.del(`/api/clients/${id}`)
}

export function pingClient(id: string) {
  return api.get<{ latency_ms: number }>(`/api/clients/${id}/ping`)
}

export interface AvailablePeer {
  id: string
  name: string
  proxy_listen: string
}

// 获取同用户下可用的 peer 客户端列表
export function listAvailablePeers() {
  return api.get<AvailablePeer[]>('/api/clients/available-peers')
}

// 单条隧道连接详情
export interface ConnDetail {
  remote_addr: string   // 客户端地址
  created_at: string    // 连接创建时间（ISO）
  num_streams: number   // 活跃 stream 数
  latency_ms: number    // 最近 RTT（毫秒，-1 表示未知）
  draining: boolean     // 是否正在 drain
  bytes_sent: number    // 发出字节（服务端→客户端，即下载）
  bytes_recv: number    // 收到字节（客户端→服务端，即上传）
}

// 获取客户端所有隧道连接详情
export function fetchClientConns(id: string) {
  return api.get<ConnDetail[]>(`/api/clients/${id}/conns`)
}
