import { api } from './client'

export interface AgentKey {
  id: number
  user_id: number
  uuid: string
  name: string
  created_at: string
  updated_at: string
}

export function listAgentKeys() {
  return api.get<AgentKey[]>('/api/agent-keys')
}

export function createAgentKey(data: { name: string }) {
  return api.post<AgentKey>('/api/agent-keys', data)
}

export function updateAgentKey(id: number, data: { name: string }) {
  return api.put(`/api/agent-keys/${id}`, data)
}

export function deleteAgentKey(id: number) {
  return api.del(`/api/agent-keys/${id}`)
}
