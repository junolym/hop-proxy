package server

// 用户资源上限
const (
	MaxAppsPerUser    = 100 // 每个用户最多创建的应用数
	MaxClientsPerUser = 100 // 每个用户最多创建的客户端数
)

// 时间常量
const (
	SecondsPerDay    = 86400 // 一天的秒数
	DefaultSSOTTLS   = 86400 // SSO Cookie 默认有效期（24小时）
	HTTPTimeout      = 30    // HTTP 请求超时（秒）
	TOTPPendingTTL   = 300   // TOTP 待验证临时 Token 有效期（5分钟）
	LoginMaxAttempts = 5     // 登录最大尝试次数
	LoginWindowSecs  = 300   // 登录限流窗口（秒）
)

// 认证方式
const (
	AuthMethodNone     = "none"      // 无需认证
	AuthMethodSSO      = "sso"       // 仅 SSO 认证（不支持票据，未认证返回 302 重定向）
	AuthMethodSSOToken = "sso_token" // SSO + 票据混合（票据优先，SSO 回退；API 客户端返回 401）
	AuthMethodToken    = "token"     // 仅允许使用票据访问
	// 兼容旧数据
	AuthMethodSSOOwner = "sso_owner"
	AuthMethodSSOAll   = "sso_all"
)

// 授权用户范围
const (
	AllowedUsersOwner = "owner" // 仅限应用所有者
	AllowedUsersAll   = "all"   // 所有已登录用户
)

// 应用 SSO 二次验证方式
const (
	SecondFactorNone    = ""        // 无二次验证
	SecondFactorTOTP    = "totp"    // TOTP 验证码
	SecondFactorPasskey = "passkey" // 通行密钥（#76）
)

// Cookie 名称
const (
	CookieSession     = "hopproxy_session"      // Session Cookie
	CookieCSRF        = "hopproxy_csrf"         // CSRF Token Cookie
	CookieTOTPPending = "hopproxy_totp_pending" // TOTP 待验证临时 Cookie
)

// 管理端会话 Cookie 模式（settings.session_cookie_mode，#77）
const (
	SessionCookiePersistent = "persistent" // 持久 Cookie：带 Max-Age，落盘，重启浏览器仍有效（默认）
	SessionCookieSession    = "session"    // 会话级 Cookie：不落盘，关闭浏览器即失效
)
