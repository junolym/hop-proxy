package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/api/ratelimit"
	"github.com/robin/hop-proxy/internal/server/db"
)

// === 分区：类型与常量 ===

// 【设计原则：为什么自建扫码授权，而不用通行密钥自带的跨设备扫码（hybrid/caBLE）】
//
// 通行密钥的混合传输本身支持"扫码登录"：电脑上的登录页展示二维码，手机扫码
// 做生物验证，断言回传后**发起方页面**的登录即告完成。但它对本场景不适用：
//
//  1. 授权语义是隐式的。混合传输的二维码只是一段不透明的中继配对码，用户
//     扫码等于说"向发起这个仪式的一方认证我本人"——授权范围是整个账户，
//     而不是用户以为的那个用途。"码到底授权了什么"只能从手机系统弹窗里的
//     站点名推断，而发起方是可控的，用户在"帮忙临时登录"场景下不会细看。
//  2. 存在提权（授权范围混淆）风险。"别人请求帮忙临时登录某应用"的场景下，
//     攻击者发来的码实际可以是**管理后台**的通行密钥登录请求：用户以为是
//     单应用临时授权，实际交出的是与管理后台等价的身份。临时登录的语义
//     （单应用、单子域名、短时效）与通行密钥断言的实际授权（完整账户）不匹配。
//
// 自建流程把责任反转：
//
//   - 二维码只是指向**本系统渲染的**授权页（/qr-authorize）的指针；
//   - 授权页要求手机侧已有管理 session，页面内容（目标应用、具体子域名、
//     发起方 IP/UA）由服务端按 sid 渲染，第三方无法伪造；
//   - passkey 在本流程中**只做确认手势**（用户在场 + 生物验证，UV=required
//     且 1 分钟时效），不做授权主体；
//   - 电脑侧从构造上就只能换取该应用的 SSO cookie，永远拿不到管理 session
//     ——最小权限。
//
// 后续改造扫码登录时必须保持：授权语义始终由本系统页面呈现并限定范围，
// passkey 只确认、不授权。
//
// QRLoginHandler 扫码授权登录处理（#71 P2）。
// 仅支持给指定应用发 SSO cookie（临时登录场景），不签发管理 session。
// 流程：电脑在登录页创建 sid → 渲染二维码 → iPhone 扫码打开 /qr-authorize →
// 授权页确认（passkey 用户验证）→ 电脑轮询到 approved 后下发 SSO cookie。
//
// 子域名有效性校验发生在扫码（Info）与批准（ApproveFinish）阶段而非创建阶段：
// 创建端点免认证，创建即成功可防止未登录用户探测子域名是否存在 (#74)；
// 授权页展示完整域名与应用名，SSO session 绑定"子域名+应用"配对，不可放大转移。
type QRLoginHandler struct {
	db *db.DB
	// 通行密钥 handler：复用 waInstance / buildWaUser / challenge 会话存储
	wa *WebAuthnHandler
	// 创建限流（防内存滥用；sid 为 128-bit 随机，本身不可枚举）
	createLimiter *ratelimit.RateLimiter
	// 轮询限流：电脑 2s 一轮最长 5 分钟 ≈ 150 次，额度放宽
	statusLimiter *ratelimit.RateLimiter

	mu       sync.Mutex
	sessions map[string]*qrSession
}

// qrSession 一次扫码登录的服务端会话
type qrSession struct {
	sid       string
	appID     int64  // 批准时经子域名解析出的应用 ID（pending 阶段为 0）
	subdomain string // cookie name 后缀与 session 绑定 (#40)
	redirect  string // 登录成功后电脑的跳转地址
	state     string // pending / scanned / approved / consumed
	creatorIP string
	creatorUA string

	approveUserID     int64     // 批准用户（approved 后有效）
	approveSessionRef string    // 批准方登录会话的短 ID（#80：授权来源会话，供会话管理关联）
	assertChallenge   string    // approve/begin 下发的 challenge（hex key）
	assertIssuedAt    time.Time // 断言签发时间，finish 校验 1 分钟窗口

	createdAt time.Time
	expiresAt time.Time
}

// 扫码会话状态
const (
	qrStatePending   = "pending"
	qrStateScanned   = "scanned"
	qrStateApproved  = "approved"
	qrStateConsumed  = "consumed"
	qrSessionTTL     = 5 * time.Minute // 二维码有效期
	qrApproveWindow  = 1 * time.Minute // 批准断言的时效窗口（强制新鲜生物验证）
	qrCreateMaxPerIP = 30              // 每 IP 窗口期内最大创建次数
	qrStatusMaxPerIP = 600             // 每 IP 窗口期内最大轮询次数（电脑 2s 一轮 5 分钟约 150 次）
)

func newQRLoginHandler(database *db.DB, wa *WebAuthnHandler) *QRLoginHandler {
	h := &QRLoginHandler{
		db:            database,
		wa:            wa,
		createLimiter: ratelimit.New(qrCreateMaxPerIP, time.Duration(server.LoginWindowSecs)*time.Second),
		statusLimiter: ratelimit.New(qrStatusMaxPerIP, time.Duration(server.LoginWindowSecs)*time.Second),
		sessions:      make(map[string]*qrSession),
	}
	go h.cleanupLoop()
	return h
}

// cleanupLoop 后台定期清理过期会话，避免 map 无界增长
func (h *QRLoginHandler) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		now := time.Now()
		for k, s := range h.sessions {
			if now.After(s.expiresAt) {
				delete(h.sessions, k)
			}
		}
		h.mu.Unlock()
	}
}

// getSession 查询会话（不删除），过期返回 nil
func (h *QRLoginHandler) getSession(sid string) *qrSession {
	if sid == "" || len(sid) != 32 {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sid]
	if !ok || time.Now().After(s.expiresAt) {
		return nil
	}
	return s
}

// === 分区：电脑侧（登录页） ===

// Create 创建扫码登录会话（免认证）。请求体: { redirect }，redirect 为目标应用 URL。
// 仅做语法级校验（redirect 属于 proxy_domain 的子域名），不查应用存在性——
// 本端点免认证，若在此区分"子域名有无应用/是否 SSO"会向未登录用户泄漏子域名
// 有效性 (#74)。应用存在性与 SSO 方法校验推迟到扫码（Info）与批准（ApproveFinish）
// 阶段，友好提示只面向已登录用户；allowed_users 权限在批准时校验。
func (h *QRLoginHandler) Create(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if secs := h.createLimiter.IsBlocked(ip); secs > 0 {
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("操作过于频繁，请 %d 秒后重试", secs))
		return
	}

	var req struct {
		Redirect string `json:"redirect"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	if req.Redirect == "" {
		jsonError(w, http.StatusBadRequest, "参数不能为空")
		return
	}

	subdomain, ok := extractRedirectSubdomain(h.db, req.Redirect)
	if !ok {
		h.createLimiter.Record(ip)
		slog.InfoContext(r.Context(), "扫码登录创建失败", "type", "api", "reason", "invalid_redirect", "redirect", req.Redirect, "ip", ip)
		jsonError(w, http.StatusBadRequest, "无效的跳转地址")
		return
	}

	// 128-bit 随机 sid
	raw := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		jsonError(w, http.StatusInternalServerError, "创建会话失败")
		return
	}
	sid := hex.EncodeToString(raw)

	now := time.Now()
	h.mu.Lock()
	h.sessions[sid] = &qrSession{
		sid:       sid,
		subdomain: subdomain,
		redirect:  req.Redirect,
		state:     qrStatePending,
		creatorIP: ip,
		creatorUA: r.UserAgent(),
		createdAt: now,
		expiresAt: now.Add(qrSessionTTL),
	}
	h.mu.Unlock()

	// 子域名有效性不再于创建时区分，会话必然创建成功，须计数防内存滥用
	h.createLimiter.Record(ip)

	slog.InfoContext(r.Context(), "扫码登录会话创建", "type", "api", "subdomain", subdomain, "sid", sid, "ip", ip)

	jsonOK(w, map[string]string{"sid": sid})
}

// Status 电脑轮询会话状态（免认证，靠 sid 熵 + 限流防枚举）。
// approved 时为电脑下发该应用的 SSO cookie 并消耗会话（一次性）。
func (h *QRLoginHandler) Status(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	h.statusLimiter.Record(ip)
	if secs := h.statusLimiter.IsBlocked(ip); secs > 0 {
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("操作过于频繁，请 %d 秒后重试", secs))
		return
	}

	s := h.getSession(r.URL.Query().Get("sid"))
	if s == nil {
		jsonOK(w, map[string]string{"state": "expired"})
		return
	}

	// 兑现：approved → 下发 SSO cookie，一次性消耗
	if s.state == qrStateApproved {
		h.mu.Lock()
		if s.state != qrStateApproved { // 并发轮询双重兑现保护
			h.mu.Unlock()
			jsonOK(w, map[string]string{"state": "expired"})
			return
		}
		s.state = qrStateConsumed
		h.mu.Unlock()

		user, err := h.db.GetUserByID(s.approveUserID)
		if err != nil {
			slog.ErrorContext(r.Context(), "扫码登录兑现失败：用户不存在", "type", "api", "user_id", s.approveUserID, "sid", s.sid)
			jsonError(w, http.StatusInternalServerError, "登录失败")
			return
		}
		app, err := h.db.GetApp(s.appID)
		if err != nil || !app.Enabled {
			slog.ErrorContext(r.Context(), "扫码登录兑现失败：应用不可用", "type", "api", "app_id", s.appID, "sid", s.sid)
			jsonError(w, http.StatusInternalServerError, "登录失败")
			return
		}

		// 兑现前复核子域名与应用配对：SSO session 只授权给指定子域名解析出的应用，
		// 防止批准到兑现之间应用变化导致授权被转移 (#74)
		if cur, _, ok := parseAppRedirect(h.db, s.redirect); !ok || cur.ID != s.appID {
			slog.ErrorContext(r.Context(), "扫码登录兑现失败：子域名与应用配对校验未通过", "type", "api",
				"app_id", s.appID, "subdomain", s.subdomain, "sid", s.sid)
			jsonError(w, http.StatusInternalServerError, "登录失败")
			return
		}

		ttl := ssoSessionTTL(app.SSOCookieMaxAge)
		cookieMaxAge := ssoCookieMaxAge(app.SSOCookieMaxAge)
		// 来源会话（#80）：扫码授权为一次性单应用会话，授权来源记录批准方登录会话的短 ID
		sess, err := h.db.CreateAuthSession(user.ID, db.SessionSourceQR, s.approveSessionRef, ip, r.UserAgent(), ttl)
		if err != nil {
			slog.ErrorContext(r.Context(), "扫码登录：创建会话记录失败", "type", "api", "error", err, "app_id", app.ID, "sid", s.sid)
			jsonError(w, http.StatusInternalServerError, "创建会话失败")
			return
		}
		ssoToken, err := h.db.CreateSSOSession(user.ID, app.ID, s.subdomain, ttl, sess.ID)
		if err != nil {
			slog.ErrorContext(r.Context(), "扫码登录：创建 SSO session 失败", "type", "api", "error", err, "app_id", app.ID, "sid", s.sid)
			jsonError(w, http.StatusInternalServerError, "创建会话失败")
			return
		}

		proxyDomain, _ := h.db.GetSetting("proxy_domain")
		http.SetCookie(w, &http.Cookie{
			Name:     db.SSOCookieName(app.ID, s.subdomain),
			Value:    ssoToken,
			Path:     "/",
			Domain:   "." + proxyDomain,
			MaxAge:   cookieMaxAge,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})

		h.statusLimiter.Reset(ip)
		slog.InfoContext(r.Context(), "扫码登录成功", "type", "api", "user_id", user.ID, "username", user.Username,
			"app_id", app.ID, "app_name", app.Name, "subdomain", s.subdomain, "sid", s.sid, "ip", ip)

		jsonOK(w, map[string]string{"state": "approved", "redirect": s.redirect})
		return
	}

	jsonOK(w, map[string]any{"state": s.state, "expires_in": int(time.Until(s.expiresAt).Seconds())})
}

// === 分区：iPhone 侧（授权页） ===

// Info 授权页加载会话信息（需管理 session）。
// 子域名→应用校验在扫码阶段进行：错误提示只面向已登录用户，未登录的创建方
// 只会等到二维码过期，无法借此区分子域名是否有效 (#74)。
// 标记 pending→scanned（电脑轮询到后提示"请在手机上确认"）。
func (h *QRLoginHandler) Info(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	s := h.getSession(r.URL.Query().Get("sid"))
	if s == nil {
		jsonError(w, http.StatusNotFound, "二维码无效或已过期")
		return
	}

	// 应用存在性与 SSO 方法校验（面向已登录用户）
	app, _, ok := parseAppRedirect(h.db, s.redirect)
	if !ok {
		slog.InfoContext(r.Context(), "扫码登录校验失败：子域名无可用应用", "type", "api",
			"user_id", claims.UserID, "subdomain", s.subdomain, "sid", s.sid, "ip", clientIP(r))
		jsonError(w, http.StatusNotFound, "该子域名对应的应用不存在或未启用")
		return
	}
	if app.AuthMethod != server.AuthMethodSSO &&
		app.AuthMethod != server.AuthMethodSSOToken &&
		app.AuthMethod != server.AuthMethodSSOOwner &&
		app.AuthMethod != server.AuthMethodSSOAll {
		jsonError(w, http.StatusBadRequest, "该应用未启用 SSO 认证")
		return
	}

	// 标记已扫码（校验通过才标记：无效会话电脑侧只显示"请扫描"直到过期）
	h.mu.Lock()
	if s.state == qrStatePending {
		s.state = qrStateScanned
	}
	h.mu.Unlock()

	proxyDomain, _ := h.db.GetSetting("proxy_domain")

	slog.InfoContext(r.Context(), "扫码登录已扫码", "type", "api", "user_id", claims.UserID, "sid", s.sid, "ip", clientIP(r))

	jsonOK(w, map[string]any{
		"state":       s.state,
		"app_name":    app.Name,
		"subdomain":   s.subdomain,
		"full_domain": s.subdomain + "." + proxyDomain,
		"redirect":    s.redirect,
		"creator_ip":  s.creatorIP,
		"creator_ua":  s.creatorUA,
		"expires_in":  int(time.Until(s.expiresAt).Seconds()),
	})
}

// ApproveBegin 开始批准：为授权页用户发起 passkey 断言（UV=required）。
// 请求体: { sid }。会话须在 1 分钟内完成 finish，强制新鲜生物验证。
func (h *QRLoginHandler) ApproveBegin(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	var req struct {
		SID string `json:"sid"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	s := h.getSession(req.SID)
	if s == nil || (s.state != qrStatePending && s.state != qrStateScanned) {
		jsonError(w, http.StatusNotFound, "二维码无效或已过期")
		return
	}

	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	// 用户必须持有至少一个通行密钥才能批准
	creds, err := h.db.ListWebAuthnCredentials(user.ID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "获取凭据失败")
		return
	}
	if len(creds) == 0 {
		jsonError(w, http.StatusBadRequest, "您尚未注册通行密钥，无法扫码授权")
		return
	}

	wa, err := h.wa.waInstance()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return
	}
	u, err := h.wa.buildWaUser(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "加载凭据失败")
		return
	}

	options, session, err := wa.BeginLogin(u,
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "扫码批准：发起断言失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "发起确认失败")
		return
	}

	key := h.wa.storeSession(session, user.ID, "")
	h.mu.Lock()
	s.assertChallenge = key
	s.assertIssuedAt = time.Now()
	h.mu.Unlock()

	jsonOK(w, options)
}

// ApproveFinish 完成批准：验证 passkey 断言（双条件之一：1 分钟内）+
// 当前管理 session（之二）+ 用户对应用的 allowed_users 权限。
// 请求体为 navigator.credentials.get() 的原始结果，sid 走查询参数。
func (h *QRLoginHandler) ApproveFinish(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	sid := r.URL.Query().Get("sid")
	s := h.getSession(sid)
	if s == nil || (s.state != qrStatePending && s.state != qrStateScanned) {
		jsonError(w, http.StatusNotFound, "二维码无效或已过期")
		return
	}

	// 断言必须在approve窗口内完成（强制新鲜生物验证）
	h.mu.Lock()
	challengeKey := s.assertChallenge
	issuedAt := s.assertIssuedAt
	h.mu.Unlock()
	if challengeKey == "" || time.Since(issuedAt) > qrApproveWindow {
		jsonError(w, http.StatusBadRequest, "确认已超时，请重试")
		return
	}

	parsed, err := protocol.ParseCredentialRequestResponseBody(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "确认响应无效")
		return
	}

	// 取回断言会话（一次性），须属于当前用户
	session := h.wa.takeSessionByChallenge(challengeKey)
	if session == nil || session.userID != claims.UserID {
		jsonError(w, http.StatusBadRequest, "确认会话已过期，请重试")
		return
	}

	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}
	u, err := h.wa.buildWaUser(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "加载凭据失败")
		return
	}

	wa, err := h.wa.waInstance()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return
	}

	credential, err := wa.ValidateLogin(u, session.data, parsed)
	if err != nil {
		slog.InfoContext(r.Context(), "扫码批准：断言验证失败", "type", "api", "user_id", user.ID, "error", err, "sid", sid)
		jsonError(w, http.StatusUnauthorized, "验证失败")
		return
	}

	// 子域名→应用配对校验：以"子域名解析出的应用"为准（而非只校验应用 ID），
	// 确保授权对象就是发起登录的具体子域名，不可放大或转移到其他子域名 (#74)
	app, subdomain, ok := parseAppRedirect(h.db, s.redirect)
	if !ok || subdomain != s.subdomain {
		jsonError(w, http.StatusNotFound, "该子域名对应的应用不存在或未启用")
		return
	}
	if app.AuthMethod != server.AuthMethodSSO &&
		app.AuthMethod != server.AuthMethodSSOToken &&
		app.AuthMethod != server.AuthMethodSSOOwner &&
		app.AuthMethod != server.AuthMethodSSOAll {
		jsonError(w, http.StatusBadRequest, "该应用未启用 SSO 认证")
		return
	}
	if !userAllowedForApp(user, app) {
		slog.InfoContext(r.Context(), "扫码批准拒绝：无应用权限", "type", "api", "user_id", user.ID, "app_id", app.ID, "sid", sid)
		jsonError(w, http.StatusForbidden, "您不在允许访问该应用的用户列表中")
		return
	}

	// 记录批准方登录会话的短 ID（#80：作为扫码会话的授权来源，可在会话管理页搜索关联）
	approveSessionRef := ""
	if sess := getAuthSessionFromContext(r); sess != nil {
		approveSessionRef = sess.ShortID
	}

	// 标记批准（appID 在此落定：批准时该子域名解析出的应用）
	h.mu.Lock()
	if s.state != qrStatePending && s.state != qrStateScanned {
		h.mu.Unlock()
		jsonError(w, http.StatusBadRequest, "二维码状态已变化，请刷新")
		return
	}
	s.state = qrStateApproved
	s.approveUserID = user.ID
	s.approveSessionRef = approveSessionRef
	s.appID = app.ID
	h.mu.Unlock()

	// 更新凭据使用状态
	_ = h.db.UpdateWebAuthnCredentialUsageByCredentialID(
		base64.RawURLEncoding.EncodeToString(credential.ID), credential.Authenticator.SignCount, credential.Flags.BackupState,
	)

	slog.InfoContext(r.Context(), "扫码登录已批准", "type", "api", "user_id", user.ID, "username", user.Username,
		"app_id", app.ID, "app_name", app.Name, "sid", sid, "ip", clientIP(r))

	jsonOK(w, map[string]bool{"ok": true})
}

// === 分区：辅助 ===

// takeSessionByChallenge 按 hex key 取出并删除 challenge 会话
func (h *WebAuthnHandler) takeSessionByChallenge(key string) *waChallengeSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[key]
	if !ok || time.Now().After(s.expires) {
		delete(h.sessions, key)
		return nil
	}
	delete(h.sessions, key)
	return s
}
