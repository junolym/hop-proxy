package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/pkg/httputil"
	subdomainpkg "github.com/robin/hop-proxy/pkg/subdomain"
)

// ClientTarget 客户端目标地址配置
type ClientTarget struct {
	ClientID  string
	TargetURL string // 可为空
}

// ClientProxyConfig 客户端代理配置
// 用于 API 层传入代理设置（已废弃，使用 ProxyID 替代）
type ClientProxyConfig struct {
	ProxyType     string // socks5 / shadowsocks / 空
	ProxyAddress  string // 代理地址
	ProxyPassword string // 代理密码
}

// ClientFullConfig 客户端完整配置（用于 SetAppClients）
type ClientFullConfig struct {
	TargetURL string `json:"target_url"`
	ProxyID   *int64 `json:"proxy_id"` // 代理 ID，nil 表示不使用代理
}

// SetAppClients 设置应用的客户端列表（替换全部关联），clientIDs 按优先级排序
// configs 可选，指定每个客户端的完整配置（目标地址、代理 ID 等）
func (db *DB) SetAppClients(tx *sql.Tx, appID int64, clientIDs []string, configs map[string]ClientFullConfig) error {
	// 删除旧关联
	if _, err := tx.Exec("DELETE FROM app_clients WHERE app_id = ?", appID); err != nil {
		return fmt.Errorf("删除旧客户端关联失败: %w", err)
	}
	// 插入新关联
	for i, cid := range clientIDs {
		cfg := configs[cid]
		var targetURL, proxyID interface{}
		if cfg.TargetURL != "" {
			targetURL = cfg.TargetURL
		}
		if cfg.ProxyID != nil {
			proxyID = *cfg.ProxyID
		}
		if _, err := tx.Exec(
			"INSERT INTO app_clients (app_id, client_id, priority, target_url, proxy_id) VALUES (?, ?, ?, ?, ?)",
			appID, cid, i, targetURL, proxyID,
		); err != nil {
			return fmt.Errorf("插入客户端关联失败: %w", err)
		}
	}
	return nil
}

// GetAppClientIDs 获取应用关联的客户端 ID 列表（按 priority 排序）
func (db *DB) GetAppClientIDs(appID int64) ([]string, error) {
	rows, err := db.conn.Query(
		"SELECT client_id FROM app_clients WHERE app_id = ? ORDER BY priority ASC",
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// CreateApp 创建应用
func (db *DB) CreateApp(userID int64, name, subdomain, targetURL string, authMethod string, allowedUsers string, ssoCookieMaxAge int, loadBalance bool, clientIDs []string, clientConfigs map[string]ClientFullConfig, inactiveDays *int, customHeaders map[string]string, headerMode, proxyConfig string, exemptPaths []string, agentKeyUUID string, secondFactor string) (*App, error) {
	now := NowUTC()

	// 序列化 custom_headers 为 JSON
	var customHeadersJSON string
	if len(customHeaders) > 0 {
		data, _ := json.Marshal(customHeaders)
		customHeadersJSON = string(data)
	}

	// 请求头缺省处理模式缺省值
	if headerMode == "" {
		headerMode = httputil.HeaderModeAutoXFF
	}

	// 序列化 exempt_paths 为 JSON
	var exemptPathsJSON string
	if len(exemptPaths) > 0 {
		data, _ := json.Marshal(exemptPaths)
		exemptPathsJSON = string(data)
	}

	// 计算兼容字段 requireAuth：只要不是 none 就需要认证
	requireAuth := authMethod != server.AuthMethodNone

	// 默认值处理
	if allowedUsers == "" {
		allowedUsers = server.AllowedUsersOwner
	}
	if ssoCookieMaxAge == 0 {
		ssoCookieMaxAge = server.DefaultSSOTTLS
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO apps (user_id, name, subdomain, target_url, enabled, require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, name, subdomain, targetURL, requireAuth, authMethod, allowedUsers, ssoCookieMaxAge, loadBalance, inactiveDays, customHeadersJSON, headerMode, proxyConfig, exemptPathsJSON, agentKeyUUID, secondFactor, now, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()

	if err := db.SetAppClients(tx, id, clientIDs, clientConfigs); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	t := ParseTime(now)
	return &App{
		ID:              id,
		UserID:          userID,
		Name:            name,
		Subdomain:       subdomain,
		TargetURL:       targetURL,
		Enabled:         true,
		RequireAuth:     requireAuth,
		AuthMethod:      authMethod,
		AllowedUsers:    allowedUsers,
		SSOCookieMaxAge: ssoCookieMaxAge,
		LoadBalance:     loadBalance,
		InactiveDays:    inactiveDays,
		CustomHeaders:   customHeaders,
		HeaderMode:      headerMode,
		ProxyConfig:     proxyConfig,
		ExemptPaths:     exemptPaths,
		AgentKeyUUID:    agentKeyUUID,
		SecondFactor:    secondFactor,
		LastUsedAt:      &t,
		ClientIDs:       clientIDs,
		CreatedAt:       t,
		UpdatedAt:       t,
	}, nil
}

// scanApp 辅助函数：从 sql.Scanner 扫描 App 数据
// 支持 *sql.Rows 和 *sql.Row
type appScanner interface {
	Scan(dest ...any) error
}

// scanApp 从扫描器中读取 App 数据
func scanApp(scanner appScanner) (*App, error) {
	a := &App{}
	var createdAt, updatedAt string
	var lastUsedAt sql.NullString
	var inactiveDays sql.NullInt64
	var customHeadersJSON sql.NullString
	var exemptPathsJSON sql.NullString
	var probeStatusJSON sql.NullString

	err := scanner.Scan(&a.ID, &a.UserID, &a.Name, &a.Subdomain, &a.TargetURL, &a.Enabled,
		&a.RequireAuth, &a.AuthMethod, &a.AllowedUsers, &a.SSOCookieMaxAge, &a.LoadBalance,
		&inactiveDays, &customHeadersJSON, &a.HeaderMode, &a.ProxyConfig, &exemptPathsJSON, &a.AgentKeyUUID, &a.SecondFactor, &lastUsedAt, &probeStatusJSON, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}

	if inactiveDays.Valid {
		v := int(inactiveDays.Int64)
		a.InactiveDays = &v
	}
	if customHeadersJSON.Valid && customHeadersJSON.String != "" {
		json.Unmarshal([]byte(customHeadersJSON.String), &a.CustomHeaders)
	}
	if a.CustomHeaders == nil {
		a.CustomHeaders = make(map[string]string)
	}
	if exemptPathsJSON.Valid && exemptPathsJSON.String != "" {
		json.Unmarshal([]byte(exemptPathsJSON.String), &a.ExemptPaths)
	}
	if a.ExemptPaths == nil {
		a.ExemptPaths = []string{}
	}
	if lastUsedAt.Valid {
		t := ParseTime(lastUsedAt.String)
		a.LastUsedAt = &t
	}
	if probeStatusJSON.Valid && probeStatusJSON.String != "" {
		var ps AppProbeStatus
		if err := json.Unmarshal([]byte(probeStatusJSON.String), &ps); err == nil {
			a.ProbeStatus = &ps
		}
	}
	a.CreatedAt = ParseTime(createdAt)
	a.UpdatedAt = ParseTime(updatedAt)

	return a, nil
}

// ListApps 列出用户的所有应用
func (db *DB) ListApps(userID int64) ([]App, error) {
	rows, err := db.conn.Query(`
		SELECT id, user_id, name, subdomain, target_url, enabled,
		       require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, probe_status, created_at, updated_at
		FROM apps
		WHERE user_id = ?
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 批量填充客户端信息
	for i := range apps {
		infos, err := db.getAppClientInfos(apps[i].ID)
		if err == nil {
			apps[i].ClientInfos = infos
			for _, info := range infos {
				apps[i].ClientIDs = append(apps[i].ClientIDs, info.ClientID)
			}
			if len(infos) > 0 {
				apps[i].ClientName = infos[0].ClientName
				cid := infos[0].ClientID
				apps[i].ClientID = &cid
			}
		}
	}

	return apps, nil
}

// GetApp 获取单个应用
func (db *DB) GetApp(id int64) (*App, error) {
	a, err := scanApp(db.conn.QueryRow(
		`SELECT id, user_id, name, subdomain, target_url, enabled,
		        require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, probe_status, created_at, updated_at FROM apps WHERE id = ?`,
		id))
	if err != nil {
		return nil, err
	}

	infos, _ := db.getAppClientInfos(id)
	a.ClientInfos = infos
	for _, info := range infos {
		a.ClientIDs = append(a.ClientIDs, info.ClientID)
	}
	if len(infos) > 0 {
		cid := infos[0].ClientID
		a.ClientID = &cid
		a.ClientName = infos[0].ClientName
	}
	return a, nil
}

// GetAppBySubdomain 根据子域名查找应用（含客户端列表）。
// 优先精确匹配；未命中时在所有模糊模式（包含 * 或 / 的子域名）中
// 按 (字面量字符数 DESC, id ASC) 选最具体的命中应用。
func (db *DB) GetAppBySubdomain(subdomain string) (*App, error) {
	a, err := scanApp(db.conn.QueryRow(
		`SELECT id, user_id, name, subdomain, target_url, enabled,
		        require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, probe_status, created_at, updated_at FROM apps WHERE subdomain = ?`,
		subdomain))
	if err == nil {
		clientIDs, _ := db.GetAppClientIDs(a.ID)
		a.ClientIDs = clientIDs
		if len(clientIDs) > 0 {
			a.ClientID = &clientIDs[0]
		}
		return a, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// 精确未命中，尝试模糊匹配
	return db.getAppByFuzzySubdomain(subdomain, false)
}

// AppSubdomainRef 全局子域名注册表条目：仅含路由解析所需的 id 与 subdomain，
// 不携带任何应用配置（防泄漏），随 ActionAppsResponse 下发 (#57)。
type AppSubdomainRef struct {
	ID        int64  `json:"id"`
	Subdomain string `json:"subdomain"`
}

// GetAllAppSubdomains 返回所有已启用应用的 (id, subdomain) 注册表。
// 客户端本地代理用它复刻服务端解析（全局精确优先 → 全局模糊排序），
// 防止本地模糊模式吞并其他客户端的精确子域名应用 (#57)。
func (db *DB) GetAllAppSubdomains() ([]AppSubdomainRef, error) {
	rows, err := db.conn.Query(`SELECT id, subdomain FROM apps WHERE enabled = 1 ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	refs := make([]AppSubdomainRef, 0)
	for rows.Next() {
		var r AppSubdomainRef
		if err := rows.Scan(&r.ID, &r.Subdomain); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}

// getAppByFuzzySubdomain 在所有模糊模式中匹配子域名，按 (字面量字符数 DESC, id ASC) 选最优。
// 字面量越多的模式越具体，优先级越高（例：`local-*` 优先于 `*-dev` 优先于 `/.*/`）。
// enabledOnly=true 时仅匹配已启用的应用。
func (db *DB) getAppByFuzzySubdomain(sub string, enabledOnly bool) (*App, error) {
	query := `SELECT id, user_id, name, subdomain, target_url, enabled,
	        require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, probe_status, created_at, updated_at
	        FROM apps WHERE subdomain LIKE '%*%' OR subdomain LIKE '%/%'
	        ORDER BY id ASC`
	if enabledOnly {
		query = `SELECT id, user_id, name, subdomain, target_url, enabled,
		        require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, probe_status, created_at, updated_at
		        FROM apps WHERE enabled = 1 AND (subdomain LIKE '%*%' OR subdomain LIKE '%/%')
		        ORDER BY id ASC`
	}
	// 注意：SQLite 连接池 SetMaxOpenConns(1)，rows 迭代期间独占连接。
	// 必须先 Close rows 再调用任何其他 DB 方法（如 GetAppClientIDs），否则死锁。
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		app    *App
		litLen int
	}
	var cands []candidate
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		pat, err := subdomainpkg.Parse(a.Subdomain)
		if err != nil {
			continue // 跳过无效模式（防御）
		}
		if pat.Match(sub) != nil {
			cands = append(cands, candidate{app: a, litLen: pat.LiteralLen()})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(cands) == 0 {
		return nil, sql.ErrNoRows
	}
	// 选字面量最多（最具体）的；并列时 id 最小（最早创建）的优先，保证确定性
	best := cands[0]
	for _, c := range cands[1:] {
		if c.litLen > best.litLen || (c.litLen == best.litLen && c.app.ID < best.app.ID) {
			best = c
		}
	}
	// rows 已 Close，可以安全调用其他 DB 方法
	clientIDs, _ := db.GetAppClientIDs(best.app.ID)
	best.app.ClientIDs = clientIDs
	if len(clientIDs) > 0 {
		best.app.ClientID = &clientIDs[0]
	}
	return best.app, nil
}

// UpdateApp 更新应用
func (db *DB) UpdateApp(id int64, name, subdomain, targetURL string, enabled bool, authMethod string, allowedUsers string, ssoCookieMaxAge int, loadBalance bool, clientIDs []string, clientConfigs map[string]ClientFullConfig, inactiveDays *int, customHeaders map[string]string, headerMode, proxyConfig string, exemptPaths []string, agentKeyUUID string, secondFactor string) error {
	// 序列化 custom_headers 为 JSON
	var customHeadersJSON string
	if len(customHeaders) > 0 {
		data, _ := json.Marshal(customHeaders)
		customHeadersJSON = string(data)
	}

	// 请求头缺省处理模式缺省值
	if headerMode == "" {
		headerMode = httputil.HeaderModeAutoXFF
	}

	// 序列化 exempt_paths 为 JSON
	var exemptPathsJSON string
	if len(exemptPaths) > 0 {
		data, _ := json.Marshal(exemptPaths)
		exemptPathsJSON = string(data)
	}

	// 计算兼容字段 requireAuth：只要不是 none 就需要认证
	requireAuth := authMethod != server.AuthMethodNone

	// 默认值处理
	if allowedUsers == "" {
		allowedUsers = server.AllowedUsersOwner
	}
	if ssoCookieMaxAge == 0 {
		ssoCookieMaxAge = server.DefaultSSOTTLS
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE apps SET name = ?, subdomain = ?, target_url = ?, enabled = ?,
		 require_auth = ?, auth_method = ?, allowed_users = ?, sso_cookie_max_age = ?, load_balance = ?, inactive_days = ?, custom_headers = ?, header_mode = ?, proxy_config = ?, exempt_paths = ?, agent_key_uuid = ?, second_factor = ?, updated_at = ?
		 WHERE id = ?`,
		name, subdomain, targetURL, enabled, requireAuth, authMethod, allowedUsers, ssoCookieMaxAge, loadBalance, inactiveDays, customHeadersJSON, headerMode, proxyConfig, exemptPathsJSON, agentKeyUUID, secondFactor,
		NowUTC(), id,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}

	if err := db.SetAppClients(tx, id, clientIDs, clientConfigs); err != nil {
		return err
	}

	return tx.Commit()
}

// DeleteApp 删除应用
func (db *DB) DeleteApp(id int64) error {
	result, err := db.conn.Exec("DELETE FROM apps WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SubdomainExists 检查子域名是否已被使用（排除指定 ID）
func (db *DB) SubdomainExists(subdomain string, excludeID int64) (bool, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM apps WHERE subdomain = ? AND id != ?",
		subdomain, excludeID,
	).Scan(&count)
	return count > 0, err
}

// duplicateMaxAttempts 克隆名称/子域名后缀推导的最大尝试次数（-1 至 -10000）
const duplicateMaxAttempts = 10000

// DuplicateApp 克隆应用（含 app_clients / app_routes / app_redirects 全部子表配置）。
//
// 复制方式为"整行动态复制"：通过 PRAGMA table_info 枚举表的全集列，
// 除 id / app_id 外全部原样复制，因此今后 apps 表或子表新增配置列时克隆自动生效，
// 无需修改本函数。仅以下字段在复制后显式覆盖：
//   - name / subdomain：推导为 <原值>-N（N 从 1 起，任一冲突则同步递增），保证不与现有应用重复
//   - last_used_at / created_at / updated_at：重置为当前时间
//   - probe_status：清空（探测状态属于原应用的运行时状态，不应沿用）
//
// 注意：若未来新增"克隆时不应沿用"的字段（如指向外部资源的独占引用），必须在此处的
// 覆盖 UPDATE 中补充处理，否则会被动态复制逻辑自动带走。
func (db *DB) DuplicateApp(appID int64) (*App, error) {
	app, err := db.GetApp(appID)
	if err != nil {
		return nil, err
	}

	// 推导不重复的名称与子域名（同一序号 N 同步递增，任一冲突则继续递增）
	var newName, newSubdomain string
	for i := 1; i <= duplicateMaxAttempts; i++ {
		name := fmt.Sprintf("%s-%d", app.Name, i)
		sub := fmt.Sprintf("%s-%d", app.Subdomain, i)
		nameExists, err := db.appNameExists(name)
		if err != nil {
			return nil, err
		}
		if nameExists {
			continue
		}
		subExists, err := db.SubdomainExists(sub, 0)
		if err != nil {
			return nil, err
		}
		if !subExists {
			newName, newSubdomain = name, sub
			break
		}
	}
	if newName == "" {
		return nil, fmt.Errorf("推导克隆名称失败：已达最大尝试次数 %d", duplicateMaxAttempts)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 覆盖必须在 INSERT 阶段完成：apps.subdomain 有 UNIQUE 约束，
	// 若先整行原样复制再 UPDATE，中间态会与源应用冲突
	now := NowUTC()
	newID, err := insertAppClone(tx, appID, map[string]any{
		"name":         newName,
		"subdomain":    newSubdomain,
		"last_used_at": now,
		"probe_status": "",
		"created_at":   now,
		"updated_at":   now,
	})
	if err != nil {
		return nil, err
	}

	// 子表整行复制（动态枚举列，新增配置列自动被克隆）
	for _, table := range []string{"app_clients", "app_routes", "app_redirects"} {
		if err := copyChildRows(tx, table, appID, newID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetApp(newID)
}

// appNameExists 检查应用名称是否已被使用（全表查重，供克隆推导唯一名称）
func (db *DB) appNameExists(name string) (bool, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM apps WHERE name = ?", name).Scan(&count)
	return count > 0, err
}

// cloneColumns 返回表的全集列名（排除 exclude 指定的列）。
// 通过 PRAGMA table_info 动态枚举，表结构变更后克隆逻辑无需调整。
func cloneColumns(tx *sql.Tx, table string, exclude ...string) ([]string, error) {
	rows, err := tx.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("枚举表 %s 列失败: %w", table, err)
	}
	defer rows.Close()
	skip := make(map[string]bool, len(exclude))
	for _, c := range exclude {
		skip[c] = true
	}
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if !skip[name] {
			cols = append(cols, name)
		}
	}
	return cols, rows.Err()
}

// insertAppClone 将 apps 表中 id=srcID 的行整行复制为新行（排除 id 列），返回新行 ID。
// overrides 指定复制时直接覆盖为新值的列（列名 → 值），如克隆推导出的 name/subdomain。
// 覆盖在 INSERT 的 SELECT 列表中以占位符完成，避免中间态触发 UNIQUE 约束。
func insertAppClone(tx *sql.Tx, srcID int64, overrides map[string]any) (int64, error) {
	cols, err := cloneColumns(tx, "apps", "id")
	if err != nil {
		return 0, err
	}
	var selectParts []string
	var args []any
	for _, col := range cols {
		if v, ok := overrides[col]; ok {
			selectParts = append(selectParts, "?")
			args = append(args, v)
		} else {
			selectParts = append(selectParts, col)
		}
	}
	colList := strings.Join(cols, ", ")
	selectList := strings.Join(selectParts, ", ")
	args = append(args, srcID)
	result, err := tx.Exec(
		fmt.Sprintf("INSERT INTO apps (%s) SELECT %s FROM apps WHERE id = ?", colList, selectList),
		args...,
	)
	if err != nil {
		return 0, fmt.Errorf("复制应用主表失败: %w", err)
	}
	newID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return newID, nil
}

// copyChildRows 将子表（以 app_id 关联应用的表）中源应用的行整行复制到新应用（排除 id / app_id 列）。
// 子表今后新增配置列会被自动复制；若新增"不应沿用"的列，需在此函数内补充排除与重建逻辑。
func copyChildRows(tx *sql.Tx, table string, srcAppID, dstAppID int64) error {
	cols, err := cloneColumns(tx, table, "id", "app_id")
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	colList := strings.Join(cols, ", ")
	if _, err := tx.Exec(
		fmt.Sprintf("INSERT INTO %s (app_id, %s) SELECT ?, %s FROM %s WHERE app_id = ?", table, colList, colList, table),
		dstAppID, srcAppID,
	); err != nil {
		return fmt.Errorf("复制子表 %s 失败: %w", table, err)
	}
	return nil
}

// CountApps 统计用户的应用数量
func (db *DB) CountApps(userID int64) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM apps WHERE user_id = ?", userID).Scan(&count)
	return count, err
}

// getAppClientInfos 获取应用客户端详情（含名称，按优先级排序）
func (db *DB) getAppClientInfos(appID int64) ([]AppClient, error) {
	rows, err := db.conn.Query(`
		SELECT ac.client_id, c.name, ac.priority, ac.target_url, ac.proxy_id,
		       COALESCE(cp.name, ''), COALESCE(cp.proxy_type, ''), COALESCE(cp.proxy_address, ''), cp.proxy_password,
		       cp.target_client_id
		FROM app_clients ac
		LEFT JOIN clients c ON ac.client_id = c.id
		LEFT JOIN client_proxies cp ON ac.proxy_id = cp.id
		WHERE ac.app_id = ?
		ORDER BY ac.priority ASC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var infos []AppClient
	for rows.Next() {
		var info AppClient
		var name sql.NullString
		var targetURL sql.NullString
		var proxyID sql.NullInt64
		var proxyName, proxyType, proxyAddress, proxyPassword, proxyTargetClient sql.NullString
		if err := rows.Scan(&info.ClientID, &name, &info.Priority, &targetURL, &proxyID,
			&proxyName, &proxyType, &proxyAddress, &proxyPassword, &proxyTargetClient); err != nil {
			return nil, err
		}
		if name.Valid {
			info.ClientName = name.String
		}
		if targetURL.Valid {
			info.TargetURL = &targetURL.String
		}
		if proxyID.Valid {
			info.ProxyID = &proxyID.Int64
		}
		if proxyName.Valid && proxyName.String != "" {
			info.ProxyName = &proxyName.String
		}
		if proxyType.Valid && proxyType.String != "" {
			info.ProxyType = &proxyType.String
		}
		if proxyAddress.Valid && proxyAddress.String != "" {
			info.ProxyAddress = &proxyAddress.String
		}
		// 密码脱敏：返回时隐藏实际密码
		if proxyPassword.Valid && proxyPassword.String != "" {
			masked := "******"
			info.ProxyPassword = &masked
		}
		if proxyTargetClient.Valid && proxyTargetClient.String != "" {
			info.ProxyTargetClient = &proxyTargetClient.String
		}
		infos = append(infos, info)
	}
	return infos, rows.Err()
}

// GetAppIDsByUserID 获取用户所有应用的 ID（用于 Logout 清除 SSO Cookie）
func (db *DB) GetAppIDsByUserID(userID int64) ([]int64, error) {
	rows, err := db.conn.Query("SELECT id FROM apps WHERE user_id = ?", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetAppClientsBySubdomain 获取应用的客户端列表（供代理层使用，返回 clientIDs 按优先级）
func (db *DB) GetAppClientsBySubdomain(subdomain string) (appID int64, clientIDs []string, loadBalance bool, err error) {
	err = db.conn.QueryRow(
		"SELECT id, load_balance FROM apps WHERE subdomain = ? AND enabled = 1",
		subdomain,
	).Scan(&appID, &loadBalance)
	if err != nil {
		return
	}
	clientIDs, err = db.GetAppClientIDs(appID)
	return
}

// ClientWithTarget 客户端及其目标地址
// 供代理层使用，包含代理配置
type ClientWithTarget struct {
	ClientID          string
	TargetURL         string // 客户端专属目标地址（可为空）
	ProxyType         string // 代理类型：socks5 / shadowsocks / peer / 空
	ProxyAddress      string // 代理地址
	ProxyPassword     string // 代理密码
	ProxyTargetClient string // peer 类型的目标客户端 ID
	PeerSecret        string // peer 密钥（目标客户端的 peer_secret）
}

// AppResolve 应用解析结果（代理层入口共用）：命中的应用及其客户端列表。
type AppResolve struct {
	AppID        int64
	AppTargetURL string // 应用级目标地址（客户端关联级为空时回退用）
	LoadBalance  bool
	AgentKeyUUID string
	Clients      []ClientWithTarget
	Captures     []string // 模糊匹配命中的捕获组（精确匹配时为 nil）
}

// GetAppClientsWithTarget 获取应用的客户端列表及其目标地址（供代理层使用）。
// 优先精确匹配；未命中时在模糊模式中按 (字面量字符数 DESC, id ASC) 选最具体的命中。
// 返回的 Captures 为模糊匹配命中的捕获组（精确匹配时为 nil）。
func (db *DB) GetAppClientsWithTarget(subdomain string) (*AppResolve, error) {
	var (
		appID        int64
		appTargetURL string
		loadBalance  bool
		agentKeyUUID string
		captures     []string
	)
	err := db.conn.QueryRow(
		"SELECT id, target_url, load_balance, COALESCE(agent_key_uuid, '') FROM apps WHERE subdomain = ? AND enabled = 1",
		subdomain,
	).Scan(&appID, &appTargetURL, &loadBalance, &agentKeyUUID)
	if err == nil {
		clients, cerr := db.FetchClientsWithTarget(appID)
		if cerr != nil {
			return nil, cerr
		}
		return &AppResolve{
			AppID:        appID,
			AppTargetURL: appTargetURL,
			LoadBalance:  loadBalance,
			AgentKeyUUID: agentKeyUUID,
			Clients:      clients,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// 精确未命中，尝试模糊匹配
	a, ferr := db.getAppByFuzzySubdomain(subdomain, true)
	if ferr != nil {
		return nil, ferr
	}
	pat, perr := subdomainpkg.Parse(a.Subdomain)
	if perr != nil {
		return nil, perr
	}
	captures = pat.Match(subdomain)
	clients, cerr := db.FetchClientsWithTarget(a.ID)
	if cerr != nil {
		return nil, cerr
	}
	return &AppResolve{
		AppID:        a.ID,
		AppTargetURL: a.TargetURL,
		LoadBalance:  a.LoadBalance,
		AgentKeyUUID: a.AgentKeyUUID,
		Clients:      clients,
		Captures:     captures,
	}, nil
}

// FetchClientsWithTarget 按 app_id 加载客户端列表（含代理配置）。
func (db *DB) FetchClientsWithTarget(appID int64) ([]ClientWithTarget, error) {
	rows, err := db.conn.Query(`
		SELECT ac.client_id, ac.target_url,
		       COALESCE(cp.proxy_type, ''), COALESCE(cp.proxy_address, ''), COALESCE(cp.proxy_password, ''),
		       COALESCE(cp.target_client_id, ''),
		       COALESCE(tc.peer_secret, '')
		FROM app_clients ac
		LEFT JOIN client_proxies cp ON ac.proxy_id = cp.id
		LEFT JOIN clients tc ON cp.target_client_id = tc.id
		WHERE ac.app_id = ?
		ORDER BY ac.priority ASC
	`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []ClientWithTarget
	for rows.Next() {
		var c ClientWithTarget
		var targetURL, proxyType, proxyAddress, proxyPassword, proxyTargetClient, peerSecret sql.NullString
		if err := rows.Scan(&c.ClientID, &targetURL, &proxyType, &proxyAddress, &proxyPassword, &proxyTargetClient, &peerSecret); err != nil {
			return nil, err
		}
		if targetURL.Valid {
			c.TargetURL = targetURL.String
		}
		if proxyType.Valid {
			c.ProxyType = proxyType.String
		}
		if proxyAddress.Valid {
			c.ProxyAddress = proxyAddress.String
		}
		if proxyPassword.Valid {
			c.ProxyPassword = proxyPassword.String
		}
		if proxyTargetClient.Valid {
			c.ProxyTargetClient = proxyTargetClient.String
		}
		if peerSecret.Valid {
			c.PeerSecret = peerSecret.String
		}
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

// UpdateAppLastUsed 更新应用最近使用时间
func (db *DB) UpdateAppLastUsed(appID int64) error {
	_, err := db.conn.Exec(
		"UPDATE apps SET last_used_at = ? WHERE id = ?",
		NowUTC(), appID,
	)
	return err
}

// UpdateAppProbeStatus 更新应用探测状态（JSON 序列化存储）
func (db *DB) UpdateAppProbeStatus(appID int64, status AppProbeStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("序列化探测状态失败: %w", err)
	}
	_, err = db.conn.Exec(
		"UPDATE apps SET probe_status = ? WHERE id = ?",
		string(data), appID,
	)
	return err
}

// ClearAppProbeStatus 清空应用探测状态（应用禁用/重新启用等场景）
func (db *DB) ClearAppProbeStatus(appID int64) error {
	_, err := db.conn.Exec(
		"UPDATE apps SET probe_status = '' WHERE id = ?",
		appID,
	)
	return err
}

// ListAllEnabledAppsForProbe 列出所有已启用且非模糊匹配的应用（供探测调度器使用）。
// 不填充 ClientInfos（探测时通过 GetApp 重新获取以保证客户端在线状态实时性）。
func (db *DB) ListAllEnabledAppsForProbe() ([]App, error) {
	rows, err := db.conn.Query(`
		SELECT id, user_id, name, subdomain, target_url, enabled,
		       require_auth, auth_method, allowed_users, sso_cookie_max_age, load_balance, inactive_days, custom_headers, header_mode, proxy_config, exempt_paths, agent_key_uuid, second_factor, last_used_at, probe_status, created_at, updated_at
		FROM apps
		WHERE enabled = 1 AND subdomain NOT LIKE '%*%' AND subdomain NOT LIKE '%/%'
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, *a)
	}
	return apps, rows.Err()
}

// ExternalAppItem 外部 API 返回的应用信息（只读，仅暴露导航所需字段）
type ExternalAppItem struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Subdomain  string     `json:"subdomain"`
	URL        string     `json:"url"`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// ListExternalApps 列出用户所有已启用的应用（按 last_used_at DESC 排序，nulls last）。
// 仅返回导航所需字段，不暴露后端配置信息。proxyDomain 用于构造完整 URL。
func (db *DB) ListExternalApps(userID int64, proxyDomain string) ([]ExternalAppItem, error) {
	rows, err := db.conn.Query(
		`SELECT id, name, subdomain, last_used_at
		 FROM apps
		 WHERE user_id = ? AND enabled = 1
		 ORDER BY last_used_at IS NULL ASC, last_used_at DESC, id ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ExternalAppItem
	for rows.Next() {
		var item ExternalAppItem
		var lastUsedAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &item.Subdomain, &lastUsedAt); err != nil {
			return nil, err
		}
		if lastUsedAt.Valid {
			t := ParseTime(lastUsedAt.String)
			item.LastUsedAt = &t
		}
		item.URL = fmt.Sprintf("https://%s.%s", item.Subdomain, proxyDomain)
		items = append(items, item)
	}
	if items == nil {
		items = []ExternalAppItem{}
	}
	return items, rows.Err()
}

// GetAppCustomHeaders 获取应用的自定义 Headers（供代理层使用）
func (db *DB) GetAppCustomHeaders(appID int64) (map[string]string, error) {
	var customHeadersJSON sql.NullString
	err := db.conn.QueryRow(
		"SELECT custom_headers FROM apps WHERE id = ?",
		appID,
	).Scan(&customHeadersJSON)
	if err != nil {
		return nil, err
	}

	headers := make(map[string]string)
	if customHeadersJSON.Valid && customHeadersJSON.String != "" {
		json.Unmarshal([]byte(customHeadersJSON.String), &headers)
	}
	return headers, nil
}

// AutoDisableInactiveApps 自动禁用超期不活跃的应用
// 使用用户的 auto_disable_days 设置，若用户未启用（值为 0）则跳过该用户的所有应用
// 返回被禁用的应用数量
func (db *DB) AutoDisableInactiveApps() (int, error) {
	// 查找满足条件的应用：
	// 1. enabled = 1
	// 2. 用户的 auto_disable_days > 0
	// 3. last_used_at 距今超过 auto_disable_days 天
	result, err := db.conn.Exec(`
		UPDATE apps SET enabled = 0, updated_at = ?
		WHERE enabled = 1
		  AND id IN (
		    SELECT a.id FROM apps a
		    JOIN users u ON a.user_id = u.id
		    WHERE u.auto_disable_days > 0
		      AND a.last_used_at IS NOT NULL
		      AND julianday('now') - julianday(a.last_used_at) > u.auto_disable_days
		  )
	`, NowUTC())
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}
