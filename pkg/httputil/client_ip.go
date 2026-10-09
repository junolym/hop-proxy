package httputil

import (
	"net/http"
	"strings"
)

// ClientIP 从请求中提取客户端真实 IP
// 依次检查 X-Forwarded-For、X-Real-IP、RemoteAddr
func ClientIP(r *http.Request) string {
	// X-Forwarded-For 可能包含多个 IP（逗号分隔），取第一个
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		return strings.TrimSpace(strings.SplitN(ip, ",", 2)[0])
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	// RemoteAddr 格式为 "ip:port"
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
