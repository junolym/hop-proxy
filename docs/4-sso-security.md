# 4 - SSO 认证与安全体系

## 概述

HopProxy 内置了完整的 SSO (Single Sign-On) 单点登录系统，为代理的应用提供统一的身份认证和访问控制能力。SSO 不是可选插件，而是与代理引擎深度集成的核心安全层。

## 设计理念：零信任应用访问

默认情况下，通过 HopProxy 代理的**所有应用都需要认证**才能访问。未认证用户不会被拒绝（返回 403），而是被**重定向到 SSO 授权页面**——这一设计的目的是：

1. **信息隐藏**：不向未认证者暴露子域名是否存在（统一跳转到 SSO 登录页）
2. **用户体验**：一次授权后可在 Cookie 有效期内免登访问
3. **权限粒度**：每个应用独立控制谁能访问

## 双层认证架构

```
┌─────────────────────────────────────────────────────┐
│                 认证请求到达                          │
└───────────────────┬─────────────────────────────────┘
                    │
          ┌─────────▼─────────┐
          │ 路径豁免检查        │
          │ exempt_paths 配置?  │
          └────┬────────┬──────┘
               是       否
                │         │
                ▼         ▼
            直接放行   ┌──────────────────────┐
                      │ Layer 1: SSO Cookie    │
                      │ hopproxy_sso_{app_id}  │
                      └────┬─────────────┬────┘
                           存在           不存在
                           │              │
                    ┌──────▼──────┐       │
                    │ 验证 Session │       │
                    │ (DB 查询)    │       │
                    └──────┬──────┘       │
                           │              │
                      有效   无效          │
                       │      │            │
                       ▼      ▼            ▼
                    通过  ┌─────────────────────┐
                          │ Layer 2: Access Token│
                          │ Bearer / Basic Auth  │
                          └──────────┬──────────┘
                                     │
                               存在        不存在
                                │           │
                            验证 token     │
                             │             │
                        有效   无效         ▼
                         │      │     ┌──────────┐
                         ▼      ▼     │ 重定向到  │
                        通过   拒绝   │ SSO 页面  │
                                       └──────────┘
```

### 认证优先级规则

**严格顺序**（服务端 `CheckSSOAuth` 和客户端本地代理 `checkAuth` 完全一致）：

1. **SSO Cookie 存在** → 验证 Session 有效性。如果有效则直接通过，**忽略 Authorization header**
   - 理由：浏览器用户的 SSO Cookie 优先级最高；Access Token 主要用于 API 客户端
2. **Access Token (Bearer / Basic)** → 验证 Token 是否有效且有权访问该应用
3. **都没有** → 302 重定向到 SSO 授权页面 (`/sso?redirect=<当前URL>`)

### 为什么 Cookie 优先于 Token？

因为浏览器在发送请求时会同时携带 Cookie 和自定义 Header（如 JavaScript fetch 设置的 Authorization）。如果不规定优先级，可能出现：
- 用户已通过 SSO 登录（有 Cookie）
- 但同时代码中错误地设置了过期的/无效的 Bearer Token
- 如果 Token 先被检查并拒绝了请求，会导致「已登录却被踢出」的困惑体验

## SSO 授权流程

### 完整流程图

```
用户首次访问 https://myapp.proxy.example.com/dashboard
                                                    │
                    ┌───────────────────────────────▼──────────────────────────────┐
                    │  服务端 Proxy.CheckSSOAuth() 或                              │
                    │  客户端 LocalProxy.checkAuth()                               │
                    │                                                              │
                    │  1. 检查 SSO Cookie: hopproxy_sso_<app_id>                   │
                    │     → 未找到                                                │
                    │  2. 检查 Access Token: Authorization header                  │
                    │     → 未找到                                                │
                    │  3. 302 Redirect to:                                          │
                    │     https://admin_domain/sso?redirect=                       │
                    │       https%3A%2F%2Fmyapp.proxy.example.com%2Fdashboard      │
                    └───────────────────────────────┬──────────────────────────────┘
                                                    │
                                                    ▼
              ┌─────────────────────────────────────────────────────┐
              │  浏览器加载 /sso 页面                                 │
              │                                                      │
              │  前置检查 [server/main.go:67-84]:                     │
              │  ├─ session Cookie (hopproxy_session) 是否存在?      │
              │  │   ├─ NO → 302 到 /login?next=/sso?redirect=...    │
              │  │   └─ YES → 继续                                   │
              │  │                                                    │
              │  ├─ redirect 参数中的目标应用是否有有效的 SSO Cookie?  │
              │  │   └─ YES → 自动 302 到目标地址 (跳过确认步骤)       │
              │  │                                                    │
              │  └─ 显示 SSO 授权确认页面                               │
              │     "是否允许 <你的用户名> 访问 <应用名称>?"            │
              │     [授权] [取消]                                      │
              └──────────────────────────────┬───────────────────────┘
                                               │
                                    用户点击 [授权]
                                               │
                                               ▼
              ┌─────────────────────────────────────────────────────┐
              │  POST /api/sso/authorize                              │
              │                                                      │
              │  后端处理 [api/sso.go]:                               │
              │  1. 从 JWT session 获取 user_id                       │
              │  2. 检查 app 是否启用 + auth_method == "sso"          │
              │  3. 权限检查:                                         │
              │     ├─ allowed_users == "owner" → 必须是应用拥有者或管理员│
              │     └─ allowed_users == "all"   → 任何已登录用户       │
              │  4. 生成 SSO Session:                                  │
              │     ├─ token = crypto/rand(64 bytes, hex encoded)     │
              │     ├─ expires_at = now() + sso_cookie_max_age        │
              │     ├─ 写入 sso_sessions 表                           │
              │  5. Set-Cookie:                                       │
              │     hopproxy_sso_<app_id> = <token>                   │
              │     Domain = .proxy_domain (跨子域共享)                │
              │     HttpOnly = true                                   │
              │     SameSite = Lax                                    │
              │     MaxAge = configured (默认24h, 最长10年)           │
              │  6. 302 Redirect 到目标 URL                            │
              └──────────────────────────────┬───────────────────────┘
                                               │
                                               ▼
              用户浏览器跟随 302 → 目标应用
                                               │
                    ┌───────────────────────────▼───────────────────────┐
                    │  再次进入 CheckSSOAuth / checkAuth                │
                    │                                                   │
                    │  1. 检查 SSO Cookie: hopproxy_sso_<app_id>        │
                    │     → 找到!                                       │
                    │  2. 查询 sso_sessions 表验证 token                 │
                    │     → 有效! user_id 匹配, 未过期                  │
                    │  3. ✓ 通过认证 → 代理请求正常执行                   │
                    └───────────────────────────────────────────────────┘
```

## SSO Cookie 安全属性

| 属性 | 值 | 安全目的 |
|------|-----|----------|
| **HttpOnly** | `true` | 防止 JavaScript 访问 Cookie（防 XSS 窃取） |
| **SameSite** | `Lax` | 防止 CSRF（跨站 POST 请求不携带 Cookie） |
| **Domain** | `.proxy_domain` | 同一代理域名的所有子域名共享（SSO 登录一次后所有应用可用） |
| **Path** | `/` | 全站有效 |
| **MaxAge** | 可配置 (默认 `86400`=24h, 最大 `315360000`=10年) | 平衡安全性与便利性 |

### Cookie Domain 的跨子域设计

```go
// Domain 设为 .proxy_domain
// 例如 proxy_domain = "proxy.example.com"
// Domain = ".proxy.example.com"

// 这样:
// admin.proxy.example.com 的 Cookie ← 可读
// myapp.proxy.example.com 的 Cookie ← 可读
// api.proxy.example.com 的 Cookie ← 可读
```

这实现了**真正的单点登录效果**：用户在 `/sso` 页面完成一次授权后，Cookie 写入 `.proxy_domain` 下，后续访问任何子域名应用时都能携带此 Cookie。

## Access Token — 程序化访问认证

对于非浏览器客户端（curl、API 调用、服务间通信），SSO Cookie 不适用。HopProxy 提供 **Access Token** 机制：

### 使用方式

```bash
# Bearer Token 方式
curl -H "Authorization: Bearer my_access_token" \
  https://myapp.proxy.example.com/api/data

# Basic Auth 兼容方式 (token 作为密码)
curl -u "token:my_access_token" \
  https://myapp.proxy.example.com/api/data
```

### Token 特性

| 属性 | 说明 |
|------|------|
| 格式 | 32 字节随机 hex 字符串 (`crypto/rand`) |
| 存储 | `access_tokens` 表 (DB v19 迁移引入) |
| 关联方式 | 可绑定到特定应用 ID 列表 (`allowed_app_ids`)，也可设为全局 (`allowed_entries`) |
| 过期时间 | 创建时可配置 |
| 撤销 | 在管理界面删除即立即失效 |

### 与 SSO Cookie 的关系

- Access Token 和 SSO Cookie 是**互斥的优先级关系**，不是叠加
- 有 SSO Cookie 时，Token 不被检查
- 没有 Cookie 时，Token 才参与验证
- 这意味着 Token 不能用于「提升」已有 SSO 会话的权限

## 应用级访问控制

### 认证方法 (`auth_method`)

| 值 | 行为 |
|----|------|
| `"none"` | 完全开放，不做任何检查 |
| `"sso"` | 需要 SSO 授权（默认） |
| `"sso_owner"` | 仅应用所有者和管理员可访问 |
| `"sso_all"` | 所有已注册用户均可授权访问 |

### 用户范围 (`allowed_users`)

配合 `auth_method = "sso"` 使用：

| 值 | 行为 |
|----|------|
| `"owner"` | 只有应用的创建者（owner）和管理员可对自己授权 |
| `"all"` | 任何已登录的用户都可以对自己授权访问 |

### 路径豁免 (`exempt_paths`)

某些路径需要绕过 SSO 认证（如健康检查端点、webhook 回调）：

```json
{
  "exempt_paths": [
    "/api/health",        // 精确匹配
    "/static/",           // 前缀匹配: /static/* 都豁免
    "/webhooks/github"     // 精确匹配
  ]
}
```

**实现**: [`isPathExempt()`](internal/server/proxy/proxy.go:214-227) 支持精确匹配和以 `/` 结尾的前缀匹配。

### 自定义 Headers (`custom_headers`)

认证通过后，可注入额外的 HTTP Header 到代理请求中：

```json
{
  "custom_headers": {
    "X-App-Name": "myapp",
    "X-Forwarded-User": "${user_id}"  // 占位符会被替换为实际值
  }
}
```

典型用途：向后端服务传递经过验证的身份信息。

## 客户端侧 SSO 实现

### 本地 SSO Session 缓存

**文件**: `internal/client/localproxy/session.go`

当客户端启用本地代理 (`--listen`) 时，为了避免每次 SSO 验证都走隧道查询服务端，引入了本地缓存层：

```
缓存结构:
  map[session_token]*SessionCacheEntry{
    UserID:    int64,
    AppID:     int64,
    ExpiresAt: time.Time,
    CachedAt:  time.Time,  // 缓存写入时刻
  }

TTL: 60 秒 (硬编码)

工作流程:
  请求到达 → 提取 SSO Cookie → 查本地缓存:
    ├─ 命中 + 未过期 → ✓ 放行 (零网络开销)
    ├─ 命中 + 已过期 + 隧道在线 → 发送 ActionVerifySession 控制流到服务端
    │   ├─ 有效 → 更新缓存, 放行
    │   └─ 无效 → 删除缓存, 触发 SSO 重定向
    └─ 未命中 + 隧道在线 → 发送 ActionVerifySession 控制流到服务端
```

### 离线容灾机制

```go
if !tunnelOnline && cachedEntry != nil {
    // 隧道离线时, 已缓存的 Session 即使超过 TTL 也视为有效
    return true  // 放行
}
```

**设计意图**：如果服务端不可达（网络故障、Server 维护等），不应该让内网用户完全无法使用已经授权过的服务。风险可控——缓存的 Session 本身是之前成功验证过的。

## 审计日志

**文件**: 各调用点直接通过 `log/slog` 输出结构化日志（不再单独存储 DB）

关键安全操作通过 `slog.Info` 输出审计事件，供事后追溯（在 docker logs / 日志文件中按 message 与字段筛选）：

### 事件类型

| 日志消息 | 触发场景 | 关键字段 |
|---------|---------|---------|
| `登录成功` | 用户登录成功（含 TOTP 后二次登录） | `user_id`, `username`, `totp`, `ip` |
| `登录失败` | 登录失败（用户不存在 / 密码错误 / 限流） | `username`, `user_id?`, `reason`, `ip` |
| `退出登录` | 用户退出 | `user_id`, `username`, `ip` |
| `TOTP 启用/禁用/重置` | TOTP 状态变更 | `user_id`, `username`, `ip` |
| `TOTP 验证失败` | TOTP 二次验证失败 | `user_id`, `username`, `ip` |
| `SSO 授权` | SSO 授权确认 | `user_id`, `app_id`, `app_name`, `subdomain`, `totp`, `ip` |
| `SSO 撤销授权` | SSO 授权撤销 | `user_id`, `app_id`, `ip` |
| `临时登录成功` | 临时登录（PIN+TOTP）通过 | `user_id`, `username`, `app_id`, `subdomain`, `ip` |
| `临时登录失败` | 临时登录失败（含多种 reason） | `username`, `user_id?`, `reason`, `ip` |
| `本地代理访问` | 客户端本地代理访问上报 | `client_id`, `app_id`, `user_id`, `method`, `path` |

### 日志输出

- 直接走 `log/slog` 结构化日志（`slog.Info`），不再写入 DB
- 通过 message + 结构化字段（`reason`、`ip`、`user_id` 等）筛选
- 日志随服务端日志系统统一收集、轮转

## CSRF 保护

### 实现方式：双提交 Cookie 模式

**文件**: `internal/server/api/middleware.go`

```
1. 中间件为每个响应生成 CSRF Token:
   - Set-Cookie: hopproxy_csrf=<random_32_bytes>; HttpOnly; SameSite=Strict
   - X-CSRF-Token: <same_value> (响应头)

2. 前端 (client.ts) 自动从 Cookie 读取并在请求头附带:
   - 所有非 GET/OPTIONS/HEAD 请求自动添加:
     X-CSRF-Token: <cookie_value>

3. 服务端中间件校验:
   - 对比 Cookie 值和 Header 值
   - 不一致 → 403 Forbidden
```

### 为什么选择双提交 Cookie 模式而非 Synchronizer Token?

| 方案 | 优点 | 缺点 |
|------|------|------|
| 双提交 Cookie | 前端无需额外存储（Token 在 Cookie 中）；前端自动化程度高 | 依赖 SameSite Cookie（但现代浏览器都支持） |
| Synchronizer Token | 安全性略高（Token 存储在 JS 内存中，Cookie 被窃取也无法利用） | 前端需要管理 Token 生命周期；SPA 处理多 Tab 更复杂 |

HopProxy 选择双提交 Cookie 是因为它与 httpOnly SSO Cookie 架构一致，且前端可以完全自动化处理。

## 暴力破解防护

**文件**: `internal/server/api/auth.go` (内存 rate limiter)

```
限制规则:
  - 时间窗口: 5 分钟 (constants.go: LoginWindowSecs = 300)
  - 最大尝试次数: 5 次 (constants.go: LoginMaxAttempts = 5)
  - 计数维度: IP 地址 (支持 X-Forwarded-For)

超限行为:
  - 返回 429 Too Many Requests
  - 包含 Retry-After 头 (距窗口结束的秒数)

实现 (`internal/server/api/ratelimit/limiter.go`):
  inMemoryRateLimiter: map[ip]AttemptRecord{
    count:    int,
    windowStart: time.Time,
  }
  自 260716 起为 O(1) 后台清理: 不再在 IsBlocked 路径上做 O(n) 遍历,
  改为后台 ticker 定期清理过期条目;
  并设 maxAttemptsEntries = 10000 上限, 防止伪造 X-Forwarded-For 导致 map 无界增长
```

**注意**：这是纯内存实现，服务端重启后计数归零。生产环境如有更高安全需求，可考虑基于 Redis 的分布式限流。

## 密码安全

| 措施 | 实现 | 强度 |
|------|------|------|
| 哈希算法 | bcrypt (DefaultCost) | ~12 轮（Go 默认），约 250ms/次 |
| 盐值 | bcrypt 内建 | 每个 password 自动唯一盐 |
| 存储 | `users.password_hash` TEXT 字段 | 仅存哈希值，明文永不落盘 |

## 安全总结：纵深防御层次

```
┌──────────────────────────────────────────────────────────┐
│ 第 1 层: 传输加密                                          │
│ TLS (Nginx SSL 终结) + WebSocket Secure (wss://)           │
├──────────────────────────────────────────────────────────┤
│ 第 2 层: 身份认证                                           │
│ SSO Cookie (httpOnly) + Access Token + 暴力破解限速         │
│ + CSRF Token (双提交 Cookie)                               │
├──────────────────────────────────────────────────────────┤
│ 第 3 层: 授权控制                                           │
│ per-app auth_method / allowed_users / exempt_paths         │
│ + 自定义 Header 注入                                      │
├──────────────────────────────────────────────────────────┤
│ 第 4 层: Peer 通信安全                                      │
│ HMAC-SHA256 签名 + 时间戳防重放 + 密钥轮换 + 头隔离          │
├──────────────────────────────────────────────────────────┤
│ 第 5 层: 审计追溯                                           │
│ login/sso/revoke 操作全程记录 + SSO 全生命周期追踪          │
└──────────────────────────────────────────────────────────┘
```
