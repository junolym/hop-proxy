package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
)

// AgentKeysHandler 安全代理密钥管理处理
type AgentKeysHandler struct {
	db *db.DB
}

func newAgentKeysHandler(database *db.DB) *AgentKeysHandler {
	return &AgentKeysHandler{db: database}
}

// List 列出当前用户的所有安全代理密钥（私钥打码）
func (h *AgentKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	keys, err := h.db.ListAgentKeys(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取安全代理密钥列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取安全代理密钥列表失败")
		return
	}
	jsonOK(w, keys)
}

// agentKeyRequest 创建/更新请求体
type agentKeyRequest struct {
	Name string `json:"name"`
}

// Create 创建安全代理密钥，返回完整私钥（仅此一次）
func (h *AgentKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)

	var req agentKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "名称不能为空")
		return
	}

	key, err := h.db.CreateAgentKey(claims.UserID, req.Name)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建安全代理密钥失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建安全代理密钥失败")
		return
	}

	// model 的 PrivateKey/PublicKey 标记为 json:"-"，不会序列化到响应
	slog.InfoContext(r.Context(), "代理密钥创建", "type", "api", "user_id", claims.UserID, "uuid", key.UUID, "name", key.Name)
	jsonOK(w, key)
}

// Update 更新安全代理密钥名称
func (h *AgentKeysHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的密钥 ID")
		return
	}

	claims := getUserFromContext(r)
	existing, err := h.db.GetAgentKey(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "安全代理密钥不存在")
		return
	}
	if existing.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req agentKeyRequest
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "名称不能为空")
		return
	}

	if err := h.db.UpdateAgentKey(id, req.Name); err != nil {
		slog.ErrorContext(r.Context(), "更新安全代理密钥失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新安全代理密钥失败")
		return
	}

	slog.InfoContext(r.Context(), "代理密钥更新", "type", "api", "key_id", id)
	jsonMsg(w, "更新成功")
}

// Delete 删除安全代理密钥
func (h *AgentKeysHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的密钥 ID")
		return
	}

	claims := getUserFromContext(r)
	existing, err := h.db.GetAgentKey(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "安全代理密钥不存在")
		return
	}
	if existing.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	if err := h.db.DeleteAgentKey(id); err != nil {
		slog.ErrorContext(r.Context(), "删除安全代理密钥失败", "type", "api", "id", id, "error", err)
		jsonError(w, http.StatusInternalServerError, "删除安全代理密钥失败")
		return
	}
	slog.InfoContext(r.Context(), "代理密钥删除", "type", "api", "key_id", id)

	jsonMsg(w, "删除成功")
}

// GetPublicKey 公开端点：根据 UUID 返回 agent 公钥（无需登录）
func (h *AgentKeysHandler) GetPublicKey(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	if uuid == "" {
		jsonError(w, http.StatusBadRequest, "缺少 UUID")
		return
	}

	pubKey, err := h.db.GetAgentKeyPublicKey(uuid)
	if err != nil {
		jsonError(w, http.StatusNotFound, "密钥不存在")
		return
	}

	jsonOK(w, map[string]string{"uuid": uuid, "public_key": pubKey})
}
