package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
)

// AccessTokensHandler 访问票据管理处理
type AccessTokensHandler struct {
	db *db.DB
}

func newAccessTokensHandler(database *db.DB) *AccessTokensHandler {
	return &AccessTokensHandler{db: database}
}

// List 列出当前用户的所有访问票据（token 打码）
func (h *AccessTokensHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	tokens, err := h.db.ListAccessTokens(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取访问票据列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取访问票据列表失败")
		return
	}
	jsonOK(w, tokens)
}

// accessTokenRequest 创建/更新请求体
type accessTokenRequest struct {
	Name           string `json:"name"`
	AllowedEntries struct {
		Server  bool     `json:"server"`
		Clients []string `json:"clients"`
	} `json:"allowed_entries"`
	AllowedAppIDs []int64 `json:"allowed_app_ids"`
	ExpiresInSecs *int    `json:"expires_in_secs"` // 相对秒数，nil 表示永不过期
}

// Create 创建访问票据，返回完整 token（仅一次）
func (h *AccessTokensHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)

	var req accessTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "备注名称不能为空")
		return
	}

	allowedEntries, err := json.Marshal(req.AllowedEntries)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "授权入口格式错误")
		return
	}

	if req.AllowedAppIDs == nil {
		req.AllowedAppIDs = []int64{}
	}
	allowedAppIDs, err := json.Marshal(req.AllowedAppIDs)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "授权应用格式错误")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresInSecs != nil && *req.ExpiresInSecs > 0 {
		t := time.Now().Add(time.Duration(*req.ExpiresInSecs) * time.Second)
		expiresAt = &t
	}

	token, err := h.db.CreateAccessToken(claims.UserID, req.Name,
		string(allowedEntries), string(allowedAppIDs), expiresAt)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建访问票据失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建访问票据失败")
		return
	}

	// 返回完整 token（仅此一次）
	slog.InfoContext(r.Context(), "访问票据创建", "type", "api", "user_id", claims.UserID, "token_id", token.ID, "name", token.Name)
	jsonOK(w, token)
}

// Update 更新访问票据
func (h *AccessTokensHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的票据 ID")
		return
	}

	claims := getUserFromContext(r)
	existing, err := h.db.GetAccessToken(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "访问票据不存在")
		return
	}
	if existing.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req accessTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "备注名称不能为空")
		return
	}

	allowedEntries, err := json.Marshal(req.AllowedEntries)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "授权入口格式错误")
		return
	}

	if req.AllowedAppIDs == nil {
		req.AllowedAppIDs = []int64{}
	}
	allowedAppIDs, err := json.Marshal(req.AllowedAppIDs)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "授权应用格式错误")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresInSecs != nil && *req.ExpiresInSecs > 0 {
		t := time.Now().Add(time.Duration(*req.ExpiresInSecs) * time.Second)
		expiresAt = &t
	}

	if err := h.db.UpdateAccessToken(id, req.Name,
		string(allowedEntries), string(allowedAppIDs), expiresAt); err != nil {
		slog.ErrorContext(r.Context(), "更新访问票据失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新访问票据失败")
		return
	}

	slog.InfoContext(r.Context(), "访问票据更新", "type", "api", "token_id", id)
	jsonMsg(w, "更新成功")
}

// Delete 删除访问票据
func (h *AccessTokensHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的票据 ID")
		return
	}

	claims := getUserFromContext(r)
	existing, err := h.db.GetAccessToken(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "访问票据不存在")
		return
	}
	if existing.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	if err := h.db.DeleteAccessToken(id); err != nil {
		slog.ErrorContext(r.Context(), "删除访问票据失败", "type", "api", "id", id, "error", err)
		jsonError(w, http.StatusInternalServerError, "删除访问票据失败")
		return
	}
	slog.InfoContext(r.Context(), "访问票据删除", "type", "api", "token_id", id)

	jsonMsg(w, "删除成功")
}
