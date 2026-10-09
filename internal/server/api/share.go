package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	subdomainpkg "github.com/robin/hop-proxy/pkg/subdomain"
)

// ShareHandler 分享码管理处理
type ShareHandler struct {
	db *db.DB
}

// NewShareHandler 创建分享码处理器
func NewShareHandler(database *db.DB) *ShareHandler {
	return &ShareHandler{db: database}
}

// List 列出当前用户的所有分享码
func (h *ShareHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	codes, err := h.db.ListShareCodes(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取分享码列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取分享码列表失败")
		return
	}
	jsonOK(w, codes)
}

// shareCodeRequest 创建/更新请求体
type shareCodeRequest struct {
	AppID             int64  `json:"app_id"`
	MaxUses           int    `json:"max_uses"`           // 跳转次数限制
	CookieTTL         int    `json:"cookie_ttl"`         // cookie 有效期（秒）
	ExpiresInSecs     int    `json:"expires_in_secs"`    // 分享码有效期（秒）
	RedirectPath      string `json:"redirect_path"`      // 兑换后跳转的应用内路径，空表示 /
	ConcreteSubdomain string `json:"concrete_subdomain"` // 模糊匹配应用的具体子域名（如 abc-dev 对应 *-dev），精确匹配应用必须为空
}

// validateRedirectPath 校验跳转路径：必须为空或以 / 开头，禁止 // 和反斜杠转义
func validateRedirectPath(p string) error {
	if p == "" {
		return nil
	}
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("跳转路径必须以 / 开头")
	}
	// 禁止 // 和 /\ 以避免协议相对 URL 或路径穿越
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return fmt.Errorf("跳转路径格式非法")
	}
	return nil
}

// Create 创建分享码
func (h *ShareHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)

	var req shareCodeRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	if req.AppID <= 0 {
		jsonError(w, http.StatusBadRequest, "请选择应用")
		return
	}

	// 查询应用
	app, err := h.db.GetApp(req.AppID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}

	// 权限检查：仅应用所有者或管理员可创建
	if app.UserID != claims.UserID && !claims.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	// 认证方式检查：分享码仅对 SSO 类应用有意义
	if app.AuthMethod != server.AuthMethodSSO && app.AuthMethod != server.AuthMethodSSOToken {
		jsonError(w, http.StatusBadRequest, "分享码仅支持 SSO 或 SSO+票据 认证方式的应用")
		return
	}

	// 参数默认值与校验
	if req.MaxUses <= 0 {
		req.MaxUses = 3
	}
	if req.CookieTTL <= 0 {
		req.CookieTTL = 604800 // 7 天
	}
	if req.ExpiresInSecs <= 0 {
		req.ExpiresInSecs = 86400 // 24 小时
	}
	if req.MaxUses > 1000 {
		jsonError(w, http.StatusBadRequest, "跳转次数上限不能超过 1000")
		return
	}
	if req.CookieTTL > 315360000 {
		jsonError(w, http.StatusBadRequest, "cookie 有效期不能超过 10 年")
		return
	}
	if err := validateRedirectPath(req.RedirectPath); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	// concrete_subdomain 校验：模糊匹配应用必须提供，且需匹配模式；精确匹配应用必须为空
	concreteSub := strings.ToLower(strings.TrimSpace(req.ConcreteSubdomain))
	if subdomainpkg.IsFuzzy(app.Subdomain) {
		if concreteSub == "" {
			jsonError(w, http.StatusBadRequest, "模糊匹配应用需指定具体子域名")
			return
		}
		pat, err := subdomainpkg.Parse(app.Subdomain)
		if err != nil {
			slog.ErrorContext(r.Context(), "分享码创建：应用子域名模式编译失败", "type", "api", "app_id", app.ID, "subdomain", app.Subdomain, "error", err)
			jsonError(w, http.StatusInternalServerError, "应用子域名模式无效")
			return
		}
		if pat.Match(concreteSub) == nil {
			jsonError(w, http.StatusBadRequest, fmt.Sprintf("具体子域名 %q 不匹配应用模式 %q", concreteSub, app.Subdomain))
			return
		}
	} else if concreteSub != "" {
		jsonError(w, http.StatusBadRequest, "精确匹配应用不支持指定具体子域名")
		return
	}

	expiresAt := time.Now().Add(time.Duration(req.ExpiresInSecs) * time.Second)

	code, err := h.db.CreateShareCode(claims.UserID, req.AppID, req.MaxUses, req.CookieTTL, expiresAt, req.RedirectPath, concreteSub)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建分享码失败", "type", "api", "error", err, "app_id", req.AppID)
		jsonError(w, http.StatusInternalServerError, "创建分享码失败")
		return
	}

	// 填充应用信息（前端创建后即可展示）
	code.AppName = app.Name
	code.Subdomain = app.Subdomain

	jsonOK(w, code)
}

// Update 更新分享码（max_uses, cookie_ttl, expires_at, enabled）
func (h *ShareHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的分享码 ID")
		return
	}

	claims := getUserFromContext(r)
	existing, err := h.db.GetShareCode(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "分享码不存在")
		return
	}
	if existing.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req struct {
		MaxUses       int     `json:"max_uses"`
		CookieTTL     int     `json:"cookie_ttl"`
		ExpiresInSecs *int    `json:"expires_in_secs"` // 相对秒数；nil 表示保持原过期时间
		RedirectPath  *string `json:"redirect_path"`   // nil 表示不修改；空串表示重置为 /
		Enabled       *bool   `json:"enabled"`         // nil 表示不修改
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	maxUses := existing.MaxUses
	if req.MaxUses > 0 {
		maxUses = req.MaxUses
	}
	if req.MaxUses > 1000 {
		jsonError(w, http.StatusBadRequest, "跳转次数上限不能超过 1000")
		return
	}

	cookieTTL := existing.CookieTTL
	if req.CookieTTL > 0 {
		cookieTTL = req.CookieTTL
	}
	if req.CookieTTL > 315360000 {
		jsonError(w, http.StatusBadRequest, "cookie 有效期不能超过 10 年")
		return
	}

	redirectPath := existing.RedirectPath
	if req.RedirectPath != nil {
		if err := validateRedirectPath(*req.RedirectPath); err != nil {
			jsonError(w, http.StatusBadRequest, err.Error())
			return
		}
		redirectPath = *req.RedirectPath
	}

	expiresAt := existing.ExpiresAt
	if req.ExpiresInSecs != nil && *req.ExpiresInSecs > 0 {
		expiresAt = time.Now().Add(time.Duration(*req.ExpiresInSecs) * time.Second)
	}

	enabled := existing.Enabled
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	if err := h.db.UpdateShareCode(id, maxUses, cookieTTL, expiresAt, redirectPath, enabled); err != nil {
		slog.ErrorContext(r.Context(), "更新分享码失败", "type", "api", "error", err, "id", id)
		jsonError(w, http.StatusInternalServerError, "更新分享码失败")
		return
	}

	jsonMsg(w, "更新成功")
}

// Delete 删除分享码
func (h *ShareHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的分享码 ID")
		return
	}

	claims := getUserFromContext(r)
	existing, err := h.db.GetShareCode(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "分享码不存在")
		return
	}
	if existing.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	if err := h.db.DeleteShareCode(id); err != nil {
		slog.ErrorContext(r.Context(), "删除分享码失败", "type", "api", "error", err, "id", id)
		jsonError(w, http.StatusInternalServerError, "删除分享码失败")
		return
	}

	jsonMsg(w, "删除成功")
}

// Redeem 分享码兑换端点（/s/{code}，无需登录）
// 流程：查码 → 已有 cookie 则跳转 → 禁用/过期/次数用尽统一报错 → 计数+发 cookie+跳转
//
// 安全考虑：所有无效原因（不存在/禁用/过期/用尽）返回统一错误页面，
// 不向外部区分具体原因，避免攻击者通过文案差异枚举有效分享码。
func (h *ShareHandler) Redeem(w http.ResponseWriter, r *http.Request) {
	// 从路径 /s/{code} 提取 code
	code := strings.TrimPrefix(r.URL.Path, "/s/")
	if code == "" || strings.Contains(code, "/") {
		renderShareError(w, r)
		return
	}

	// 1. 查找分享码
	sc, err := h.db.GetShareCodeByCode(code)
	if err != nil {
		slog.InfoContext(r.Context(), "分享码兑换：码不存在", "type", "api", "code", code)
		renderShareError(w, r)
		return
	}

	// 2. 查询关联应用（码存在但应用已删/已禁用也统一报错）
	app, err := h.db.GetApp(sc.AppID)
	if err != nil {
		slog.ErrorContext(r.Context(), "分享码兑换：关联应用不存在", "type", "api", "share_code_id", sc.ID, "app_id", sc.AppID)
		renderShareError(w, r)
		return
	}
	if !app.Enabled {
		slog.InfoContext(r.Context(), "分享码兑换：关联应用已禁用", "type", "api", "share_code_id", sc.ID, "app_id", app.ID)
		renderShareError(w, r)
		return
	}

	// 3. 获取 proxy_domain（未配置属于服务端问题，但对用户也统一报错）
	proxyDomain, _ := h.db.GetSetting("proxy_domain")
	if proxyDomain == "" {
		slog.ErrorContext(r.Context(), "分享码兑换：proxy_domain 未配置", "type", "api")
		renderShareError(w, r)
		return
	}
	// 跳转路径：分享码未设置则默认 /；否则使用设置值（DB 写入时已校验 / 开头）
	redirectPath := sc.RedirectPath
	if redirectPath == "" {
		redirectPath = "/"
	}
	// 子域名：模糊匹配应用使用创建时绑定的具体子域名；精确匹配应用直接用 app.Subdomain
	subdomainForRedirect := app.Subdomain
	if sc.ConcreteSubdomain != "" {
		subdomainForRedirect = sc.ConcreteSubdomain
	}
	redirectURL := fmt.Sprintf("https://%s.%s%s", subdomainForRedirect, proxyDomain, redirectPath)

	// 4. 检查是否已持有有效 SSO cookie → 直接跳转（不计次数）
	//    先于禁用/过期/用尽检查：已有合法 cookie 的用户始终放行，
	//    避免通过「有 cookie 时行为不同」反推分享码状态。
	//    cookie name 含具体子域名后缀 (#40)
	cookieName := db.SSOCookieName(app.ID, subdomainForRedirect)
	if cookie, err := r.Cookie(cookieName); err == nil {
		if session, err := h.db.GetSSOSession(cookie.Value, app.ID); err == nil && session.Subdomain == subdomainForRedirect {
			slog.InfoContext(r.Context(), "分享码兑换：已有有效 cookie，直接跳转", "type", "api",
				"share_code_id", sc.ID, "app_id", app.ID)
			http.Redirect(w, r, redirectURL, http.StatusFound)
			return
		}
	}

	// 5. 检查分享码状态：禁用/过期/次数用尽 → 统一错误页面
	if !sc.Enabled {
		slog.InfoContext(r.Context(), "分享码兑换：码已禁用", "type", "api", "share_code_id", sc.ID)
		renderShareError(w, r)
		return
	}
	if time.Now().After(sc.ExpiresAt) {
		slog.InfoContext(r.Context(), "分享码兑换：码已过期", "type", "api", "share_code_id", sc.ID, "expires_at", sc.ExpiresAt)
		renderShareError(w, r)
		return
	}
	if sc.UseCount >= sc.MaxUses {
		slog.InfoContext(r.Context(), "分享码兑换：跳转次数已用尽", "type", "api", "share_code_id", sc.ID,
			"use_count", sc.UseCount, "max_uses", sc.MaxUses)
		renderShareError(w, r)
		return
	}

	// 6. 递增使用次数
	if err := h.db.IncrementShareCodeUse(sc.ID); err != nil {
		slog.ErrorContext(r.Context(), "分享码兑换：递增使用次数失败", "type", "api", "error", err, "share_code_id", sc.ID)
		renderShareError(w, r)
		return
	}

	// 7. 创建 SSO session（使用分享码创建者的 user_id）
	//    subdomain 绑定到具体子域名 (#40)
	ttl := time.Duration(sc.CookieTTL) * time.Second
	// 来源会话（#80）：分享码兑换为一次性单应用会话，授权来源记录分享码本身（可在分享码页对上）
	sess, err := h.db.CreateAuthSession(sc.UserID, db.SessionSourceShareCode, sc.Code, clientIP(r), r.UserAgent(), ttl)
	if err != nil {
		slog.ErrorContext(r.Context(), "分享码兑换：创建会话记录失败", "type", "api", "error", err, "share_code_id", sc.ID)
		renderShareError(w, r)
		return
	}
	token, err := h.db.CreateSSOSession(sc.UserID, app.ID, subdomainForRedirect, ttl, sess.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "分享码兑换：创建 SSO session 失败", "type", "api", "error", err, "app_id", app.ID)
		renderShareError(w, r)
		return
	}

	// 8. 下发 SSO cookie
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Domain:   "." + proxyDomain,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	slog.InfoContext(r.Context(), "分享码兑换成功", "type", "api",
		"share_code_id", sc.ID, "app_id", app.ID,
		"use_count", sc.UseCount+1, "max_uses", sc.MaxUses)

	http.Redirect(w, r, redirectURL, http.StatusFound)
}

// renderShareError 通过 302 跳转到前端统一错误页面。
// 所有无效原因（不存在/禁用/过期/用尽）均跳转到同一页面，避免信息泄露。
func renderShareError(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/share-error", http.StatusFound)
}
