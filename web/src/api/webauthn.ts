import { api } from './client'

// ===== 通行密钥（WebAuthn）基础类型 =====

// base64url 字符串 ↔ ArrayBuffer 转换（WebAuthn API 需要二进制）
function b64uToBuf(s: string): Uint8Array {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4)
  const bin = atob(b64)
  const arr = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i)
  return arr
}

function bufToB64u(buf: ArrayBuffer | Uint8Array): string {
  const arr = buf instanceof Uint8Array ? buf : new Uint8Array(buf)
  let bin = ''
  for (const b of arr) bin += String.fromCharCode(b)
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

// 服务器下发的注册/登录选项（challenge 等字段为 base64url 字符串）
interface ServerCreationOptions {
  publicKey: {
    challenge: string
    rp?: { id?: string; name?: string }
    user: { id: string; name: string; displayName: string }
    pubKeyCredParams: Array<{ type: string; alg: number }>
    timeout?: number
    excludeCredentials?: Array<{ type: string; id: string }>
    authenticatorSelection?: Record<string, unknown>
    attestation?: string
  }
}

// 服务器下发的断言请求选项（challenge 等字段为 base64url 字符串）
export interface ServerRequestOptions {
  publicKey: {
    challenge: string
    rpId?: string
    timeout?: number
    allowCredentials?: Array<{ type: string; id: string }>
    userVerification?: string
  }
}

// navigator.credentials.create 的入参（二进制版）
function toCreateArgs(opts: ServerCreationOptions): CredentialCreationOptions {
  const pk = opts.publicKey
  return {
    publicKey: {
      ...pk,
      challenge: b64uToBuf(pk.challenge),
      user: { ...pk.user, id: b64uToBuf(pk.user.id) },
      excludeCredentials: (pk.excludeCredentials || []).map(c => ({ ...c, id: b64uToBuf(c.id) })),
    } as PublicKeyCredentialCreationOptions,
  }
}

// navigator.credentials.get 的入参（二进制版）
function toGetArgs(opts: ServerRequestOptions): CredentialRequestOptions {
  const pk = opts.publicKey
  return {
    publicKey: {
      ...pk,
      challenge: b64uToBuf(pk.challenge),
      allowCredentials: (pk.allowCredentials || []).map(c => ({ ...c, id: b64uToBuf(c.id) })),
    } as PublicKeyCredentialRequestOptions,
  }
}

// ===== 凭据管理 =====

export interface WebAuthnCredentialInfo {
  id: number
  device_name: string
  backup_eligible: boolean
  backup_state: boolean
  last_used_at: string | null
  created_at: string
}

export function listCredentials() {
  return api.get<WebAuthnCredentialInfo[]>('/api/auth/webauthn/credentials')
}

export function renameCredential(id: number, deviceName: string) {
  return api.put(`/api/auth/webauthn/credentials/${id}`, { device_name: deviceName })
}

export function deleteCredential(id: number) {
  return api.del(`/api/auth/webauthn/credentials/${id}`)
}

// ===== 注册（需登录 + TOTP 再验证） =====

export async function registerPasskey(totpCode: string, deviceName: string): Promise<string> {
  // 1. begin：获取创建选项
  const beginRes = await api.post<ServerCreationOptions>('/api/auth/webauthn/register/begin', {
    totp_code: totpCode,
    device_name: deviceName,
  })
  if (beginRes.error) return beginRes.error
  if (!beginRes.data) return '获取注册参数失败'

  // 2. 浏览器创建凭据
  const credential = await navigator.credentials.create(toCreateArgs(beginRes.data)) as PublicKeyCredential
  if (!credential) return '已取消注册'

  const response = credential.response as AuthenticatorAttestationResponse
  // 3. finish：提交注册结果
  const finishRes = await api.post('/api/auth/webauthn/register/finish', {
    id: bufToB64u(credential.rawId),
    rawId: bufToB64u(credential.rawId),
    type: credential.type,
    response: {
      attestationObject: bufToB64u(response.attestationObject),
      clientDataJSON: bufToB64u(response.clientDataJSON),
    },
  })
  return finishRes.error || ''
}

// ===== 浏览器断言（收口：登录 / 扫码批准 / SSO 二次验证共用，#76） =====

// navigator.credentials.get 断言结果的序列化形态（提交给服务端）
export interface SerializedAssertion {
  id: string
  rawId: string
  type: string
  response: {
    authenticatorData: string
    clientDataJSON: string
    signature: string
    userHandle: string | null
  }
}

// 发起浏览器通行密钥断言，返回提交给服务端的序列化结果；取消或失败返回 null
export async function performAssertion(opts: ServerRequestOptions): Promise<SerializedAssertion | null> {
  try {
    const credential = await navigator.credentials.get(toGetArgs(opts)) as PublicKeyCredential
    if (!credential) return null
    const response = credential.response as AuthenticatorAssertionResponse
    return {
      id: bufToB64u(credential.rawId),
      rawId: bufToB64u(credential.rawId),
      type: credential.type,
      response: {
        authenticatorData: bufToB64u(response.authenticatorData),
        clientDataJSON: bufToB64u(response.clientDataJSON),
        signature: bufToB64u(response.signature),
        userHandle: response.userHandle ? bufToB64u(response.userHandle) : null,
      },
    }
  } catch {
    // 用户取消或浏览器拒绝（NotAllowedError 等）
    return null
  }
}

// ===== 登录（可发现凭据，免用户名） =====

export async function loginWithPasskey(): Promise<string> {
  // 1. begin：获取断言请求选项
  const beginRes = await api.post<ServerRequestOptions>('/api/auth/webauthn/login/begin')
  if (beginRes.error) return beginRes.error
  if (!beginRes.data) return '获取登录参数失败'

  // 2. 浏览器断言（Face ID / Touch ID）
  const assertion = await performAssertion(beginRes.data)
  if (!assertion) return '已取消登录'

  // 3. finish：提交断言，成功后服务端下发 session cookie
  const finishRes = await api.post('/api/auth/webauthn/login/finish', assertion)
  return finishRes.error || ''
}

// ===== 扫码授权登录（iPhone 授权页用） =====

export interface QRInfo {
  state: string
  app_name: string
  subdomain: string
  full_domain: string
  redirect: string
  creator_ip: string
  creator_ua: string
  expires_in: number
}

export function getQRInfo(sid: string) {
  return api.get<QRInfo>(`/api/auth/qr/info?sid=${encodeURIComponent(sid)}`)
}

// 发起批准断言并完成批准，返回错误信息（空 = 成功）
export async function approveQRLogin(sid: string): Promise<string> {
  // 1. begin：获取断言请求选项（UV=required，强制新鲜生物验证）
  const beginRes = await api.post<ServerRequestOptions>('/api/auth/qr/approve/begin', { sid })
  if (beginRes.error) return beginRes.error
  if (!beginRes.data) return '获取确认参数失败'

  // 2. 浏览器断言（Face ID / Touch ID）
  const assertion = await performAssertion(beginRes.data)
  if (!assertion) return '已取消确认'

  // 3. finish：提交断言完成批准
  const finishRes = await api.post(`/api/auth/qr/approve/finish?sid=${encodeURIComponent(sid)}`, assertion)
  return finishRes.error || ''
}
