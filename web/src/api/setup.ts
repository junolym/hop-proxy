import { api } from './client'

export function getSetupStatus() {
  return api.get<{ initialized: boolean }>('/api/setup/status')
}

export function initSetup(data: {
  username: string
  password: string
  site_name: string
  admin_domain: string
  proxy_domain: string
}) {
  return api.post('/api/setup/init', data)
}
