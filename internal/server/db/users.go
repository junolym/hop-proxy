package db

import (
	"database/sql"
	"fmt"

	"github.com/robin/hop-proxy/pkg/random"
)

// GetUserByUsername 根据用户名查询用户
func (db *DB) GetUserByUsername(username string) (*User, error) {
	user := &User{}
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		"SELECT id, username, password_hash, is_admin, COALESCE(role, 'user'), totp_enabled, COALESCE(totp_secret, ''), temp_login_enabled, COALESCE(temp_login_pin, ''), quick_login, auto_disable_days, app_api_enabled, COALESCE(app_api_token, ''), created_at, updated_at FROM users WHERE username = ?",
		username,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.IsAdmin, &user.Role, &user.TOTPEnabled, &user.TOTPSecret, &user.TempLoginEnabled, &user.TempLoginPIN, &user.QuickLogin, &user.AutoDisableDays, &user.AppAPIEnabled, &user.AppAPIToken, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if user.Role == "" {
		user.Role = "user"
	}
	user.CreatedAt = ParseTime(createdAt)
	user.UpdatedAt = ParseTime(updatedAt)
	return user, nil
}

// GetUserByID 根据 ID 查询用户
func (db *DB) GetUserByID(id int64) (*User, error) {
	user := &User{}
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		"SELECT id, username, password_hash, is_admin, COALESCE(role, 'user'), totp_enabled, COALESCE(totp_secret, ''), temp_login_enabled, COALESCE(temp_login_pin, ''), quick_login, auto_disable_days, app_api_enabled, COALESCE(app_api_token, ''), created_at, updated_at FROM users WHERE id = ?",
		id,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.IsAdmin, &user.Role, &user.TOTPEnabled, &user.TOTPSecret, &user.TempLoginEnabled, &user.TempLoginPIN, &user.QuickLogin, &user.AutoDisableDays, &user.AppAPIEnabled, &user.AppAPIToken, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if user.Role == "" {
		user.Role = "user"
	}
	user.CreatedAt = ParseTime(createdAt)
	user.UpdatedAt = ParseTime(updatedAt)
	return user, nil
}

// ListUsers 列出所有用户（排除管理员）
func (db *DB) ListUsers() ([]User, error) {
	rows, err := db.conn.Query(
		"SELECT id, username, is_admin, COALESCE(role, 'user'), auto_disable_days, created_at, updated_at FROM users WHERE is_admin = 0 ORDER BY id ASC",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var createdAt, updatedAt string
		if err := rows.Scan(&u.ID, &u.Username, &u.IsAdmin, &u.Role, &u.AutoDisableDays, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if u.Role == "" {
			u.Role = "user"
		}
		u.CreatedAt = ParseTime(createdAt)
		u.UpdatedAt = ParseTime(updatedAt)
		users = append(users, u)
	}
	return users, rows.Err()
}

// CreateUser 创建用户
func (db *DB) CreateUser(username, passwordHash string, isAdmin bool, role string) (*User, error) {
	if role == "" {
		role = "user"
	}
	now := NowUTC()
	result, err := db.conn.Exec(
		"INSERT INTO users (username, password_hash, is_admin, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		username, passwordHash, isAdmin, role, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	t := ParseTime(now)
	return &User{ID: id, Username: username, IsAdmin: isAdmin, Role: role, CreatedAt: t, UpdatedAt: t}, nil
}

// UpdateUser 修改用户（用户名）
func (db *DB) UpdateUser(id int64, username string) error {
	result, err := db.conn.Exec(
		"UPDATE users SET username = ?, updated_at = ? WHERE id = ?",
		username, NowUTC(), id,
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

// UpdateUserRole 更新用户角色（不允许设为 admin）
func (db *DB) UpdateUserRole(id int64, role string) error {
	if role != "user" && role != "guest" {
		return fmt.Errorf("无效的角色值: %s", role)
	}
	result, err := db.conn.Exec(
		"UPDATE users SET role = ?, updated_at = ? WHERE id = ? AND is_admin = 0",
		role, NowUTC(), id,
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

// DeleteUser 删除用户
func (db *DB) DeleteUser(id int64) error {
	result, err := db.conn.Exec("DELETE FROM users WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdatePassword 更新用户密码
func (db *DB) UpdatePassword(userID int64, passwordHash string) error {
	result, err := db.conn.Exec(
		"UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?",
		passwordHash, NowUTC(), userID,
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

// CountUsers 统计用户数
func (db *DB) CountUsers() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count, err
}

// UserExists 检查用户名是否已存在
func (db *DB) UserExists(username string) (bool, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", username).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("检查用户是否存在失败: %w", err)
	}
	return count > 0, nil
}

// UserExistsExcluding 检查用户名是否已存在（排除指定 ID）
func (db *DB) UserExistsExcluding(username string, excludeID int64) (bool, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COUNT(*) FROM users WHERE username = ? AND id != ?",
		username, excludeID,
	).Scan(&count)
	return count > 0, err
}

// UpdateTOTPSecret 更新用户的 TOTP 密钥（启用前设置）
func (db *DB) UpdateTOTPSecret(userID int64, secret string) error {
	result, err := db.conn.Exec(
		"UPDATE users SET totp_secret = ?, updated_at = ? WHERE id = ?",
		secret, NowUTC(), userID,
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

// EnableTOTP 启用用户的 TOTP 二次验证
func (db *DB) EnableTOTP(userID int64) error {
	result, err := db.conn.Exec(
		"UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?",
		NowUTC(), userID,
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

// DisableTOTP 禁用用户的 TOTP 二次验证。
// 密钥（totp_secret）始终保留——一旦生成即持久存储，避免重复绑定；
// 重置密钥请使用 ResetTOTPSecret。
func (db *DB) DisableTOTP(userID int64) error {
	result, err := db.conn.Exec(
		"UPDATE users SET totp_enabled = 0, updated_at = ? WHERE id = ?",
		NowUTC(), userID,
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

// ResetTOTPSecret 清空用户的 TOTP 密钥（独立的重置功能）。
// 调用前应确保 totp_enabled=false 且 temp_login_enabled=false，
// 否则正在使用中的密钥被清空会导致功能失效。
func (db *DB) ResetTOTPSecret(userID int64) error {
	result, err := db.conn.Exec(
		"UPDATE users SET totp_secret = '', updated_at = ? WHERE id = ?",
		NowUTC(), userID,
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

// UpdateAutoDisableDays 更新用户自动禁用天数
func (db *DB) UpdateAutoDisableDays(userID int64, days int) error {
	result, err := db.conn.Exec(
		"UPDATE users SET auto_disable_days = ?, updated_at = ? WHERE id = ?",
		days, NowUTC(), userID,
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

// UpdateTempLogin 更新临时登录开关与 PIN。
// totp_secret 始终保留（一旦生成即持久存储），本函数不清空密钥；
// 重置密钥请使用 ResetTOTPSecret。
// 若 pin 为空字符串，保留原 PIN 不变（用于仅切换开关的场景）。
func (db *DB) UpdateTempLogin(userID int64, enabled bool, pin string) error {
	if pin == "" {
		_, err := db.conn.Exec(
			"UPDATE users SET temp_login_enabled = ?, updated_at = ? WHERE id = ?",
			enabled, NowUTC(), userID,
		)
		return err
	}
	_, err := db.conn.Exec(
		"UPDATE users SET temp_login_enabled = ?, temp_login_pin = ?, updated_at = ? WHERE id = ?",
		enabled, pin, NowUTC(), userID,
	)
	return err
}

// UpdateQuickLogin 更新用户的 SSO 快速登录开关。
// 启用后，/sso 页面在管理端 session 有效时静默下发新 SSO cookie 并 302 回目标应用。
func (db *DB) UpdateQuickLogin(userID int64, enabled bool) error {
	result, err := db.conn.Exec(
		"UPDATE users SET quick_login = ?, updated_at = ? WHERE id = ?",
		enabled, NowUTC(), userID,
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

// UpdateAppAPI 启用/禁用用户的应用 API。
// 启用时若 token 为空则自动生成并返回新 token；禁用时清空 token。
// 启用且 token 已存在时返回空字符串（不重复返回已有 token）。
func (db *DB) UpdateAppAPI(userID int64, enabled bool) (string, error) {
	if enabled {
		// 先查询现有 token
		var existingToken string
		err := db.conn.QueryRow(
			"SELECT COALESCE(app_api_token, '') FROM users WHERE id = ?",
			userID,
		).Scan(&existingToken)
		if err != nil {
			return "", err
		}
		if existingToken != "" {
			// 已有 token，仅置 enabled=1，不返回 token（前端可通过专用接口查看）
			_, err := db.conn.Exec(
				"UPDATE users SET app_api_enabled = 1, updated_at = ? WHERE id = ?",
				NowUTC(), userID,
			)
			return "", err
		}
		// 无 token，生成新 token 并启用
		newToken := random.Hex(32)
		_, err = db.conn.Exec(
			"UPDATE users SET app_api_enabled = 1, app_api_token = ?, updated_at = ? WHERE id = ?",
			newToken, NowUTC(), userID,
		)
		if err != nil {
			return "", err
		}
		return newToken, nil
	}
	// 禁用：清空 token 与开关
	_, err := db.conn.Exec(
		"UPDATE users SET app_api_enabled = 0, app_api_token = '', updated_at = ? WHERE id = ?",
		NowUTC(), userID,
	)
	return "", err
}

// ResetAppAPIToken 重置用户的应用 API token，并确保开关为启用状态。
// 返回新生成的 token。
func (db *DB) ResetAppAPIToken(userID int64) (string, error) {
	newToken := random.Hex(32)
	_, err := db.conn.Exec(
		"UPDATE users SET app_api_enabled = 1, app_api_token = ?, updated_at = ? WHERE id = ?",
		newToken, NowUTC(), userID,
	)
	if err != nil {
		return "", err
	}
	return newToken, nil
}

// GetUserByAppAPIToken 根据 token 值查找启用了应用 API 的用户（用于 /external-api/ 鉴权）
func (db *DB) GetUserByAppAPIToken(token string) (*User, error) {
	user := &User{}
	var createdAt, updatedAt string
	err := db.conn.QueryRow(
		"SELECT id, username, password_hash, is_admin, COALESCE(role, 'user'), totp_enabled, COALESCE(totp_secret, ''), temp_login_enabled, COALESCE(temp_login_pin, ''), quick_login, auto_disable_days, app_api_enabled, COALESCE(app_api_token, ''), created_at, updated_at FROM users WHERE app_api_token = ? AND app_api_enabled = 1",
		token,
	).Scan(&user.ID, &user.Username, &user.PasswordHash, &user.IsAdmin, &user.Role, &user.TOTPEnabled, &user.TOTPSecret, &user.TempLoginEnabled, &user.TempLoginPIN, &user.QuickLogin, &user.AutoDisableDays, &user.AppAPIEnabled, &user.AppAPIToken, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if user.Role == "" {
		user.Role = "user"
	}
	user.CreatedAt = ParseTime(createdAt)
	user.UpdatedAt = ParseTime(updatedAt)
	return user, nil
}
