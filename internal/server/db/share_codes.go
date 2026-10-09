package db

import (
	"fmt"
	"time"

	"github.com/robin/hop-proxy/pkg/random"
)

// CreateShareCode 创建分享码
// concreteSubdomain 用于模糊匹配应用：填写命中的具体子域名（如 abc-dev 对应模式 *-dev），
// 兑换时跳转到该子域名而非模式本身；精确匹配应用传空字符串。
func (db *DB) CreateShareCode(userID, appID int64, maxUses, cookieTTL int, expiresAt time.Time, redirectPath, concreteSubdomain string) (*ShareCode, error) {
	now := NowUTC()
	expiresAtStr := expiresAt.UTC().Format(time.RFC3339)

	// 重试生成唯一 code（碰撞概率极低，3 次足够）
	var code string
	for i := 0; i < 3; i++ {
		code = random.ShareCode()
		result, err := db.conn.Exec(
			`INSERT INTO share_codes (user_id, app_id, code, max_uses, use_count, expires_at, cookie_ttl, redirect_path, concrete_subdomain, enabled, created_at, updated_at)
			 VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, 1, ?, ?)`,
			userID, appID, code, maxUses, expiresAtStr, cookieTTL, redirectPath, concreteSubdomain, now, now,
		)
		if err == nil {
			id, _ := result.LastInsertId()
			return &ShareCode{
				ID:                id,
				UserID:            userID,
				AppID:             appID,
				Code:              code,
				MaxUses:           maxUses,
				UseCount:          0,
				ExpiresAt:         expiresAt,
				CookieTTL:         cookieTTL,
				RedirectPath:      redirectPath,
				ConcreteSubdomain: concreteSubdomain,
				Enabled:           true,
				CreatedAt:         ParseTime(now),
				UpdatedAt:         ParseTime(now),
			}, nil
		}
	}
	return nil, fmt.Errorf("生成唯一分享码失败")
}

// ListShareCodes 列出用户的所有分享码（含应用名称和子域名）
func (db *DB) ListShareCodes(userID int64) ([]ShareCode, error) {
	rows, err := db.conn.Query(
		`SELECT sc.id, sc.user_id, sc.app_id, sc.code, sc.max_uses, sc.use_count,
		        sc.expires_at, sc.cookie_ttl, sc.redirect_path, sc.concrete_subdomain, sc.enabled, sc.created_at, sc.updated_at,
		        a.name, a.subdomain
		 FROM share_codes sc
		 JOIN apps a ON sc.app_id = a.id
		 WHERE sc.user_id = ?
		 ORDER BY sc.created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var codes []ShareCode
	for rows.Next() {
		var sc ShareCode
		var expiresAt, createdAt, updatedAt string
		if err := rows.Scan(&sc.ID, &sc.UserID, &sc.AppID, &sc.Code, &sc.MaxUses, &sc.UseCount,
			&expiresAt, &sc.CookieTTL, &sc.RedirectPath, &sc.ConcreteSubdomain, &sc.Enabled, &createdAt, &updatedAt,
			&sc.AppName, &sc.Subdomain,
		); err != nil {
			return nil, err
		}
		sc.ExpiresAt = ParseTime(expiresAt)
		sc.CreatedAt = ParseTime(createdAt)
		sc.UpdatedAt = ParseTime(updatedAt)
		codes = append(codes, sc)
	}
	if codes == nil {
		codes = []ShareCode{}
	}
	return codes, nil
}

// GetShareCode 根据 ID 获取分享码
func (db *DB) GetShareCode(id int64) (*ShareCode, error) {
	sc := &ShareCode{}
	var expiresAt, createdAt, updatedAt string
	err := db.conn.QueryRow(
		`SELECT id, user_id, app_id, code, max_uses, use_count, expires_at, cookie_ttl, redirect_path, concrete_subdomain, enabled, created_at, updated_at
		 FROM share_codes WHERE id = ?`,
		id,
	).Scan(&sc.ID, &sc.UserID, &sc.AppID, &sc.Code, &sc.MaxUses, &sc.UseCount,
		&expiresAt, &sc.CookieTTL, &sc.RedirectPath, &sc.ConcreteSubdomain, &sc.Enabled, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	sc.ExpiresAt = ParseTime(expiresAt)
	sc.CreatedAt = ParseTime(createdAt)
	sc.UpdatedAt = ParseTime(updatedAt)
	return sc, nil
}

// GetShareCodeByCode 根据 code 字符串获取分享码（用于兑换端点）
func (db *DB) GetShareCodeByCode(code string) (*ShareCode, error) {
	sc := &ShareCode{}
	var expiresAt, createdAt, updatedAt string
	err := db.conn.QueryRow(
		`SELECT id, user_id, app_id, code, max_uses, use_count, expires_at, cookie_ttl, redirect_path, concrete_subdomain, enabled, created_at, updated_at
		 FROM share_codes WHERE code = ?`,
		code,
	).Scan(&sc.ID, &sc.UserID, &sc.AppID, &sc.Code, &sc.MaxUses, &sc.UseCount,
		&expiresAt, &sc.CookieTTL, &sc.RedirectPath, &sc.ConcreteSubdomain, &sc.Enabled, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	sc.ExpiresAt = ParseTime(expiresAt)
	sc.CreatedAt = ParseTime(createdAt)
	sc.UpdatedAt = ParseTime(updatedAt)
	return sc, nil
}

// UpdateShareCode 更新分享码（max_uses, cookie_ttl, expires_at, redirect_path, enabled）
func (db *DB) UpdateShareCode(id int64, maxUses, cookieTTL int, expiresAt time.Time, redirectPath string, enabled bool) error {
	now := NowUTC()
	expiresAtStr := expiresAt.UTC().Format(time.RFC3339)
	_, err := db.conn.Exec(
		`UPDATE share_codes SET max_uses = ?, cookie_ttl = ?, expires_at = ?, redirect_path = ?, enabled = ?, updated_at = ?
		 WHERE id = ?`,
		maxUses, cookieTTL, expiresAtStr, redirectPath, enabled, now, id,
	)
	return err
}

// IncrementShareCodeUse 递增分享码使用次数
func (db *DB) IncrementShareCodeUse(id int64) error {
	_, err := db.conn.Exec(
		`UPDATE share_codes SET use_count = use_count + 1, updated_at = ? WHERE id = ?`,
		NowUTC(), id,
	)
	return err
}

// DeleteShareCode 删除分享码
func (db *DB) DeleteShareCode(id int64) error {
	_, err := db.conn.Exec("DELETE FROM share_codes WHERE id = ?", id)
	return err
}
