// Package loghandler 实现自定义 slog.Handler，把现有 slog.Info/Error/...
// 调用透明接入 internal/server/logstore。
//
// 设计：
//   - 业务代码继续用 slog.Info/Error/...，无需修改
//   - 业务代码通过 loghandler.WithContext(ctx, meta) 把 subdomain/user_id/type 塞入 ctx
//   - Critical 级别同时写 stderr（docker logs 可见）
//   - 其他级别只写 logstore
package loghandler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/robin/hop-proxy/internal/server/logstore"
)

// LevelCritical 自定义日志级别（高于 Error=8）。
// slog 默认不提供 Critical，需要业务代码用 loghandler.Critical(...) 显式调用。
const LevelCritical = slog.Level(12)

// Critical 顶层 helper：以 Critical 级别记录日志。
// 用法：loghandler.Critical("服务端启动失败", "error", err)
func Critical(msg string, args ...any) {
	slog.Default().Log(context.Background(), LevelCritical, msg, args...)
}

// LogMeta 业务上下文元数据，通过 ctx 传递给 handler。
// 由中间件/入口注入（如 api 的 loggingMiddleware 注入 Type/RequestID，
// authMiddleware 补 UserID），调用点用 slog.*Context(ctx, ...) 记录日志时自动携带。
type LogMeta struct {
	Subdomain string
	UserID    *int64
	Type      string // 日志类型：auth/proxy/share/api/...
	RequestID string // 全链路请求关联 ID（#62；管理 API 由 loggingMiddleware 注入）
}

type ctxKey struct{}

// WithContext 把 LogMeta 塞入 ctx
func WithContext(ctx context.Context, meta LogMeta) context.Context {
	return context.WithValue(ctx, ctxKey{}, meta)
}

// FromContext 从 ctx 提取 LogMeta（无则返回空）
func FromContext(ctx context.Context) LogMeta {
	if v, ok := ctx.Value(ctxKey{}).(LogMeta); ok {
		return v
	}
	return LogMeta{}
}

// Handler 实现 slog.Handler 接口
//
// attrs 字段不可变：WithAttrs 返回新 Handler 时只复制 slice，不修改原 Handler 的 attrs。
// 因此无需锁保护。stderrHandler 在 New 时一次创建并复用，也不需要锁。
type Handler struct {
	store    *logstore.Store
	source   string // "host"（服务端）或客户端 name
	clientID string // 服务端为空，客户端上报时为 client_id

	attrs  []slog.Attr
	groups []string

	// stderr handler（仅 Critical 用，复用同一实例避免每条日志重建）
	stderrHandler slog.Handler
}

// New 创建服务端 slog.Handler
//   - source="host"，client_id 为空
//   - critical 级别日志同时写 os.Stderr
func New(store *logstore.Store) *Handler {
	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: LevelCritical,
	})
	return &Handler{
		store:         store,
		source:        "host",
		stderrHandler: stderrHandler,
	}
}

// NewWithSource 创建带自定义 source/clientID 的 Handler（用于客户端日志在服务端落盘时）
func NewWithSource(store *logstore.Store, source, clientID string) *Handler {
	h := New(store)
	h.source = source
	h.clientID = clientID
	return h
}

// Enabled 决定是否处理该日志。
// 由 store 统一的落盘级别下限（HP_LOG_LEVEL）提前拦截，低级别不进 Handle 减少开销。
func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return h.store.LevelEnabled(levelToString(level))
}

// Handle 处理一条日志记录
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	meta := FromContext(ctx)

	// 收集 attrs：WithAttrs 累积的 + record 自带的
	attrs := make([]logstore.Field, 0, len(h.attrs)+r.NumAttrs())

	for _, a := range h.attrs {
		attrs = append(attrs, slogAttrToField(a))
	}

	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slogAttrToField(a))
		return true
	})

	levelStr := levelToString(r.Level)
	rec := &logstore.LogRecord{
		Ts:        r.Time.UnixMilli(),
		Level:     levelStr,
		Source:    h.source,
		ClientID:  h.clientID,
		Type:      meta.Type,
		Subdomain: meta.Subdomain,
		UserID:    meta.UserID,
		RequestID: meta.RequestID,
		Message:   r.Message,
		Fields:    attrs,
	}

	// 专用列提取（meta 优先）：meta 有值时剥离 attrs 中的同名占位（避免列与 Fields
	// 重复展示）；meta 无值时从 attrs 提取（业务代码可能未用 WithContext，而是
	// 直接 slog.With("type", "xxx") 或显式 attrs 传字段）。
	if rec.Type == "" {
		if v, ok := popField(&attrs, "type"); ok {
			rec.Type = v
		}
	} else {
		popField(&attrs, "type")
	}
	if rec.Subdomain == "" {
		if v, ok := popField(&attrs, "subdomain"); ok {
			rec.Subdomain = v
		}
	} else {
		popField(&attrs, "subdomain")
	}
	// request_id：请求链路日志统一带 request_id，
	// 请求无关日志（连接管理、后台任务等）填 "-"，保证每行日志都有该字段
	if rec.RequestID == "" {
		if v, ok := popField(&attrs, "request_id"); ok && v != "" {
			rec.RequestID = v
		}
	} else {
		popField(&attrs, "request_id")
	}
	if rec.RequestID == "" {
		rec.RequestID = "-"
	}
	if rec.UserID == nil {
		if v, ok := popField(&attrs, "user_id"); ok {
			if uid, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				rec.UserID = &uid
			}
		}
	} else {
		popField(&attrs, "user_id")
	}
	// app_id 从 Fields 提取到专门字段（避免 message 和列重复展示）
	if v, ok := popField(&attrs, "app_id"); ok {
		if aid, perr := strconv.ParseInt(v, 10, 64); perr == nil {
			rec.AppID = &aid
		}
	}
	rec.Fields = attrs

	// 默认值：类型和用户为空时填 "system"
	// user_id 用 0 作为 system 用户的哨兵值（nil → 0），-1 表示 guest，>0 表示实际用户
	if rec.Type == "" {
		rec.Type = "system"
	}
	if rec.UserID == nil {
		uid := int64(0)
		rec.UserID = &uid
	}

	// Critical 同时写 stderr（用复用的 stderrHandler 保持格式一致）
	if r.Level >= LevelCritical {
		_ = h.stderrHandler.Handle(ctx, r)
	}

	return h.store.Append(rec)
}

// WithAttrs 返回带额外属性的新 Handler
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	newH := *h
	newH.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &newH
}

// WithGroup 返回带分组的新 Handler
// 当前实现不真正支持分组（业务代码未使用），仅记录 group name 以备扩展
func (h *Handler) WithGroup(name string) slog.Handler {
	newH := *h
	newH.groups = append(append([]string(nil), h.groups...), name)
	return &newH
}

// levelToString 把 slog.Level 映射为日志级别字符串
func levelToString(l slog.Level) string {
	switch {
	case l >= LevelCritical:
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

// slogAttrToField 把 slog.Attr 转换为 logstore.Field
// 复杂值（slice/map/struct）JSON 序列化为字符串
func slogAttrToField(a slog.Attr) logstore.Field {
	v := a.Value.Any()
	if v == nil {
		return logstore.Field{K: a.Key, V: ""}
	}
	switch x := v.(type) {
	case string:
		return logstore.Field{K: a.Key, V: x}
	case bool:
		if x {
			return logstore.Field{K: a.Key, V: "true"}
		}
		return logstore.Field{K: a.Key, V: "false"}
	case error:
		// error 类型走 json.Marshal 会得到 "{}"（底层结构体未导出字段），改用 .Error()
		return logstore.Field{K: a.Key, V: x.Error()}
	case time.Duration:
		// time.Duration 走 json.Marshal 会变成纳秒数（int64），改用阅读友好的格式
		return logstore.Field{K: a.Key, V: formatDuration(x)}
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return logstore.Field{K: a.Key, V: "<unserializable>"}
		}
		return logstore.Field{K: a.Key, V: string(data)}
	}
}

// popField 从 fields 中取出指定 key 的值并移除该字段
func popField(fields *[]logstore.Field, key string) (string, bool) {
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
