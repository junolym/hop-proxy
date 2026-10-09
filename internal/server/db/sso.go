package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/robin/hop-proxy/pkg/random"
)

// SSOCookieName 返回指定应用+具体子域名的 SSO Cookie 名称。
// 子域名不同则 cookie name 不同，防止通配符应用跨子域 cookie 串号 (#40)。
// subdomain 应为请求 host 提取的具体子域名（已小写，不含通配符 `*` / `/`）。
func SSOCookieName(appID int64, subdomain string) string {
	return fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
}

// CreateSSOSession 为指定应用+具体子域名创建 SSO 会话（授权），返回 token。
// subdomain 记入 DB，后续验证时与请求 host 提取的子域名比对，防止伪造 cookie name。
// authSessionID 为归属的登录会话（#80，0 表示无归属）；会话管理页据此展示
// "某会话授权了哪些应用"。
func (db *DB) CreateSSOSession(userID, appID int64, subdomain string, ttl time.Duration, authSessionID int64) (string, error) {
	token := random.Hex(32)

	expiresAt := time.Now().Add(ttl).UTC().Format(time.RFC3339)
	now := NowUTC()

	_, err := db.conn.Exec(
		`INSERT INTO sso_sessions (token, user_id, app_id, subdomain, auth_session_id, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		token, userID, appID, subdomain, authSessionID, expiresAt, now,
	)
	if err != nil {
		return "", fmt.Errorf("创建 SSO 会话失败: %w", err)
	}

	return token, nil
}

// GetSSOSession 根据 token 和 appID 获取有效会话（含 subdomain 字段，由调用方比对）。
// 已撤销（revoked_at 非空）或已过期返回 sql.ErrNoRows。
// 过期不再删行（#80：审计保留，由每日清理任务按保留期处理）。
func (db *DB) GetSSOSession(token string, appID int64) (*SSOSession, error) {
	s := &SSOSession{}
	var expiresAt, createdAt string
	err := db.conn.QueryRow(
		"SELECT token, user_id, app_id, subdomain, auth_session_id, expires_at, created_at FROM sso_sessions WHERE token = ? AND app_id = ? AND revoked_at IS NULL",
		token, appID,
	).Scan(&s.Token, &s.UserID, &s.AppID, &s.Subdomain, &s.AuthSessionID, &expiresAt, &createdAt)
	if err != nil {
		return nil, err
	}

	s.ExpiresAt = ParseTime(expiresAt)
	s.CreatedAt = ParseTime(createdAt)

	if time.Now().After(s.ExpiresAt) {
		return nil, sql.ErrNoRows
	}

	return s, nil
}

// ssoSessionTouchThrottle 应用授权最近使用时间的节流间隔
const ssoSessionTouchThrottle = 60 * time.Second

// TouchSSOSessionRequest 节流更新应用授权的最近使用时间（#80）：
// 同一 token 60 秒内只写一次；首次使用立即写入。
// 调用点：经服务端的 SSO 校验（proxy/sso.go）、本地代理远程校验（tunnel/handler.go，Path 3/4）。
func (db *DB) TouchSSOSessionRequest(token string) {
	if token == "" {
		return
	}
	db.grantTouchMu.Lock()
	if last, ok := db.grantTouch[token]; ok && time.Since(last) < ssoSessionTouchThrottle {
		db.grantTouchMu.Unlock()
		return
	}
	db.grantTouch[token] = time.Now()
	db.grantTouchMu.Unlock()

	db.conn.Exec(
		"UPDATE sso_sessions SET last_request_at = ? WHERE token = ?",
		NowUTC(), token,
	)
}

// MarkUserSSOSessionsRevoked 标记用户所有应用授权失效（退出登录时调用，#80）。
// 不删行：cookie 由 revoked_at 判定作废，记录保留供会话管理审计。
func (db *DB) MarkUserSSOSessionsRevoked(userID int64) error {
	_, err := db.conn.Exec(
		"UPDATE sso_sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL",
		NowUTC(), userID,
	)
	return err
}

// RevokeUserAppSSOSessions 撤销用户在指定应用上的所有应用授权（标记失效，#80）。
// cookie name 含子域名后缀无法精确读取，按 user+app 批量标记。
func (db *DB) RevokeUserAppSSOSessions(userID, appID int64) error {
	_, err := db.conn.Exec(
		"UPDATE sso_sessions SET revoked_at = ? WHERE user_id = ? AND app_id = ? AND revoked_at IS NULL",
		NowUTC(), userID, appID,
	)
	return err
}

// CleanupRetainedSSOSessions 清理超出保留期的已失效应用授权（#80）。
// 已撤销按 revoked_at、已过期按 expires_at 判断；retention <= 0 表示永久保留。
func (db *DB) CleanupRetainedSSOSessions(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339)
	res, err := db.conn.Exec(
		`DELETE FROM sso_sessions
		 WHERE (revoked_at IS NOT NULL AND revoked_at < ?)
		    OR (revoked_at IS NULL AND expires_at < ?)`,
		cutoff, cutoff,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
