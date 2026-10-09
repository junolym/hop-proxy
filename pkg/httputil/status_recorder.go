package httputil

import (
	"net/http"
	"strconv"
)

// StatusRecorder 包装 http.ResponseWriter，捕获 status code 和 bytes written
// 用于代理完成时记录响应状态和回包大小
type StatusRecorder struct {
	http.ResponseWriter
	status       int
	bytesWritten int
	wroteHeader  bool
}

// NewStatusRecorder 包装一个 ResponseWriter
func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, status: http.StatusOK}
}

// WriteHeader 拦截 WriteHeader 记录 status
func (r *StatusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

// Write 拦截 Write 累加字节数（WriteHeader 未显式调用时按 200 记）
func (r *StatusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.wroteHeader = true
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytesWritten += n
	return n, err
}

// Status 返回捕获的 status code
func (r *StatusRecorder) Status() int {
	return r.status
}

// BytesWritten 返回累计写入字节数
func (r *StatusRecorder) BytesWritten() int {
	return r.bytesWritten
}

// Unwrap 返回底层 ResponseWriter，让 Flusher/Hijacker 接口能被外部识别
func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Flush 实现 http.Flusher，透传给底层 ResponseWriter
// 必需：stdlib ReverseProxy 用 FlushInterval=-1 时会断言 Flusher 接口做流式 flush，
// 不透传会导致流式响应被缓冲（Content-Length 被错误设置）
func (r *StatusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// HeaderCapture 捕获发送给后端服务的最末端请求/响应头
// 由调用方在代理入口创建，经 transport wrapper 或直接赋值填充
type HeaderCapture struct {
	ReqHeaders  http.Header
	RespHeaders http.Header
}

// headerCaptureTransport 包装 http.RoundTripper，在 RoundTrip 时捕获最末端 req/resp headers
type headerCaptureTransport struct {
	base    http.RoundTripper
	capture *HeaderCapture
}

// NewHeaderCaptureTransport 包装 base transport，把最末端 req/resp headers 写入 capture
func NewHeaderCaptureTransport(base http.RoundTripper, capture *HeaderCapture) http.RoundTripper {
	return &headerCaptureTransport{base: base, capture: capture}
}

func (t *headerCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.capture.ReqHeaders = req.Header.Clone()
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp != nil {
		t.capture.RespHeaders = resp.Header.Clone()
	}
	return resp, err
}

// FormatSize 把字节数格式化为阅读友好的字符串
// <1KB 显示 B；<1MB 显示 K（带 1 位小数）；≥1MB 显示 M（带 2 位小数）
func FormatSize(bytes int) string {
	if bytes < 1024 {
		return strconv.Itoa(bytes) + "B"
	}
	if bytes < 1024*1024 {
		return strconv.FormatFloat(float64(bytes)/1024, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(float64(bytes)/(1024*1024), 'f', 2, 64) + "M"
}
