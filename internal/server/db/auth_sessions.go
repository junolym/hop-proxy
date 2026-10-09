package db

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robin/hop-proxy/pkg/random"
)

// ErrAuthSessionActive 会话仍有效（未退出/未被强制下线且未过期），不允许删除记录
var ErrAuthSessionActive = errors.New("会话仍有效，请先强制下线")

// ErrAuthSessionGrantsActive 会话已失效但仍有生效中的应用授权，不允许删除记录
// （删除会话不会撤销应用访问，先删除/撤销相关授权后再清理会话，#80）
var ErrAuthSessionGrantsActive = errors.New("会话下的应用授权仍有效，请先删除相关授权")

// 登录会话来源（auth_sessions.source，#80）
const (
	SessionSourcePassword  = "password"   // 密码（+TOTP）管理登录
	SessionSourcePasskey   = "passkey"    // 通行密钥管理登录
	SessionSourceTempLogin = "temp_login" // 临时登录票据（PIN + TOTP）
	SessionSourceShareCode = "share_code" // 分享码兑换
	SessionSourceQR        = "qr"         // 扫码授权
)

// 登录会话状态（auth_sessions.status，#80）
const (
	SessionStatusActive    = "active"     // 有效
	SessionStatusLoggedOut = "logged_out" // 主动退出登录
	SessionStatusRevoked   = "revoked"    // 被强制下线
)

// 会话展示状态（派生，状态筛选用，#80）：
// 与前端状态列一一对应，区别于原始 status 列（有效/两种已过期同属 active）
const (
	SessionViewValid             = "valid"               // 有效（未过期且 7 天内有活跃）
	SessionViewIdle              = "idle"                // 不活跃（有效但 7 天无任何活跃）
	SessionViewExpired           = "expired"             // 已过期（无生效授权）
	SessionViewExpiredWithGrants = "expired_with_grants" // 已过期（授权生效中）
)

// sessionIdleThreshold 有效会话被判定为「不活跃」的静默时长（前端状态列同口径）
const sessionIdleThreshold = 7 * 24 * time.Hour

// sessionActivityExpr 展示用活跃时间表达式：max(会话自身活跃时间, 其全部授权的最近使用时间)。
// 一次性会话（分享码/临时登录/扫码）自身活跃时间不再更新，靠授权使用体现。
const sessionActivityExpr = `MAX(COALESCE(s.last_active_at, ''), COALESCE((SELECT MAX(g2.last_request_at) FROM sso_sessions g2 WHERE g2.auth_session_id = s.id), ''))`

// AuthSession 登录会话（会话管理主体，#80）。
// 管理登录（password/passkey）一条可关联多个应用授权（sso_sessions.auth_session_id）；
// 一次性授权流程（temp_login/share_code/qr）各一条，仅关联单个应用。
// 快速登录不产生会话：应用授权归属当前登录会话。
type AuthSession struct {
	ID           int64      `json:"id"`
	SID          string     `json:"-"`        // 管理 JWT 携带的会话标识，不下发前端
	ShortID      string     `json:"short_id"` // SID 前缀（8 位），前端展示与搜索用
	UserID       int64      `json:"user_id"`
	Username     string     `json:"username"` // JOIN 填充
	Source       string     `json:"source"`
	SourceRef    string     `json:"source_ref"` // 来源引用（分享码 ID / 扫码 sid 等）
	Status       string     `json:"status"`
	IP           string     `json:"ip"`
	UserAgent    string     `json:"user_agent"`
	CreatedAt    time.Time  `json:"created_at"`
	LastActiveAt *time.Time `json:"last_active_at"`
	ExpiresAt    time.Time  `json:"expires_at"`
	EndedAt      *time.Time `json:"ended_at"`

	// 列表聚合（ListAuthSessions 填充）
	GrantCount      int  `json:"grant_count"`
	HasActiveGrants bool `json:"has_active_grants"` // 是否仍有生效中的应用授权（过期会话保护删除用）
}

// shortSessionID 返回会话短 ID：长 ID（sid）前 8 位（不足则取全量），用于前端展示与关联搜索
func shortSessionID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
}

// Active 会话是否有效（未退出/未被强制下线且未过期）
func (s *AuthSession) Active() bool {
	return s.Status == SessionStatusActive && time.Now().Before(s.ExpiresAt)
}

// SessionGrant 会话下的应用授权视图（sso_sessions 审计视角，#80）
type SessionGrant struct {
	GrantRef      int64      `json:"grant_ref"` // 单条授权句柄（删除用，SQLite rowid）
	AppID         int64      `json:"app_id"`
	AppName       string     `json:"app_name"` // JOIN 填充（应用已删除时为空）
	Subdomain     string     `json:"subdomain"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	LastRequestAt *time.Time `json:"last_request_at"` // 该授权最近一次被使用的时间（60s 节流更新）
	RevokedAt     *time.Time `json:"revoked_at"`
}

// sessionListLimit 会话列表最大返回条数
const sessionListLimit = 500

// CreateAuthSession 创建登录会话记录，返回含 sid 的会话。
// sid 写入管理 JWT（仅管理登录使用）；一次性授权会话的 sid 仅作内部标识。
func (db *DB) CreateAuthSession(userID int64, source, sourceRef, ip, userAgent string, ttl time.Duration) (*AuthSession, error) {
	now := time.Now().UTC()
	s := &AuthSession{
		SID:       random.Hex(16),
		UserID:    userID,
		Source:    source,
		SourceRef: sourceRef,
		Status:    SessionStatusActive,
		IP:        ip,
		UserAgent: userAgent,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	res, err := db.conn.Exec(
		`INSERT INTO auth_sessions (sid, user_id, source, source_ref, status, ip, user_agent, created_at, last_active_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SID, s.UserID, s.Source, s.SourceRef, s.Status, s.IP, s.UserAgent,
		now.Format(time.RFC3339), now.Format(time.RFC3339), s.ExpiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("创建登录会话失败: %w", err)
	}
	s.ID, _ = res.LastInsertId()
	s.LastActiveAt = &now
	return s, nil
}

// GetAuthSessionBySID 按 sid 查询登录会话（管理 JWT 校验用，#80）
func (db *DB) GetAuthSessionBySID(sid string) (*AuthSession, error) {
	return db.queryAuthSession(
		`SELECT s.id, s.sid, s.user_id, s.source, s.source_ref, s.status, s.ip, s.user_agent,
		        s.created_at, s.last_active_at, s.expires_at, s.ended_at
		 FROM auth_sessions s WHERE s.sid = ?`, sid,
	)
}

// GetAuthSession 按 ID 查询登录会话（会话详情用，#80）
func (db *DB) GetAuthSession(id int64) (*AuthSession, error) {
	s, err := db.queryAuthSession(
		`SELECT s.id, s.sid, s.user_id, s.source, s.source_ref, s.status, s.ip, s.user_agent,
		        s.created_at, s.last_active_at, s.expires_at, s.ended_at
		 FROM auth_sessions s WHERE s.id = ?`, id,
	)
	if err != nil {
		return nil, err
	}
	// 详情补充用户名与聚合
	if user, err := db.GetUserByID(s.UserID); err == nil && user != nil {
		s.Username = user.Username
	}
	var grantLastUsed sql.NullString
	_ = db.conn.QueryRow(
		`SELECT COUNT(token),
		        EXISTS (SELECT 1 FROM sso_sessions WHERE auth_session_id = ? AND revoked_at IS NULL AND expires_at > ?),
		        MAX(last_request_at)
		 FROM sso_sessions WHERE auth_session_id = ?`,
		id, NowUTC(), id,
	).Scan(&s.GrantCount, &s.HasActiveGrants, &grantLastUsed)
	// 展示口径与列表一致：最后活跃 = max(会话自身活跃, 授权最近使用)
	if grantLastUsed.Valid {
		if t := ParseTime(grantLastUsed.String); s.LastActiveAt == nil || t.After(*s.LastActiveAt) {
			s.LastActiveAt = &t
		}
	}
	return s, nil
}

// queryAuthSession 查询单条登录会话的公共实现
func (db *DB) queryAuthSession(query string, args ...any) (*AuthSession, error) {
	s := &AuthSession{}
	var createdAt, expiresAt string
	var lastActive, endedAt sql.NullString
	err := db.conn.QueryRow(query, args...).Scan(
		&s.ID, &s.SID, &s.UserID, &s.Source, &s.SourceRef, &s.Status, &s.IP, &s.UserAgent,
		&createdAt, &lastActive, &expiresAt, &endedAt,
	)
	if err != nil {
		return nil, err
	}
	s.CreatedAt = ParseTime(createdAt)
	s.ExpiresAt = ParseTime(expiresAt)
	s.ShortID = shortSessionID(s.SID)
	if lastActive.Valid {
		t := ParseTime(lastActive.String)
		s.LastActiveAt = &t
	}
	if endedAt.Valid {
		t := ParseTime(endedAt.String)
		s.EndedAt = &t
	}
	return s, nil
}

// authSessionTouchThrottle 会话最后活跃时间的节流间隔
const authSessionTouchThrottle = 60 * time.Second

// TouchAuthSession 节流更新会话最后活跃时间（同一 sid 60 秒内只写一次）。
// 只前进不回退（并发写入时保留较新值）。
func (db *DB) TouchAuthSession(sid string) {
	db.sessionTouchMu.Lock()
	if last, ok := db.sessionTouch[sid]; ok && time.Since(last) < authSessionTouchThrottle {
		db.sessionTouchMu.Unlock()
		return
	}
	db.sessionTouch[sid] = time.Now()
	db.sessionTouchMu.Unlock()

	now := NowUTC()
	db.conn.Exec(
		`UPDATE auth_sessions SET last_active_at = ? WHERE sid = ? AND (last_active_at IS NULL OR last_active_at < ?)`,
		now, sid, now,
	)
}

// EndAuthSession 结束登录会话（退出/强制下线），仅对有效会话生效。
func (db *DB) EndAuthSession(sid, status string) error {
	_, err := db.conn.Exec(
		`UPDATE auth_sessions SET status = ?, ended_at = ? WHERE sid = ? AND status = ?`,
		status, NowUTC(), sid, SessionStatusActive,
	)
	if err == nil {
		db.sessionTouchMu.Lock()
		delete(db.sessionTouch, sid)
		db.sessionTouchMu.Unlock()
	}
	return err
}

// RevokeAuthSession 强制下线会话（管理端操作，#80）：
// 标记 revoked 并级联失效其全部应用授权；会话不存在返回 sql.ErrNoRows，幂等。
func (db *DB) RevokeAuthSession(id int64) error {
	now := NowUTC()
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`UPDATE auth_sessions SET status = ?, ended_at = ? WHERE id = ? AND status = ?`,
		SessionStatusRevoked, now, id, SessionStatusActive,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE sso_sessions SET revoked_at = ? WHERE auth_session_id = ? AND revoked_at IS NULL`,
		now, id,
	); err != nil {
		return err
	}

	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM auth_sessions WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// MarkAllAuthSessionsRevoked 全部有效会话强制失效（重置 JWT 密钥时调用，#80）
func (db *DB) MarkAllAuthSessionsRevoked() error {
	_, err := db.conn.Exec(
		`UPDATE auth_sessions SET status = ?, ended_at = ? WHERE status = ?`,
		SessionStatusRevoked, NowUTC(), SessionStatusActive,
	)
	return err
}

// DeleteAuthSession 删除已失效的会话记录及其应用授权记录（#80）。
// 有效会话不允许删除，返回 ErrAuthSessionActive；会话不存在返回 sql.ErrNoRows。
func (db *DB) DeleteAuthSession(id int64) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var sid, status, expiresAt string
	var hasActiveGrants bool
	err = tx.QueryRow(
		`SELECT a.sid, a.status, a.expires_at,
		        EXISTS (SELECT 1 FROM sso_sessions g WHERE g.auth_session_id = a.id AND g.revoked_at IS NULL AND g.expires_at > ?)
		 FROM auth_sessions a WHERE a.id = ?`,
		NowUTC(), id,
	).Scan(&sid, &status, &expiresAt, &hasActiveGrants)
	if err != nil {
		return err
	}
	// 仍有效（未退出/未被强制下线且未过期）的会话必须先下线
	if status == SessionStatusActive && time.Now().Before(ParseTime(expiresAt)) {
		return ErrAuthSessionActive
	}
	// 已失效但仍有生效中的应用授权：删除会话不会撤销应用访问，先处理授权
	if hasActiveGrants {
		return ErrAuthSessionGrantsActive
	}

	if _, err := tx.Exec(`DELETE FROM sso_sessions WHERE auth_session_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM auth_sessions WHERE id = ?`, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	db.sessionTouchMu.Lock()
	delete(db.sessionTouch, sid)
	db.sessionTouchMu.Unlock()
	return nil
}

// ListAuthSessions 查询登录会话列表（含授权应用数聚合，按最后活跃倒序）。
// userID/source/status 为可选筛选（0/空串表示不筛选）。
func (db *DB) ListAuthSessions(userID int64, source, status string) ([]AuthSession, error) {
	// 展示用「最后活跃」= sessionActivityExpr（max 口径）；仅影响展示与排序，不改变写入（#80）
	query := `SELECT s.id, s.sid, s.user_id, COALESCE(u.username, ''), s.source, s.source_ref, s.status, s.ip, s.user_agent,
	                 s.created_at,
	                 NULLIF(` + sessionActivityExpr + `, '') AS last_active,
	                 s.expires_at, s.ended_at,
	                 COUNT(g.token),
	                 EXISTS (SELECT 1 FROM sso_sessions ga WHERE ga.auth_session_id = s.id AND ga.revoked_at IS NULL AND ga.expires_at > ?)
	          FROM auth_sessions s
	          LEFT JOIN users u ON u.id = s.user_id
	          LEFT JOIN sso_sessions g ON g.auth_session_id = s.id`
	var conds []string
	var args []any
	if userID > 0 {
		conds = append(conds, "s.user_id = ?")
		args = append(args, userID)
	}
	if source != "" {
		conds = append(conds, "s.source = ?")
		args = append(args, source)
	}
	if status != "" {
		// 按展示状态（派生）过滤，与前端状态列一致；logged_out/revoked 与原始值同名
		now := NowUTC()
		idleCutoff := time.Now().UTC().Add(-sessionIdleThreshold).Format(time.RFC3339)
		activeGrantExists := `EXISTS (SELECT 1 FROM sso_sessions ga WHERE ga.auth_session_id = s.id AND ga.revoked_at IS NULL AND ga.expires_at > ?)`
		switch status {
		case SessionViewValid:
			conds = append(conds, "s.status = ? AND s.expires_at > ? AND "+sessionActivityExpr+" >= ?")
			args = append(args, SessionStatusActive, now, idleCutoff)
		case SessionViewIdle:
			conds = append(conds, "s.status = ? AND s.expires_at > ? AND "+sessionActivityExpr+" < ?")
			args = append(args, SessionStatusActive, now, idleCutoff)
		case SessionViewExpired:
			conds = append(conds, "s.status = ? AND s.expires_at <= ? AND NOT "+activeGrantExists)
			args = append(args, SessionStatusActive, now, now)
		case SessionViewExpiredWithGrants:
			conds = append(conds, "s.status = ? AND s.expires_at <= ? AND "+activeGrantExists)
			args = append(args, SessionStatusActive, now, now)
		default:
			conds = append(conds, "s.status = ?")
			args = append(args, status)
		}
	}
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += fmt.Sprintf(" GROUP BY s.id ORDER BY COALESCE(last_active, s.created_at) DESC LIMIT %d", sessionListLimit)

	rows, err := db.conn.Query(query, append([]any{NowUTC()}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []AuthSession
	for rows.Next() {
		var s AuthSession
		var createdAt, expiresAt string
		var lastActive, endedAt sql.NullString
		if err := rows.Scan(
			&s.ID, &s.SID, &s.UserID, &s.Username, &s.Source, &s.SourceRef, &s.Status, &s.IP, &s.UserAgent,
			&createdAt, &lastActive, &expiresAt, &endedAt, &s.GrantCount, &s.HasActiveGrants,
		); err != nil {
			return nil, err
		}
		s.CreatedAt = ParseTime(createdAt)
		s.ExpiresAt = ParseTime(expiresAt)
		s.ShortID = shortSessionID(s.SID)
		if lastActive.Valid {
			t := ParseTime(lastActive.String)
			s.LastActiveAt = &t
		}
		if endedAt.Valid {
			t := ParseTime(endedAt.String)
			s.EndedAt = &t
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// SessionUser 会话筛选下拉项：拥有登录会话的用户
type SessionUser struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
}

// ListSessionUsers 查询拥有登录会话的用户（含管理员，按用户名排序）。
// 供会话管理的用户筛选下拉使用——/api/admin/users 面向用户管理、不含管理员。
func (db *DB) ListSessionUsers() ([]SessionUser, error) {
	rows, err := db.conn.Query(
		`SELECT DISTINCT s.user_id, COALESCE(u.username, '')
		 FROM auth_sessions s
		 LEFT JOIN users u ON u.id = s.user_id
		 ORDER BY 2 ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []SessionUser
	for rows.Next() {
		var u SessionUser
		if err := rows.Scan(&u.UserID, &u.Username); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// DeleteSessionGrant 删除会话下的单条应用授权记录（#80）。
// 仍有效的授权删除即撤销（应用 cookie 立即失效）；不存在返回 sql.ErrNoRows。
func (db *DB) DeleteSessionGrant(authSessionID, grantRef int64) error {
	res, err := db.conn.Exec(
		`DELETE FROM sso_sessions WHERE rowid = ? AND auth_session_id = ?`,
		grantRef, authSessionID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListSessionGrants 查询会话下的应用授权列表（含应用名与最近使用时间）
func (db *DB) ListSessionGrants(authSessionID int64) ([]SessionGrant, error) {
	rows, err := db.conn.Query(
		`SELECT g.rowid, g.app_id, COALESCE(a.name, ''), g.subdomain, g.created_at, g.expires_at, g.last_request_at, g.revoked_at
		 FROM sso_sessions g
		 LEFT JOIN apps a ON a.id = g.app_id
		 WHERE g.auth_session_id = ?
		 ORDER BY g.created_at DESC`, authSessionID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []SessionGrant
	for rows.Next() {
		var g SessionGrant
		var createdAt, expiresAt string
		var lastRequest, revokedAt sql.NullString
		if err := rows.Scan(
			&g.GrantRef, &g.AppID, &g.AppName, &g.Subdomain, &createdAt, &expiresAt, &lastRequest, &revokedAt,
		); err != nil {
			return nil, err
		}
		g.CreatedAt = ParseTime(createdAt)
		g.ExpiresAt = ParseTime(expiresAt)
		if lastRequest.Valid {
			t := ParseTime(lastRequest.String)
			g.LastRequestAt = &t
		}
		if revokedAt.Valid {
			t := ParseTime(revokedAt.String)
			g.RevokedAt = &t
		}
		grants = append(grants, g)
	}
	return grants, rows.Err()
}

// CleanupRetainedAuthSessions 清理超出保留期的失效登录会话（#80）。
// retention <= 0 表示永久保留。返回删除条数。
func (db *DB) CleanupRetainedAuthSessions(retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-retention).Format(time.RFC3339)
	now := NowUTC()
	res, err := db.conn.Exec(
		`DELETE FROM auth_sessions
		 WHERE ((status != ? AND COALESCE(ended_at, expires_at) < ?)
		     OR (status = ? AND expires_at < ?))
		   AND NOT EXISTS (
		       SELECT 1 FROM sso_sessions g
		       WHERE g.auth_session_id = auth_sessions.id
		         AND g.revoked_at IS NULL AND g.expires_at > ?
		   )`,
		SessionStatusActive, cutoff, SessionStatusActive, cutoff, now,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
