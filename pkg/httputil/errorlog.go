package httputil

import (
	"log"
	"log/slog"
)

// NewServerErrorLog 返回适配 http.Server.ErrorLog 的 *log.Logger，
// 把 net/http 内部错误（如 superfluous response.WriteHeader、TLS 握手错误）
// 桥接到 slog 默认管道。
//
// 不设置 ErrorLog 时，这些错误走标准 log 包直写 stderr（裸时间戳格式），
// 与项目 slog 结构化日志格式不一致，且不会进入日志收集（服务端 logstore /
// 客户端 logsink）。必须在 slog.SetDefault 之后调用。
func NewServerErrorLog() *log.Logger {
	return slog.NewLogLogger(slog.Default().With("type", "system").Handler(), slog.LevelWarn)
}
