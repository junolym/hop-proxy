package api

import (
	"net/http"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/logstore"
	"github.com/robin/hop-proxy/internal/server/proxy"
	"github.com/robin/hop-proxy/internal/server/tunnel"
)

// NewRouter 创建 API 路由（管理域名下的路由）
func NewRouter(database *db.DB, hub *tunnel.Hub, p *proxy.Proxy, logStore *logstore.Store, buildVersion, buildCommit string) http.Handler {
	mux := http.NewServeMux()

	setup := newSetupHandler(database)
	auth := newAuthHandler(database)
	clients := newClientsHandler(database, hub)
	apps := newAppsHandler(database, hub, p)
	routes := newRoutesHandler(database, hub)
	redirects := newRedirectsHandler(database, hub)
	settings := newSettingsHandler(database)
	userSettings := newUserSettingsHandler(database)
	webauthnAuth := newWebAuthnHandler(database)
	sso := newSSOHandler(database, webauthnAuth)
	admin := newAdminHandler(database)
	totp := newTOTPHandler(database)
	proxies := newProxiesHandler(database, p)
	accessTokens := newAccessTokensHandler(database)
	share := NewShareHandler(database)
	tempLogin := newTempLoginHandler(database)
	qrLogin := newQRLoginHandler(database, webauthnAuth)
	agentKeys := newAgentKeysHandler(database)
	version := newVersionHandler(buildVersion, buildCommit)
	externalAPI := newExternalAPIHandler(database)
	logs := newLogsHandler(logStore, database)
	sessions := newSessionsHandler(database)

	// 引导配置（无需认证）
	mux.HandleFunc("GET /api/setup/status", setup.Status)
	mux.HandleFunc("POST /api/setup/init", setup.Init)

	// 版本信息（无需认证）
	mux.HandleFunc("GET /api/version", version.Get)

	// agent 公钥查询（无需认证，agent 启动时拉取）
	mux.HandleFunc("GET /pubkey/{uuid}", agentKeys.GetPublicKey)

	// 认证（登录无需认证）
	mux.HandleFunc("POST /api/auth/login", auth.Login)
	mux.HandleFunc("POST /api/auth/totp", auth.VerifyTOTP)           // TOTP 二次验证
	mux.HandleFunc("POST /api/auth/temp-login", tempLogin.Login)     // 临时登录（PIN + TOTP token）
	mux.HandleFunc("GET /api/auth/login-options", auth.LoginOptions) // 登录页登录方式开关（#71）

	// 通行密钥登录（#71 P1，可发现凭据，免认证）
	mux.HandleFunc("POST /api/auth/webauthn/login/begin", webauthnAuth.LoginBegin)
	mux.HandleFunc("POST /api/auth/webauthn/login/finish", webauthnAuth.LoginFinish)

	// 扫码授权登录（#71 P2，电脑侧免认证，靠 sid 熵 + 限流）
	mux.HandleFunc("POST /api/auth/qr/create", qrLogin.Create)
	mux.HandleFunc("GET /api/auth/qr/status", qrLogin.Status)

	// 需要 JWT 认证的路由
	authMux := http.NewServeMux()

	// 访客可访问的路由
	authMux.HandleFunc("GET /api/auth/me", auth.Me)
	authMux.HandleFunc("POST /api/auth/password", auth.ChangePassword)
	authMux.HandleFunc("POST /api/auth/logout", auth.Logout)

	// SSO 授权（访客可访问）
	authMux.HandleFunc("GET /api/sso/app", sso.GetAppBySubdomain)
	authMux.HandleFunc("POST /api/sso/authorize", sso.Authorize)
	authMux.HandleFunc("DELETE /api/sso/authorize", sso.RevokeApp)

	// 需要普通用户权限（访客不可访问）
	userMux := http.NewServeMux()

	userMux.HandleFunc("GET /api/clients/available-peers", clients.AvailablePeers)
	userMux.HandleFunc("GET /api/clients", clients.List)
	userMux.HandleFunc("POST /api/clients", clients.Create)
	userMux.HandleFunc("PUT /api/clients/{id}", clients.Update)
	userMux.HandleFunc("DELETE /api/clients/{id}", clients.Delete)
	userMux.HandleFunc("GET /api/clients/{id}/ping", clients.Ping)
	userMux.HandleFunc("GET /api/clients/{id}/conns", clients.Conns)

	// 代理管理
	userMux.HandleFunc("GET /api/clients/{id}/proxies", proxies.List)
	userMux.HandleFunc("POST /api/clients/{id}/proxies", proxies.Create)
	userMux.HandleFunc("PUT /api/proxies/{id}", proxies.Update)
	userMux.HandleFunc("DELETE /api/proxies/{id}", proxies.Delete)

	userMux.HandleFunc("GET /api/apps", apps.List)
	userMux.HandleFunc("POST /api/apps", apps.Create)
	userMux.HandleFunc("POST /api/apps/{id}/duplicate", apps.Duplicate)
	userMux.HandleFunc("PUT /api/apps/{id}", apps.Update)
	userMux.HandleFunc("DELETE /api/apps/{id}", apps.Delete)

	// 应用路由规则
	userMux.HandleFunc("GET /api/apps/{id}/routes", routes.List)
	userMux.HandleFunc("POST /api/apps/{id}/routes", routes.Create)
	userMux.HandleFunc("PUT /api/routes/{rid}", routes.Update)
	userMux.HandleFunc("DELETE /api/routes/{rid}", routes.Delete)

	userMux.HandleFunc("GET /api/apps/{id}/redirects", redirects.List)
	userMux.HandleFunc("POST /api/apps/{id}/redirects", redirects.Create)
	userMux.HandleFunc("PUT /api/redirects/{rid}", redirects.Update)
	userMux.HandleFunc("DELETE /api/redirects/{rid}", redirects.Delete)

	userMux.HandleFunc("GET /api/settings", settings.Get)

	// 代理限制配置元数据（#47，前端下拉与提示由此驱动）
	userMux.HandleFunc("GET /api/proxy-config/keys", newProxyConfigHandler().Keys)

	// 访问票据管理
	userMux.HandleFunc("GET /api/access-tokens", accessTokens.List)
	userMux.HandleFunc("POST /api/access-tokens", accessTokens.Create)
	userMux.HandleFunc("PUT /api/access-tokens/{id}", accessTokens.Update)
	userMux.HandleFunc("DELETE /api/access-tokens/{id}", accessTokens.Delete)

	// 分享码管理
	userMux.HandleFunc("GET /api/share-codes", share.List)
	userMux.HandleFunc("POST /api/share-codes", share.Create)
	userMux.HandleFunc("PUT /api/share-codes/{id}", share.Update)
	userMux.HandleFunc("DELETE /api/share-codes/{id}", share.Delete)

	// 安全代理密钥管理
	userMux.HandleFunc("GET /api/agent-keys", agentKeys.List)
	userMux.HandleFunc("POST /api/agent-keys", agentKeys.Create)
	userMux.HandleFunc("PUT /api/agent-keys/{id}", agentKeys.Update)
	userMux.HandleFunc("DELETE /api/agent-keys/{id}", agentKeys.Delete)

	// 用户设置
	userMux.HandleFunc("GET /api/user-settings", userSettings.Get)
	userMux.HandleFunc("PUT /api/user-settings", userSettings.Update)

	// TOTP 二次验证
	userMux.HandleFunc("GET /api/totp/status", totp.Status)
	userMux.HandleFunc("GET /api/totp/setup", totp.Setup)
	userMux.HandleFunc("POST /api/totp/enable", totp.Enable)
	userMux.HandleFunc("POST /api/totp/disable", totp.Disable)
	userMux.HandleFunc("POST /api/totp/reset", totp.Reset)

	// 通行密钥管理（#71 P1，注册需 TOTP 再验证）
	userMux.HandleFunc("POST /api/auth/webauthn/register/begin", webauthnAuth.RegisterBegin)
	userMux.HandleFunc("POST /api/auth/webauthn/register/finish", webauthnAuth.RegisterFinish)
	userMux.HandleFunc("GET /api/auth/webauthn/credentials", webauthnAuth.ListCredentials)
	userMux.HandleFunc("PUT /api/auth/webauthn/credentials/{id}", webauthnAuth.RenameCredential)
	userMux.HandleFunc("DELETE /api/auth/webauthn/credentials/{id}", webauthnAuth.DeleteCredential)

	// 扫码授权登录 iPhone 侧（#71 P2，需管理 session + passkey 断言双条件）
	userMux.HandleFunc("GET /api/auth/qr/info", qrLogin.Info)
	userMux.HandleFunc("POST /api/auth/qr/approve/begin", qrLogin.ApproveBegin)
	userMux.HandleFunc("POST /api/auth/qr/approve/finish", qrLogin.ApproveFinish)

	// 将需要普通用户权限的路由包裹访客拦截中间件
	authMux.Handle("/api/", guestBlockMiddleware(userMux))

	// 管理员专用路由
	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /api/admin/users", admin.ListUsers)
	adminMux.HandleFunc("POST /api/admin/users", admin.CreateUser)
	adminMux.HandleFunc("PUT /api/admin/users/{id}", admin.UpdateUser)
	adminMux.HandleFunc("DELETE /api/admin/users/{id}", admin.DeleteUser)
	adminMux.HandleFunc("POST /api/admin/users/{id}/reset-password", admin.ResetPassword)
	adminMux.HandleFunc("PUT /api/admin/settings", admin.UpdateSettings)
	adminMux.HandleFunc("POST /api/admin/settings/reset-jwt", admin.ResetJWTSecret)

	// 会话管理（#80，admin only）
	adminMux.HandleFunc("GET /api/admin/sessions", sessions.List)
	adminMux.HandleFunc("GET /api/admin/sessions/users", sessions.Users)
	adminMux.HandleFunc("GET /api/admin/sessions/{id}", sessions.Get)
	adminMux.HandleFunc("POST /api/admin/sessions/{id}/revoke", sessions.Revoke)
	adminMux.HandleFunc("DELETE /api/admin/sessions/{id}", sessions.Delete)
	adminMux.HandleFunc("DELETE /api/admin/sessions/{id}/grants/{ref}", sessions.DeleteGrant)

	// 系统日志（admin only，挂在 /api/admin/logs 下与 adminMux 前缀一致）
	adminMux.HandleFunc("GET /api/admin/logs", logs.List)
	adminMux.HandleFunc("GET /api/admin/logs/dates", logs.Dates)
	adminMux.HandleFunc("GET /api/admin/logs/stream", logs.Stream)
	adminMux.HandleFunc("GET /api/admin/logs/sources", logs.Sources)
	adminMux.HandleFunc("GET /api/admin/logs/types", logs.Types)
	adminMux.HandleFunc("GET /api/admin/logs/msg-types", logs.MsgTypes)
	adminMux.HandleFunc("GET /api/admin/logs/levels", logs.Levels)
	adminMux.HandleFunc("GET /api/admin/logs/users", logs.Users)

	// 管理员路由包裹 adminMiddleware
	authMux.Handle("/api/admin/", adminMiddleware(adminMux))

	// 将认证路由包裹 authMiddleware
	mux.Handle("/api/", authMiddleware(database)(authMux))

	// WebSocket 隧道
	tunnelHandler := tunnel.NewHandler(database, hub, p, logStore)
	mux.Handle("/ws/", tunnelHandler)

	// 外部 API（/external-api/，独立 token 鉴权，不进 csrf/auth 中间件链）
	externalMux := http.NewServeMux()
	externalMux.HandleFunc("GET /external-api/apps", externalAPI.ListApps)
	mux.Handle("/external-api/", externalAPIAuthMiddleware(database)(recoveryMiddleware(externalMux)))

	return loggingMiddleware(recoveryMiddleware(&compressionMiddleware{next: csrfMiddleware(mux)}))
}
