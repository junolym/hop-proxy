package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/proxy"
	"github.com/robin/hop-proxy/internal/server/tunnel"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	"github.com/robin/hop-proxy/pkg/validation"
)

// AppsHandler 应用管理处理
type AppsHandler struct {
	db    *db.DB
	hub   *tunnel.Hub
	proxy *proxy.Proxy
}

func newAppsHandler(database *db.DB, hub *tunnel.Hub, p *proxy.Proxy) *AppsHandler {
	return &AppsHandler{db: database, hub: hub, proxy: p}
}

// List 应用列表
func (h *AppsHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)

	// 检查是否有 user_id 参数
	userIDStr := r.URL.Query().Get("user_id")
	var targetUserID int64 = claims.UserID

	if userIDStr != "" {
		// 非管理员不允许查看其他用户的应用
		if !claims.IsAdmin {
			slog.WarnContext(r.Context(), "权限拒绝：查看他人应用", "type", "api")
			jsonError(w, http.StatusForbidden, "无权查看其他用户的应用")
			return
		}
		userID, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "无效的用户 ID")
			return
		}
		targetUserID = userID
	}

	apps, err := h.db.ListApps(targetUserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取应用列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取应用列表失败")
		return
	}
	for i := range apps {
		for j := range apps[i].ClientInfos {
			cid := apps[i].ClientInfos[j].ClientID
			if cid == proxy.HostClientID {
				apps[i].ClientInfos[j].Online = true
				apps[i].ClientInfos[j].ClientName = "Host"
			} else {
				apps[i].ClientInfos[j].Online = h.hub.IsOnline(cid)
			}
		}
		for _, info := range apps[i].ClientInfos {
			if info.Online {
				apps[i].ClientOnline = true
				break
			}
		}
	}
	if apps == nil {
		apps = []db.App{}
	}
	jsonOK(w, apps)
}

// Create 添加应用
func (h *AppsHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)

	count, _ := h.db.CountApps(claims.UserID)
	if count >= server.MaxAppsPerUser {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("应用数量已达上限(%d)", server.MaxAppsPerUser))
		return
	}

	var req struct {
		Name            string                         `json:"name"`
		Subdomain       string                         `json:"subdomain"`
		TargetURL       string                         `json:"target_url"`
		ClientIDs       []string                       `json:"client_ids"`
		ClientConfigs   map[string]db.ClientFullConfig `json:"client_configs"`     // 客户端配置（目标地址、代理 ID 等）
		AuthMethod      string                         `json:"auth_method"`        // none 或 sso
		AllowedUsers    string                         `json:"allowed_users"`      // owner 或 all
		SSOCookieMaxAge int                            `json:"sso_cookie_max_age"` // SSO Cookie 过期时间（秒）
		LoadBalance     bool                           `json:"load_balance"`
		CustomHeaders   map[string]string              `json:"custom_headers"` // 自定义 HTTP Header
		HeaderMode      string                         `json:"header_mode"`    // 请求头缺省处理模式：auto_xff（默认）/ auto_origin / none (#46)
		ProxyConfig     string                         `json:"proxy_config"`   // 应用级代理配置覆盖（多行 key: value 文本，#47）
		ExemptPaths     []string                       `json:"exempt_paths"`   // 豁免 SSO 认证的路径列表
		AgentKeyUUID    string                         `json:"agent_key_uuid"` // 安全代理密钥 UUID（空=不使用 agent）
		SecondFactor    string                         `json:"second_factor"`  // SSO 授权二次验证方式：空=无 / totp / passkey (#76)
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Subdomain = strings.TrimSpace(strings.ToLower(req.Subdomain))
	req.TargetURL = strings.TrimSpace(req.TargetURL)

	// 应用级代理配置校验（#47）：只允许应用级 key
	if err := proxycfg.ValidateForApp(req.ProxyConfig); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 默认值
	if req.AuthMethod == "" {
		req.AuthMethod = server.AuthMethodSSO
	}
	if req.AllowedUsers == "" {
		req.AllowedUsers = server.AllowedUsersOwner
	}
	if req.SSOCookieMaxAge == 0 {
		req.SSOCookieMaxAge = server.DefaultSSOTTLS
	}
	if req.HeaderMode == "" {
		req.HeaderMode = httputil.HeaderModeAutoXFF
	}
	if !httputil.ValidHeaderMode(req.HeaderMode) {
		jsonError(w, http.StatusBadRequest, "无效的请求头缺省处理模式")
		return
	}

	// 二次验证方式前置校验：要求 SSO 类认证 + 当前用户已具备对应验证条件
	if err := h.validateSecondFactor(claims.UserID, req.AuthMethod, req.SecondFactor); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 普通用户：强制认证 + 子域名加前缀
	if !claims.IsAdmin {
		// 普通用户强制开启认证
		req.AuthMethod = server.AuthMethodSSO
		// 普通用户子域名自动加 <username>- 前缀
		prefix := strings.ToLower(claims.Username) + "-"
		if !strings.HasPrefix(req.Subdomain, prefix) {
			req.Subdomain = prefix + req.Subdomain
		}
	}

	if req.Name == "" || req.Subdomain == "" || req.TargetURL == "" {
		jsonError(w, http.StatusBadRequest, "名称、子域名和目标地址不能为空")
		return
	}
	if len(req.ClientIDs) == 0 {
		jsonError(w, http.StatusBadRequest, "至少需要关联一个客户端")
		return
	}

	// 普通用户不能使用 __host__
	if !claims.IsAdmin {
		for _, cid := range req.ClientIDs {
			if cid == proxy.HostClientID {
				jsonError(w, http.StatusForbidden, "仅管理员可使用本机(Host)客户端")
				return
			}
		}
	}

	if err := validation.ValidSubdomainPattern(req.Subdomain); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	exists, _ := h.db.SubdomainExists(req.Subdomain, 0)
	if exists {
		jsonError(w, http.StatusBadRequest, "子域名已被使用")
		return
	}

	if err := h.validateClientConfigs(claims.UserID, req.ClientIDs, req.ClientConfigs); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(req.ClientIDs) < 2 {
		req.LoadBalance = false
	}

	// inactive_days 已迁移至用户设置，不再使用应用级别设置，保留 nil
	var inactiveDays *int = nil

	app, err := h.db.CreateApp(claims.UserID, req.Name, req.Subdomain, req.TargetURL,
		req.AuthMethod, req.AllowedUsers, req.SSOCookieMaxAge, req.LoadBalance, req.ClientIDs, req.ClientConfigs, inactiveDays, req.CustomHeaders, req.HeaderMode, req.ProxyConfig, req.ExemptPaths, req.AgentKeyUUID, req.SecondFactor)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建应用失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建应用失败")
		return
	}

	// 通知相关客户端应用列表已变更
	h.hub.NotifyAppsChanged(req.ClientIDs)
	// 新子域名进入全局注册表，广播所有在线客户端刷新 (#57)
	h.hub.NotifyAppsChangedAll()

	slog.InfoContext(r.Context(), "应用创建", "type", "api", "app_id", app.ID, "user_id", claims.UserID, "name", app.Name, "subdomain", app.Subdomain)
	jsonOK(w, app)
}

// Update 编辑应用
func (h *AppsHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的应用 ID")
		return
	}

	claims := getUserFromContext(r)
	app, err := h.db.GetApp(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req struct {
		Name            string                         `json:"name"`
		Subdomain       string                         `json:"subdomain"`
		TargetURL       string                         `json:"target_url"`
		ClientIDs       []string                       `json:"client_ids"`
		ClientConfigs   map[string]db.ClientFullConfig `json:"client_configs"` // 客户端配置（目标地址、代理 ID 等）
		Enabled         *bool                          `json:"enabled"`
		AuthMethod      string                         `json:"auth_method"`        // none 或 sso
		AllowedUsers    string                         `json:"allowed_users"`      // owner 或 all
		SSOCookieMaxAge *int                           `json:"sso_cookie_max_age"` // SSO Cookie 过期时间（秒）
		LoadBalance     *bool                          `json:"load_balance"`
		CustomHeaders   map[string]string              `json:"custom_headers"` // 自定义 HTTP Header
		HeaderMode      *string                        `json:"header_mode"`    // 请求头缺省处理模式（nil=不修改，#46）
		ProxyConfig     *string                        `json:"proxy_config"`   // 应用级代理配置覆盖（nil=不修改，空字符串=清除，#47）
		ExemptPaths     []string                       `json:"exempt_paths"`   // 豁免 SSO 认证的路径列表
		AgentKeyUUID    *string                        `json:"agent_key_uuid"` // 安全代理密钥 UUID（nil=不修改，空字符串=清除）
		SecondFactor    *string                        `json:"second_factor"`  // SSO 授权二次验证方式（nil=不修改，#76）
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Subdomain = strings.TrimSpace(strings.ToLower(req.Subdomain))
	req.TargetURL = strings.TrimSpace(req.TargetURL)

	// 默认值处理
	if req.AuthMethod == "" {
		req.AuthMethod = app.AuthMethod
	}
	if req.AllowedUsers == "" {
		req.AllowedUsers = app.AllowedUsers
	}
	if req.AllowedUsers == "" {
		req.AllowedUsers = server.AllowedUsersOwner
	}
	ssoCookieMaxAge := app.SSOCookieMaxAge
	if req.SSOCookieMaxAge != nil {
		ssoCookieMaxAge = *req.SSOCookieMaxAge
	}

	// 普通用户：强制认证 + 子域名加前缀
	if !claims.IsAdmin {
		req.AuthMethod = server.AuthMethodSSO // 普通用户不能关闭认证
		prefix := strings.ToLower(claims.Username) + "-"
		if !strings.HasPrefix(req.Subdomain, prefix) {
			req.Subdomain = prefix + req.Subdomain
		}
	}

	if req.Name == "" || req.Subdomain == "" || req.TargetURL == "" {
		jsonError(w, http.StatusBadRequest, "名称、子域名和目标地址不能为空")
		return
	}
	if len(req.ClientIDs) == 0 {
		jsonError(w, http.StatusBadRequest, "至少需要关联一个客户端")
		return
	}

	if !claims.IsAdmin {
		for _, cid := range req.ClientIDs {
			if cid == proxy.HostClientID {
				jsonError(w, http.StatusForbidden, "仅管理员可使用本机(Host)客户端")
				return
			}
		}
	}

	if err := validation.ValidSubdomainPattern(req.Subdomain); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	exists, _ := h.db.SubdomainExists(req.Subdomain, id)
	if exists {
		jsonError(w, http.StatusBadRequest, "子域名已被使用")
		return
	}

	if err := h.validateClientConfigs(claims.UserID, req.ClientIDs, req.ClientConfigs); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	enabled := app.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	loadBalance := app.LoadBalance
	if req.LoadBalance != nil {
		loadBalance = *req.LoadBalance
	}
	if len(req.ClientIDs) < 2 {
		loadBalance = false
	}

	// inactive_days 已迁移至用户设置，保留原值
	inactiveDays := app.InactiveDays

	// agent_key_uuid: nil=不修改，空字符串=清除，非空=设置
	agentKeyUUID := app.AgentKeyUUID
	if req.AgentKeyUUID != nil {
		agentKeyUUID = *req.AgentKeyUUID
	}

	// header_mode: nil=不修改，否则使用新值（空串走 UpdateApp 缺省 auto_xff）
	headerMode := app.HeaderMode
	if req.HeaderMode != nil {
		if *req.HeaderMode != "" && !httputil.ValidHeaderMode(*req.HeaderMode) {
			jsonError(w, http.StatusBadRequest, "无效的请求头缺省处理模式")
			return
		}
		headerMode = *req.HeaderMode
	}

	// proxy_config: nil=不修改，否则校验后使用新值（空字符串=清除覆盖）
	proxyConfig := app.ProxyConfig
	if req.ProxyConfig != nil {
		if err := proxycfg.ValidateForApp(*req.ProxyConfig); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		proxyConfig = *req.ProxyConfig
	}

	// second_factor: nil=不修改，否则使用新值（空=关闭二次验证）
	secondFactor := app.SecondFactor
	if req.SecondFactor != nil {
		secondFactor = *req.SecondFactor
	}
	// 二次验证方式校验（含未修改场景：防止认证方式改为非 SSO 类时残留二次验证）
	if err := h.validateSecondFactor(claims.UserID, req.AuthMethod, secondFactor); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.db.UpdateApp(id, req.Name, req.Subdomain, req.TargetURL,
		enabled, req.AuthMethod, req.AllowedUsers, ssoCookieMaxAge, loadBalance, req.ClientIDs, req.ClientConfigs, inactiveDays, req.CustomHeaders, headerMode, proxyConfig, req.ExemptPaths, agentKeyUUID, secondFactor); err != nil {
		slog.ErrorContext(r.Context(), "更新应用失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新应用失败")
		return
	}

	// 应用从启用变为禁用时，清空探测状态（避免下次启用后展示陈旧结果）
	if app.Enabled && !enabled {
		if err := h.db.ClearAppProbeStatus(id); err != nil {
			slog.WarnContext(r.Context(), "清空应用探测状态失败", "type", "api", "app_id", id, "error", err)
		}
	}

	// 通知相关客户端应用列表已变更（新旧客户端列表合并去重）
	allClientIDs := append(app.ClientIDs, req.ClientIDs...)
	uniqueClientIDs := make(map[string]struct{})
	for _, cid := range allClientIDs {
		uniqueClientIDs[cid] = struct{}{}
	}
	var notifyClientIDs []string
	for cid := range uniqueClientIDs {
		notifyClientIDs = append(notifyClientIDs, cid)
	}
	h.hub.NotifyAppsChanged(notifyClientIDs)
	// 子域名或启停变更会改变全局子域名注册表，广播所有在线客户端刷新 (#57)；
	// 其他配置修改不影响无关客户端的解析，不广播
	if app.Subdomain != req.Subdomain || app.Enabled != enabled {
		h.hub.NotifyAppsChangedAll()
	}

	slog.InfoContext(r.Context(), "应用更新", "type", "api", "user_id", claims.UserID, "app_id", id)
	jsonMsg(w, "更新成功")
}

// Duplicate 克隆应用 (#51)
// 后端一次性复制全部配置（含 app_clients / app_routes / app_redirects 子表），
// 名称与子域名自动推导为 <原值>-N 保证不重复，返回克隆后的新应用
func (h *AppsHandler) Duplicate(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的应用 ID")
		return
	}

	claims := getUserFromContext(r)
	app, err := h.db.GetApp(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	count, _ := h.db.CountApps(claims.UserID)
	if count >= server.MaxAppsPerUser {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("应用数量已达上限(%d)", server.MaxAppsPerUser))
		return
	}

	newApp, err := h.db.DuplicateApp(id)
	if err != nil {
		slog.ErrorContext(r.Context(), "克隆应用失败", "type", "api", "error", err, "app_id", id)
		jsonError(w, http.StatusInternalServerError, "克隆应用失败")
		return
	}

	// 通知相关客户端应用列表已变更（克隆沿用原客户端关联）
	h.hub.NotifyAppsChanged(app.ClientIDs)
	// 新子域名进入全局注册表，广播所有在线客户端刷新 (#57)
	h.hub.NotifyAppsChangedAll()

	slog.InfoContext(r.Context(), "应用克隆", "type", "api", "src_app_id", id, "new_app_id", newApp.ID, "user_id", claims.UserID)
	jsonOK(w, newApp)
}

// Delete 删除应用
func (h *AppsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的应用 ID")
		return
	}

	claims := getUserFromContext(r)
	app, err := h.db.GetApp(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	if err := h.db.DeleteApp(id); err != nil {
		slog.ErrorContext(r.Context(), "删除应用失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "删除应用失败")
		return
	}

	// 清理 appLastUsed 节流 map 中的残留记录，避免随 App 创建-删除历史无界增长
	h.proxy.ForgetAppLastUsed(id)

	// 通知相关客户端应用列表已变更
	h.hub.NotifyAppsChanged(app.ClientIDs)
	// 子域名退出全局注册表，广播所有在线客户端刷新 (#57)
	h.hub.NotifyAppsChangedAll()
	slog.InfoContext(r.Context(), "应用删除", "type", "api", "user_id", claims.UserID, "app_id", id)

	jsonMsg(w, "删除成功")
}

// validateClientConfigs 验证客户端列表属于当前用户，并验证代理 ID 属于对应客户端
func (h *AppsHandler) validateClientConfigs(userID int64, clientIDs []string, configs map[string]db.ClientFullConfig) error {
	for _, cid := range clientIDs {
		if cid == proxy.HostClientID {
			// __host__ 没有 proxy_id，跳过
			continue
		}
		client, err := h.db.GetClient(cid)
		if err != nil || client.UserID != userID {
			return fmt.Errorf("客户端 %s 不存在", cid)
		}
		// 验证代理 ID 属于该客户端
		cfg := configs[cid]
		if cfg.ProxyID != nil {
			proxy, err := h.db.GetProxy(*cfg.ProxyID)
			if err != nil {
				return fmt.Errorf("代理不存在")
			}
			if proxy.ClientID != cid {
				return fmt.Errorf("代理不属于该客户端")
			}
		}
	}
	return nil
}

// isSSOAuthMethod 判断是否为 SSO 类认证方式
func isSSOAuthMethod(method string) bool {
	switch method {
	case server.AuthMethodSSO, server.AuthMethodSSOToken, server.AuthMethodSSOOwner, server.AuthMethodSSOAll:
		return true
	}
	return false
}

// validateSecondFactor 校验应用二次验证方式的取值与前置条件 (#76)：
// 仅 SSO 类认证可开启；TOTP 要求用户已启用 TOTP，passkey 要求用户已注册通行密钥
func (h *AppsHandler) validateSecondFactor(userID int64, authMethod, secondFactor string) error {
	switch secondFactor {
	case server.SecondFactorNone:
		return nil
	case server.SecondFactorTOTP, server.SecondFactorPasskey:
	default:
		return errors.New("无效的二次验证方式")
	}
	if !isSSOAuthMethod(authMethod) {
		return errors.New("二次验证仅支持 SSO 类认证的应用")
	}
	switch secondFactor {
	case server.SecondFactorTOTP:
		user, err := h.db.GetUserByID(userID)
		if err != nil || user == nil || !user.TOTPEnabled {
			return errors.New("启用 TOTP 二次验证前请先在个人设置中开启 TOTP")
		}
	case server.SecondFactorPasskey:
		creds, err := h.db.ListWebAuthnCredentials(userID)
		if err != nil || len(creds) == 0 {
			return errors.New("启用通行密钥二次验证前请先在个人设置中注册通行密钥")
		}
	}
	return nil
}
