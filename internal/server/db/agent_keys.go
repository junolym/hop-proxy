package db

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/google/uuid"
	"golang.org/x/crypto/curve25519"
)

// generateCurve25519KeyPairHex 生成 Curve25519 密钥对，返回 (privateHex, publicHex, error)
func generateCurve25519KeyPairHex() (string, string, error) {
	priv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		return "", "", err
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(priv), hex.EncodeToString(pub), nil
}

// CreateAgentKey 创建安全代理密钥，自动生成 UUID 和 Curve25519 密钥对
func (db *DB) CreateAgentKey(userID int64, name string) (*AgentKey, error) {
	uuid := uuid.New().String() // 标准 UUID v4
	privHex, pubHex, err := generateCurve25519KeyPairHex()
	if err != nil {
		return nil, fmt.Errorf("生成密钥对失败: %w", err)
	}
	now := NowUTC()

	result, err := db.conn.Exec(
		`INSERT INTO agent_keys (user_id, uuid, name, private_key, public_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, uuid, name, privHex, pubHex, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("创建安全代理密钥失败: %w", err)
	}

	id, _ := result.LastInsertId()
	return &AgentKey{
		ID:         id,
		UserID:     userID,
		UUID:       uuid,
		Name:       name,
		PrivateKey: privHex,
		PublicKey:  pubHex,
		CreatedAt:  ParseTime(now),
		UpdatedAt:  ParseTime(now),
	}, nil
}

// ListAgentKeys 列出用户的所有安全代理密钥（不返回密钥内容，仅元数据）
func (db *DB) ListAgentKeys(userID int64) ([]AgentKey, error) {
	rows, err := db.conn.Query(
		`SELECT id, user_id, uuid, name, created_at, updated_at
		 FROM agent_keys WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []AgentKey
	for rows.Next() {
		var k AgentKey
		var createdAt, updatedAt sql.NullString
		if err := rows.Scan(&k.ID, &k.UserID, &k.UUID, &k.Name, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			k.CreatedAt = ParseTime(createdAt.String)
		}
		if updatedAt.Valid {
			k.UpdatedAt = ParseTime(updatedAt.String)
		}
		keys = append(keys, k)
	}
	if keys == nil {
		keys = []AgentKey{}
	}
	return keys, nil
}

// GetAgentKey 根据 ID 获取密钥（含完整私钥，仅内部使用）
func (db *DB) GetAgentKey(id int64) (*AgentKey, error) {
	k := &AgentKey{}
	var createdAt, updatedAt sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, user_id, uuid, name, private_key, public_key, created_at, updated_at
		 FROM agent_keys WHERE id = ?`,
		id,
	).Scan(&k.ID, &k.UserID, &k.UUID, &k.Name, &k.PrivateKey, &k.PublicKey, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if createdAt.Valid {
		k.CreatedAt = ParseTime(createdAt.String)
	}
	if updatedAt.Valid {
		k.UpdatedAt = ParseTime(updatedAt.String)
	}
	return k, nil
}

// GetAgentKeyByUUID 根据 UUID 获取密钥（含完整私钥，用于控制流下发）
func (db *DB) GetAgentKeyByUUID(uuid string) (*AgentKey, error) {
	k := &AgentKey{}
	var createdAt, updatedAt sql.NullString
	err := db.conn.QueryRow(
		`SELECT id, user_id, uuid, name, private_key, public_key, created_at, updated_at
		 FROM agent_keys WHERE uuid = ?`,
		uuid,
	).Scan(&k.ID, &k.UserID, &k.UUID, &k.Name, &k.PrivateKey, &k.PublicKey, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if createdAt.Valid {
		k.CreatedAt = ParseTime(createdAt.String)
	}
	if updatedAt.Valid {
		k.UpdatedAt = ParseTime(updatedAt.String)
	}
	return k, nil
}

// GetAgentKeyPublicKey 根据 UUID 获取公钥（用于 /pubkey 端点，不返回私钥）
func (db *DB) GetAgentKeyPublicKey(uuid string) (string, error) {
	var pubKey string
	err := db.conn.QueryRow(
		`SELECT public_key FROM agent_keys WHERE uuid = ?`,
		uuid,
	).Scan(&pubKey)
	return pubKey, err
}

// UpdateAgentKey 更新密钥名称
func (db *DB) UpdateAgentKey(id int64, name string) error {
	_, err := db.conn.Exec(
		`UPDATE agent_keys SET name = ?, updated_at = ? WHERE id = ?`,
		name, NowUTC(), id,
	)
	return err
}

// DeleteAgentKey 删除密钥
func (db *DB) DeleteAgentKey(id int64) error {
	_, err := db.conn.Exec("DELETE FROM agent_keys WHERE id = ?", id)
	return err
}
