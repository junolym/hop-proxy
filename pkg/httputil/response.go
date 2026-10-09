package httputil

import (
	"io"
	"net/http"
	"strings"
)

// hopByHopHeaders RFC 2616 定义的逐跳头，代理转发时必须剔除。
var hopByHopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

// RemoveHopHeaders 从 header map 中剔除逐跳头以及 Connection 头中列出的字段。
func RemoveHopHeaders(h http.Header) {
	if c := h.Get("Connection"); c != "" {
		for _, f := range strings.Split(c, ",") {
			f = strings.TrimSpace(f)
			if f != "" {
				h.Del(f)
			}
		}
	}
	for _, k := range hopByHopHeaders {
		h.Del(k)
	}
}

// hopProxyCookiePrefix HopProxy 内部 Cookie 的统一前缀（SSO 会话、管理会话、CSRF、TOTP 等）。
// 这些 Cookie 仅用于 HopProxy 自身的认证流程，绝不能转发给后端应用。
const hopProxyCookiePrefix = "hopproxy_"

// StripHopProxyCookies 从请求头中剔除所有 HopProxy 内部 Cookie（前缀 hopproxy_）。
// 在代理转发到后端前调用，防止 SSO 会话、管理会话、CSRF 等内部 Cookie 泄漏给后端。
// 鉴权阶段需要先读取 SSO Cookie，因此必须在鉴权通过后调用。
func StripHopProxyCookies(h http.Header) {
	cookieHeader := h.Get("Cookie")
	if cookieHeader == "" {
		return
	}
	// 复用 stdlib 解析，正确处理 quoted value 与空白
	req := &http.Request{Header: h}
	cookies := req.Cookies()
	var kept []string
	stripped := false
	for _, c := range cookies {
		if strings.HasPrefix(strings.ToLower(c.Name), hopProxyCookiePrefix) {
			stripped = true
			continue
		}
		kept = append(kept, c.Name+"="+c.Value)
	}
	if !stripped {
		return
	}
	if len(kept) == 0 {
		h.Del("Cookie")
	} else {
		h.Set("Cookie", strings.Join(kept, "; "))
	}
}

// streamBufSize 流式读取缓冲区大小
const streamBufSize = 32 * 1024

// StreamResponse 将 *http.Response 写回 http.ResponseWriter。
// 流式响应（SSE/chunked/大文件）逐块写入并注入 SSE keepalive；
// 非流式响应一次性写出。请求上下文取消（浏览器断开）时关闭 resp.Body 释放上游资源。
func StreamResponse(w http.ResponseWriter, r *http.Request, resp *http.Response) error {
	// 复制响应头（剥离 X-Hop-* 带外头：peer 执行端会把对端密钥指纹经
	// X-Hop-Peer-Secret-FP 回传给服务端，该信息仅限内部链路，严禁泄漏给浏览器，#67）
	StripHopProxyHeaders(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	isStream := IsStreamingResponse(resp)
	if isStream {
		// 仅长度未知（上游 chunked）或 SSE（keepalive 会注入额外字节，
		// 与声明长度冲突）时才剥离 Content-Length 走 chunked；
		// 长度已知的大文件保留 Content-Length 流式写回，客户端可见总长度（#66）
		if resp.ContentLength < 0 || IsSSEResponse(resp.Header.Get("Content-Type")) {
			w.Header().Del("Content-Length")
		}
	}
	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	if isStream {
		w.WriteHeader(statusCode)
		ct := resp.Header.Get("Content-Type")
		kw := NewSSEKeepaliveWriter(w, ct)
		defer kw.Stop()

		// 浏览器断开时关闭 resp.Body，释放上游/隧道资源
		go func() {
			<-r.Context().Done()
			resp.Body.Close()
		}()

		buf := make([]byte, streamBufSize)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				if _, wErr := kw.Write(buf[:n]); wErr != nil {
					return nil // 浏览器断开
				}
				kw.Flush()
			}
			if err != nil {
				return nil
			}
		}
	}

	w.WriteHeader(statusCode)
	_, err := io.Copy(w, resp.Body)
	return err
}
