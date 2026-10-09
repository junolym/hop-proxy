package api

import (
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/tunnel"
)

// RedirectsHandler 应用跳转路径规则管理
type RedirectsHandler struct {
	db  *db.DB
	hub *tunnel.Hub
}

func newRedirectsHandler(database *db.DB, hub *tunnel.Hub) *RedirectsHandler {
	return &RedirectsHandler{db: database, hub: hub}
}

// List 列出应用的跳转规则
func (h *RedirectsHandler) List(w http.ResponseWriter, r *http.Request) {
	appID, ok := parseAppID(w, r)
	if !ok {
		return
	}
	if _, ok := h.checkAppOwner(w, r, appID); !ok {
		return
	}

	redirects, err := h.db.ListAppRedirects(appID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取跳转规则失败", "type", "api", "error", err, "app_id", appID)
		jsonError(w, http.StatusInternalServerError, "获取跳转规则失败: "+err.Error())
		return
	}
	jsonOK(w, redirects)
}

// Create 创建跳转规则
func (h *RedirectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	appID, ok := parseAppID(w, r)
	if !ok {
		return
	}
	app, ok := h.checkAppOwner(w, r, appID)
	if !ok {
		return
	}

	var req db.AppRedirect
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	req.AppID = appID

	if msg := h.validate(&req); msg != "" {
		jsonError(w, http.StatusBadRequest, msg)
		return
	}
	req.Enabled = true

	if err := h.db.CreateAppRedirect(&req); err != nil {
		slog.ErrorContext(r.Context(), "创建跳转规则失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建跳转规则失败")
		return
	}
	// 通知关联客户端刷新应用缓存：跳转规则随 get_apps 下发 (#68)
	h.hub.NotifyAppsChanged(app.ClientIDs)
	slog.InfoContext(r.Context(), "跳转规则创建", "type", "api", "app_id", appID, "redirect_id", req.ID)
	jsonOK(w, req)
}

// Update 更新跳转规则
func (h *RedirectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseRedirectID(w, r)
	if !ok {
		return
	}

	existing, err := h.db.GetAppRedirectByID(id, 0)
	if err != nil {
		jsonError(w, http.StatusNotFound, "规则不存在")
		return
	}
	app, ok := h.checkAppOwner(w, r, existing.AppID)
	if !ok {
		return
	}

	var req db.AppRedirect
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	req.ID = id
	req.AppID = existing.AppID

	if msg := h.validate(&req); msg != "" {
		jsonError(w, http.StatusBadRequest, msg)
		return
	}
	req.Enabled = true

	if err := h.db.UpdateAppRedirect(&req); err != nil {
		slog.ErrorContext(r.Context(), "更新跳转规则失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新跳转规则失败")
		return
	}
	// 通知关联客户端刷新应用缓存：跳转规则随 get_apps 下发 (#68)
	h.hub.NotifyAppsChanged(app.ClientIDs)
	slog.InfoContext(r.Context(), "跳转规则更新", "type", "api", "redirect_id", id)
	jsonMsg(w, "更新成功")
}

// Delete 删除跳转规则
func (h *RedirectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseRedirectID(w, r)
	if !ok {
		return
	}

	existing, err := h.db.GetAppRedirectByID(id, 0)
	if err != nil {
		jsonError(w, http.StatusNotFound, "规则不存在")
		return
	}
	app, ok := h.checkAppOwner(w, r, existing.AppID)
	if !ok {
		return
	}

	if err := h.db.DeleteAppRedirect(id, existing.AppID); err != nil {
		slog.ErrorContext(r.Context(), "删除跳转规则失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "删除跳转规则失败")
		return
	}
	// 通知关联客户端刷新应用缓存：跳转规则随 get_apps 下发 (#68)
	h.hub.NotifyAppsChanged(app.ClientIDs)
	slog.InfoContext(r.Context(), "跳转规则删除", "type", "api", "redirect_id", id)
	jsonMsg(w, "删除成功")
}

// validate 校验跳转规则字段，返回错误提示（空字符串表示通过）
func (h *RedirectsHandler) validate(r *db.AppRedirect) string {
	r.MatchType = strings.TrimSpace(r.MatchType)
	r.MatchPath = strings.TrimSpace(r.MatchPath)
	r.RedirectTarget = strings.TrimSpace(r.RedirectTarget)
	if r.MatchType == "" {
		r.MatchType = "exact"
	}
	if r.MatchType != "exact" && r.MatchType != "regex" {
		return "匹配类型必须为 exact 或 regex"
	}
	if r.MatchPath == "" {
		return "匹配路径不能为空"
	}
	if r.RedirectTarget == "" {
		return "跳转目标不能为空"
	}
	if r.StatusCode != 301 && r.StatusCode != 302 {
		r.StatusCode = 302
	}
	if r.MatchType == "regex" {
		if _, err := regexp.Compile(r.MatchPath); err != nil {
			return "正则表达式无效: " + err.Error()
		}
	}
	return ""
}

// checkAppOwner 验证应用归属当前用户（或管理员），通过时返回应用（含客户端列表，供变更通知）
func (h *RedirectsHandler) checkAppOwner(w http.ResponseWriter, r *http.Request, appID int64) (*db.App, bool) {
	claims := getUserFromContext(r)
	app, err := h.db.GetApp(appID)
	if err != nil {
		jsonError(w, http.StatusNotFound, "应用不存在")
		return nil, false
	}
	if app.UserID != claims.UserID && !claims.IsAdmin {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return nil, false
	}
	return app, true
}

// parseAppID 解析路径参数 {id} 为 appID
func parseAppID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	appID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的应用 ID")
		return 0, false
	}
	return appID, true
}

// parseRedirectID 解析路径参数 {rid} 为 redirect ID
func parseRedirectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("rid"), 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的规则 ID")
		return 0, false
	}
	return id, true
}
