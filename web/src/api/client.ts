// 生成随机 hex 字符串
function randomHex(len: number): string {
  const arr = new Uint8Array(len)
  crypto.getRandomValues(arr)
  return Array.from(arr, b => b.toString(16).padStart(2, '0')).join('')
}

// 获取或初始化 CSRF token Cookie（非 httpOnly，允许 JS 读取）
function getOrCreateCSRFToken(): string {
  const name = 'hopproxy_csrf'
  const match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'))
  if (match) return decodeURIComponent(match[1])
  const token = randomHex(16)
  // SameSite=Strict，不设 httpOnly（需要 JS 读取）
  document.cookie = `${name}=${token}; path=/; SameSite=Strict`
  return token
}

interface ApiResponse<T = unknown> {
  data?: T
  error?: string
  message?: string
  extra?: { error_code?: string; [key: string]: unknown }
}

const WRITE_METHODS = new Set(['POST', 'PUT', 'DELETE', 'PATCH'])

async function request<T>(method: string, path: string, body?: unknown): Promise<ApiResponse<T>> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  }

  // 写操作附带 CSRF Token
  if (WRITE_METHODS.has(method)) {
    headers['X-CSRF-Token'] = getOrCreateCSRFToken()
  }

  const res = await fetch(path, {
    method,
    headers,
    credentials: 'include',  // 自动发送 httpOnly Session Cookie
    body: body ? JSON.stringify(body) : undefined,
  })

  if (res.status === 401) {
    // Session 失效，重定向到登录页
    // SPA 管理页面：重定向到 /login（独立入口）
    // 注意：/sso 和 /login 已是独立入口，后端会处理重定向
    if (!window.location.pathname.startsWith('/login')) {
      window.location.href = '/login?next=' + encodeURIComponent(window.location.href)
    }
  }

  return res.json()
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}
