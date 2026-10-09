import { api } from './client'
import type { SerializedAssertion, ServerRequestOptions } from './webauthn'

// 根据子域名查询应用信息（second_factor 供 /sso 授权页预判二次验证方式，#76）
export function getAppBySubdomain(subdomain: string) {
  return api.get<{ id: number; name: string; require_auth: boolean; second_factor: string }>(`/api/sso/app?subdomain=${encodeURIComponent(subdomain)}`)
}

// SSO 授权（传入 redirect URL，后端从中提取子域名）
// 应用开启二次验证时为两阶段调用：
//   - totp：第一次不带 totpCode，返回 second_factor='totp'；带 totpCode 再调完成授权
//   - passkey：第一次不带 assertion，返回 second_factor='passkey' 与断言参数；
//     完成 browser 断言后带 assertion 再调完成授权 (#76)
export function ssoAuthorize(redirect: string, totpCode?: string, assertion?: SerializedAssertion) {
  return api.post<{
    app_id: number
    app_name: string
    second_factor?: string
    options?: ServerRequestOptions
  }>(`/api/sso/authorize`, {
    redirect,
    totp_code: totpCode || '',
    // 仅在第二阶段携带，服务端以字段缺失区分两阶段
    ...(assertion ? { assertion } : {}),
  })
}

export function ssoRevokeApp(appId: number) {
  return api.del(`/api/sso/authorize?app_id=${appId}`)
}
