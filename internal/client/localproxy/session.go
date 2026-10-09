// Package localproxy 实现客户端本地 HTTP 代理功能。
package localproxy

import (
	"strconv"
	"sync"
	"time"
)

// SessionCache SSO Session 验证结果缓存。
// 使用后台定时清理避免每次 Set 都全表扫描（O(N²) 问题），
// 同时设置容量上限防止高基数流量下内存无界增长。
type SessionCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry // key: "appID:token"
	done    chan struct{}
}

type cacheEntry struct {
	verifiedAt time.Time
	ttl        time.Duration
	userID     int64  // 认证返回的用户 ID，用于日志关联
	userName   string // 认证返回的用户名，用于内置变量 ${user_name} (#50)
}

// sessionCacheMaxEntries 最大条目数，超过时触发即时清理
const sessionCacheMaxEntries = 10000

// sessionCacheCleanupInterval 后台清理间隔
const sessionCacheCleanupInterval = 1 * time.Minute

// NewSessionCache 创建 Session 缓存并启动后台清理
func NewSessionCache() *SessionCache {
	c := &SessionCache{
		entries: make(map[string]cacheEntry),
		done:    make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// cacheKey 生成缓存 key
func cacheKey(appID int64, token string) string {
	return strconv.FormatInt(appID, 10) + ":" + token
}

// Get 检查缓存，返回是否有效和 userID/userName (#50)
// tunnelOnline: 隧道是否在线，离线时不检查过期
func (c *SessionCache) Get(appID int64, token string, tunnelOnline bool) (int64, string, bool) {
	if token == "" {
		return 0, "", false
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[cacheKey(appID, token)]
	if !ok {
		return 0, "", false
	}

	// 隧道离线时，不检查过期
	if !tunnelOnline {
		return entry.userID, entry.userName, true
	}

	// 隧道在线时，检查是否过期
	if time.Since(entry.verifiedAt) < entry.ttl {
		return entry.userID, entry.userName, true
	}
	return 0, "", false
}

// Set 设置缓存。超过容量上限时触发即时清理，防止 map 无界增长。
// 过期条目由后台 cleanupLoop 定期清理，Set 不再全表扫描（避免 O(N²)）。
func (c *SessionCache) Set(appID int64, token string, userID int64, userName string, ttl time.Duration) {
	if token == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// 超过容量上限时触发即时清理
	if len(c.entries) >= sessionCacheMaxEntries {
		c.cleanupLocked()
	}

	c.entries[cacheKey(appID, token)] = cacheEntry{
		verifiedAt: time.Now(),
		ttl:        ttl,
		userID:     userID,
		userName:   userName,
	}
}

// cleanupLoop 后台定期清理过期条目
func (c *SessionCache) cleanupLoop() {
	ticker := time.NewTicker(sessionCacheCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			c.cleanupLocked()
			c.mu.Unlock()
		case <-c.done:
			return
		}
	}
}

// Cleanup 手动清理所有过期条目
func (c *SessionCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cleanupLocked()
}

// cleanupLocked 清理过期条目（调用方必须持有写锁）
func (c *SessionCache) cleanupLocked() {
	now := time.Now()
	for k, entry := range c.entries {
		if now.Sub(entry.verifiedAt) >= entry.ttl {
			delete(c.entries, k)
		}
	}
}

// Clear 清空缓存
func (c *SessionCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]cacheEntry)
}

// Close 停止后台清理 goroutine
func (c *SessionCache) Close() {
	close(c.done)
}
