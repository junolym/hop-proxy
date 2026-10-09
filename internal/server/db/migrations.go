package db

// 数据库迁移 SQL，按版本号排列
var migrations = []string{
	// 版本 1：初始表结构
	`CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		is_admin BOOLEAN NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS clients (
		id TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id),
		name TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE TABLE IF NOT EXISTS apps (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id),
		client_id TEXT REFERENCES clients(id) ON DELETE SET NULL,
		name TEXT NOT NULL,
		subdomain TEXT UNIQUE NOT NULL,
		target_url TEXT NOT NULL,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_apps_subdomain ON apps(subdomain);
	CREATE INDEX IF NOT EXISTS idx_apps_client_id ON apps(client_id);
	CREATE INDEX IF NOT EXISTS idx_clients_user_id ON clients(user_id);`,

	// 版本 2：应用 SSO 认证支持
	`ALTER TABLE apps ADD COLUMN require_auth BOOLEAN NOT NULL DEFAULT 0;
	ALTER TABLE apps ADD COLUMN auth_method TEXT NOT NULL DEFAULT 'sso';

	CREATE TABLE IF NOT EXISTS sso_sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_sso_sessions_expires ON sso_sessions(expires_at);
	CREATE INDEX IF NOT EXISTS idx_sso_sessions_user_id ON sso_sessions(user_id);`,

	// 版本 3：sso_sessions 补加 app_id 列（兼容从旧版本 2 升级的数据库）
	// 旧版本 2 建的 sso_sessions 没有 app_id，需要删表重建
	`DROP TABLE IF EXISTS sso_sessions;

	CREATE TABLE IF NOT EXISTS sso_sessions (
		token TEXT PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);

	CREATE INDEX IF NOT EXISTS idx_sso_sessions_expires ON sso_sessions(expires_at);
	CREATE INDEX IF NOT EXISTS idx_sso_sessions_user_id ON sso_sessions(user_id);`,

	// 版本 4：应用多客户端支持（主备/负载均衡）
	// apps 表新增 load_balance 字段；新建 app_clients 关联表
	`ALTER TABLE apps ADD COLUMN load_balance BOOLEAN NOT NULL DEFAULT 0;

	CREATE TABLE IF NOT EXISTS app_clients (
		app_id  INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		client_id TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
		priority INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (app_id, client_id)
	);

	CREATE INDEX IF NOT EXISTS idx_app_clients_app_id ON app_clients(app_id);

	-- 迁移现有数据：把 apps.client_id 写入 app_clients 表
	INSERT INTO app_clients (app_id, client_id, priority)
	SELECT id, client_id, 0 FROM apps WHERE client_id IS NOT NULL;`,

	// 版本 5：去掉 app_clients 的 client_id 外键约束（支持 __host__ 虚拟客户端）
	`DROP TABLE IF EXISTS app_clients;

	CREATE TABLE IF NOT EXISTS app_clients (
		app_id  INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		client_id TEXT NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (app_id, client_id)
	);

	CREATE INDEX IF NOT EXISTS idx_app_clients_app_id ON app_clients(app_id);

	-- 重新迁移现有数据
	INSERT OR IGNORE INTO app_clients (app_id, client_id, priority)
	SELECT id, client_id, 0 FROM apps WHERE client_id IS NOT NULL;`,

	// 版本 6：应用自动禁用功能
	`ALTER TABLE apps ADD COLUMN inactive_days INTEGER DEFAULT 7;
	ALTER TABLE apps ADD COLUMN last_used_at TEXT;`,

	// 版本 7：客户端独立目标地址
	`ALTER TABLE app_clients ADD COLUMN target_url TEXT;`,

	// 版本 8：TOTP 二次验证
	`ALTER TABLE users ADD COLUMN totp_enabled BOOLEAN NOT NULL DEFAULT 0;
	ALTER TABLE users ADD COLUMN totp_secret TEXT DEFAULT '';`,

	// 版本 9：应用自定义 HTTP Header
	`ALTER TABLE apps ADD COLUMN custom_headers TEXT DEFAULT '';`,

	// 版本 10：客户端本地代理支持
	`ALTER TABLE clients ADD COLUMN proxy_enabled BOOLEAN NOT NULL DEFAULT 0;
	ALTER TABLE clients ADD COLUMN proxy_listen TEXT DEFAULT '';`,

	// 版本 11：应用认证设置优化
	// 新增 allowed_users（授权用户范围）和 sso_cookie_max_age（SSO Cookie 过期时间）
	`ALTER TABLE apps ADD COLUMN allowed_users TEXT NOT NULL DEFAULT 'owner';
	ALTER TABLE apps ADD COLUMN sso_cookie_max_age INTEGER NOT NULL DEFAULT 86400`,

	// 版本 12：客户端代理支持
	`ALTER TABLE app_clients ADD COLUMN proxy_type TEXT;
	ALTER TABLE app_clients ADD COLUMN proxy_address TEXT;
	ALTER TABLE app_clients ADD COLUMN proxy_password TEXT`,

	// 版本 13：自动禁用天数迁移至用户设置
	`ALTER TABLE users ADD COLUMN auto_disable_days INTEGER NOT NULL DEFAULT 0`,

	// 版本 14：代理设置重构 - 独立管理与应用关联
	// 创建 client_proxies 表存储代理配置，修改 app_clients 使用 proxy_id
	`CREATE TABLE IF NOT EXISTS client_proxies (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		client_id TEXT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		proxy_type TEXT NOT NULL,
		proxy_address TEXT NOT NULL,
		proxy_password TEXT DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_client_proxies_client_id ON client_proxies(client_id);
	ALTER TABLE app_clients ADD COLUMN proxy_id INTEGER REFERENCES client_proxies(id) ON DELETE SET NULL`,

	// 版本 15：客户端间代理连接（peer 代理类型）
	`ALTER TABLE client_proxies ADD COLUMN target_client_id TEXT`,

	// 版本 16：客户端 peer 密钥（客户端启动时生成，上报服务端存储）
	`ALTER TABLE clients ADD COLUMN peer_secret TEXT`,

	// 版本 17：用户角色（admin/user/guest）
	`ALTER TABLE users ADD COLUMN role TEXT NOT NULL DEFAULT 'user';
	UPDATE users SET role = 'admin' WHERE is_admin = 1;
	UPDATE users SET role = 'user' WHERE is_admin = 0`,

	// 版本 18：SSO Cookie「永久」选项值迁移（0 → 315360000）
	`UPDATE apps SET sso_cookie_max_age = 315360000 WHERE sso_cookie_max_age = 0`,
	// 版本 19：访问票据（Access Token）
	`CREATE TABLE IF NOT EXISTS access_tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		token TEXT UNIQUE NOT NULL,
		allowed_entries TEXT NOT NULL DEFAULT '{"server":true,"clients":[]}',
		allowed_app_ids TEXT NOT NULL DEFAULT '[]',
		expires_at TEXT,
		last_used_at TEXT,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_access_tokens_user_id ON access_tokens(user_id);
	CREATE INDEX IF NOT EXISTS idx_access_tokens_token ON access_tokens(token)`,
	// 版本 20：应用路径豁免 SSO 认证
	`ALTER TABLE apps ADD COLUMN exempt_paths TEXT DEFAULT ''`,
	// 版本 21：应用路由规则（按路径和方法配置细化的认证方式和代理后端）
	`CREATE TABLE IF NOT EXISTS app_routes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		client_id TEXT DEFAULT '',
		method TEXT DEFAULT '',
		path_pattern TEXT NOT NULL DEFAULT '',
		auth_method TEXT DEFAULT '',
		target_url TEXT DEFAULT '',
		priority INTEGER NOT NULL DEFAULT 0,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_app_routes_app_id ON app_routes(app_id);`,

	// 版本 22：应用跳转路径规则（按路径匹配返回 301/302 重定向，不代理）
	`CREATE TABLE IF NOT EXISTS app_redirects (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		match_type TEXT NOT NULL DEFAULT 'exact',
		match_path TEXT NOT NULL DEFAULT '',
		redirect_target TEXT NOT NULL DEFAULT '',
		status_code INTEGER NOT NULL DEFAULT 302,
		priority INTEGER NOT NULL DEFAULT 0,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_app_redirects_app_id ON app_redirects(app_id);`,
	// 版本 23：分享码（临时访问票据，跳过 SSO 快速分享）
	`CREATE TABLE IF NOT EXISTS share_codes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
		code TEXT UNIQUE NOT NULL,
		max_uses INTEGER NOT NULL DEFAULT 3,
		use_count INTEGER NOT NULL DEFAULT 0,
		expires_at TEXT NOT NULL,
		cookie_ttl INTEGER NOT NULL DEFAULT 604800,
		enabled BOOLEAN NOT NULL DEFAULT 1,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_share_codes_user_id ON share_codes(user_id);
	CREATE INDEX IF NOT EXISTS idx_share_codes_code ON share_codes(code);`,

	// 版本 24：临时登录（pin + TOTP token）
	// TOTP 密钥复用 totp_secret 字段，独立于 totp_enabled
	`ALTER TABLE users ADD COLUMN temp_login_enabled BOOLEAN NOT NULL DEFAULT 0;
	ALTER TABLE users ADD COLUMN temp_login_pin TEXT DEFAULT '';`,

	// 版本 25：安全代理密钥（agent_keys）+ apps 关联 agent_key_uuid
	`CREATE TABLE IF NOT EXISTS agent_keys (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		uuid TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL,
		private_key TEXT NOT NULL,
		public_key TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	);
	CREATE INDEX IF NOT EXISTS idx_agent_keys_user_id ON agent_keys(user_id);
	CREATE INDEX IF NOT EXISTS idx_agent_keys_uuid ON agent_keys(uuid);
	ALTER TABLE apps ADD COLUMN agent_key_uuid TEXT DEFAULT '';`,

	// 版本 26：用户快速登录开关
	// SSO cookie 过期/缺失时，若用户启用快速登录且管理端 session 有效，
	// /sso 页面静默下发新 SSO cookie 并 302 回目标应用，免去点「确认访问」。
	`ALTER TABLE users ADD COLUMN quick_login BOOLEAN NOT NULL DEFAULT 0`,

	// 版本 27：应用 SSO 二次验证
	// 应用开启后，SSO 授权时需要 TOTP 二次验证，且禁用快速登录
	`ALTER TABLE apps ADD COLUMN require_totp BOOLEAN NOT NULL DEFAULT 0`,

	// 版本 28：跳转规则 match_include_query 选项
	// 默认 false：仅匹配 path（兼容旧行为）；true：匹配 path?query，
	// 用于避免「同 path 不同 query」导致跳转死循环（如 / → /?token=xxx）
	`ALTER TABLE app_redirects ADD COLUMN match_include_query BOOLEAN NOT NULL DEFAULT 0`,

	// 版本 29：分享码跳转路径
	// 默认空字符串表示跳转到应用根路径 /；可设置为应用内任意 / 开头的路径
	`ALTER TABLE share_codes ADD COLUMN redirect_path TEXT NOT NULL DEFAULT ''`,

	// 版本 30：用户应用 API（外部 API 鉴权 token）
	// 启用后用户可拿到独立 token 调用 /external-api/ 路由（只读应用列表），用于导航页面等外部场景
	`ALTER TABLE users ADD COLUMN app_api_enabled BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN app_api_token TEXT NOT NULL DEFAULT '';`,

	// 版本 31：分享码具体子域名
	// 模糊匹配应用（如 *-dev）创建分享码时，需指定命中的具体子域名（如 abc-dev），
	// 兑换时跳转到该具体子域名而非模式本身。精确匹配应用此字段为空字符串。
	`ALTER TABLE share_codes ADD COLUMN concrete_subdomain TEXT NOT NULL DEFAULT ''`,

	// 版本 32：应用可用性探测状态
	// 存储 JSON: {status, status_code, detail, checked_at}
	// status: available(2xx/3xx/4xx) / error(5xx) / failed(连接失败) / timeout(超时)
	// 模糊匹配应用不探测，字段保持空
	`ALTER TABLE apps ADD COLUMN probe_status TEXT DEFAULT ''`,

	// 版本 33：SSO cookie 绑定具体子域名，防止通配符应用跨子域 cookie 串号 (#40)
	// cookie name 由 appID+具体子域名 组成（hopproxy_sso_<appID>_<subdomain>），
	// DB session 也记录子域名，server 验证时校验 session.subdomain == 请求 host 子域名，
	// 防止伪造 cookie name 绕过。
	`ALTER TABLE sso_sessions ADD COLUMN subdomain TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_sso_sessions_subdomain ON sso_sessions(subdomain);`,

	// 版本 34：应用请求头缺省处理模式 (#46)
	// auto_xff（默认，存量应用自动选中）：自动添加 X-Forwarded-*，Origin 不处理
	// auto_origin：不自动添加 X-Forwarded-*，确保 Origin 与 Host 匹配
	// none：不自动处理
	`ALTER TABLE apps ADD COLUMN header_mode TEXT NOT NULL DEFAULT 'auto_xff'`,

	// 版本 35：应用级代理配置覆盖 (#47)
	// 多行 "key: value" 文本（类 yaml），只允许应用级 key
	// （client_max_body_size / proxy_connect_timeout / proxy_read_timeout），
	// 与 settings 表全局 proxy_config 合并后生效
	`ALTER TABLE apps ADD COLUMN proxy_config TEXT NOT NULL DEFAULT ''`,

	// 版本 36：路由规则路径改写 (#53)
	// 命中规则且配置了改写目标时，转发到后端的请求路径按规则改写
	// （前缀替换语义，与 path_pattern 匹配模式对应），原查询串保留
	`ALTER TABLE app_routes ADD COLUMN path_rewrite TEXT NOT NULL DEFAULT ''`,

	// 版本 37：WebAuthn 通行密钥凭据 (#71)
	// credential_id 为 base64url 编码后唯一；public_key 为 COSE 格式公钥；
	// backup_eligible / backup_state 对应 iCloud 同步 passkey 的备份标志
	// （同步型 passkey signCount 恒 0，不做克隆检测，以备份标志为准）；
	// 一个用户可绑定多台设备
	`CREATE TABLE webauthn_credentials (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL,
	credential_id TEXT NOT NULL UNIQUE,
	public_key BLOB NOT NULL,
	attestation_type TEXT NOT NULL DEFAULT '',
	transport TEXT NOT NULL DEFAULT '',
	backup_eligible BOOLEAN NOT NULL DEFAULT 0,
	backup_state BOOLEAN NOT NULL DEFAULT 0,
	sign_count INTEGER NOT NULL DEFAULT 0,
	device_name TEXT NOT NULL DEFAULT '',
	last_used_at DATETIME,
	created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_user ON webauthn_credentials(user_id);`,

	// 版本 38：应用 SSO 二次验证支持通行密钥 (#76)
	// require_totp 布尔升级为 second_factor 枚举（空=无 / totp / passkey），
	// 存量开启 require_totp 的应用回填为 totp 后删除旧列
	`ALTER TABLE apps ADD COLUMN second_factor TEXT NOT NULL DEFAULT '';
UPDATE apps SET second_factor = 'totp' WHERE require_totp = 1;
ALTER TABLE apps DROP COLUMN require_totp;`,

	// 版本 39：会话管理 (#80)
	// auth_sessions 登录会话表（会话清单主体）：管理登录（password/passkey）与一次性
	// 授权流程（temp_login/share_code/qr）各一条；sid 为管理 JWT 携带的会话标识
	// （无 sid 的 token 一律不予放行）；失效用 status 标记不删行，超出保留期
	// （settings.audit_retention_days）由每日任务清理。
	// sso_sessions 扩列：auth_session_id 关联来源会话（快速登录归属当前登录会话；
	// 一次性流程各归属自身会话）、last_request_at 该授权最近一次被使用的时间
	// （SSO 验证通过时按 60s 节流更新）、revoked_at 标记式失效（撤销/退出不再删行）。
	`CREATE TABLE auth_sessions (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sid TEXT NOT NULL DEFAULT '',
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	source TEXT NOT NULL DEFAULT '',
	source_ref TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'active',
	ip TEXT NOT NULL DEFAULT '',
	user_agent TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	last_active_at TEXT,
	expires_at TEXT NOT NULL,
	ended_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_sid ON auth_sessions(sid);
CREATE INDEX IF NOT EXISTS idx_auth_sessions_user_id ON auth_sessions(user_id);
ALTER TABLE sso_sessions ADD COLUMN auth_session_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sso_sessions ADD COLUMN last_request_at TEXT;
ALTER TABLE sso_sessions ADD COLUMN revoked_at TEXT;
CREATE INDEX IF NOT EXISTS idx_sso_sessions_auth_session ON sso_sessions(auth_session_id);`,
}

// CurrentMigrationVersion 当前数据库迁移版本号
const CurrentMigrationVersion = 39
