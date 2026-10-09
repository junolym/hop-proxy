package db

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/robin/hop-proxy/pkg/httputil"
)

// CreateClient 创建客户端
func (db *DB) CreateClient(id string, userID int64, name string) (*Client, error) {
	now := NowUTC()
	_, err := db.conn.Exec(
		"INSERT INTO clients (id, user_id, name, created_at, updated_at, proxy_enabled, proxy_listen) VALUES (?, ?, ?, ?, ?, 0, '')",
		id, userID, name, now, now,
	)
	if err != nil {
		return nil, err
	}
	t := ParseTime(now)
	return &Client{
		ID:        id,
		UserID:    userID,
		Name:      name,
		CreatedAt: t,
		UpdatedAt: t,
	}, nil
}

// ListClients 列出用户的所有客户端（含应用计数）
func (db *DB) ListClients(userID int64) ([]Client, error) {
	rows, err := db.conn.Query(`
		SELECT c.id, c.user_id, c.name, c.created_at, c.updated_at, c.proxy_enabled, c.proxy_listen,
		       (SELECT COUNT(DISTINCT app_id) FROM app_clients WHERE client_id = c.id) as app_count
		FROM clients c
		WHERE c.user_id = ?
		ORDER BY c.created_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []Client
	for rows.Next() {
		var c Client
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &createdAt, &updatedAt, &c.ProxyEnabled, &c.ProxyListen, &c.AppCount); err != nil {
			return nil, err
		}
		c.CreatedAt = ParseTime(createdAt)
		c.UpdatedAt = ParseTime(updatedAt)
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

// GetClient 获取单个客户端
func (db *DB) GetClient(id string) (*Client, error) {
	c := &Client{}
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		"SELECT id, user_id, name, created_at, updated_at, proxy_enabled, proxy_listen FROM clients WHERE id = ?", id,
	).Scan(&c.ID, &c.UserID, &c.Name, &createdAt, &updatedAt, &c.ProxyEnabled, &c.ProxyListen)
	if err != nil {
		return nil, err
	}
	c.CreatedAt = ParseTime(createdAt)
	c.UpdatedAt = ParseTime(updatedAt)
	return c, nil
}

// UpdateClient 更新客户端名称
func (db *DB) UpdateClient(id string, name string) error {
	now := NowUTC()
	result, err := db.conn.Exec(
		"UPDATE clients SET name = ?, updated_at = ? WHERE id = ?",
		name, now, id,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateClientProxyStatus 更新客户端代理状态（由客户端上报）
func (db *DB) UpdateClientProxyStatus(id string, proxyEnabled bool, proxyListen string, peerSecret string) error {
	now := NowUTC()
	_, err := db.conn.Exec(
		"UPDATE clients SET proxy_enabled = ?, proxy_listen = ?, peer_secret = ?, updated_at = ? WHERE id = ?",
		proxyEnabled, proxyListen, peerSecret, now, id,
	)
	return err
}

// DeleteClient 删除客户端（关联的应用 client_id 会被设为 NULL）
func (db *DB) DeleteClient(id string) error {
	result, err := db.conn.Exec("DELETE FROM clients WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CountClients 统计用户的客户端数量
func (db *DB) CountClients(userID int64) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM clients WHERE user_id = ?", userID).Scan(&count)
	return count, err
}

// ClientExists 检查客户端是否存在
func (db *DB) ClientExists(id string) (bool, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM clients WHERE id = ?", id).Scan(&count)
	return count > 0, err
}

// ClientHasApps 检查客户端是否被应用关联
func (db *DB) ClientHasApps(clientID string) (bool, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM app_clients WHERE client_id = ?", clientID,
	).Scan(&count)
	return count > 0, err
}

// CountClientApps 统计关联到指定客户端的应用数
func (db *DB) CountClientApps(clientID string) (int, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(DISTINCT app_id) FROM app_clients WHERE client_id = ?", clientID,
	).Scan(&count)
	return count, err
}

// GetClientPeerSecret 获取客户端的 peer_secret
func (db *DB) GetClientPeerSecret(clientID string) (string, error) {
	var peerSecret string
	err := db.conn.QueryRow("SELECT COALESCE(peer_secret, '') FROM clients WHERE id = ?", clientID).Scan(&peerSecret)
	return peerSecret, err
}

// FindClientByPeerSecretFP 在用户客户端中按 peer 密钥指纹查找客户端 (#67)。
// peer 认证失败时对端会自报当前密钥指纹，服务端据此定位代理地址上实际监听
// 的客户端（代理配置可能仍指向重建前的旧客户端记录）。SQLite 无哈希函数，
// 取回密钥后在 Go 侧逐个比对。未找到返回空 ID（对端可能不属于该用户，非错误）。
func (db *DB) FindClientByPeerSecretFP(userID int64, fp string) (id, name string, err error) {
	rows, err := db.conn.Query("SELECT id, name, peer_secret FROM clients WHERE user_id = ?", userID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, cname string
		var secret sql.NullString
		if err := rows.Scan(&cid, &cname, &secret); err != nil {
			return "", "", err
		}
		if secret.Valid && httputil.PeerSecretFingerprint(secret.String) == fp {
			return cid, cname, nil
		}
	}
	return "", "", rows.Err()
}

// GetAvailablePeerClients 获取用户下可用的 peer 客户端列表（开启了本地代理且有监听地址）
func (db *DB) GetAvailablePeerClients(userID int64) ([]Client, error) {
	rows, err := db.conn.Query(`
		SELECT id, name, proxy_listen
		FROM clients
		WHERE user_id = ? AND proxy_enabled = 1 AND proxy_listen != ''
		ORDER BY name ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clients []Client
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.Name, &c.ProxyListen); err != nil {
			return nil, err
		}
		clients = append(clients, c)
	}
	return clients, rows.Err()
}

// GetClientApps 获取关联到指定客户端的所有应用信息（用于本地代理）
func (db *DB) GetClientApps(clientID string) ([]ClientAppInfo, error) {
	rows, err := db.conn.Query(`
		SELECT DISTINCT a.id, a.subdomain, a.name, COALESCE(ac.target_url, a.target_url), a.require_auth, a.auth_method, a.exempt_paths, a.custom_headers, a.header_mode, a.proxy_config,
		       COALESCE(cp.proxy_type, ''), COALESCE(cp.proxy_address, ''), COALESCE(cp.proxy_password, ''),
		       COALESCE(tc.peer_secret, ''),
		       COALESCE(a.agent_key_uuid, '')
		FROM apps a
		JOIN app_clients ac ON a.id = ac.app_id
		LEFT JOIN client_proxies cp ON ac.proxy_id = cp.id
		LEFT JOIN clients tc ON cp.target_client_id = tc.id
		WHERE ac.client_id = ? AND a.enabled = 1
		ORDER BY a.subdomain
	`, clientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var apps []ClientAppInfo
	for rows.Next() {
		var app ClientAppInfo
		var exemptPathsJSON, customHeaders sql.NullString
		var proxyType, proxyAddress, proxyPassword, peerSecret, agentKeyUUID sql.NullString
		if err := rows.Scan(&app.ID, &app.Subdomain, &app.Name, &app.TargetURL, &app.RequireAuth, &app.AuthMethod, &exemptPathsJSON, &customHeaders, &app.HeaderMode, &app.ProxyConfig,
			&proxyType, &proxyAddress, &proxyPassword, &peerSecret, &agentKeyUUID); err != nil {
			return nil, err
		}
		if app.HeaderMode == "" {
			app.HeaderMode = httputil.HeaderModeAutoXFF
		}
		if exemptPathsJSON.Valid && exemptPathsJSON.String != "" {
			json.Unmarshal([]byte(exemptPathsJSON.String), &app.ExemptPaths)
		}
		if customHeaders.Valid && customHeaders.String != "" {
			json.Unmarshal([]byte(customHeaders.String), &app.CustomHeaders)
		}
		if app.CustomHeaders == nil {
			app.CustomHeaders = make(map[string]string)
		}
		if proxyType.Valid && proxyType.String != "" {
			app.ProxyType = proxyType.String
		}
		if proxyAddress.Valid && proxyAddress.String != "" {
			app.ProxyAddress = proxyAddress.String
		}
		if proxyPassword.Valid && proxyPassword.String != "" {
			app.ProxyPassword = proxyPassword.String
		}
		if peerSecret.Valid && peerSecret.String != "" {
			app.ProxySecret = peerSecret.String
		}
		if agentKeyUUID.Valid && agentKeyUUID.String != "" {
			app.AgentKeyUUID = agentKeyUUID.String
		}
		apps = append(apps, app)
	}

	// 批量填充路由规则（避免 N+1 查询）
	if len(apps) > 0 {
		appIDs := make([]any, len(apps))
		for i, a := range apps {
			appIDs[i] = a.ID
		}
		placeholders := strings.Repeat(",?", len(appIDs))[1:] // "?,?,?"
		routeRows, err := db.conn.Query(
			`SELECT app_id, client_id, method, path_pattern, auth_method, target_url, path_rewrite, priority, enabled
			   FROM app_routes WHERE app_id IN (`+placeholders+`) AND enabled = 1 ORDER BY priority ASC, id ASC`,
			appIDs...,
		)
		if err == nil {
			defer routeRows.Close()
			// 按 app_id 分组
			routesByApp := make(map[int64][]AppRoute)
			for routeRows.Next() {
				var r AppRoute
				if err := routeRows.Scan(&r.AppID, &r.ClientID, &r.Method, &r.PathPattern, &r.AuthMethod, &r.TargetURL, &r.PathRewrite, &r.Priority, &r.Enabled); err != nil {
					continue
				}
				routesByApp[r.AppID] = append(routesByApp[r.AppID], r)
			}
			// 填充到 apps
			for i := range apps {
				apps[i].Routes = routesByApp[apps[i].ID]
			}
		}

		// 批量填充跳转规则（避免 N+1 查询）
		redirectRows, err := db.conn.Query(
			`SELECT app_id, match_type, match_path, match_include_query, redirect_target, status_code, priority, enabled
			   FROM app_redirects WHERE app_id IN (`+placeholders+`) AND enabled = 1 ORDER BY priority ASC, id ASC`,
			appIDs...,
		)
		if err == nil {
			defer redirectRows.Close()
			redirectsByApp := make(map[int64][]AppRedirect)
			for redirectRows.Next() {
				var r AppRedirect
				if err := redirectRows.Scan(&r.AppID, &r.MatchType, &r.MatchPath, &r.MatchIncludeQuery, &r.RedirectTarget, &r.StatusCode, &r.Priority, &r.Enabled); err != nil {
					continue
				}
				redirectsByApp[r.AppID] = append(redirectsByApp[r.AppID], r)
			}
			for i := range apps {
				apps[i].Redirects = redirectsByApp[apps[i].ID]
			}
		}
	}

	return apps, rows.Err()
}
