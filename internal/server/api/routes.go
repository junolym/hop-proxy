package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/tunnel"
)

// RoutesHandler 路由规则管理处理
type RoutesHandler struct {
	db  *db.DB
	hub *tunnel.Hub
}

func newRoutesHandler(database *db.DB, hub *tunnel.Hub) *RoutesHandler {
	return &RoutesHandler{db: database, hub: hub}
}

// List 列出应用的路由规则
func (h *RoutesHandler) List(w http.ResponseWriter, r *http.Request) {
	appIDStr := r.PathValue("id")
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的应用 ID")
		return
	}

	claims := getUserFromContext(r)
	app, err := h.db.GetApp(appID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID && !claims.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	routes, err := h.db.ListAppRoutes(appID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取路由规则失败", "type", "api", "error", err, "app_id", appID)
		jsonError(w, http.StatusInternalServerError, "获取路由规则失败: "+err.Error())
		return
	}
	jsonOK(w, routes)
}

// Create 创建路由规则
func (h *RoutesHandler) Create(w http.ResponseWriter, r *http.Request) {
	appIDStr := r.PathValue("id")
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的应用 ID")
		return
	}

	claims := getUserFromContext(r)
	app, err := h.db.GetApp(appID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID && !claims.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req db.AppRoute
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	req.AppID = appID

	// 校验：三个匹配条件不能同时为空或通配
	if !h.hasValidMatchConditions(req.ClientID, req.Method, req.PathPattern) {
		jsonError(w, http.StatusBadRequest, "来源客户端、请求方法、路径模式三者不能同时为「任意」")
		return
	}

	// 校验 auth_method 值
	if req.AuthMethod != "" &&
		req.AuthMethod != "none" &&
		req.AuthMethod != "sso" &&
		req.AuthMethod != "sso_token" &&
		req.AuthMethod != "token" {
		jsonError(w, http.StatusBadRequest, "无效的认证方式，可选：不覆盖 / none / sso / sso_token / token")
		return
	}

	// 校验路径改写 (#53)：非空时必须以 / 开头
	req.PathRewrite = strings.TrimSpace(req.PathRewrite)
	if req.PathRewrite != "" && !strings.HasPrefix(req.PathRewrite, "/") {
		jsonError(w, http.StatusBadRequest, "路径改写必须以 / 开头")
		return
	}

	req.ClientID = strings.TrimSpace(req.ClientID)
	req.Method = strings.TrimSpace(req.Method)
	req.PathPattern = strings.TrimSpace(req.PathPattern)
	req.Enabled = true // 路由规则默认启用

	if err := h.db.CreateAppRoute(&req); err != nil {
		slog.ErrorContext(r.Context(), "创建路由规则失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建路由规则失败")
		return
	}
	// 通知关联客户端刷新应用缓存：路由规则随 get_apps 下发 (#68)
	h.hub.NotifyAppsChanged(app.ClientIDs)
	slog.InfoContext(r.Context(), "路由规则创建", "type", "api", "app_id", appID, "route_id", req.ID)
	jsonOK(w, req)
}

// Update 更新路由规则
func (h *RoutesHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("rid")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的规则 ID")
		return
	}

	// 先查出规则所属的 app_id，用于权限验证
	existing, err := h.db.GetAppRouteByID(id, 0)
	if err != nil {
		jsonError(w, http.StatusNotFound, "规则不存在")
		return
	}

	claims := getUserFromContext(r)
	app, err2 := h.db.GetApp(existing.AppID)
	if err2 != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID && !claims.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req db.AppRoute
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	req.ID = id
	req.AppID = existing.AppID

	// 校验：三个匹配条件不能同时为空或通配
	if !h.hasValidMatchConditions(req.ClientID, req.Method, req.PathPattern) {
		jsonError(w, http.StatusBadRequest, "来源客户端、请求方法、路径模式三者不能同时为「任意」")
		return
	}

	// 校验 auth_method 值
	if req.AuthMethod != "" &&
		req.AuthMethod != "none" &&
		req.AuthMethod != "sso" &&
		req.AuthMethod != "sso_token" &&
		req.AuthMethod != "token" {
		jsonError(w, http.StatusBadRequest, "无效的认证方式，可选：不覆盖 / none / sso / sso_token / token")
		return
	}

	// 校验路径改写 (#53)：非空时必须以 / 开头
	req.PathRewrite = strings.TrimSpace(req.PathRewrite)
	if req.PathRewrite != "" && !strings.HasPrefix(req.PathRewrite, "/") {
		jsonError(w, http.StatusBadRequest, "路径改写必须以 / 开头")
		return
	}

	req.ClientID = strings.TrimSpace(req.ClientID)
	req.Method = strings.TrimSpace(req.Method)
	req.PathPattern = strings.TrimSpace(req.PathPattern)
	req.Enabled = true // 路由规则始终启用

	if err := h.db.UpdateAppRoute(&req); err != nil {
		slog.ErrorContext(r.Context(), "更新路由规则失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新路由规则失败")
		return
	}
	// 通知关联客户端刷新应用缓存：路由规则随 get_apps 下发 (#68)
	h.hub.NotifyAppsChanged(app.ClientIDs)
	slog.InfoContext(r.Context(), "路由规则更新", "type", "api", "route_id", id)
	jsonMsg(w, "更新成功")
}

// Delete 删除路由规则
func (h *RoutesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("rid")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的规则 ID")
		return
	}

	// 先查出规则所属的 app_id，用于权限验证
	existing, err := h.db.GetAppRouteByID(id, 0)
	if err != nil {
		jsonError(w, http.StatusNotFound, "规则不存在")
		return
	}

	claims := getUserFromContext(r)
	app, err2 := h.db.GetApp(existing.AppID)
	if err2 != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return
	}
	if app.UserID != claims.UserID && !claims.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	if err := h.db.DeleteAppRoute(id, existing.AppID); err != nil {
		slog.ErrorContext(r.Context(), "删除路由规则失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "删除路由规则失败")
		return
	}
	// 通知关联客户端刷新应用缓存：路由规则随 get_apps 下发 (#68)
	h.hub.NotifyAppsChanged(app.ClientIDs)
	slog.InfoContext(r.Context(), "路由规则删除", "type", "api", "route_id", id)
	jsonMsg(w, "删除成功")
}

// hasValidMatchConditions 检查三个匹配条件是否有效（不能全部为空/通配）
func (h *RoutesHandler) hasValidMatchConditions(clientID, method, pathPattern string) bool {
	clientWildcard := clientID == "" || clientID == "*"
	methodWildcard := method == "" || method == "*"
	pathWildcard := pathPattern == ""
	// 三者全为通配则无效
	if clientWildcard && methodWildcard && pathWildcard {
		return false
	}
	return true
}
