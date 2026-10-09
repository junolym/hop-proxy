package api

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp/totp"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/api/ratelimit"
	"github.com/robin/hop-proxy/internal/server/db"
)

// === 分区：类型与常量 ===

// WebAuthnHandler 通行密钥（passkey）认证处理（#71）。
// 覆盖管理后台的注册（需 TOTP 再验证）与可发现凭据登录；
// 扫码授权登录（qrlogin.go）复用本 handler 的 challenge 会话与用户适配器。
type WebAuthnHandler struct {
	db      *db.DB
	limiter *ratelimit.RateLimiter

	// challenge 会话：key = challenge hex，login/begin 未认证无法用 cookie 关联，
	// 只能以 challenge 为键存内存，TTL 短 + 限流防刷
	mu       sync.Mutex
	sessions map[string]*waChallengeSession
}

// waChallengeSession 一次 WebAuthn 仪式的服务端会话数据
type waChallengeSession struct {
	data       webauthn.SessionData
	userID     int64  // 注册/批准发起用户；登录（可发现凭据）为 0，由断言的 userHandle 决定
	deviceName string // 注册时携带的设备名，finish 时落库
	expires    time.Time
}

// waChallengeTTL challenge 会话有效期
const waChallengeTTL = 5 * time.Minute

func newWebAuthnHandler(database *db.DB) *WebAuthnHandler {
	h := &WebAuthnHandler{
		db:       database,
		limiter:  ratelimit.New(server.LoginMaxAttempts, time.Duration(server.LoginWindowSecs)*time.Second),
		sessions: make(map[string]*waChallengeSession),
	}
	go h.cleanupLoop()
	return h
}

// cleanupLoop 后台定期清理过期 challenge 会话，避免 map 无界增长
func (h *WebAuthnHandler) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		now := time.Now()
		for k, s := range h.sessions {
			if now.After(s.expires) {
				delete(h.sessions, k)
			}
		}
		h.mu.Unlock()
	}
}

// storeSession 保存 challenge 会话
func (h *WebAuthnHandler) storeSession(data *webauthn.SessionData, userID int64, deviceName string) string {
	key := hex.EncodeToString([]byte(data.Challenge))
	h.mu.Lock()
	h.sessions[key] = &waChallengeSession{
		data:       *data,
		userID:     userID,
		deviceName: deviceName,
		expires:    time.Now().Add(waChallengeTTL),
	}
	h.mu.Unlock()
	return key
}

// takeSession 取出并删除 challenge 会话（一次性）
func (h *WebAuthnHandler) takeSession(challenge string) *waChallengeSession {
	key := hex.EncodeToString([]byte(challenge))
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

// === 分区：用户适配器与凭据转换 ===

// waUser 把 db.User 适配为 go-webauthn 的 User 接口。
// WebAuthnID（userHandle）用十进制用户 ID 字符串，登录断言据此反查用户。
type waUser struct {
	user  *db.User
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(strconv.FormatInt(u.user.ID, 10)) }
func (u *waUser) WebAuthnName() string                       { return u.user.Username }
func (u *waUser) WebAuthnDisplayName() string                { return u.user.Username }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

// buildWaUser 加载用户全部凭据构造适配器
func (h *WebAuthnHandler) buildWaUser(user *db.User) (*waUser, error) {
	creds, err := h.db.ListWebAuthnCredentials(user.ID)
	if err != nil {
		return nil, err
	}
	lib := make([]webauthn.Credential, 0, len(creds))
	for i := range creds {
		lib = append(lib, toLibCredential(&creds[i]))
	}
	return &waUser{user: user, creds: lib}, nil
}

// toLibCredential db 凭据 → go-webauthn 凭据
func toLibCredential(c *db.WebAuthnCredential) webauthn.Credential {
	id, _ := base64.RawURLEncoding.DecodeString(c.CredentialID)
	var transports []protocol.AuthenticatorTransport
	for _, t := range strings.Split(c.Transport, ",") {
		if t != "" {
			transports = append(transports, protocol.AuthenticatorTransport(t))
		}
	}
	return webauthn.Credential{
		ID:              id,
		PublicKey:       c.PublicKey,
		AttestationType: c.AttestationType,
		Transport:       transports,
		Flags: webauthn.CredentialFlags{
			BackupEligible: c.BackupEligible,
			BackupState:    c.BackupState,
		},
		Authenticator: webauthn.Authenticator{SignCount: c.SignCount},
	}
}

// waInstance 从 settings 构造 WebAuthn 实例。
// RP ID 取 admin_domain（登录页/授权页均在其上），Origin 与浏览器实际访问一致；
// admin_domain 可变，故按需构造而非启动时缓存。
func (h *WebAuthnHandler) waInstance() (*webauthn.WebAuthn, error) {
	adminDomain, err := h.db.GetSetting("admin_domain")
	if err != nil || adminDomain == "" {
		return nil, err
	}
	rpName, _ := h.db.GetSetting("site_name")
	if rpName == "" {
		rpName = "HopProxy"
	}
	return webauthn.New(&webauthn.Config{
		RPID:          adminDomain,
		RPDisplayName: rpName,
		RPOrigins:     []string{"https://" + adminDomain},
	})
}

// === 分区：注册（需登录 + TOTP 再验证） ===

// RegisterBegin 开始注册通行密钥。要求已登录且通过 TOTP 再验证（#71 决策），
// 强制可发现凭据（ResidentKey required）+ 用户验证（Face ID / Touch ID）。
//
// 请求体: { totp_code, device_name }
func (h *WebAuthnHandler) RegisterBegin(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	var req struct {
		TOTPCode   string `json:"totp_code"`
		DeviceName string `json:"device_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	if len(req.DeviceName) > 64 {
		jsonError(w, http.StatusBadRequest, "设备名过长")
		return
	}

	user, err := h.db.GetUserByID(claims.UserID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}

	// TOTP 再验证：未启用 TOTP 的账户必须先启用（与临时登录同一依赖）
	if !user.TOTPEnabled || user.TOTPSecret == "" {
		jsonError(w, http.StatusBadRequest, "请先绑定 TOTP 密钥")
		return
	}
	if !totp.Validate(req.TOTPCode, user.TOTPSecret) {
		slog.InfoContext(r.Context(), "通行密钥注册 TOTP 验证失败", "type", "api", "user_id", user.ID, "ip", clientIP(r))
		jsonError(w, http.StatusUnauthorized, "验证码错误")
		return
	}

	wa, err := h.waInstance()
	if err != nil {
		slog.ErrorContext(r.Context(), "构造 WebAuthn 实例失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return
	}

	u, err := h.buildWaUser(user)
	if err != nil {
		slog.ErrorContext(r.Context(), "加载通行密钥凭据失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "加载凭据失败")
		return
	}

	options, session, err := wa.BeginRegistration(u,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "开始通行密钥注册失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "开始注册失败")
		return
	}

	h.storeSession(session, user.ID, req.DeviceName)
	jsonOK(w, options)
}

// RegisterFinish 完成注册：验证注册响应并落库凭据。
// 请求体为 navigator.credentials.create() 的原始结果（PublicKeyCredential JSON）。
func (h *WebAuthnHandler) RegisterFinish(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	parsed, err := protocol.ParseCredentialCreationResponseBody(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "注册响应无效")
		return
	}

	session := h.takeSession(parsed.Response.CollectedClientData.Challenge)
	if session == nil || session.userID != claims.UserID {
		jsonError(w, http.StatusBadRequest, "注册会话已过期，请重试")
		return
	}

	user, err := h.db.GetUserByID(session.userID)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "获取用户信息失败")
		return
	}
	u, err := h.buildWaUser(user)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "加载凭据失败")
		return
	}

	wa, err := h.waInstance()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return
	}

	credential, err := wa.CreateCredential(u, session.data, parsed)
	if err != nil {
		slog.InfoContext(r.Context(), "通行密钥注册验证失败", "type", "api", "user_id", user.ID, "error", err, "ip", clientIP(r))
		jsonError(w, http.StatusBadRequest, "注册验证失败")
		return
	}

	record := &db.WebAuthnCredential{
		UserID:          user.ID,
		CredentialID:    base64.RawURLEncoding.EncodeToString(credential.ID),
		PublicKey:       credential.PublicKey,
		AttestationType: credential.AttestationType,
		Transport:       transportsToString(credential.Transport),
		BackupEligible:  credential.Flags.BackupEligible,
		BackupState:     credential.Flags.BackupState,
		SignCount:       credential.Authenticator.SignCount,
		DeviceName:      session.deviceName,
	}
	if _, err := h.db.CreateWebAuthnCredential(record); err != nil {
		slog.ErrorContext(r.Context(), "保存通行密钥凭据失败", "type", "api", "error", err, "user_id", user.ID)
		jsonError(w, http.StatusInternalServerError, "保存凭据失败")
		return
	}

	slog.InfoContext(r.Context(), "通行密钥注册成功", "type", "api", "user_id", user.ID, "username", user.Username,
		"credential_id", record.CredentialID, "device_name", record.DeviceName, "ip", clientIP(r))
	jsonMsg(w, "注册成功")
}

// transportsToString 传输方式列表序列化为逗号分隔字符串
func transportsToString(ts []protocol.AuthenticatorTransport) string {
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		parts = append(parts, string(t))
	}
	return strings.Join(parts, ",")
}

// === 分区：登录（免认证，可发现凭据） ===

// LoginBegin 开始通行密钥登录。可发现凭据（无用户名）：
// 客户端调 navigator.credentials.get()，断言携带 userHandle，服务端据此反查用户。
func (h *WebAuthnHandler) LoginBegin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if secs := h.limiter.IsBlocked(ip); secs > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		jsonError(w, http.StatusTooManyRequests, fmt.Sprintf("尝试过于频繁，请 %d 秒后重试", secs))
		return
	}

	wa, err := h.waInstance()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return
	}

	options, session, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		slog.ErrorContext(r.Context(), "开始通行密钥登录失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "开始登录失败")
		return
	}

	h.storeSession(session, 0, "")
	jsonOK(w, options)
}

// LoginFinish 完成通行密钥登录：验证断言（userHandle 反查用户），
// 通过后与密码登录走同一 JWT 签发路径。生物特征验证（UV）即视为完成双因素。
func (h *WebAuthnHandler) LoginFinish(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)

	parsed, err := protocol.ParseCredentialRequestResponseBody(r.Body)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "登录响应无效")
		return
	}

	session := h.takeSession(parsed.Response.CollectedClientData.Challenge)
	if session == nil || session.userID != 0 {
		jsonError(w, http.StatusBadRequest, "登录会话已过期，请重试")
		return
	}

	wa, err := h.waInstance()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "服务配置错误")
		return
	}

	// 可发现凭据：从断言的 userHandle 反查用户
	user, credential, err := wa.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
		userID, parseErr := strconv.ParseInt(string(userHandle), 10, 64)
		if parseErr != nil {
			return nil, parseErr
		}
		u, getErr := h.db.GetUserByID(userID)
		if getErr != nil {
			return nil, getErr
		}
		return h.buildWaUser(u)
	}, session.data, parsed)
	if err != nil {
		h.limiter.Record(ip)
		slog.InfoContext(r.Context(), "通行密钥登录验证失败", "type", "api", "error", err, "ip", ip)
		jsonError(w, http.StatusUnauthorized, "通行密钥验证失败")
		return
	}

	dbUser, ok := user.(*waUser)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "用户信息错误")
		return
	}

	// 更新使用状态（签名计数 + 备份标志 + 最后使用时间）
	_ = h.db.UpdateWebAuthnCredentialUsageByCredentialID(
		base64.RawURLEncoding.EncodeToString(credential.ID),
		credential.Authenticator.SignCount, credential.Flags.BackupState,
	)

	if err := issueSessionCookie(w, r, h.db, dbUser.user, db.SessionSourcePasskey); err != nil {
		slog.ErrorContext(r.Context(), "生成令牌失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "生成令牌失败")
		return
	}

	h.limiter.Reset(ip)
	slog.InfoContext(r.Context(), "通行密钥登录成功", "type", "api", "user_id", dbUser.user.ID, "username", dbUser.user.Username, "ip", ip)
	jsonOK(w, map[string]bool{"ok": true})
}

// === 分区：凭据管理 ===

// ListCredentials 列出当前用户的通行密钥凭据
func (h *WebAuthnHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}
	creds, err := h.db.ListWebAuthnCredentials(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "查询通行密钥凭据失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "查询凭据失败")
		return
	}
	jsonOK(w, creds)
}

// RenameCredential 重命名凭据设备名
//
// 请求体: { device_name }
func (h *WebAuthnHandler) RenameCredential(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		jsonError(w, http.StatusBadRequest, "无效的凭据 ID")
		return
	}

	var req struct {
		DeviceName string `json:"device_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	if req.DeviceName == "" || len(req.DeviceName) > 64 {
		jsonError(w, http.StatusBadRequest, "设备名不能为空且不超过 64 字符")
		return
	}

	if err := h.db.RenameWebAuthnCredential(id, claims.UserID, req.DeviceName); err != nil {
		jsonError(w, http.StatusNotFound, "凭据不存在")
		return
	}
	jsonMsg(w, "已重命名")
}

// DeleteCredential 删除凭据
func (h *WebAuthnHandler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		jsonError(w, http.StatusBadRequest, "无效的凭据 ID")
		return
	}

	if err := h.db.DeleteWebAuthnCredential(id, claims.UserID); err != nil {
		jsonError(w, http.StatusNotFound, "凭据不存在")
		return
	}
	slog.InfoContext(r.Context(), "通行密钥凭据已删除", "type", "api", "user_id", claims.UserID, "credential_db_id", id)
	jsonMsg(w, "已删除")
}
