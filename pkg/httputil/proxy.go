// Package httputil 提供 HTTP 代理的通用工具函数：SSE keepalive、传输选择、响应流式写回、
// header 转换等。所有代理路径（直连/隧道/peer）共享这些原语。
package httputil

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SSE keepalive 注释行
const sseKeepaliveComment = ": keepalive\n\n"

// SSEKeepaliveInterval SSE 空闲保活间隔
const SSEKeepaliveInterval = 30 * time.Second

// IsSSEResponse 检查响应是否为 SSE（text/event-stream）
func IsSSEResponse(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/event-stream")
}

// IsSSERequest 检查请求是否声明接受 SSE（Accept 含 text/event-stream）。
// 浏览器 EventSource 按 HTML 规范始终发送 Accept: text/event-stream。
func IsSSERequest(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

// StripAcceptEncodingForSSE 对 SSE 请求剔除 Accept-Encoding，防止目标服务对 SSE
// 响应做 gzip/br/zstd 压缩导致流式数据被缓冲。
//
// 块压缩与 SSE 流式语义天然冲突：压缩器在攒满一个 deflate 块之前不会输出，
// 浏览器侧的解压也随之阻塞，事件延迟到达。这是 nginx 处理 SSE 的标准做法
// （proxy_set_header Accept-Encoding ""）。
//
// 仅对声明 Accept: text/event-stream 的请求生效，非 SSE 请求保留端到端压缩协商。
func StripAcceptEncodingForSSE(r *http.Request) {
	if !IsSSERequest(r) {
		return
	}
	r.Header.Del("Accept-Encoding")
}

// SSEKeepaliveWriter 在 SSE 流空闲时自动发送 keepalive 注释行。
// 通过 mutex 串行化 timer callback 与业务 goroutine 对 ResponseWriter 的写入，
// 避免并发写导致响应损坏。timer callback 在发送后自动重新 arm，使长时间空闲的
// SSE 连接也能周期性保活。
type SSEKeepaliveWriter struct {
	mu        sync.Mutex
	w         http.ResponseWriter
	timer     *time.Timer
	isSSE     bool
	stopped   bool
	keepalive []byte
}

// NewSSEKeepaliveWriter 创建 SSE keepalive 写入器
func NewSSEKeepaliveWriter(w http.ResponseWriter, contentType string) *SSEKeepaliveWriter {
	return &SSEKeepaliveWriter{
		w:         w,
		isSSE:     IsSSEResponse(contentType),
		keepalive: []byte(sseKeepaliveComment),
	}
}

// ResetTimer 重置空闲计时器（每次写入数据后调用）
func (k *SSEKeepaliveWriter) ResetTimer() {
	if !k.isSSE {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.resetTimerLocked()
}

// Flush 刷新写入缓冲区
func (k *SSEKeepaliveWriter) Flush() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if f, ok := k.w.(http.Flusher); ok {
		f.Flush()
	}
}

// Write 写入数据并重置计时器
func (k *SSEKeepaliveWriter) Write(data []byte) (int, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	n, err := k.w.Write(data)
	if err == nil && !k.stopped {
		k.resetTimerLocked()
	}
	return n, err
}

// WriteHeader 写入状态码
func (k *SSEKeepaliveWriter) WriteHeader(code int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.w.WriteHeader(code)
}

// Header 返回响应头
func (k *SSEKeepaliveWriter) Header() http.Header {
	return k.w.Header()
}

// Stop 停止 keepalive 计时器。
// 已开始执行的 callback 会通过 stopped 标志跳过写入。
func (k *SSEKeepaliveWriter) Stop() {
	k.mu.Lock()
	k.stopped = true
	if k.timer != nil {
		k.timer.Stop()
	}
	k.mu.Unlock()
}

// sendKeepalive 发送 keepalive 注释行（由 timer callback 调用）。
// 发送后自动重新 arm timer，使长时间空闲的 SSE 连接也能周期性保活。
func (k *SSEKeepaliveWriter) sendKeepalive() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.stopped {
		return
	}
	k.w.Write(k.keepalive)
	if f, ok := k.w.(http.Flusher); ok {
		f.Flush()
	}
	if k.timer != nil {
		k.timer.Reset(SSEKeepaliveInterval)
	}
}

// resetTimerLocked 在持锁状态下重置 timer（调用方必须持有 mu）
func (k *SSEKeepaliveWriter) resetTimerLocked() {
	if !k.isSSE || k.stopped {
		return
	}
	if k.timer == nil {
		k.timer = time.AfterFunc(SSEKeepaliveInterval, k.sendKeepalive)
	} else {
		k.timer.Reset(SSEKeepaliveInterval)
	}
}

// directTransportEntry 带 lastAccessAt 的直连 Transport 缓存条目
type directTransportEntry struct {
	transport    *http.Transport
	lastAccessAt atomic.Int64 // unix nano
}

const (
	directTransportCleanupInterval = 5 * time.Minute  // 清理检查间隔
	directTransportMaxIdle         = 30 * time.Minute // 最大空闲时间，超过则淘汰
)

// directTransports 直连 Transport 缓存，key 为 "serverName|connectMs|readMs"。
// 缓存 key 含超时值（#47）：全局配置变更或应用级覆盖产生新 key 的 Transport，
// 旧参数的 Transport 空闲 30 分钟后自然淘汰，无需主动失效。
var directTransports sync.Map

var directCleanupOnce sync.Once

// GetDirectTransport 获取或创建直连 Transport（缓存 key 含超时值，线程安全）。
// serverName 非空时设置 TLS SNI（HTTPS + 自定义 Host 的虚拟主机场景）。
// connectTimeout 为 TCP/TLS 连接超时，responseHeaderTimeout 为响应头超时；0 表示不限。
func GetDirectTransport(serverName string, connectTimeout, responseHeaderTimeout time.Duration) *http.Transport {
	key := fmt.Sprintf("%s|%d|%d", serverName, connectTimeout.Milliseconds(), responseHeaderTimeout.Milliseconds())
	directCleanupOnce.Do(startDirectTransportCleanup)
	if v, ok := directTransports.Load(key); ok {
		entry := v.(*directTransportEntry)
		entry.lastAccessAt.Store(time.Now().UnixNano())
		return entry.transport
	}
	t := &http.Transport{
		DisableCompression:    true,
		ResponseHeaderTimeout: responseHeaderTimeout,
		MaxIdleConnsPerHost:   100,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		DialContext: (&net.Dialer{
			Timeout: connectTimeout,
		}).DialContext,
	}
	if serverName != "" {
		t.TLSClientConfig = &tls.Config{ServerName: serverName, InsecureSkipVerify: true}
	} else {
		t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	entry := &directTransportEntry{transport: t}
	entry.lastAccessAt.Store(time.Now().UnixNano())
	actual, _ := directTransports.LoadOrStore(key, entry)
	actual.(*directTransportEntry).lastAccessAt.Store(time.Now().UnixNano())
	return actual.(*directTransportEntry).transport
}

// DirectTransportFor 返回直连目标所需的 RoundTripper。
// 当目标是 HTTPS 且设置了自定义 Host 时，使用按 ServerName 缓存的 SNI Transport
// （SNI 用自定义 Host，兼容虚拟主机场景）；否则使用无 SNI 的共享 Transport。
// connectTimeout / responseHeaderTimeout 语义同 GetDirectTransport（#47）。
func DirectTransportFor(targetURL, customHost string, connectTimeout, responseHeaderTimeout time.Duration) http.RoundTripper {
	if strings.HasPrefix(targetURL, "https://") && customHost != "" {
		return GetDirectTransport(customHost, connectTimeout, responseHeaderTimeout)
	}
	return GetDirectTransport("", connectTimeout, responseHeaderTimeout)
}

// startDirectTransportCleanup 启动后台 goroutine 定期清理空闲直连 Transport
func startDirectTransportCleanup() {
	go func() {
		ticker := time.NewTicker(directTransportCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			directTransports.Range(func(key, value any) bool {
				entry := value.(*directTransportEntry)
				last := time.Unix(0, entry.lastAccessAt.Load())
				if now.Sub(last) > directTransportMaxIdle {
					entry.transport.CloseIdleConnections()
					directTransports.Delete(key)
					slog.Debug("清理空闲直连 Transport", "type", "proxy", "cache_key", key, "idle_duration", now.Sub(last).Round(time.Second))
				}
				return true
			})
		}
	}()
}

// HeadersFromHTTP 从 http.Header 转换为 map[string]string
// 多值 header 用逗号拼接（如 Accept: type1, type2），确保 Docker Registry
// 等 API 的 Accept 协商不被截断
func HeadersFromHTTP(h http.Header) map[string]string {
	result := make(map[string]string, len(h))
	for key, vals := range h {
		if len(vals) > 0 {
			if len(vals) == 1 {
				result[key] = vals[0]
			} else {
				result[key] = strings.Join(vals, ", ")
			}
		}
	}
	return result
}

// HostOf 从 customHeaders 提取 Host（大小写不敏感）
func HostOf(customHeaders map[string]string) string {
	for k, v := range customHeaders {
		if strings.EqualFold(k, "Host") {
			return v
		}
	}
	return ""
}

// HostOfURL 从目标 URL 提取 host
func HostOfURL(targetURL string) string {
	if u, err := url.Parse(targetURL); err == nil {
		return u.Host
	}
	return ""
}

// streamingThreshold 超过此 Content-Length 的响应走流式传输，避免全量读入内存
const streamingThreshold = 1 * 1024 * 1024 // 1MB

// IsStreamingResponse 检查 HTTP 响应是否应走流式传输
// 符合以下任一条件即为流式响应：
//   - Content-Type 包含 text/event-stream
//   - Content-Length 未设置（< 0，如 chunked 传输）
//   - Content-Length 超过 streamingThreshold（大文件）
func IsStreamingResponse(resp *http.Response) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/event-stream") {
		return true
	}
	if resp.ContentLength < 0 {
		return true
	}
	if resp.ContentLength > streamingThreshold {
		return true
	}
	return false
}
