// Package logsink 提供客户端异步日志收集 + 批量上报。
//
// 设计：
//   - slog.Handler 接管现有 slog.Info/Error/... 调用，无需修改业务代码
//   - 异步 ring buffer（默认 1000 条）：满了按级别优先丢 debug → info → warning → error → critical
//   - 批量上报：1 秒定时 / 满 100 条 / 累计 64KB 三选最早
//   - 隧道断开时 buffer 保留，重连后自动补发
//   - 单批上限 200KB（pkgTunnel.LogBatchMaxBytes）
package logsink

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// Sink 异步日志收集 + 批量上报
type Sink struct {
	// 入口 channel（带小缓冲，避免 Append 阻塞业务）
	entryCh chan *pkgTunnel.LogEntry

	// ring buffer（单 goroutine 拥有，无需锁）
	// 用值类型存储与 pkgTunnel.LogBatch.Logs 一致，flush 时直接赋值
	buffer      []pkgTunnel.LogEntry
	bufferBytes int
	maxBuffer   int // ring buffer 容量
	maxBatch    int // 单批最大条数
	maxBytes    int // 单批最大字节数

	// reporter（可能为 nil，nil 时只缓冲不上报）
	reporter *Reporter

	// flush 间隔
	flushInterval time.Duration

	// 统计
	droppedCount atomic.Int64

	cancel context.CancelFunc
	done   chan struct{}
}

// Option Sink 配置项
type Option func(*Sink)

func WithMaxBuffer(n int) Option               { return func(s *Sink) { s.maxBuffer = n } }
func WithMaxBatch(n int) Option                { return func(s *Sink) { s.maxBatch = n } }
func WithMaxBytes(n int) Option                { return func(s *Sink) { s.maxBytes = n } }
func WithFlushInterval(d time.Duration) Option { return func(s *Sink) { s.flushInterval = d } }

// New 创建 Sink
//   - reporter 为 nil 时只缓冲不上报（用于测试或离线模式）
func New(reporter *Reporter, opts ...Option) *Sink {
	s := &Sink{
		entryCh:       make(chan *pkgTunnel.LogEntry, 256),
		maxBuffer:     1000,
		maxBatch:      100,
		maxBytes:      64 * 1024,
		flushInterval: 1 * time.Second,
		reporter:      reporter,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Append 异步加入一条日志（非阻塞，channel 满则丢）
func (s *Sink) Append(entry *pkgTunnel.LogEntry) {
	select {
	case s.entryCh <- entry:
	default:
		// channel 满了（罕见，说明 run goroutine 卡住），丢弃
		s.droppedCount.Add(1)
	}
}

// Run 启动后台 goroutine 处理日志缓冲与上报
// 调用方应在单独 goroutine 中调用：go sink.Run(ctx)
func (s *Sink) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	defer close(s.done)

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// 退出前最后 flush（带短超时）
			s.flushWithTimeout(2 * time.Second)
			return

		case e := <-s.entryCh:
			s.addToBuffer(e)
			// 触发条件检查
			if len(s.buffer) >= s.maxBatch || s.bufferBytes >= s.maxBytes {
				s.flush()
			}

		case <-ticker.C:
			if len(s.buffer) > 0 {
				s.flush()
			}
		}
	}
}

// Stop 停止 Sink（触发 Run 退出并等待）
func (s *Sink) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.done != nil {
		<-s.done
	}
}

// DroppedCount 返回因 channel 满/级别淘汰而丢弃的日志总数
func (s *Sink) DroppedCount() int64 {
	return s.droppedCount.Load()
}

// addToBuffer 把 entry 加入 ring buffer。
// 满时按级别优先淘汰最老的 debug → info → warning → error → critical
func (s *Sink) addToBuffer(e *pkgTunnel.LogEntry) {
	if len(s.buffer) >= s.maxBuffer {
		s.evictByLevel()
	}
	s.buffer = append(s.buffer, *e)
	s.bufferBytes += estimateEntrySize(e)
}

// evictByLevel 淘汰一条最老的、级别最低的条目
func (s *Sink) evictByLevel() {
	// 按淘汰优先级遍历：debug 最先被丢
	levels := []string{"debug", "info", "warning", "error", "critical"}
	for _, target := range levels {
		for i := range s.buffer {
			if s.buffer[i].Level == target {
				s.removeFromBuffer(i)
				return
			}
		}
	}
	// 没找到匹配级别（不应该），丢第一个
	if len(s.buffer) > 0 {
		s.removeFromBuffer(0)
	}
}

func (s *Sink) removeFromBuffer(idx int) {
	e := s.buffer[idx]
	s.buffer = append(s.buffer[:idx], s.buffer[idx+1:]...)
	s.bufferBytes -= estimateEntrySize(&e)
	s.droppedCount.Add(1)
}

// flush 把当前 buffer 全部上报，清空 buffer
func (s *Sink) flush() {
	if len(s.buffer) == 0 || s.reporter == nil {
		return
	}

	batch := &pkgTunnel.LogBatch{
		ClientNow: time.Now().UnixMilli(),
		Logs:      s.buffer,
	}

	err := s.reporter.Send(batch)
	if err != nil {
		// 发送失败：保留 buffer（不清空），下次 flush 重试
		// 但如果 buffer 已经接近上限，主动淘汰一些以释放内存
		if len(s.buffer) > s.maxBuffer*4/5 {
			// 淘汰一半最老的
			half := len(s.buffer) / 2
			for i := 0; i < half; i++ {
				s.removeFromBuffer(0)
			}
		}
		return
	}

	// 发送成功，清空 buffer
	s.buffer = s.buffer[:0]
	s.bufferBytes = 0
}

// flushWithTimeout 在指定时间内尝试 flush，超时放弃
func (s *Sink) flushWithTimeout(timeout time.Duration) {
	if len(s.buffer) == 0 || s.reporter == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.flush()
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// estimateEntrySize 估算 LogEntry 序列化后字节数
// 用于触发 maxBytes 条件，避免精确计算开销
func estimateEntrySize(e *pkgTunnel.LogEntry) int {
	// 粗略估算：每个字段平均 50 字节，加上消息长度
	size := 100 + len(e.Message) + len(e.Subdomain) + len(e.RequestID) + len(e.Type) + len(e.Level)
	for _, f := range e.Fields {
		size += len(f.K) + len(f.V) + 8
	}
	return size
}

// —— slog.Handler 实现 ——

// Handler 实现 slog.Handler，把 slog 调用转入 Sink
//
// attrs 字段不可变：WithAttrs 返回新 Handler 时只复制 slice。
type Handler struct {
	sink   *Sink
	attrs  []slog.Attr
	groups []string
}

// NewHandler 创建 slog.Handler
func NewHandler(sink *Sink) *Handler {
	return &Handler{sink: sink}
}

// NewLogger 创建 slog.Logger 并设为默认
func NewLogger(sink *Sink) *slog.Logger {
	return slog.New(NewHandler(sink))
}

func (h *Handler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	attrs := make([]pkgTunnel.Field, 0, len(h.attrs)+r.NumAttrs())
	for _, a := range h.attrs {
		attrs = append(attrs, slogAttrToField(a))
	}

	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slogAttrToField(a))
		return true
	})

	// 从 attrs 提取专用字段（客户端日志全走显式 attrs：type/subdomain/user_id/
	// request_id 由调用点或 hop.LogAttrs() 提供）
	levelStr := levelToString(r.Level)
	entryType := ""
	subdomain := ""
	var userID *int64
	if v, ok := popField(&attrs, "type"); ok {
		entryType = v
	}
	if v, ok := popField(&attrs, "subdomain"); ok {
		subdomain = v
	}
	// request_id 提取到专门字段（#62）：请求链路日志统一携带，
	// 请求无关日志填 "-"，保证每行日志都有该字段
	requestID := ""
	if v, ok := popField(&attrs, "request_id"); ok && v != "" {
		requestID = v
	}
	if requestID == "" {
		requestID = "-"
	}
	if v, ok := popField(&attrs, "user_id"); ok {
		if uid, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			userID = &uid
		}
	}

	// 默认值：类型和用户为空时填 "system"
	// user_id 用 0 作为 system 用户的哨兵值（nil → 0），-1 表示 guest，>0 表示实际用户
	if entryType == "" {
		entryType = "system"
	}
	if userID == nil {
		uid := int64(0)
		userID = &uid
	}

	entry := &pkgTunnel.LogEntry{
		Ts:        r.Time.UnixMilli(),
		Level:     levelStr,
		Type:      entryType,
		Subdomain: subdomain,
		RequestID: requestID,
		UserID:    userID,
		Message:   r.Message,
		Fields:    attrs,
	}
	h.sink.Append(entry)
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newH := *h
	newH.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &newH
}

func (h *Handler) WithGroup(name string) slog.Handler {
	newH := *h
	newH.groups = append(append([]string(nil), h.groups...), name)
	return &newH
}

// levelToString 把 slog.Level 映射为日志级别字符串
func levelToString(l slog.Level) string {
	switch {
	case l >= loghandlerCritical:
		return "critical"
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warning"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}

// loghandlerCritical 与 internal/server/loghandler.LevelCritical 保持一致
// 不直接 import 避免循环依赖（server → client）
const loghandlerCritical = slog.Level(12)

// slogAttrToField 把 slog.Attr 转换为 pkgTunnel.Field
func slogAttrToField(a slog.Attr) pkgTunnel.Field {
	v := a.Value.Any()
	if v == nil {
		return pkgTunnel.Field{K: a.Key, V: ""}
	}
	switch x := v.(type) {
	case string:
		return pkgTunnel.Field{K: a.Key, V: x}
	case bool:
		if x {
			return pkgTunnel.Field{K: a.Key, V: "true"}
		}
		return pkgTunnel.Field{K: a.Key, V: "false"}
	case error:
		// error 类型走 json.Marshal 会得到 "{}"（底层结构体未导出字段），改用 .Error()
		return pkgTunnel.Field{K: a.Key, V: x.Error()}
	case time.Duration:
		// time.Duration 走 json.Marshal 会变成纳秒数（int64），改用阅读友好的格式
		return pkgTunnel.Field{K: a.Key, V: formatDuration(x)}
	default:
		data, err := jsonMarshal(v)
		if err != nil {
			return pkgTunnel.Field{K: a.Key, V: "<unserializable>"}
		}
		return pkgTunnel.Field{K: a.Key, V: string(data)}
	}
}

// popField 从 fields 中取出指定 key 的值并移除
func popField(fields *[]pkgTunnel.Field, key string) (string, bool) {
	for i, f := range *fields {
		if f.K == key {
			v := f.V
			*fields = append((*fields)[:i], (*fields)[i+1:]...)
			return v, true
		}
	}
	return "", false
}

// formatDuration 把 time.Duration 格式化为阅读友好的字符串
// 最小单位 ms：<1ms 显示 0.xx ms；<1s 显示 xx.xx ms；≥1s 显示 x.xxs
// 与 internal/server/loghandler.formatDuration 保持一致
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d.Nanoseconds())/1e6)
	case d < time.Second:
		return fmt.Sprintf("%.2fms", float64(d.Microseconds())/1e3)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}
