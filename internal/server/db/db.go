// Package db 提供 SQLite 数据库操作。
package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB 封装数据库连接
type DB struct {
	conn *sql.DB
	// 访问票据最近使用时间节流
	tokenLastUsedMu sync.Mutex
	tokenLastUsed   map[int64]time.Time

	// 登录会话最后活跃时间节流（#80）
	sessionTouchMu sync.Mutex
	sessionTouch   map[string]time.Time

	// 应用授权（SSO 会话）最近使用时间节流（#80）
	grantTouchMu sync.Mutex
	grantTouch   map[string]time.Time
}

// Open 打开数据库并执行迁移
func Open(dbPath string) (*DB, error) {
	// 确保目录存在
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("创建数据库目录失败: %w", err)
	}

	conn, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(on)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// SQLite 不支持并发写，限制为单连接避免 SQLITE_BUSY
	conn.SetMaxOpenConns(1)

	// 测试连接
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("数据库连接测试失败: %w", err)
	}

	db := &DB{
		conn:          conn,
		tokenLastUsed: make(map[int64]time.Time),
		sessionTouch:  make(map[string]time.Time),
		grantTouch:    make(map[string]time.Time),
	}

	// 执行迁移
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}

	slog.Info("数据库已就绪", "type", "system", "path", dbPath)
	return db, nil
}

// Close 关闭数据库连接
func (db *DB) Close() error {
	return db.conn.Close()
}

// migrate 执行数据库迁移
func (db *DB) migrate() error {
	// 获取当前版本
	var version int
	err := db.conn.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		return fmt.Errorf("读取数据库版本失败: %w", err)
	}

	// 执行未应用的迁移
	for i := version; i < len(migrations); i++ {
		migrationVersion := i + 1
		slog.Info("开始执行数据库迁移", "type", "system", "version", migrationVersion)

		// 使用事务确保原子性
		tx, err := db.conn.Begin()
		if err != nil {
			return fmt.Errorf("迁移 %d 开启事务失败: %w", migrationVersion, err)
		}

		// 将迁移 SQL 按分号分割为独立语句，逐条执行
		statements := splitSQLStatements(migrations[i])
		for j, stmt := range statements {
			if stmt == "" {
				continue
			}
			slog.Debug("执行迁移语句", "type", "system", "version", migrationVersion, "statement", j+1, "sql", truncateSQL(stmt))
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("迁移 %d 语句 %d 失败: %w\nSQL: %s", migrationVersion, j+1, err, stmt)
			}
		}

		// 更新版本号
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", migrationVersion)); err != nil {
			tx.Rollback()
			return fmt.Errorf("迁移 %d 更新版本号失败: %w", migrationVersion, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("迁移 %d 提交事务失败: %w", migrationVersion, err)
		}

		slog.Info("数据库迁移完成", "type", "system", "version", migrationVersion)
	}

	return nil
}

// splitSQLStatements 将 SQL 脚本按分号分割为独立语句
func splitSQLStatements(sql string) []string {
	var statements []string
	var current strings.Builder
	inString := false
	stringChar := byte(0)

	for i := 0; i < len(sql); i++ {
		ch := sql[i]

		// 处理字符串边界
		if !inString && (ch == '\'' || ch == '"') {
			inString = true
			stringChar = ch
			current.WriteByte(ch)
			continue
		}
		if inString && ch == stringChar {
			// 检查是否是转义引号
			if i+1 < len(sql) && sql[i+1] == stringChar {
				current.WriteByte(ch)
				current.WriteByte(ch)
				i++
				continue
			}
			inString = false
			current.WriteByte(ch)
			continue
		}

		// 在字符串内，直接添加字符
		if inString {
			current.WriteByte(ch)
			continue
		}

		// 遇到分号，结束当前语句
		if ch == ';' {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}

		current.WriteByte(ch)
	}

	// 处理最后一条没有分号结尾的语句
	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

// truncateSQL 截断 SQL 用于日志输出
func truncateSQL(sql string) string {
	const maxLen = 100
	if len(sql) <= maxLen {
		return sql
	}
	return sql[:maxLen] + "..."
}
