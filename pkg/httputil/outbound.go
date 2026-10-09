// 出站请求头统一处理（#46）。
// 所有代理路径的发起端在构建发往后端的请求时，统一调用 ResolveOutboundHeader
// 一次完成：X-Hop-* 带外头剥离、自定义 headers 合并、Host/Origin 解析、
// X-Forwarded-* 三件套处理（按应用配置的请求头缺省处理模式）。
// 执行端只透传，不做二次改写。请求上下文（X-Hop-Ctx envelope）的读写在
// pkg/tunnel 内收口；此处只负责按前缀剥离，作为最后防线避免泄漏给目标。
package httputil

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// 请求头缺省处理模式（#46），按应用配置（apps.header_mode）
const (
	// HeaderModeAutoXFF 自动添加 X-Forwarded-*，Origin 不处理（默认，存量应用）
	HeaderModeAutoXFF = "auto_xff"
	// HeaderModeAutoOrigin 自动处理 Origin：不自动添加 X-Forwarded-*，
	// 确保 Origin 与 Host 匹配（改写为 scheme://host，自定义 Origin 优先）
	HeaderModeAutoOrigin = "auto_origin"
	// HeaderModeNone 不自动处理：不添加 X-Forwarded-*、不改写 Origin
	HeaderModeNone = "none"
)

// ValidHeaderMode 校验请求头缺省处理模式是否合法
func ValidHeaderMode(mode string) bool {
	return mode == HeaderModeAutoXFF || mode == HeaderModeAutoOrigin || mode == HeaderModeNone
}

// isHopProxyHeader 判定是否为 X-Hop-* 隧道带外头（前缀匹配，大小写不敏感）。
// X-Hop-* 前缀判定的唯一实现，剥离（StripHopProxyHeaders）、出站解析
// （ResolveOutboundHeader）、WS 拨号过滤（BuildWSDialOptions）共用。
func isHopProxyHeader(key string) bool {
	return strings.HasPrefix(strings.ToLower(key), "x-hop-")
}

// StripHopProxyHeaders 剥离 X-Hop-* 带外头（envelope 已在入口 PopHopContext 剥离，
// 本函数作为最后防线，用于出站解析与抓包展示"发给后端的最末端请求"时，避免泄漏给目标）。
func StripHopProxyHeaders(h http.Header) {
	for k := range h {
		if isHopProxyHeader(k) {
			h.Del(k)
		}
	}
}

// RequestProto 返回请求到达本节点时使用的协议（浏览器侧协议）。
// 前置反代（nginx）已设置 X-Forwarded-Proto 时以该头为准，由
// ResolveOutboundHeader 保留，此处仅作缺省推导。
func RequestProto(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// RemoteIP 提取 r.RemoteAddr 中的客户端 IP（无端口时原样返回）。
// 与 ClientIP 不同：ClientIP 信任既有 X-Forwarded-For 链（用于取"最原始"IP），
// RemoteIP 只取本节点直连地址，用于追加到 XFF 链尾。
func RemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// AppendForwardedFor 将 ip 追加到 X-Forwarded-For 链尾（已有值保留在前，
// 多级代理链取最完整链路）。ip 为空时不操作。
func AppendForwardedFor(h http.Header, ip string) {
	if ip == "" {
		return
	}
	if prior := h.Get("X-Forwarded-For"); prior != "" {
		h.Set("X-Forwarded-For", prior+", "+ip)
	} else {
		h.Set("X-Forwarded-For", ip)
	}
}

// ResolveOutboundHeader 统一解析发往后端的出站请求头，返回最终 header 与 Host。
// 所有代理路径的发起端（server 侧与 client 侧）只在此处处理，执行端透传。
//
// 处理规则（#46）：
//  1. 剥离入站 X-Hop-* 带外头（调用方解析后设置的 X-Hop-* 为出站带外头，
//     由执行端读取后剥离，不会发给后端）
//  2. 合并自定义 headers（Host 键除外，Host 单独解析返回）
//  3. Host：自定义 Host 优先，否则从 targetURL 提取（Host 必填）
//  4. Origin（按 headerMode，空值视为 HeaderModeAutoXFF）：
//     - auto_origin：自定义 Origin 直接采用；原始请求无 Origin 则不设置
//     （不构造）；有则改写为 scheme://host（scheme 取 targetURL，host 按上一条）
//     - auto_xff / none：Origin 不处理（原样透传，自定义值已在第 2 步合并）
//  5. X-Forwarded-* 三件套（按 headerMode）：
//     - auto_xff：X-Forwarded-Proto 缺省取 origProto；X-Forwarded-Host 缺省取
//     origHost（已有值保留 = 多级链路取最原始值）；X-Forwarded-For 由 clientIP
//     非空时追加到链尾
//     - auto_origin / none：剥离既有的 X-Forwarded-Proto/Host/For（含 Path 5
//     client A 首跳盲加的值——该跳不认识应用配置；自定义 headers 设置的除外），
//     保证后端不会收到 hop-proxy 产出的 X-Forwarded-*，需要时用自定义 headers
//     显式配置
//
// targetURL 为空时（Path 5 client A 侧转发到 server 的场景）不解析 Host/Origin，
// 仅完成 X-Hop-* 剥离与 X-Forwarded-* 处理，Host/Origin 由下一跳（server）
// 再次调用本函数解析。
//
// clientIP 为空时不追加 X-Forwarded-For：stdlib ReverseProxy 路径由标准库
// 自动追加 RemoteAddr，此处再追加会导致链尾重复。
func ResolveOutboundHeader(orig http.Header, customHeaders map[string]string, headerMode, targetURL, origHost, origProto, clientIP string) (http.Header, string) {
	if headerMode == "" {
		headerMode = HeaderModeAutoXFF
	}
	out := make(http.Header, len(orig)+len(customHeaders)+3)
	// 1. 复制原始头（X-Hop-* 带外头的剥离统一由 StripHopProxyHeaders 完成）
	for k, vals := range orig {
		out[k] = append([]string(nil), vals...)
	}
	StripHopProxyHeaders(out)
	// 2. 合并自定义 headers（Host 键跳过）；记录自定义 Origin / X-Forwarded-*
	// 供第 4/5 步判定（大小写不敏感）
	customOrigin := ""
	customForwarded := map[string]bool{}
	for k, v := range customHeaders {
		if strings.EqualFold(k, "Host") {
			continue
		}
		out.Set(k, v)
		if strings.EqualFold(k, "Origin") {
			customOrigin = v
		}
		for _, xf := range [...]string{"X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For"} {
			if strings.EqualFold(k, xf) {
				customForwarded[xf] = true
			}
		}
	}
	// 3. Host：自定义优先，否则从目标 URL 提取
	host := HostOf(customHeaders)
	scheme := ""
	if u, err := url.Parse(targetURL); err == nil && u.Host != "" {
		scheme = u.Scheme
		if host == "" {
			host = u.Host
		}
	}
	// 4. Origin：仅 auto_origin 模式改写（自定义 > scheme://host；原始无 Origin 不构造）
	if headerMode == HeaderModeAutoOrigin && customOrigin == "" && scheme != "" && host != "" {
		if orig.Get("Origin") != "" {
			out.Set("Origin", scheme+"://"+host)
		} else {
			out.Del("Origin")
		}
	}
	// 5. X-Forwarded-* 三件套（按 headerMode；自定义值在两种模式下均保留）
	if headerMode == HeaderModeAutoXFF {
		if out.Get("X-Forwarded-Proto") == "" && origProto != "" {
			out.Set("X-Forwarded-Proto", origProto)
		}
		if out.Get("X-Forwarded-Host") == "" && origHost != "" {
			out.Set("X-Forwarded-Host", origHost)
		}
		AppendForwardedFor(out, clientIP)
	} else {
		for _, xf := range [...]string{"X-Forwarded-Proto", "X-Forwarded-Host", "X-Forwarded-For"} {
			if !customForwarded[xf] {
				out.Del(xf)
			}
		}
	}
	return out, host
}
