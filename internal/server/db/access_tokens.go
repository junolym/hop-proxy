package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/robin/hop-proxy/pkg/random"
)

// CreateAccessToken 创建访问票据，返回完整 token（仅此一次）
func (db *DB) CreateAccessToken(userID int64, name, allowedEntries, allowedAppIDs string, expiresAt *time.Time) (*AccessToken, error) {
	token := random.Hex(32)
	now := NowUTC()

	var expiresAtStr *string
	if expiresAt != nil {
		s := expiresAt.UTC().Format(time.RFC3339)
		expiresAtStr = &s
	}

	result, err := db.conn.Exec(
		`INSERT INTO access_tokens (user_id, name, token, allowed_entries, allowed_app_ids, expires_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, name, token, allowedEntries, allowedAppIDs, expiresAtStr, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("创建访问票据失败: %w", err)
	}

	id, _ := result.LastInsertId()
	t := &AccessToken{
		ID:             id,
		UserID:         userID,
		Name:           name,
		Token:          token,
		AllowedEntries: allowedEntries,
		AllowedAppIDs:  allowedAppIDs,
		ExpiresAt:      expiresAt,
		CreatedAt:      ParseTime(now),
		UpdatedAt:      ParseTime(now),
	}
	return t, nil
}

// ListAccessTokens 列出用户的所有访问票据（token 部分打码）
func (db *DB) ListAccessTokens(userID int64) ([]AccessToken, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, name, token, allowed_entries, allowed_app_ids, expires_at, last_used_at, created_at, updated_at
		 FROM access_tokens WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []AccessToken
	for rows.Next() {
		var t AccessToken
		var expiresAt, lastUsedAt, createdAt, updatedAt sql.NullString
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Token,
			&t.AllowedEntries, &t.AllowedAppIDs,
			&expiresAt, &lastUsedAt, &createdAt, &updatedAt,
		); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			ts := ParseTime(expiresAt.String)
			t.ExpiresAt = &ts
		}
		if lastUsedAt.Valid {
			ts := ParseTime(lastUsedAt.String)
			t.LastUsedAt = &ts
		}
		t.CreatedAt = ParseTime(createdAt.String)
		t.UpdatedAt = ParseTime(updatedAt.String)
		// 打码：只保留前 8 位，其余替换为 *
		if len(t.Token) > 8 {
			t.Token = t.Token[:8] + "************************"
		}
		tokens = append(tokens, t)
	}
	if tokens == nil {
		tokens = []AccessToken{}
	}
	return tokens, nil
}

// GetAccessToken 根据 ID 获取票据（含完整 token，仅内部使用）
func (db *DB) GetAccessToken(id int64) (*AccessToken, error) {
	t := &AccessToken{}
	var expiresAt, lastUsedAt, createdAt, updatedAt sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, user_id, name, token, allowed_entries, allowed_app_ids, expires_at, last_used_at, created_at, updated_at
		 FROM access_tokens WHERE id = ?`,
		id,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Token,
		&t.AllowedEntries, &t.AllowedAppIDs,
		&expiresAt, &lastUsedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		ts := ParseTime(expiresAt.String)
		t.ExpiresAt = &ts
	}
	if lastUsedAt.Valid {
		ts := ParseTime(lastUsedAt.String)
		t.LastUsedAt = &ts
	}
	t.CreatedAt = ParseTime(createdAt.String)
	t.UpdatedAt = ParseTime(updatedAt.String)
	return t, nil
}

// GetAccessTokenByValue 根据 token 值查找票据（用于代理认证）
func (db *DB) GetAccessTokenByValue(token string) (*AccessToken, error) {
	t := &AccessToken{}
	var expiresAt, lastUsedAt, createdAt, updatedAt sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, user_id, name, token, allowed_entries, allowed_app_ids, expires_at, last_used_at, created_at, updated_at
		 FROM access_tokens WHERE token = ?`,
		token,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Token,
		&t.AllowedEntries, &t.AllowedAppIDs,
		&expiresAt, &lastUsedAt, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		ts := ParseTime(expiresAt.String)
		t.ExpiresAt = &ts
	}
	if lastUsedAt.Valid {
		ts := ParseTime(lastUsedAt.String)
		t.LastUsedAt = &ts
	}
	t.CreatedAt = ParseTime(createdAt.String)
	t.UpdatedAt = ParseTime(updatedAt.String)
	return t, nil
}

// UpdateAccessToken 更新票据信息
func (db *DB) UpdateAccessToken(id int64, name, allowedEntries, allowedAppIDs string, expiresAt *time.Time) error {
	now := NowUTC()
	var expiresAtStr *string
	if expiresAt != nil {
		s := expiresAt.UTC().Format(time.RFC3339)
		expiresAtStr = &s
	}
	_, err := db.conn.Exec(
		`UPDATE access_tokens SET name = ?, allowed_entries = ?, allowed_app_ids = ?, expires_at = ?, updated_at = ?
		 WHERE id = ?`,
		name, allowedEntries, allowedAppIDs, expiresAtStr, now, id,
	)
	return err
}

// tokenLastUsedThrottleInterval 访问票据最近使用时间的节流间隔
const tokenLastUsedThrottleInterval = 60 * time.Second

// TouchAccessToken 更新票据最近使用时间（节流：同一 token 60 秒内只写一次）
func (db *DB) TouchAccessToken(id int64) {
	db.tokenLastUsedMu.Lock()
	if last, ok := db.tokenLastUsed[id]; ok && time.Since(last) < tokenLastUsedThrottleInterval {
		db.tokenLastUsedMu.Unlock()
		return
	}
	db.tokenLastUsed[id] = time.Now()
	db.tokenLastUsedMu.Unlock()

	db.conn.Exec(
		`UPDATE access_tokens SET last_used_at = ? WHERE id = ?`,
		NowUTC(), id,
	)
}

// DeleteAccessToken 删除票据
// 同步清理节流 map 中的记录，避免随票据创建-删除历史无界增长
func (db *DB) DeleteAccessToken(id int64) error {
	_, err := db.conn.Exec("DELETE FROM access_tokens WHERE id = ?", id)
	if err == nil {
		db.tokenLastUsedMu.Lock()
		delete(db.tokenLastUsed, id)
		db.tokenLastUsedMu.Unlock()
	}
	return err
}

// Verify 验证票据是否对特定应用和入口有效
// clientID 为空表示服务端入口
func (t *AccessToken) Verify(appID int64, clientID string) bool {
	// 1. 检查过期 (使用 UTC 时间比较)
	if t.ExpiresAt != nil && time.Now().UTC().After(t.ExpiresAt.UTC()) {
		return false
	}

	// 2. 检查授权入口
	var entries AllowedEntriesPayload
	if err := json.Unmarshal([]byte(t.AllowedEntries), &entries); err == nil {
		if clientID == "" {
			if !entries.Server {
				return false
			}
		} else {
			// clients 列表为空且 server=true 时，允许任意客户端
			if len(entries.Clients) == 0 {
				if !entries.Server {
					return false
				}
			} else {
				allowed := false
				for _, cid := range entries.Clients {
					if cid == clientID {
						allowed = true
						break
					}
				}
				if !allowed {
					return false
				}
			}
		}
	}

	// 3. 检查授权应用
	var allowedAppIDs []int64
	if err := json.Unmarshal([]byte(t.AllowedAppIDs), &allowedAppIDs); err == nil {
		if len(allowedAppIDs) > 0 {
			found := false
			for _, id := range allowedAppIDs {
				if id == appID {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}

	return true
}
