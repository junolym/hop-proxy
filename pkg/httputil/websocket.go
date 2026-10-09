// Package httputil 提供 HTTP 请求构建和发送的通用工具函数。
package httputil

import (
	"net/http"
	"strings"

	"github.com/coder/websocket"
)

// IsWebSocketUpgrade 判断是否为 WebSocket 升级请求
func IsWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// ParseSubprotocolsFromHTTP 从 http.Header 中提取 WebSocket 子协议列表。
func ParseSubprotocolsFromHTTP(h http.Header) []string {
	values := h.Values("Sec-WebSocket-Protocol")
	if len(values) == 0 {
		return nil
	}
	var protocols []string
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				protocols = append(protocols, p)
			}
		}
	}
	return protocols
}

// wsSkipHeaders 构建 WS 拨号选项时需跳过的头（hop-by-hop 与 WS 握手专用头）。
var wsSkipHeaders = map[string]bool{
	"connection":               true,
	"upgrade":                  true,
	"transfer-encoding":        true,
	"host":                     true, // Host 通过 opts.Host 设置
	"sec-websocket-protocol":   true, // 子协议通过 Subprotocols 字段设置
	"sec-websocket-extensions": true,
	"sec-websocket-key":        true,
	"sec-websocket-version":    true,
	"sec-websocket-accept":     true,
}

// BuildWSDialOptions 从（已由 ResolveOutboundHeader 统一解析的）请求头构建
// websocket.DialOptions，过滤 WS 握手专用头与 hop-by-hop 头。供所有 WS 代理
// 路径复用。
//
// Origin 不在此构造或改写：由发起端 ResolveOutboundHeader 按 #46 规则统一
// 处理（自定义优先 / 无则不设 / 有则改写 scheme://host）后透传至此。
//
// host 参数非空时设置 opts.Host，覆盖 URL 推导出的 Host——用于透传解析后的
// Host（自定义 Host 或目标 URL host）。host 为空时由 websocket 库按 URL.Host 推导。
func BuildWSDialOptions(headers http.Header, host string) *websocket.DialOptions {
	opts := &websocket.DialOptions{HTTPHeader: make(http.Header)}
	for key, vals := range headers {
		if len(vals) == 0 {
			continue
		}
		lower := strings.ToLower(key)
		if wsSkipHeaders[lower] {
			continue
		}
		// X-Hop-* 是隧道带外头（日志关联/目标地址传递），统一按前缀剥离，不发给目标服务
		if isHopProxyHeader(key) {
			continue
		}
		opts.HTTPHeader.Set(key, vals[0])
	}
	if host != "" {
		opts.Host = host
	}
	opts.Subprotocols = ParseSubprotocolsFromHTTP(headers)
	return opts
}
