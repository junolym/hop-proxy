package main

import (
	"context"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/api"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/loghandler"
	"github.com/robin/hop-proxy/internal/server/logstore"
	"github.com/robin/hop-proxy/internal/server/probe"
	"github.com/robin/hop-proxy/internal/server/proxy"
	"github.com/robin/hop-proxy/internal/server/tunnel"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxycfg"
)

// 构建时通过 ldflags 注入:
//
//	-ldflags "-X main.buildVersion=20260407.123456 -X main.buildCommit=abc1234"
var (
	buildVersion string // 构建时间戳（格式: YYYYMMDD.HHmmss）
	buildCommit  string // git commit short hash
)

func main() {
	listen := flag.String("listen", envOrDefault("HP_LISTEN", ":8080"), "监听地址")
	dbPath := flag.String("db", envOrDefault("HP_DB_PATH", "./data/hopproxy.db"), "SQLite 数据库路径")
	flag.Parse()

	// 初始化日志系统（在数据库之前，让后续 slog 都进新系统）
	// 日志目录与 db 同级：./data/logs/
	logDir := filepath.Join(filepath.Dir(*dbPath), "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		slog.Error("创建日志目录失败", "type", "system", "dir", logDir, "error", err)
		os.Exit(1)
	}
	logStore, err := logstore.New(logDir, "hopproxy",
		logstore.WithMaxDays(envIntOrDefault("HP_LOG_MAX_DAYS", 7)),
		logstore.WithMaxFileSize(envBytesOrDefault("HP_LOG_MAX_SIZE", 100*1024*1024)),
		logstore.WithMinLevel(envOrDefault("HP_LOG_LEVEL", "")),
	)
	if err != nil {
		slog.Error("初始化日志存储失败", "type", "system", "error", err)
		os.Exit(1)
	}
	defer logStore.Close()
	slog.SetDefault(slog.New(loghandler.New(logStore)))
	slog.Info("日志系统已初始化", "type", "system", "dir", logDir)

	// 初始化数据库
	database, err := db.Open(*dbPath)
	if err != nil {
		slog.Error("初始化数据库失败", "type", "system", "error", err)
		os.Exit(1)
	}
	defer database.Close()

	// 创建隧道 Hub
	hub := tunnel.NewHub()

	// 创建代理处理器（子域名流量）
	proxyHandler := proxy.NewProxy(database, hub)

	// 创建 API 路由（管理域名流量）
	apiRouter := api.NewRouter(database, hub, proxyHandler, logStore, buildVersion, buildCommit)

	// 分享码兑换处理器（/s/{code}，无需登录）
	shareHandler := api.NewShareHandler(database)

	// 前端静态文件
	var staticFS http.Handler
	webFS, err := fs.Sub(webDistFS, "web/dist")
	if err == nil {
		staticFS = http.FileServer(http.FS(webFS))
	}

	// 主路由：根据 Host 头分发
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 判断是管理域名还是代理域名
		if isAdminRequest(database, r) {
			// 管理域名流量
			// 健康探测端点：仅管理域名/本地回环可访问，代理域名的 /healthz 原样转发给后端应用
			if r.URL.Path == "/healthz" {
				w.WriteHeader(http.StatusOK)
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") || strings.HasPrefix(r.URL.Path, "/pubkey/") || strings.HasPrefix(r.URL.Path, "/external-api/") {
				apiRouter.ServeHTTP(w, r)
				return
			}
			// 前端静态文件
			if staticFS != nil {
				path := r.URL.Path

				// 多入口路由处理
				switch {
				case path == "/sso" || strings.HasPrefix(path, "/sso?"):
					// SSO 页面：检查 session，未登录则重定向到登录页
					if !isSessionValid(database, r) {
						// 构造重定向 URL，保留原有查询参数。
						// 必须对 r.URL.String() 做 QueryEscape，否则其中的 ? 会被
						// 浏览器解析为外层 query，导致 next 参数被截断。
						redirectURL := "/login?next=" + url.QueryEscape(r.URL.String())
						http.Redirect(w, r, redirectURL, http.StatusFound)
						return
					}
					// 已登录：检查是否已有目标应用的有效 SSO Cookie
					if redirectTarget := r.URL.Query().Get("redirect"); redirectTarget != "" {
						if autoRedirectIfSSOValid(w, r, database, redirectTarget) {
							return
						}
					}
					// 返回 sso.html
					r.URL.Path = "/sso.html"
					staticFS.ServeHTTP(w, r)
					return

				case path == "/login" || strings.HasPrefix(path, "/login?"):
					// 登录页面：直接返回 login.html
					r.URL.Path = "/login.html"
					staticFS.ServeHTTP(w, r)
					return

				case path == "/qr-authorize" || strings.HasPrefix(path, "/qr-authorize?"):
					// 扫码授权页面（#71 P2）：iPhone 扫码后打开。
					// 未登录由前端页面自行跳转 /login?next=...（与 /sso 页面同模型）
					r.URL.Path = "/qr-authorize.html"
					staticFS.ServeHTTP(w, r)
					return

				case path == "/guest":
					// 访客提示页面：直接返回 guest.html
					r.URL.Path = "/guest.html"
					staticFS.ServeHTTP(w, r)
					return

				case path == "/share-error":
					// 分享链接无效/过期统一错误页面
					r.URL.Path = "/share-error.html"
					staticFS.ServeHTTP(w, r)
					return

				case strings.HasPrefix(path, "/s/"):
					// 分享码兑换端点：无需登录，直接处理
					shareHandler.Redeem(w, r)
					return

				default:
					// 其他路由：SPA 回退到 index.html
					if path == "/" || !hasFileExtension(path) {
						r.URL.Path = "/"
					}
					staticFS.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, "前端未构建", http.StatusNotFound)
		} else {
			// 代理域名流量
			proxyHandler.ServeHTTP(w, r)
		}
	})

	// 代理限制配置（#47）：监听器级参数（读头/读 body/写/keepalive/头上限）
	// 在收到 Host 头之前就生效，只能启动时读取全局配置设置；变更需重启
	globalCfgText, cfgErr := database.GetSetting("proxy_config")
	if cfgErr != nil {
		globalCfgText = ""
	}
	globalCfg := proxycfg.Merge(globalCfgText, "")

	// 启动 HTTP 服务器
	server := &http.Server{
		Addr:    *listen,
		Handler: recoveryHandler(handler),
		// net/http 内部错误（如 superfluous WriteHeader）统一走 slog 管道
		ErrorLog:          httputil.NewServerErrorLog(),
		ReadHeaderTimeout: proxycfg.DurationOf(globalCfg, "client_header_timeout"),      // 读请求头超时，防止 Slowloris
		ReadTimeout:       proxycfg.DurationOf(globalCfg, "client_body_timeout"),        // 读整个请求超时（含 body，0=不限）
		WriteTimeout:      proxycfg.DurationOf(globalCfg, "send_timeout"),               // 写响应超时（0=不限，SSE/流式响应需要）
		IdleTimeout:       proxycfg.DurationOf(globalCfg, "keepalive_timeout"),          // Keep-Alive 空闲超时
		MaxHeaderBytes:    int(proxycfg.SizeOf(globalCfg, "client_header_buffer_size")), // 请求头上限
	}

	// 优雅关闭
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 启动定时任务：每日凌晨 3 点自动禁用不活跃应用 + 会话保留期清理
	go startInactiveAppCleaner(ctx, database, hub)

	// 启动应用可用性定时探测
	probeService := probe.NewService(database, proxyHandler)
	go probeService.Run(ctx)

	// 进程自监控：定期自探测管理 API 全链路（中间件 + DB 读 + slog 落盘），
	// 连续失败说明进程卡死（监听器僵死/全局死锁，如 #54 logstore 自死锁），
	// 主动退出交由容器 restart 策略重启（Docker healthcheck 只标记不重启，避免依赖 docker.sock）
	go startHealthWatchdog(ctx, *listen)

	go func() {
		<-ctx.Done()
		slog.Info("正在关闭服务器...", "type", "system")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("服务器关闭超时或出错", "type", "system", "error", err)
		}
	}()

	slog.Info("HopProxy 服务端启动", "type", "system", "listen", *listen, "db", *dbPath)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("服务器异常", "type", "system", "error", err)
		os.Exit(1)
	}
	slog.Info("服务器已关闭", "type", "system")
}

// startHealthWatchdog 进程自监控：自探测管理 API，连续失败则退出进程。
// 探测 /api/setup/status 而非 /healthz：/healthz 分支不经过中间件/DB/日志链路，
// 全局死锁（如 #54 logstore 自死锁导致所有 slog 调用永久阻塞）时仍返回 200，
// 无法发现问题；setup/status 无需认证，且完整覆盖中间件 + DB 读 + slog 落盘路径，
// 任何一环卡死都能在探测超时中暴露。
// Docker/Compose 的 healthcheck 只会把容器标记为 unhealthy，不会触发重启；
// 由进程自杀 + restart 策略实现"挂了就重启"，无需挂载 docker.sock。
func startHealthWatchdog(ctx context.Context, listen string) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		slog.Warn("自监控未启用：监听地址无法解析", "type", "system", "listen", listen)
		return
	}
	probeURL := "http://127.0.0.1:" + port + "/api/setup/status"
	client := &http.Client{Timeout: 10 * time.Second}

	const interval = 30 * time.Second
	const maxFailures = 3
	failures := 0
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return // 优雅关闭期间不再探测，避免与 Shutdown 竞争误判
		case <-ticker.C:
		}

		healthy := false
		if resp, err := client.Get(probeURL); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			healthy = resp.StatusCode == http.StatusOK
		}
		if healthy {
			failures = 0
			continue
		}
		failures++
		// 探测超时即服务实际不可用（真实用户请求大概率同样失败），用 Critical 级别
		loghandler.Critical("自健康探测失败", "type", "system", "url", probeURL, "failures", failures, "max", maxFailures)
		if failures >= maxFailures {
			loghandler.Critical("自健康探测连续失败，进程退出交由容器重启", "type", "system", "failures", failures)
			os.Exit(1)
		}
	}
}

// isAdminRequest 判断请求是否为管理域名流量
func isAdminRequest(database *db.DB, r *http.Request) bool {
	adminDomain, err := database.GetSetting("admin_domain")
	if err != nil {
		// 未初始化时，所有请求都视为管理流量
		return true
	}

	host := r.Host
	// 去掉端口
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	// 本地回环视为管理域名：容器 healthcheck / watchdog 自探测 / 本地调试
	// 无需知道 admin_domain 配置即可访问 /healthz 等管理端点
	if isLoopbackHost(host) {
		return true
	}

	return strings.EqualFold(host, adminDomain)
}

// isLoopbackHost 判断 Host（已去端口）是否为本地回环地址
func isLoopbackHost(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || strings.Trim(host, "[]") == "::1"
}

// isSessionValid 检查 session Cookie 是否有效（存在未失效的登录会话）
func isSessionValid(database *db.DB, r *http.Request) bool {
	return getAuthSession(database, r) != nil
}

// getAuthSession 解析 session Cookie 并返回有效的登录会话（#80）。
// 无效/无 sid/已退出/被强制下线/已过期一律返回 nil；
// 后续操作以会话主体为准（快速登录归属、SSO 页面跳过确认页）。
func getAuthSession(database *db.DB, r *http.Request) *db.AuthSession {
	cookie, err := r.Cookie("hopproxy_session")
	if err != nil {
		return nil
	}

	// 获取 JWT Secret
	jwtSecret, err := database.GetSetting("jwt_secret")
	if err != nil || jwtSecret == "" {
		return nil
	}

	// 解析 JWT Token
	token, err := jwt.Parse(cookie.Value, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(jwtSecret), nil
	})
	if err != nil || !token.Valid {
		return nil
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil
	}

	// 登录会话有效性校验（#80）：sid 必须存在且会话有效（未退出/未被强制下线/未过期），
	// 否则被踢下线的旧 token 仍可触发快速登录等旁路行为
	sid, _ := claims["sid"].(string)
	if sid == "" {
		return nil
	}
	session, err := database.GetAuthSessionBySID(sid)
	if err != nil || !session.Active() {
		return nil
	}
	return session
}

// autoRedirectIfSSOValid 在 /sso 页面判定是否可跳过确认页直接 302 回目标应用。
// 返回 true 表示已跳转，调用方不应再渲染 sso.html。
//
// 跳过确认页的两条路径（均要求管理端 session 有效）：
//  1. SSO cookie 仍有效且归属当前登录用户 → 直接 302 回目标。
//     （仅当 admin_domain 是 proxy_domain 子域时 cookie 才会被带上，部署相关，best-effort。）
//  2. 快速登录：用户开启了 quick_login 且应用是 SSO 类认证（sso / sso_token /
//     sso_owner / sso_all）且通过 allowed_users 权限校验 → 直接下发新 SSO cookie 并 302。
//
// 旧方案（issue #8 第一版）失败原因：浏览器在 SSO cookie 过期后会删除它，
// 服务端拿不到 oldToken，无法查询 expired session 判断归属。新方案不依赖旧 cookie，
// 改由管理端 session 识别用户身份。
//
// 权限校验需与 internal/server/api/sso.go Authorize、internal/server/proxy/sso.go
// validateSSOCookie、internal/server/api/temp_login.go userAllowedForApp 保持一致。
func autoRedirectIfSSOValid(w http.ResponseWriter, r *http.Request, database *db.DB, redirectTarget string) bool {
	redirectURL, err := url.Parse(redirectTarget)
	if err != nil {
		return false
	}

	proxyDomain, _ := database.GetSetting("proxy_domain")
	if proxyDomain == "" {
		return false
	}

	host := redirectURL.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	host = strings.ToLower(host)
	suffix := "." + strings.ToLower(proxyDomain)
	if !strings.HasSuffix(host, suffix) {
		return false
	}
	subdomain := strings.TrimSuffix(host, suffix)
	if subdomain == "" {
		return false
	}

	app, err := database.GetAppBySubdomain(subdomain)
	if err != nil || !app.Enabled {
		return false
	}

	sess := getAuthSession(database, r)
	if sess == nil {
		return false
	}

	// 路径 1：SSO cookie 仍有效且归属本用户 → 直接跳转
	// cookie name 含具体子域名后缀 (#40)
	cookieName := db.SSOCookieName(app.ID, subdomain)
	if cookie, err := r.Cookie(cookieName); err == nil {
		if session, err := database.GetSSOSession(cookie.Value, app.ID); err == nil && session.UserID == sess.UserID && session.Subdomain == subdomain {
			http.Redirect(w, r, redirectTarget, http.StatusFound)
			return true
		}
	}

	// 路径 2：快速登录 —— 用户开启 quick_login + SSO 类认证 + 权限通过 → 下发新 cookie 跳转
	return issueSSOForQuickLogin(w, r, database, app, sess, proxyDomain, subdomain, redirectTarget)
}

// issueSSOForQuickLogin 在快速登录开关开启时为当前登录会话静默下发新 SSO cookie 并 302 跳转。
// 返回 true 表示已下发 cookie 并跳转；false 表示不符合快速登录条件（应回退到 SSO 确认页）。
// sess 为当前登录会话（#80：快速登录不是独立会话，应用授权归属该登录会话）；
// subdomain 用于 cookie name 后缀与 session 绑定 (#40)。
func issueSSOForQuickLogin(w http.ResponseWriter, r *http.Request, database *db.DB, app *db.App, sess *db.AuthSession, proxyDomain, subdomain, redirectTarget string) bool {
	// 应用必须是 SSO 类认证
	switch app.AuthMethod {
	case server.AuthMethodSSO, server.AuthMethodSSOToken, server.AuthMethodSSOOwner, server.AuthMethodSSOAll:
		// ok
	default:
		return false
	}

	// 应用开启二次验证（TOTP / 通行密钥）时不允许快速登录 (#76)
	if app.SecondFactor != "" {
		return false
	}

	user, err := database.GetUserByID(sess.UserID)
	if err != nil || user == nil {
		return false
	}
	if !user.QuickLogin {
		return false
	}

	// allowed_users 权限校验
	if app.AllowedUsers == server.AllowedUsersOwner || app.AllowedUsers == "" {
		if app.UserID != user.ID && !user.IsAdmin {
			return false
		}
	} else if app.AllowedUsers != server.AllowedUsersAll {
		allowed := false
		for _, name := range strings.Split(app.AllowedUsers, ",") {
			if strings.TrimSpace(name) == user.Username {
				allowed = true
				break
			}
		}
		if !allowed && !user.IsAdmin {
			return false
		}
	}

	ttl := time.Duration(app.SSOCookieMaxAge) * time.Second
	if app.SSOCookieMaxAge < 0 {
		// 会话 cookie：DB session 用默认 24h
		ttl = 24 * time.Hour
	}
	// 应用授权归属当前登录会话（#80：快速登录不新建会话）
	newToken, err := database.CreateSSOSession(sess.UserID, app.ID, subdomain, ttl, sess.ID)
	if err != nil {
		slog.Error("SSO 快速登录：创建新 session 失败", "type", "auth", "app_id", app.ID, "user_id", sess.UserID, "error", err)
		return false
	}

	cookieMaxAge := int(ttl.Seconds())
	if app.SSOCookieMaxAge < 0 {
		// 会话 cookie：不设置 Max-Age
		cookieMaxAge = 0
	}
	http.SetCookie(w, &http.Cookie{
		Name:     db.SSOCookieName(app.ID, subdomain),
		Value:    newToken,
		Path:     "/",
		Domain:   "." + proxyDomain,
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	slog.Info("SSO 快速登录", "type", "auth",
		"app_id", app.ID,
		"user_id", sess.UserID,
	)

	http.Redirect(w, r, redirectTarget, http.StatusFound)
	return true
}

// hasFileExtension 检查路径是否包含文件扩展名
func hasFileExtension(path string) bool {
	for i := len(path) - 1; i >= 0; i-- {
		switch path[i] {
		case '.':
			return true
		case '/':
			return false
		}
	}
	return false
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// envIntOrDefault 读整型环境变量，缺失或非法时用默认值
func envIntOrDefault(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVal
}

// envBytesOrDefault 读字节数环境变量，支持 M/G（大小写不敏感）后缀和纯数字（字节）
// 例如 "100M" / "1G" / "104857600"
func envBytesOrDefault(key string, defaultVal int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return defaultVal
	}
	multiplier := int64(1)
	upper := strings.ToUpper(v)
	switch {
	case strings.HasSuffix(upper, "G"):
		multiplier, v = 1024*1024*1024, strings.TrimSpace(v[:len(v)-1])
	case strings.HasSuffix(upper, "M"):
		multiplier, v = 1024*1024, strings.TrimSpace(v[:len(v)-1])
	case strings.HasSuffix(upper, "K"):
		multiplier, v = 1024, strings.TrimSpace(v[:len(v)-1])
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return defaultVal
	}
	return n * multiplier
}

// startInactiveAppCleaner 定时检查并禁用不活跃应用
func startInactiveAppCleaner(ctx context.Context, database *db.DB, hub *tunnel.Hub) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 计算下次凌晨 3 点的时间
	nextRun := nextTimeAt(3, 0)
	slog.Info("定时任务已启动", "type", "system", "first_run", nextRun.Format("2006-01-02 15:04:05"))

	for {
		select {
		case <-ctx.Done():
			slog.Info("定时任务已停止", "type", "system")
			return
		case now := <-ticker.C:
			if now.After(nextRun) || now.Equal(nextRun) {
				count, err := database.AutoDisableInactiveApps()
				if err != nil {
					slog.Error("自动禁用不活跃应用失败", "type", "system", "error", err)
				} else if count > 0 {
					slog.Info("自动禁用了不活跃应用", "type", "system", "count", count)
					// 禁用改变全局子域名注册表，广播所有在线客户端刷新 (#57)
					hub.NotifyAppsChangedAll()
				}
				// 会话/授权保留期清理（#80）
				cleanupRetainedSessions(database)
				// 计算下一天凌晨 3 点
				nextRun = nextTimeAt(3, 0)
				slog.Debug("下次检查时间", "type", "system", "next_run", nextRun.Format("2006-01-02 15:04:05"))
			}
		}
	}
}

// nextTimeAt 计算下一个指定时间点（今天或明天）
func nextTimeAt(hour, minute int) time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if next.Before(now) || next.Equal(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// cleanupRetainedSessions 按保留期清理失效登录会话与应用授权（#80）。
// 保留期来自 settings.audit_retention_days（默认 90 天，0=永久保留）；
// 有效会话与授权永不清理，只删已失效且超出保留期的记录。
func cleanupRetainedSessions(database *db.DB) {
	days := 90
	if v, err := database.GetSetting("audit_retention_days"); err == nil && v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			days = n
		}
	}
	if days == 0 {
		slog.Debug("会话保留期为永久，跳过清理", "type", "system")
		return
	}

	retention := time.Duration(days) * 24 * time.Hour
	if n, err := database.CleanupRetainedSSOSessions(retention); err != nil {
		slog.Error("清理过期应用授权失败", "type", "system", "error", err)
	} else if n > 0 {
		slog.Info("已清理过期应用授权", "type", "system", "count", n)
	}
	if n, err := database.CleanupRetainedAuthSessions(retention); err != nil {
		slog.Error("清理过期登录会话失败", "type", "system", "error", err)
	} else if n > 0 {
		slog.Info("已清理过期登录会话", "type", "system", "count", n)
	}
}

// recoveryHandler 全局 panic 恢复，保护代理流量不崩溃
func recoveryHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.Error("请求处理 panic", "type", "api", "error", err, "method", r.Method, "path", r.URL.Path)
				http.Error(w, "内部服务器错误", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
