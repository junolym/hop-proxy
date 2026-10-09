package db

import (
	"database/sql"
	"fmt"
)

// CreateProxy 创建代理配置
func (db *DB) CreateProxy(clientID, name, proxyType, proxyAddress, proxyPassword, targetClientID string) (*ClientProxy, error) {
	now := NowUTC()
	result, err := db.conn.Exec(
		`INSERT INTO client_proxies (client_id, name, proxy_type, proxy_address, proxy_password, target_client_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		clientID, name, proxyType, proxyAddress, proxyPassword, nullIfEmpty(targetClientID), now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()

	var targetClientIDPtr *string
	if targetClientID != "" {
		targetClientIDPtr = &targetClientID
	}

	return &ClientProxy{
		ID:             id,
		ClientID:       clientID,
		Name:           name,
		ProxyType:      proxyType,
		ProxyAddress:   proxyAddress,
		ProxyPassword:  "******", // 新创建时返回脱敏值
		TargetClientID: targetClientIDPtr,
		CreatedAt:      ParseTime(now),
	}, nil
}

// ListProxies 获取客户端的代理列表
func (db *DB) ListProxies(clientID string) ([]ClientProxy, error) {
	rows, err := db.conn.Query(
		`SELECT id, client_id, name, proxy_type, proxy_address, proxy_password, target_client_id, created_at
		 FROM client_proxies WHERE client_id = ? ORDER BY created_at ASC`,
		clientID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var proxies []ClientProxy
	for rows.Next() {
		var p ClientProxy
		var createdAt string
		var password sql.NullString
		var targetClientID sql.NullString
		if err := rows.Scan(&p.ID, &p.ClientID, &p.Name, &p.ProxyType, &p.ProxyAddress, &password, &targetClientID, &createdAt); err != nil {
			return nil, err
		}
		// 密码脱敏：返回时隐藏实际密码
		if password.Valid && password.String != "" {
			p.ProxyPassword = "******"
		}
		if targetClientID.Valid && targetClientID.String != "" {
			p.TargetClientID = &targetClientID.String
		}
		p.CreatedAt = ParseTime(createdAt)
		proxies = append(proxies, p)
	}
	return proxies, rows.Err()
}

// GetProxy 获取单个代理配置
func (db *DB) GetProxy(id int64) (*ClientProxy, error) {
	p := &ClientProxy{}
	var createdAt string
	var password sql.NullString
	var targetClientID sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, client_id, name, proxy_type, proxy_address, proxy_password, target_client_id, created_at
		 FROM client_proxies WHERE id = ?`,
		id,
	).Scan(&p.ID, &p.ClientID, &p.Name, &p.ProxyType, &p.ProxyAddress, &password, &targetClientID, &createdAt)
	if err != nil {
		return nil, err
	}
	// 密码脱敏：返回时隐藏实际密码
	if password.Valid && password.String != "" {
		p.ProxyPassword = "******"
	}
	if targetClientID.Valid && targetClientID.String != "" {
		p.TargetClientID = &targetClientID.String
	}
	p.CreatedAt = ParseTime(createdAt)
	return p, nil
}

// GetProxyWithPassword 获取代理配置（包含真实密码，供内部使用）
func (db *DB) GetProxyWithPassword(id int64) (*ClientProxy, error) {
	p := &ClientProxy{}
	var createdAt string
	var password sql.NullString
	var targetClientID sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, client_id, name, proxy_type, proxy_address, proxy_password, target_client_id, created_at
		 FROM client_proxies WHERE id = ?`,
		id,
	).Scan(&p.ID, &p.ClientID, &p.Name, &p.ProxyType, &p.ProxyAddress, &password, &targetClientID, &createdAt)
	if err != nil {
		return nil, err
	}
	if password.Valid {
		p.ProxyPassword = password.String
	}
	if targetClientID.Valid && targetClientID.String != "" {
		p.TargetClientID = &targetClientID.String
	}
	p.CreatedAt = ParseTime(createdAt)
	return p, nil
}

// UpdateProxy 更新代理配置
func (db *DB) UpdateProxy(id int64, name, proxyType, proxyAddress, proxyPassword, targetClientID string) error {
	// 如果密码为空或脱敏值，不更新密码字段
	if proxyPassword == "" || proxyPassword == "******" {
		_, err := db.conn.Exec(
			`UPDATE client_proxies SET name = ?, proxy_type = ?, proxy_address = ?, target_client_id = ? WHERE id = ?`,
			name, proxyType, proxyAddress, nullIfEmpty(targetClientID), id,
		)
		return err
	}

	_, err := db.conn.Exec(
		`UPDATE client_proxies SET name = ?, proxy_type = ?, proxy_address = ?, proxy_password = ?, target_client_id = ? WHERE id = ?`,
		name, proxyType, proxyAddress, proxyPassword, nullIfEmpty(targetClientID), id,
	)
	return err
}

// GetPeerDependentClients 查询所有将指定客户端作为 peer 代理目标的客户端 ID 列表
func (db *DB) GetPeerDependentClients(targetClientID string) ([]string, error) {
	rows, err := db.conn.Query(`
		SELECT DISTINCT ac.client_id
		FROM app_clients ac
		JOIN client_proxies cp ON ac.proxy_id = cp.id
		WHERE cp.target_client_id = ? AND cp.proxy_type = 'peer'
	`, targetClientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var clientIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		clientIDs = append(clientIDs, id)
	}
	return clientIDs, rows.Err()
}

// nullIfEmpty 空字符串转为 nil，非空转为自身（用于 SQL NULL 插入）
func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// DeleteProxy 删除代理配置
// 返回是否有关联的应用使用该代理
func (db *DB) DeleteProxy(id int64) error {
	// 检查是否有关联的应用
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM app_clients WHERE proxy_id = ?",
		id,
	).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("该代理正被 %d 个应用使用，请先解除关联", count)
	}

	_, err = db.conn.Exec("DELETE FROM client_proxies WHERE id = ?", id)
	return err
}

// ProxyInUse 检查代理是否被使用
func (db *DB) ProxyInUse(proxyID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM app_clients WHERE proxy_id = ?",
		proxyID,
	).Scan(&count)
	return count, err
}

// GetProxyByClientAndID 获取代理配置并验证客户端归属
func (db *DB) GetProxyByClientAndID(proxyID int64, clientID string) (*ClientProxy, error) {
	p := &ClientProxy{}
	var createdAt string
	var password sql.NullString
	var targetClientID sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, client_id, name, proxy_type, proxy_address, proxy_password, target_client_id, created_at
		 FROM client_proxies WHERE id = ? AND client_id = ?`,
		proxyID, clientID,
	).Scan(&p.ID, &p.ClientID, &p.Name, &p.ProxyType, &p.ProxyAddress, &password, &targetClientID, &createdAt)
	if err != nil {
		return nil, err
	}
	if password.Valid && password.String != "" {
		p.ProxyPassword = "******"
	}
	if targetClientID.Valid && targetClientID.String != "" {
		p.TargetClientID = &targetClientID.String
	}
	p.CreatedAt = ParseTime(createdAt)
	return p, nil
}
