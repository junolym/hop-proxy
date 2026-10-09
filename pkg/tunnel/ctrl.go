package tunnel

import (
	"io"
)

// CtrlMessage 控制消息（gob 编码，通过 StreamCtrl 流传输）。
// 流格式: [1B StreamType=0x03][WriteGob CtrlMessage]
// 如需响应，在同一个流上写入 [WriteGob CtrlMessage] 作为回复，然后关闭流。
type CtrlMessage struct {
	Action string // 见 Action* 常量
	// VerifySession / VerifyResponse
	AppID   int64
	UserID  int64
	Success bool
	// UserName 用于 VerifyResponse：鉴权用户名，供客户端本地代理展开
	// 内置变量 ${user_name} (#50)
	UserName string
	// Subdomain 用于 VerifySession：客户端发起 SSO session 远程验证时
	// 传请求 host 提取的具体子域名，server 比对 session.subdomain 防止伪造 cookie name (#40)
	Subdomain string
	// Kick
	Reason string
	// AppsResponse / ProxyDomain
	ProxyDomain string
	AdminDomain string
	// 通用 payload（apps 列表 JSON / proxy_status 信息 / verify token / agent 私钥 hex）
	Body []byte
	// AppsResponse：全局子域名注册表 JSON（[]db.AppSubdomainRef，id+subdomain），
	// 供客户端本地代理复刻服务端解析（全局精确优先 → 全局模糊排序），
	// 防止本地模糊模式吞并其他客户端的精确子域名应用 (#57)
	SubdomainRegistry []byte
	// ProxyStatus
	Enabled    bool
	Listen     string
	PeerSecret string
	Version    string
	// GetAgentKey / AgentKeyResponse
	AgentKeyUUID string
}

// 控制消息 Action 常量
const (
	ActionGetApps         = "get_apps"
	ActionAppsResponse    = "apps_response"
	ActionAppsChanged     = "apps_changed"
	ActionVerifySession   = "verify_session"
	ActionVerifyResponse  = "verify_response"
	ActionProxyStatus     = "proxy_status"
	ActionKick            = "kick"
	ActionConnDraining    = "conn_draining"     // 客户端连接 drain 通知（连接池淘汰旧连接时发送，服务端 OpenStream 跳过该连接）
	ActionGetAgentKey     = "get_agent_key"     // client 请求 agent 私钥（Body=uuid）
	ActionAgentKeyResp    = "agent_key_resp"    // server 返回 agent 私钥（Body=hex，Success=false 表示无权限或不存在）
	ActionReportAppAccess = "report_app_access" // client 上报本地代理访问（Path 3/4），server 更新 last_used_at 并审计（Body=JSON {method,path}）
)

// WriteCtrlRequest 在流上写入控制消息请求。
// 调用后应 CloseWrite()（如需等待响应）或 Close()（如无需响应）。
func WriteCtrlRequest(stream io.Writer, msg *CtrlMessage) error {
	if err := WriteStreamType(stream, StreamCtrl); err != nil {
		return err
	}
	return WriteGob(stream, msg)
}

// WriteCtrlResponse 在流上写入控制消息响应（不写 StreamType，因为已经在请求时写了）。
func WriteCtrlResponse(stream io.Writer, msg *CtrlMessage) error {
	return WriteGob(stream, msg)
}

// ReadCtrlMessage 从流读取控制消息。
func ReadCtrlMessage(r io.Reader) (*CtrlMessage, error) {
	var msg CtrlMessage
	if err := ReadGob(r, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
