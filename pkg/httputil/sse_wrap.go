package httputil

import (
	"net/http"
)

// sseKeepaliveResponseWriter 包装 http.ResponseWriter，在 SSE 响应上自动注入 keepalive 注释行。
//
// 用于 httputil.ReverseProxy 场景：ReverseProxy 内部完成 header 复制与 WriteHeader，
// 本包装器在 WriteHeader 时检测 Content-Type，若为 SSE 则启动周期性 keepalive，
// 防止 Nginx 等中间层因上游长时间静默而断连。
//
// 非流式写入：通过包装器转发，keepalive 写入器在非 SSE 情况下为透传。
type sseKeepaliveResponseWriter struct {
	rw            http.ResponseWriter
	kw            *SSEKeepaliveWriter
	headerWritten bool
}

// NewSSEKeepaliveResponseWriter 创建一个 ReverseProxy 适用的 SSE keepalive 包装器。
// 调用方应在 ReverseProxy.ServeHTTP 返回后调用 stop() 释放计时器。
func NewSSEKeepaliveResponseWriter(rw http.ResponseWriter) *sseKeepaliveResponseWriter {
	return &sseKeepaliveResponseWriter{rw: rw}
}

func (w *sseKeepaliveResponseWriter) Header() http.Header { return w.rw.Header() }

func (w *sseKeepaliveResponseWriter) WriteHeader(code int) {
	if w.headerWritten {
		return
	}
	w.headerWritten = true
	ct := w.rw.Header().Get("Content-Type")
	w.kw = NewSSEKeepaliveWriter(w.rw, ct)
	w.kw.WriteHeader(code)
	// SSE 响应在 WriteHeader 时即 arm 计时器，覆盖首条事件前的初始静默期
	w.kw.ResetTimer()
}

func (w *sseKeepaliveResponseWriter) Write(p []byte) (int, error) {
	if !w.headerWritten {
		// ReverseProxy 可能对 200 响应省略 WriteHeader，直接 Write
		w.WriteHeader(http.StatusOK)
	}
	return w.kw.Write(p)
}

func (w *sseKeepaliveResponseWriter) Flush() {
	if w.kw != nil {
		w.kw.Flush()
		return
	}
	if f, ok := w.rw.(http.Flusher); ok {
		f.Flush()
	}
}

// Stop 停止 keepalive 计时器，应在 ServeHTTP 返回后调用
func (w *sseKeepaliveResponseWriter) Stop() {
	if w.kw != nil {
		w.kw.Stop()
	}
}
