package tunnel

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/logstore"
	"github.com/robin/hop-proxy/pkg/proxycfg"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// ProxyHandler 处理来自客户端的转发请求（Path 5 relay）与本地代理访问上报（Path 3/4）
type ProxyHandler interface {
	HandleForwardedRequest(ctx context.Context, stream *yamux.Stream, fromClientID string)
	HandleForwardedWSOpen(ctx context.Context, stream *yamux.Stream, fromClientID string)
	ReportAppAccess(fromClientID string, appID, userID int64, method, path string)
}

// Handler 隧道 HTTP 处理器
type Handler struct {
	db           *db.DB
	hub          *Hub
	proxyHandler ProxyHandler
	logStore     *logstore.Store

	// 客户端时钟偏移跟踪：clientID → *offsetState
	// 用于客户端上报日志时的时间对齐
	offsets sync.Map
}

// offsetState 客户端时钟偏移状态（滑动平均）
type offsetState struct {
	mu     sync.Mutex
	offset int64 // 毫秒
	has    bool
}

// offsetAlpha 滑动平均系数（新观测权重）
// 0.3 平衡稳定性和响应速度：约 3 次观测收敛到新值
const offsetAlpha = 0.3

func NewHandler(database *db.DB, hub *Hub, proxyHandler ProxyHandler, logStore *logstore.Store) *Handler {
	return &Handler{db: database, hub: hub, proxyHandler: proxyHandler, logStore: logStore}
}

// ServeHTTP 处理 WebSocket 隧道连接请求 /ws/<client-uuid>
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	clientID := strings.TrimPrefix(r.URL.Path, "/ws/")
	if clientID == "" {
		http.Error(w, "缺少客户端 ID", http.StatusBadRequest)
		return
	}

	exists, err := h.db.ClientExists(clientID)
	if err != nil || !exists {
		http.Error(w, "客户端不存在", http.StatusUnauthorized)
		return
	}

	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		slog.Error("WebSocket 升级失败", "type", "tunnel", "error", err)
		return
	}

	conn, err := NewConn(clientID, ws, r.Context())
	if err != nil {
		slog.Error("创建 yamux session 失败", "type", "tunnel", "error", err)
		ws.Close(websocket.StatusInternalError, "yamux 创建失败")
		return
	}

	conn.SetStreamHandler(h.handleStream)
	h.hub.Register(clientID, conn)

	conn.Run()
	h.hub.Unregister(clientID, conn)
}

// handleStream 处理来自客户端的流
func (h *Handler) handleStream(ctx context.Context, stream *yamux.Stream, conn *Conn) {
	defer stream.Close()
	clientID := conn.ClientID()

	streamType, err := pkgTunnel.ReadStreamTypeWithTimeout(stream, pkgTunnel.StreamTypeReadTimeout)
	if err != nil {
		return
	}

	switch streamType {
	case pkgTunnel.StreamHTTP:
		// 原生 HTTP/1.1 请求由 HandleForwardedRequest 自行读取
		if h.proxyHandler != nil {
			h.proxyHandler.HandleForwardedRequest(ctx, stream, clientID)
		}

	case pkgTunnel.StreamWS:
		// 原生 HTTP/1.1 升级请求由 HandleForwardedWSOpen 自行读取
		if h.proxyHandler != nil {
			h.proxyHandler.HandleForwardedWSOpen(ctx, stream, clientID)
		}

	case pkgTunnel.StreamCtrl:
		msg, err := pkgTunnel.ReadCtrlMessage(stream)
		if err != nil {
			return
		}
		h.handleControl(ctx, stream, conn, msg)

	case pkgTunnel.StreamLog:
		h.handleLogBatch(stream, clientID)
	}
}

// handleControl 处理控制消息
func (h *Handler) handleControl(ctx context.Context, stream *yamux.Stream, conn *Conn, msg *pkgTunnel.CtrlMessage) {
	clientID := conn.ClientID()
	switch msg.Action {
	case pkgTunnel.ActionGetApps:
		h.handleGetApps(stream, clientID)
	case pkgTunnel.ActionVerifySession:
		h.handleVerifySession(stream, clientID, msg)
	case pkgTunnel.ActionProxyStatus:
		h.handleProxyStatus(stream, clientID, msg)
	case pkgTunnel.ActionConnDraining:
		// 客户端连接池淘汰该连接，标记 draining 让 ConnGroup.OpenStream 不再选它
		conn.SetDraining(true)
		slog.Info("连接标记为 draining", "type", "tunnel", "client_id", clientID)
	case pkgTunnel.ActionGetAgentKey:
		h.handleGetAgentKey(stream, clientID, msg)
	case pkgTunnel.ActionReportAppAccess:
		h.handleReportAppAccess(stream, clientID, msg)
	default:
		slog.Warn("未知控制消息", "type", "tunnel", "action", msg.Action, "client_id", clientID)
	}
}

// handleReportAppAccess 处理客户端本地代理访问上报（Path 3/4）。
// 由 proxy.Proxy 更新 last_used_at（节流 60s）并写审计日志，流无需响应。
func (h *Handler) handleReportAppAccess(stream *yamux.Stream, clientID string, msg *pkgTunnel.CtrlMessage) {
	if h.proxyHandler == nil {
		return
	}
	var info struct {
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	if len(msg.Body) > 0 {
		_ = json.Unmarshal(msg.Body, &info)
	}
	h.proxyHandler.ReportAppAccess(clientID, msg.AppID, msg.UserID, info.Method, info.Path)
}

// handleGetAgentKey 处理 client 请求 agent 私钥
// 权限校验：client 归属的 user 必须是 agent_key 的所有者
func (h *Handler) handleGetAgentKey(stream *yamux.Stream, clientID string, msg *pkgTunnel.CtrlMessage) {
	resp := &pkgTunnel.CtrlMessage{Action: pkgTunnel.ActionAgentKeyResp}
	defer pkgTunnel.WriteCtrlResponse(stream, resp)

	uuid := msg.AgentKeyUUID
	if uuid == "" {
		slog.Warn("get_agent_key 缺少 uuid", "type", "auth", "client_id", clientID)
		return
	}

	// 查 client 归属 user
	client, err := h.db.GetClient(clientID)
	if err != nil {
		slog.Warn("get_agent_key client 不存在", "type", "auth", "client_id", clientID, "error", err)
		return
	}

	// 查 agent_key
	agentKey, err := h.db.GetAgentKeyByUUID(uuid)
	if err != nil {
		slog.Warn("get_agent_key 密钥不存在", "type", "auth", "uuid", uuid, "client_id", clientID)
		return
	}

	// 权限校验：client 的 user 必须是 agent_key 的所有者
	if agentKey.UserID != client.UserID {
		slog.Warn("get_agent_key 权限拒绝", "type", "auth", "client_id", clientID, "client_user", client.UserID, "agent_key_user", agentKey.UserID)
		return
	}

	resp.Success = true
	resp.Body = []byte(agentKey.PrivateKey)
	slog.Debug("下发 agent 私钥", "type", "auth", "client_id", clientID, "uuid", uuid)
}

// handleGetApps 处理获取应用列表请求
func (h *Handler) handleGetApps(stream *yamux.Stream, clientID string) {
	apps, err := h.db.GetClientApps(clientID)
	if err != nil {
		slog.Error("获取客户端应用列表失败", "type", "tunnel", "client_id", clientID, "error", err)
		pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{Action: pkgTunnel.ActionAppsResponse, Reason: "获取应用列表失败"})
		return
	}

	proxyDomain, _ := h.db.GetSetting("proxy_domain")
	adminDomain, _ := h.db.GetSetting("admin_domain")

	// 代理配置下发"合并后的最终生效值"（#47）：客户端本地代理（Path 3/4）
	// 无 DB 访问，由服务端一次合并全局默认与应用级覆盖
	globalCfg, err := h.db.GetSetting("proxy_config")
	if err != nil {
		globalCfg = ""
	}
	for i := range apps {
		apps[i].ProxyConfig = proxycfg.Format(proxycfg.Merge(globalCfg, apps[i].ProxyConfig))
	}

	body, err := json.Marshal(apps)
	if err != nil {
		slog.Error("序列化应用列表失败", "type", "tunnel", "client_id", clientID, "error", err)
		pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{Action: pkgTunnel.ActionAppsResponse, Reason: "序列化失败"})
		return
	}

	// 全局子域名注册表 (#57)：客户端本地代理复刻服务端解析（全局精确优先 →
	// 全局模糊排序），防止本地模糊模式吞并其他客户端的精确子域名应用
	registry, err := h.db.GetAllAppSubdomains()
	if err != nil {
		slog.Error("获取全局子域名注册表失败", "type", "tunnel", "client_id", clientID, "error", err)
	}
	registryBody, _ := json.Marshal(registry)

	pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{
		Action:            pkgTunnel.ActionAppsResponse,
		ProxyDomain:       proxyDomain,
		AdminDomain:       adminDomain,
		Body:              body,
		SubdomainRegistry: registryBody,
	})
}

// handleVerifySession 处理 SSO Session 验证请求。
// 响应附带用户名（UserName），供客户端本地代理展开内置变量 ${user_name} (#50)。
func (h *Handler) handleVerifySession(stream *yamux.Stream, clientID string, msg *pkgTunnel.CtrlMessage) {
	appID := msg.AppID
	token := string(msg.Body)
	subdomain := msg.Subdomain

	if token == "" {
		pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{
			Action: pkgTunnel.ActionVerifyResponse, AppID: appID, Success: false, Reason: "缺少 token",
		})
		return
	}

	session, err := h.db.GetSSOSession(token, appID)
	if err == nil {
		// 校验 session 绑定的子域名与请求子域名一致，防止伪造 cookie name 跨子域串号 (#40)
		if subdomain != "" && session.Subdomain != subdomain {
			pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{
				Action: pkgTunnel.ActionVerifyResponse, AppID: appID, Success: false, Reason: "子域名不匹配",
			})
			return
		}
		// 该授权最近使用时间（#80）：本地代理（Path 3/4）的远程校验同样计入（60s 节流），
		// 使内网直连访问也能体现在会话/授权明细中
		h.db.TouchSSOSessionRequest(session.Token)
		pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{
			Action: pkgTunnel.ActionVerifyResponse, AppID: appID, UserID: session.UserID,
			UserName: h.userNameOf(session.UserID), Success: true,
		})
		return
	}

	ticket, err := h.db.GetAccessTokenByValue(token)
	if err == nil {
		if ticket.Verify(appID, clientID) {
			go h.db.TouchAccessToken(ticket.ID)
			pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{
				Action: pkgTunnel.ActionVerifyResponse, AppID: appID, UserID: ticket.UserID,
				UserName: h.userNameOf(ticket.UserID), Success: true,
			})
			return
		}
	}

	pkgTunnel.WriteCtrlResponse(stream, &pkgTunnel.CtrlMessage{
		Action: pkgTunnel.ActionVerifyResponse, AppID: appID, Success: false, Reason: "认证无效或已过期",
	})
}

// userNameOf 查询用户名（查不到返回空串，不影响验证结果本身）
func (h *Handler) userNameOf(userID int64) string {
	user, err := h.db.GetUserByID(userID)
	if err != nil {
		return ""
	}
	return user.Username
}

// handleProxyStatus 处理客户端上报的代理状态
func (h *Handler) handleProxyStatus(stream *yamux.Stream, clientID string, msg *pkgTunnel.CtrlMessage) {
	// 更新版本
	if msg.Version != "" {
		if conn, ok := h.hub.GetConn(clientID); ok {
			conn.SetVersion(msg.Version)
		}
	}

	// 获取旧 peer_secret
	oldSecret, _ := h.db.GetClientPeerSecret(clientID)

	if err := h.db.UpdateClientProxyStatus(clientID, msg.Enabled, msg.Listen, msg.PeerSecret); err != nil {
		slog.Error("更新客户端代理状态失败", "type", "tunnel", "client_id", clientID, "error", err)
	} else {
		slog.Debug("客户端代理状态已更新", "type", "tunnel", "client_id", clientID, "enabled", msg.Enabled, "listen", msg.Listen)
	}

	// peer_secret 变更通知
	if msg.PeerSecret != "" && msg.PeerSecret != oldSecret {
		clientIDs, err := h.db.GetPeerDependentClients(clientID)
		if err == nil && len(clientIDs) > 0 {
			h.hub.NotifyAppsChanged(clientIDs)
		}
	}

	// 无需响应，直接关闭流
}

// handleLogBatch 处理客户端批量日志上报。
//   - 估算客户端时钟偏移 offset = now - batch.ClientNow
//   - 用滑动平均更新该 client 的 offset（防止单次网络抖动）
//   - 整批查询一次客户端 name（source 字段）
//   - 对每条日志对齐时间后写入 logstore
func (h *Handler) handleLogBatch(stream *yamux.Stream, clientID string) {
	if h.logStore == nil {
		return
	}

	batch, err := pkgTunnel.ReadLogBatch(stream)
	if err != nil {
		slog.Warn("读取 LogBatch 失败", "type", "system", "client_id", clientID, "error", err)
		return
	}
	if len(batch.Logs) == 0 {
		return
	}

	// 时间对齐：估算 offset 并滑动平均
	serverNow := logstore.NowMs()
	observedOffset := serverNow - batch.ClientNow
	smoothedOffset := h.updateOffset(clientID, observedOffset)

	// 整批查一次客户端 name
	clientName := clientID
	if client, err := h.db.GetClient(clientID); err == nil && client.Name != "" {
		clientName = client.Name
	}

	// 逐条写入
	for _, entry := range batch.Logs {
		alignedTs := entry.Ts + smoothedOffset
		fields := make([]logstore.Field, 0, len(entry.Fields))
		var appID *int64
		for _, f := range entry.Fields {
			if f.K == "app_id" {
				if v, err := strconv.ParseInt(f.V, 10, 64); err == nil {
					appID = &v
				}
				// app_id 不放入 Fields（已有专门列）
				continue
			}
			fields = append(fields, logstore.Field{K: f.K, V: f.V})
		}
		rec := &logstore.LogRecord{
			Ts:        alignedTs,
			Level:     entry.Level,
			Source:    clientName,
			ClientID:  clientID,
			Type:      entry.Type,
			Subdomain: entry.Subdomain,
			RequestID: requestIDOf(entry.RequestID),
			UserID:    entry.UserID,
			AppID:     appID,
			Message:   entry.Message,
			Fields:    fields,
		}
		if err := h.logStore.Append(rec); err != nil {
			slog.Error("写入客户端日志失败", "type", "system", "client_id", clientID, "error", err)
			// 单条失败不中断整批
		}
	}
}

// requestIDOf 归一化客户端上报的 request_id：空值（旧版本客户端/请求无关日志）
// 统一填 "-"，保证每行日志都有该字段（#62）
func requestIDOf(id string) string {
	if id == "" {
		return "-"
	}
	return id
}

// updateOffset 用滑动平均更新该 client 的时钟偏移
// 返回平滑后的 offset（毫秒）
func (h *Handler) updateOffset(clientID string, observed int64) int64 {
	v, _ := h.offsets.LoadOrStore(clientID, &offsetState{})
	state := v.(*offsetState)

	state.mu.Lock()
	defer state.mu.Unlock()

	if !state.has {
		// 首次观测，直接采用
		state.offset = observed
		state.has = true
		return observed
	}

	// 滑动平均：offset = alpha * observed + (1 - alpha) * old
	state.offset = int64(float64(offsetAlpha)*float64(observed) + (1-offsetAlpha)*float64(state.offset))
	return state.offset
}
