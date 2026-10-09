// Package ratelimit 提供基于内存的 IP 限流器
package ratelimit

import (
	"sync"
	"time"
)

// RateLimiter 基于 IP 的频率限制器（内存存储）
type RateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attempt
	maxCount int
	window   time.Duration
	done     chan struct{}
}

type attempt struct {
	count   int
	firstAt time.Time
}

// New 创建新的限流器
func New(maxCount int, window time.Duration) *RateLimiter {
	l := &RateLimiter{
		attempts: make(map[string]*attempt),
		maxCount: maxCount,
		window:   window,
		done:     make(chan struct{}),
	}
	go l.cleanupLoop()
	return l
}

// cleanupLoop 后台定期清理过期条目，避免 map 无限增长
// IsBlocked 不再承担清理职责，只做 O(1) 查询，消除攻击流量下的 O(n) 放大效应
func (l *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(l.window)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			now := time.Now()
			for k, a := range l.attempts {
				if now.Sub(a.firstAt) > l.window {
					delete(l.attempts, k)
				}
			}
			l.mu.Unlock()
		case <-l.done:
			return
		}
	}
}

// Stop 停止清理 goroutine，释放资源
func (l *RateLimiter) Stop() {
	close(l.done)
}

// IsBlocked 检查 IP 是否被限制，返回剩余封锁秒数（0 表示未封锁）
// 只做 O(1) 查询，清理逻辑在后台 cleanupLoop 中运行
func (l *RateLimiter) IsBlocked(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.attempts[ip]
	if !ok {
		return 0
	}
	if a.count >= l.maxCount {
		remaining := l.window - time.Since(a.firstAt)
		return int(remaining.Seconds()) + 1
	}
	return 0
}

// maxAttemptsEntries 限流 map 最大条目数，防止伪造 X-Forwarded-For 导致无界增长。
// 超过时触发即时清理，淘汰过期或最旧的条目。
const maxAttemptsEntries = 10000

// Record 记录一次失败
func (l *RateLimiter) Record(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[ip]
	if !ok || time.Since(a.firstAt) > l.window {
		// 新条目：检查容量上限，超过时清理过期条目
		if len(l.attempts) >= maxAttemptsEntries {
			l.cleanupExpiredLocked()
		}
		l.attempts[ip] = &attempt{count: 1, firstAt: time.Now()}
		return
	}
	a.count++
}

// cleanupExpiredLocked 清理过期条目（调用方必须持有锁）
func (l *RateLimiter) cleanupExpiredLocked() {
	now := time.Now()
	for k, a := range l.attempts {
		if now.Sub(a.firstAt) > l.window {
			delete(l.attempts, k)
		}
	}
}

// Reset 成功后清零
func (l *RateLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}
