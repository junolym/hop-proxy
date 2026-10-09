package db

import (
	"database/sql"
	"fmt"
)

// scanWebAuthnCredential 从行扫描凭据字段
func scanWebAuthnCredential(scan func(dest ...any) error) (*WebAuthnCredential, error) {
	c := &WebAuthnCredential{}
	var lastUsedAt, createdAt sql.NullString
	if err := scan(
		&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &c.AttestationType,
		&c.Transport, &c.BackupEligible, &c.BackupState, &c.SignCount,
		&c.DeviceName, &lastUsedAt, &createdAt,
	); err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		t := ParseTime(lastUsedAt.String)
		c.LastUsedAt = &t
	}
	if createdAt.Valid {
		c.CreatedAt = ParseTime(createdAt.String)
	}
	return c, nil
}

// ListWebAuthnCredentials 列出用户的所有通行密钥凭据
func (db *DB) ListWebAuthnCredentials(userID int64) ([]WebAuthnCredential, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, credential_id, public_key, attestation_type, transport,
		        backup_eligible, backup_state, sign_count, device_name, last_used_at, created_at
		 FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []WebAuthnCredential
	for rows.Next() {
		c, err := scanWebAuthnCredential(rows.Scan)
		if err != nil {
			return nil, err
		}
		creds = append(creds, *c)
	}
	if creds == nil {
		creds = []WebAuthnCredential{}
	}
	return creds, nil
}

// CreateWebAuthnCredential 新增通行密钥凭据
func (db *DB) CreateWebAuthnCredential(c *WebAuthnCredential) (int64, error) {
	result, err := db.conn.Exec(
		`INSERT INTO webauthn_credentials
		 (user_id, credential_id, public_key, attestation_type, transport,
		  backup_eligible, backup_state, sign_count, device_name, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.UserID, c.CredentialID, c.PublicKey, c.AttestationType, c.Transport,
		c.BackupEligible, c.BackupState, c.SignCount, c.DeviceName, NowUTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("创建通行密钥凭据失败: %w", err)
	}
	id, _ := result.LastInsertId()
	return id, nil
}

// UpdateWebAuthnCredentialUsage 更新凭据使用状态（签名计数、备份标志、最后使用时间）
func (db *DB) UpdateWebAuthnCredentialUsage(id int64, signCount uint32, backupState bool) error {
	_, err := db.conn.Exec(
		`UPDATE webauthn_credentials SET sign_count = ?, backup_state = ?, last_used_at = ? WHERE id = ?`,
		signCount, backupState, NowUTC(), id,
	)
	return err
}

// UpdateWebAuthnCredentialUsageByCredentialID 按凭据 ID（base64url）更新使用状态，
// 通行密钥登录验证通过后调用
func (db *DB) UpdateWebAuthnCredentialUsageByCredentialID(credentialID string, signCount uint32, backupState bool) error {
	_, err := db.conn.Exec(
		`UPDATE webauthn_credentials SET sign_count = ?, backup_state = ?, last_used_at = ? WHERE credential_id = ?`,
		signCount, backupState, NowUTC(), credentialID,
	)
	return err
}

// RenameWebAuthnCredential 重命名凭据（设备名），仅限本人的凭据
func (db *DB) RenameWebAuthnCredential(id, userID int64, name string) error {
	result, err := db.conn.Exec(
		`UPDATE webauthn_credentials SET device_name = ? WHERE id = ? AND user_id = ?`,
		name, id, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteWebAuthnCredential 删除凭据，仅限本人的凭据
func (db *DB) DeleteWebAuthnCredential(id, userID int64) error {
	result, err := db.conn.Exec(
		`DELETE FROM webauthn_credentials WHERE id = ? AND user_id = ?`,
		id, userID,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteUserWebAuthnCredentials 删除用户全部凭据（用户删除时清理）
func (db *DB) DeleteUserWebAuthnCredentials(userID int64) error {
	_, err := db.conn.Exec("DELETE FROM webauthn_credentials WHERE user_id = ?", userID)
	return err
}
