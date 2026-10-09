package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/proxy"
)

// ProxiesHandler 代理管理处理
type ProxiesHandler struct {
	db    *db.DB
	proxy *proxy.Proxy
}

func newProxiesHandler(database *db.DB, p *proxy.Proxy) *ProxiesHandler {
	return &ProxiesHandler{db: database, proxy: p}
}

// List 获取客户端的代理列表
func (h *ProxiesHandler) List(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("id")
	if clientID == "" {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	// 验证客户端归属
	claims := getUserFromContext(r)
	if clientID == proxy.HostClientID {
		if !claims.IsAdmin {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	} else {
		client, err := h.db.GetClient(clientID)
		if err != nil {
			jsonError(w, http.StatusNotFound, "客户端不存在")
			return
		}
		if client.UserID != claims.UserID {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	}

	proxies, err := h.db.ListProxies(clientID)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取代理列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取代理列表失败")
		return
	}
	if proxies == nil {
		proxies = []db.ClientProxy{}
	}
	jsonOK(w, proxies)
}

// Create 创建代理
func (h *ProxiesHandler) Create(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("id")
	if clientID == "" {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	// 验证客户端归属
	claims := getUserFromContext(r)
	if clientID == proxy.HostClientID {
		if !claims.IsAdmin {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	} else {
		client, err := h.db.GetClient(clientID)
		if err != nil {
			jsonError(w, http.StatusNotFound, "客户端不存在")
			return
		}
		if client.UserID != claims.UserID {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	}

	var req struct {
		Name           string `json:"name"`
		ProxyType      string `json:"proxy_type"`
		ProxyAddress   string `json:"proxy_address"`
		ProxyPassword  string `json:"proxy_password"`
		TargetClientID string `json:"target_client_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.ProxyType = strings.TrimSpace(req.ProxyType)
	req.ProxyAddress = strings.TrimSpace(req.ProxyAddress)
	req.TargetClientID = strings.TrimSpace(req.TargetClientID)

	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "代理名称不能为空")
		return
	}
	if req.ProxyType == "" {
		jsonError(w, http.StatusBadRequest, "代理类型不能为空")
		return
	}

	// 验证代理类型
	switch req.ProxyType {
	case "socks5", "shadowsocks":
		if req.ProxyAddress == "" {
			jsonError(w, http.StatusBadRequest, "代理地址不能为空")
			return
		}
	case "peer":
		if req.TargetClientID == "" {
			jsonError(w, http.StatusBadRequest, "请选择目标客户端")
			return
		}
		// 验证目标客户端属于同一用户且开启了本地代理
		targetClient, err := h.db.GetClient(req.TargetClientID)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "目标客户端不存在")
			return
		}
		if targetClient.UserID != claims.UserID {
			jsonError(w, http.StatusBadRequest, "目标客户端不属于当前用户")
			return
		}
		if !targetClient.ProxyEnabled || targetClient.ProxyListen == "" {
			jsonError(w, http.StatusBadRequest, "目标客户端未启用本地代理")
			return
		}
		if req.TargetClientID == clientID {
			jsonError(w, http.StatusBadRequest, "不能选择自身作为目标客户端")
			return
		}
	default:
		jsonError(w, http.StatusBadRequest, "不支持的代理类型")
		return
	}

	proxyRec, err := h.db.CreateProxy(clientID, req.Name, req.ProxyType, req.ProxyAddress, req.ProxyPassword, req.TargetClientID)
	if err != nil {
		slog.ErrorContext(r.Context(), "创建代理失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "创建代理失败")
		return
	}

	slog.InfoContext(r.Context(), "客户端代理创建", "type", "api", "client_id", clientID, "proxy_id", proxyRec.ID, "name", proxyRec.Name)
	jsonOK(w, proxyRec)
}

// Update 更新代理
func (h *ProxiesHandler) Update(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的代理 ID")
		return
	}

	// 获取代理并验证归属
	proxyRec, err := h.db.GetProxy(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "代理不存在")
		return
	}

	claims := getUserFromContext(r)
	if proxyRec.ClientID == proxy.HostClientID {
		if !claims.IsAdmin {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	} else {
		client, err := h.db.GetClient(proxyRec.ClientID)
		if err != nil || client.UserID != claims.UserID {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	}

	var req struct {
		Name           string `json:"name"`
		ProxyType      string `json:"proxy_type"`
		ProxyAddress   string `json:"proxy_address"`
		ProxyPassword  string `json:"proxy_password"`
		TargetClientID string `json:"target_client_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.ProxyType = strings.TrimSpace(req.ProxyType)
	req.ProxyAddress = strings.TrimSpace(req.ProxyAddress)
	req.TargetClientID = strings.TrimSpace(req.TargetClientID)

	if req.Name == "" {
		jsonError(w, http.StatusBadRequest, "代理名称不能为空")
		return
	}
	if req.ProxyType == "" {
		jsonError(w, http.StatusBadRequest, "代理类型不能为空")
		return
	}

	// 验证代理类型
	switch req.ProxyType {
	case "socks5", "shadowsocks":
		if req.ProxyAddress == "" {
			jsonError(w, http.StatusBadRequest, "代理地址不能为空")
			return
		}
	case "peer":
		if req.TargetClientID == "" {
			jsonError(w, http.StatusBadRequest, "请选择目标客户端")
			return
		}
		// 验证目标客户端属于同一用户且开启了本地代理
		targetClient, err := h.db.GetClient(req.TargetClientID)
		if err != nil {
			jsonError(w, http.StatusBadRequest, "目标客户端不存在")
			return
		}
		if targetClient.UserID != claims.UserID {
			jsonError(w, http.StatusBadRequest, "目标客户端不属于当前用户")
			return
		}
		if !targetClient.ProxyEnabled || targetClient.ProxyListen == "" {
			jsonError(w, http.StatusBadRequest, "目标客户端未启用本地代理")
			return
		}
		if req.TargetClientID == proxyRec.ClientID {
			jsonError(w, http.StatusBadRequest, "不能选择自身作为目标客户端")
			return
		}
	default:
		jsonError(w, http.StatusBadRequest, "不支持的代理类型")
		return
	}

	if err := h.db.UpdateProxy(id, req.Name, req.ProxyType, req.ProxyAddress, req.ProxyPassword, req.TargetClientID); err != nil {
		slog.ErrorContext(r.Context(), "更新代理失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "更新代理失败")
		return
	}

	// 代理配置变更后，主动淘汰旧出口 Transport 缓存，避免残留连接池引用旧凭据/地址
	// peer 与 SOCKS5/SS 缓存按 proxyAddress 前缀匹配淘汰，覆盖所有 SNI 变体
	if proxyRec.ProxyType == "peer" && proxyRec.ProxyAddress != "" {
		h.proxy.EvictPeerTransport(proxyRec.ProxyAddress)
	} else if (proxyRec.ProxyType == "socks5" || proxyRec.ProxyType == "shadowsocks") && proxyRec.ProxyAddress != "" {
		h.proxy.EvictSocksTransport(proxyRec.ProxyAddress)
	}

	slog.InfoContext(r.Context(), "客户端代理更新", "type", "api", "proxy_id", id)
	jsonMsg(w, "更新成功")
}

// Delete 删除代理
func (h *ProxiesHandler) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "无效的代理 ID")
		return
	}

	// 获取代理并验证归属
	proxyRec, err := h.db.GetProxy(id)
	if err != nil {
		jsonError(w, http.StatusNotFound, "代理不存在")
		return
	}

	claims := getUserFromContext(r)
	if proxyRec.ClientID == proxy.HostClientID {
		if !claims.IsAdmin {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	} else {
		client, err := h.db.GetClient(proxyRec.ClientID)
		if err != nil || client.UserID != claims.UserID {
			slog.WarnContext(r.Context(), "权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "无权操作")
			return
		}
	}

	// 删除代理（会检查是否有关联应用）
	if err := h.db.DeleteProxy(id); err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 代理删除后，主动淘汰出口 Transport 缓存，释放可能残留的连接池
	if proxyRec.ProxyType == "peer" && proxyRec.ProxyAddress != "" {
		h.proxy.EvictPeerTransport(proxyRec.ProxyAddress)
	} else if (proxyRec.ProxyType == "socks5" || proxyRec.ProxyType == "shadowsocks") && proxyRec.ProxyAddress != "" {
		h.proxy.EvictSocksTransport(proxyRec.ProxyAddress)
	}
	slog.InfoContext(r.Context(), "客户端代理删除", "type", "api", "proxy_id", id)

	jsonMsg(w, "删除成功")
}
