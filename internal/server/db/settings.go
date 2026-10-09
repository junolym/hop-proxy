package db

import (
	"fmt"
	"os"
)

// envOverridable 配置允许通过环境变量覆盖的 settings key 及对应环境变量名。
// 仅这两个域名相关配置允许环境变量覆盖：服务端启动时若设置了对应环境变量，
// 运行时读取的值以环境变量为准，但不写回数据库。
var envOverridable = map[string]string{
	"admin_domain": "HP_ADMIN_DOMAIN",
	"proxy_domain": "HP_PROXY_DOMAIN",
}

// applyEnvOverride 若 key 在 envOverridable 中且对应环境变量非空，返回环境变量值。
// 否则返回原值。
func applyEnvOverride(key, dbVal string) string {
	if envKey, ok := envOverridable[key]; ok {
		if v := os.Getenv(envKey); v != "" {
			return v
		}
	}
	return dbVal
}

// GetSetting 获取单个设置项
func (db *DB) GetSetting(key string) (string, error) {
	var value string
	err := db.conn.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err != nil {
		return "", err
	}
	return applyEnvOverride(key, value), nil
}

// SetSetting 设置单个设置项（不存在则插入）
func (db *DB) SetSetting(key, value string) error {
	_, err := db.conn.Exec(
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, NowUTC(),
	)
	return err
}

// GetAllSettings 获取所有设置项
func (db *DB) GetAllSettings() (map[string]string, error) {
	rows, err := db.conn.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		settings[key] = applyEnvOverride(key, value)
	}
	return settings, rows.Err()
}

// IsInitialized 检查系统是否已初始化
func (db *DB) IsInitialized() (bool, error) {
	val, err := db.GetSetting("initialized")
	if err != nil {
		return false, nil // 设置不存在，视为未初始化
	}
	return val == "true", nil
}

// Initialize 执行初始化（创建管理员 + 写入站点设置）
func (db *DB) Initialize(username, passwordHash, siteName, adminDomain, proxyDomain, jwtSecret string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("开始事务失败: %w", err)
	}
	defer tx.Rollback()

	now := NowUTC()

	// 创建管理员
	_, err = tx.Exec(
		"INSERT INTO users (username, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, 1, ?, ?)",
		username, passwordHash, now, now,
	)
	if err != nil {
		return fmt.Errorf("创建管理员失败: %w", err)
	}

	// 写入设置
	settings := map[string]string{
		"site_name":    siteName,
		"admin_domain": adminDomain,
		"proxy_domain": proxyDomain,
		"jwt_secret":   jwtSecret,
		"session_ttl":  "24h",
		"initialized":  "true",
	}

	for key, value := range settings {
		_, err = tx.Exec(
			"INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)",
			key, value, now,
		)
		if err != nil {
			return fmt.Errorf("写入设置 %s 失败: %w", key, err)
		}
	}

	return tx.Commit()
}
