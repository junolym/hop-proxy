package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/proxy"
	"github.com/robin/hop-proxy/internal/server/tunnel"
)

// ClientsHandler 客户端管理处理
type ClientsHandler struct {
	db  *db.DB
	hub *tunnel.Hub
}

func newClientsHandler(database *db.DB, hub *tunnel.Hub) *ClientsHandler {
	return &ClientsHandler{db: database, hub: hub}
}

// List 客户端列表
func (h *ClientsHandler) List(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	clients, err := h.db.ListClients(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取客户端列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取客户端列表失败")
		return
	}
	// 填充在线状态和版本信息
	for i := range clients {
		clients[i].Online = h.hub.IsOnline(clients[i].ID)
		if clients[i].Online {
			if ver := h.hub.GetClientVersion(clients[i].ID); ver != "" {
				clients[i].Version = &ver
			}
			streams, conns := h.hub.GetConnStats(clients[i].ID)
			clients[i].ActiveStreams = streams
			clients[i].ConnCount = conns
		}
	}
	if clients == nil {
		clients = []db.Client{}
	}

	// 管理员在列表首位插入虚拟 Host 客户端
	if claims.IsAdmin {
		hostAppCount, _ := h.db.CountClientApps(proxy.HostClientID)
		hostClient := db.Client{
			ID:        proxy.HostClientID,
			UserID:    claims.UserID,
			Name:      "Host",
			Online:    true,
			AppCount:  hostAppCount,
			CreatedAt: time.Time{},
			UpdatedAt: time.Time{},
		}
		clients = append([]db.Client{hostClient}, clients...)
	}

	jsonOK(w, clients)
}

// Create 添加客户端
func (h *ClientsHandler) Create(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)

	// 检查限制
	count, _ := h.db.CountClients(claims.UserID)
	if count >= server.MaxClientsPerUser {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("客户端数量已达上限(%d)", server.MaxClientsPerUser))
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "名称不能为空")
		return
	}

	id := uuid.New().String()
	client, err := h.db.CreateClient(id, claims.UserID, req.Name)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建客户端失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建客户端失败")
		return
	}

	slog.InfoContext(r.Context(), "客户端创建", "type", "api", "user_id", claims.UserID, "client_id", client.ID, "name", client.Name)
	jsonOK(w, client)
}

// Update 编辑客户端
func (h *ClientsHandler) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "缺少客户端 ID")
		return
	}

	// 验证权限
	claims := getUserFromContext(r)
	client, err := h.db.GetClient(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "客户端不存在")
		return
	}
	if client.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api", "user_id", claims.UserID)
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}
	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "名称不能为空")
		return
	}

	if err := h.db.UpdateClient(id, req.Name); err != nil {
		slog.ErrorContext(r.Context(), "更新客户端失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新客户端失败")
		return
	}

	slog.InfoContext(r.Context(), "客户端更新", "type", "api", "user_id", claims.UserID, "client_id", id)
	jsonMsg(w, "更新成功")
}

// Delete 删除客户端
func (h *ClientsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "缺少客户端 ID")
		return
	}

	// 验证权限
	claims := getUserFromContext(r)
	client, err := h.db.GetClient(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "客户端不存在")
		return
	}
	if client.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api", "user_id", claims.UserID)
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	// 检查是否有关联的应用
	hasApps, _ := h.db.ClientHasApps(id)
	if hasApps {
		jsonError(w, http.StatusBadRequest, "该客户端已被应用关联，请先解除关联后再删除")
		return
	}

	// 如果在线，先断开
	h.hub.Kick(id, "客户端已删除")

	if err := h.db.DeleteClient(id); err != nil {
		slog.ErrorContext(r.Context(), "删除客户端失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "删除客户端失败")
		return
	}
	slog.InfoContext(r.Context(), "客户端删除", "type", "api", "user_id", claims.UserID, "client_id", id)

	jsonMsg(w, "删除成功")
}

// Ping 测量客户端延迟（毫秒），返回 latency_ms 字段
func (h *ClientsHandler) Ping(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "缺少客户端 ID")
		return
	}

	// 验证权限
	claims := getUserFromContext(r)
	client, err := h.db.GetClient(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "客户端不存在")
		return
	}
	if client.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api", "user_id", claims.UserID)
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}

	latency := h.hub.GetLatency(id)
	jsonOK(w, map[string]int64{"latency_ms": latency})
}

// Conns 返回客户端所有隧道连接的详情
func (h *ClientsHandler) Conns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		jsonError(w, http.StatusBadRequest, "缺少客户端 ID")
		return
	}
	// 验证权限
	claims := getUserFromContext(r)
	client, err := h.db.GetClient(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "客户端不存在")
		return
	}
	if client.UserID != claims.UserID {
		slog.WarnContext(r.Context(), "权限拒绝", "type", "api", "user_id", claims.UserID)
		jsonError(w, http.StatusForbidden, "无权操作")
		return
	}
	details := h.hub.GetConnDetails(id)
	if details == nil {
		details = []tunnel.ConnInfo{}
	}
	jsonOK(w, details)
}

// AvailablePeers 获取同用户下可用的 peer 客户端列表
func (h *ClientsHandler) AvailablePeers(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	clients, err := h.db.GetAvailablePeerClients(claims.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取可用客户端列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取可用客户端列表失败")
		return
	}
	if clients == nil {
		clients = []db.Client{}
	}
	jsonOK(w, clients)
}
