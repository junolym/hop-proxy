package db

import "time"

// User 用户模型
type User struct {
	ID               int64     `json:"id"`
	Username         string    `json:"username"`
	PasswordHash     string    `json:"-"`
	IsAdmin          bool      `json:"is_admin"`
	Role             string    `json:"role"`               // admin / user / guest
	TOTPEnabled      bool      `json:"totp_enabled"`       // 是否启用 TOTP 二次验证
	TOTPSecret       string    `json:"-"`                  // TOTP 密钥（加密存储，不返回给前端）
	TempLoginEnabled bool      `json:"temp_login_enabled"` // 是否启用临时登录（pin + TOTP token）
	TempLoginPIN     string    `json:"-"`                  // 临时登录 PIN（不返回前端）
	QuickLogin       bool      `json:"quick_login"`        // 是否启用 SSO 快速登录（/sso 页面静默下发 cookie，免去确认）
	AutoDisableDays  int       `json:"auto_disable_days"`  // 自动禁用天数，0 表示禁用
	AppAPIEnabled    bool      `json:"app_api_enabled"`    // 是否启用应用 API（/external-api/）
	AppAPIToken      string    `json:"-"`                  // 应用 API 鉴权 token（仅在启用/重置时单独返回，不参与常规序列化）
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Setting 站点设置
type Setting struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Client 客户端模型
type Client struct {
	ID        string    `json:"id"` // UUID
	UserID    int64     `json:"user_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// 本地代理配置
	ProxyEnabled bool   `json:"proxy_enabled"` // 是否启用本地代理
	ProxyListen  string `json:"proxy_listen"`  // 监听地址，如 ":8080"

	// 非数据库字段，运行时填充
	Online   bool    `json:"online"`
	AppCount int     `json:"app_count"`
	Version  *string `json:"version,omitempty"` // 客户端构建版本

	ActiveStreams int `json:"active_streams"` // 活跃 yamux stream 数（a）
	ConnCount     int `json:"conn_count"`     // 当前 WS 连接数（b）
}

// App 应用模型
//
// 注意：新增 DB 字段时，应用克隆（DuplicateApp）通过 PRAGMA 动态枚举列整行复制，
// 普通配置字段会被自动克隆；若新字段"克隆时不应沿用"（如独占的外部资源引用），
// 必须在 DuplicateApp 的覆盖 UPDATE 中补充处理。
type App struct {
	ID              int64             `json:"id"`
	UserID          int64             `json:"user_id"`
	ClientID        *string           `json:"client_id"` // 兼容旧字段，保留第一个客户端 ID（deprecated，用 ClientIDs）
	Name            string            `json:"name"`
	Subdomain       string            `json:"subdomain"`
	TargetURL       string            `json:"target_url"`
	Enabled         bool              `json:"enabled"`
	RequireAuth     bool              `json:"require_auth"`           // 兼容旧字段，已废弃，用 AuthMethod 判断
	AuthMethod      string            `json:"auth_method"`            // none 或 sso
	AllowedUsers    string            `json:"allowed_users"`          // owner 或 all
	SSOCookieMaxAge int               `json:"sso_cookie_max_age"`     // SSO Cookie 过期时间（秒），315360000 表示永久（10 年），-1 表示会话（浏览器关闭即失效）
	SecondFactor    string            `json:"second_factor"`          // SSO 授权二次验证方式：空=无 / totp / passkey (#76)
	LoadBalance     bool              `json:"load_balance"`           // true=负载均衡，false=主备
	InactiveDays    *int              `json:"inactive_days"`          // 不活跃天数阈值（nil 或 0 表示不自动禁用）
	LastUsedAt      *time.Time        `json:"last_used_at"`           // 最近一次使用时间
	CustomHeaders   map[string]string `json:"custom_headers"`         // 自定义 HTTP Header
	HeaderMode      string            `json:"header_mode"`            // 请求头缺省处理模式：auto_xff（默认）/ auto_origin / none (#46)
	ProxyConfig     string            `json:"proxy_config"`           // 应用级代理配置覆盖（多行 key: value 文本，#47）
	ExemptPaths     []string          `json:"exempt_paths"`           // 豁免 SSO 认证的路径列表
	AgentKeyUUID    string            `json:"agent_key_uuid"`         // 安全代理密钥 UUID（空=不使用 agent）
	ProbeStatus     *AppProbeStatus   `json:"probe_status,omitempty"` // 应用可用性探测状态（最后一次探测结果）
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`

	// 非数据库字段，运行时填充
	ClientName   string      `json:"client_name,omitempty"`  // 第一个客户端名称（兼容）
	ClientOnline bool        `json:"client_online"`          // 是否有任意客户端在线
	ClientIDs    []string    `json:"client_ids"`             // 所有关联的客户端 ID（按 priority）
	ClientInfos  []AppClient `json:"client_infos,omitempty"` // 客户端详情（含在线状态）
}

// AppProbeStatus 应用可用性探测状态
type AppProbeStatus struct {
	Status     string    `json:"status"`                // available / error / failed / timeout / offline
	StatusCode int       `json:"status_code,omitempty"` // HTTP 状态码（available/error 时填充）
	Detail     string    `json:"detail,omitempty"`      // 失败原因（failed/timeout 时填充）
	CheckedAt  time.Time `json:"checked_at"`            // 探测时间
}

// 探测状态枚举
const (
	ProbeStatusAvailable = "available" // 2xx/3xx/4xx
	ProbeStatusError     = "error"     // 5xx
	ProbeStatusFailed    = "failed"    // 连接失败、端口不通
	ProbeStatusTimeout   = "timeout"   // 应用超时无响应
	ProbeStatusOffline   = "offline"   // 无可用客户端（与 client_online=false 等价）
)

// AppClient 应用关联的客户端信息
type AppClient struct {
	ClientID          string  `json:"client_id"`
	ClientName        string  `json:"client_name"`
	Priority          int     `json:"priority"`
	Online            bool    `json:"online"`
	TargetURL         *string `json:"target_url"`          // 客户端专属目标地址（可为空）
	ProxyID           *int64  `json:"proxy_id"`            // 代理 ID（可为空）
	ProxyName         *string `json:"proxy_name"`          // 代理名称（返回时填充）
	ProxyType         *string `json:"proxy_type"`          // 代理类型：socks5 / shadowsocks / peer / 空
	ProxyAddress      *string `json:"proxy_address"`       // 代理地址
	ProxyPassword     *string `json:"proxy_password"`      // 代理密码（返回时脱敏）
	ProxyTargetClient *string `json:"proxy_target_client"` // peer 类型的目标客户端 ID
}

// SSOSession SSO 会话模型（票据与应用绑定）
type SSOSession struct {
	Token         string    `json:"token"`
	UserID        int64     `json:"user_id"`
	AppID         int64     `json:"app_id"`
	Subdomain     string    `json:"subdomain"`       // 绑定的具体子域名（防通配符应用跨子域 cookie 串号，#40）
	AuthSessionID int64     `json:"auth_session_id"` // 归属登录会话（会话管理，#80；0=无归属）
	ExpiresAt     time.Time `json:"expires_at"`
	CreatedAt     time.Time `json:"created_at"`
}

// ClientProxy 客户端代理配置
type ClientProxy struct {
	ID             int64     `json:"id"`
	ClientID       string    `json:"client_id"`
	Name           string    `json:"name"`
	ProxyType      string    `json:"proxy_type"`       // socks5 / shadowsocks / peer
	ProxyAddress   string    `json:"proxy_address"`    // 代理地址
	ProxyPassword  string    `json:"proxy_password"`   // 代理密码（返回时脱敏）
	TargetClientID *string   `json:"target_client_id"` // 目标客户端 ID（仅 peer 类型）
	CreatedAt      time.Time `json:"created_at"`
}

// AccessToken 访问票据模型
type AccessToken struct {
	ID             int64      `json:"id"`
	UserID         int64      `json:"user_id"`
	Name           string     `json:"name"`
	Token          string     `json:"token"`           // 完整 token，列表中打码返回
	AllowedEntries string     `json:"allowed_entries"` // JSON: {"server":true,"clients":["uuid1"]}
	AllowedAppIDs  string     `json:"allowed_app_ids"` // JSON: [1,2,3]，空数组表示全部
	ExpiresAt      *time.Time `json:"expires_at"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// AllowedEntriesPayload 票据授权入口结构
type AllowedEntriesPayload struct {
	Server  bool     `json:"server"`
	Clients []string `json:"clients"`
}

// AgentKey 安全代理密钥（非对称密钥对，用于 caller↔agent Noise 握手）
// AgentKey 安全代理密钥模型
type AgentKey struct {
	ID         int64     `json:"id"`
	UserID     int64     `json:"user_id"`
	UUID       string    `json:"uuid"`
	Name       string    `json:"name"`
	PrivateKey string    `json:"-"` // hex，仅内部使用，不序列化到 API 响应
	PublicKey  string    `json:"-"` // hex，仅内部使用
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// WebAuthnCredential 通行密钥凭据（#71）
// 一个用户可绑定多台设备；CredentialID 为 base64url 编码的凭据 ID；
// 公钥为 COSE 格式。iCloud 同步型 passkey 的 signCount 恒 0，
// 克隆检测以 BackupEligible/BackupState 备份标志为准。
type WebAuthnCredential struct {
	ID              int64      `json:"id"`
	UserID          int64      `json:"-"`
	CredentialID    string     `json:"-"` // base64url，仅内部使用
	PublicKey       []byte     `json:"-"`
	AttestationType string     `json:"-"`
	Transport       string     `json:"-"`
	BackupEligible  bool       `json:"backup_eligible"`
	BackupState     bool       `json:"backup_state"`
	SignCount       uint32     `json:"-"`
	DeviceName      string     `json:"device_name"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

// AppRoute 应用路由规则（按路径和方法配置细化的认证方式和代理后端）。
// 克隆应用时（DuplicateApp → copyChildRows）整行复制，新增列自动被克隆；
// 若新增"克隆时不应沿用"的列，需在 copyChildRows 中补充处理。
type AppRoute struct {
	ID          int64     `json:"id"`
	AppID       int64     `json:"app_id"`
	ClientID    string    `json:"client_id"`    // 空字符串=任意客户端
	Method      string    `json:"method"`       // 空字符串=任意方法
	PathPattern string    `json:"path_pattern"` // 路径模式
	AuthMethod  string    `json:"auth_method"`  // 空=不覆盖
	TargetURL   string    `json:"target_url"`   // 空=不覆盖
	PathRewrite string    `json:"path_rewrite"` // 空=不改写；转发到后端的路径按前缀替换语义改写 (#53)
	Priority    int       `json:"priority"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AppRedirect 应用跳转路径规则（命中则返回 301/302，不代理）。
// 克隆应用时（DuplicateApp → copyChildRows）整行复制，新增列自动被克隆；
// 若新增"克隆时不应沿用"的列，需在 copyChildRows 中补充处理。
type AppRedirect struct {
	ID                int64     `json:"id"`
	AppID             int64     `json:"app_id"`
	MatchType         string    `json:"match_type"`          // exact | regex
	MatchPath         string    `json:"match_path"`          // 精确路径或正则
	MatchIncludeQuery bool      `json:"match_include_query"` // true 时匹配字符串含 query（path?query），用于避免同 path 不同 query 的死循环
	RedirectTarget    string    `json:"redirect_target"`     // /开头=站内跳转，http(s)://开头=站外；正则支持 $1 ${1}
	StatusCode        int       `json:"status_code"`         // 301 永久 | 302 临时
	Priority          int       `json:"priority"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ShareCode 分享码模型（临时访问票据，跳过 SSO 快速分享）
type ShareCode struct {
	ID                int64     `json:"id"`
	UserID            int64     `json:"user_id"`
	AppID             int64     `json:"app_id"`
	Code              string    `json:"code"`
	MaxUses           int       `json:"max_uses"`
	UseCount          int       `json:"use_count"`
	ExpiresAt         time.Time `json:"expires_at"`
	CookieTTL         int       `json:"cookie_ttl"`         // cookie 有效期（秒）
	RedirectPath      string    `json:"redirect_path"`      // 兑换后跳转的应用内路径，空表示 /
	ConcreteSubdomain string    `json:"concrete_subdomain"` // 模糊匹配应用的具体子域名（如模式 *-dev 命中 abc-dev 时存 abc-dev）；精确匹配应用为空
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	// 非数据库字段，列表查询时 JOIN 填充
	AppName   string `json:"app_name"`
	Subdomain string `json:"subdomain"`
}

// ClientAppInfo 客户端本地代理需要的应用信息
type ClientAppInfo struct {
	ID            int64             `json:"id"`
	Subdomain     string            `json:"subdomain"`
	Name          string            `json:"name"` // 应用名称，供本地代理展开内置变量 ${app_name} (#50)
	TargetURL     string            `json:"target_url"`
	RequireAuth   bool              `json:"require_auth"`
	AuthMethod    string            `json:"auth_method"`
	ExemptPaths   []string          `json:"exempt_paths"` // 豁免 SSO 认证的路径列表（#68，本地代理复刻服务端 isPathExempt）
	CustomHeaders map[string]string `json:"custom_headers"`
	HeaderMode    string            `json:"header_mode"`  // 请求头缺省处理模式：auto_xff（默认）/ auto_origin / none (#46)
	ProxyConfig   string            `json:"proxy_config"` // 代理配置最终生效值（服务端合并全局+应用级后下发，多行 key: value 文本，#47）

	// 代理配置（由应用关联的代理设置填充）
	ProxyType     string `json:"proxy_type,omitempty"`    // socks5 / shadowsocks / peer
	ProxyAddress  string `json:"proxy_address,omitempty"` // 代理地址
	ProxyPassword string `json:"proxy_password,omitempty"`
	ProxySecret   string `json:"proxy_secret,omitempty"` // peer 密钥（仅 peer 类型）

	// 安全代理密钥 UUID（非空时通过 agent 接入后端）
	AgentKeyUUID string `json:"agent_key_uuid,omitempty"`

	// 路由规则（按优先级排序，供本地代理匹配）
	Routes []AppRoute `json:"routes"`

	// 跳转路径规则（按优先级排序，供本地代理匹配）
	Redirects []AppRedirect `json:"redirects"`
}
