package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"

	"github.com/libp2p/go-yamux/v4"
)

// ErrTunnelOpen 打开隧道流失败（远端不在线）。
// 调用方可据此重试到备用客户端，此时请求 body 尚未被消费。
var ErrTunnelOpen = errors.New("tunnel stream open failed")

// streamHTTPConn 一条 HTTP 隧道流的 net.Conn 包装：
//   - 首个 Write 前置 StreamHTTP 类型字节（协议要求每条流第一字节是 StreamType，
//     旧 TunnelTransport 由 WriteStreamType 显式写入，http.Transport 需要包装器补齐）；
//   - Close() 变为 Reset()（RST 硬关闭），使对端（执行端）阻塞写立即失败，取消传播。
//
// 对一次性流（一请求一流）而言，Close 只在响应已被完整消费（或请求被取消）后
// 发生，此时 RST 不会丢弃任何尚未送达的数据。
type streamHTTPConn struct {
	*yamux.Stream
	wroteType bool
}

func (c *streamHTTPConn) Write(p []byte) (int, error) {
	if !c.wroteType {
		c.wroteType = true
		// 前置 1 字节类型标识，不计入返回值（http.Transport 期望 Write 返回 len(p)）
		if err := WriteStreamType(c.Stream, StreamHTTP); err != nil {
			return 0, err
		}
	}
	return c.Stream.Write(p)
}

func (c *streamHTTPConn) Close() error { return c.Stream.Reset() }

// AbsoluteURL 确保 req.URL 为绝对 URL（http.Transport.RoundTrip 要求非空
// scheme 与 host）。隧道请求行只使用 path+query；scheme/host 仅用于满足校验，
// 执行端根据 X-Hop-Target-URL 重建真实目标地址。同时清空 RequestURI
// （http.Transport 按 req.URL 序列化请求行，RequestURI 残留会导致格式错误）。
func AbsoluteURL(req *http.Request) {
	if req.URL.Scheme == "" {
		req.URL.Scheme = "http"
	}
	if req.URL.Host == "" {
		req.URL.Host = req.Host
		if req.URL.Host == "" {
			req.URL.Host = "tunnel"
		}
	}
	req.RequestURI = ""
}

// NewHTTPTransport 构建把"一条 yamux 流 = 一条一次性 HTTP 连接"的 http.RoundTripper。
// 请求/响应 body 流式、chunked、trailers、取消传播全部由 stdlib http.Transport 负责，
// 取代了手写的 TunnelTransport。
//
// openStream 打开一条 yamux 流（应返回远端在线的错误以便上层重试，开流失败时
// 包装为 ErrTunnelOpen，请求 body 尚未被消费）。DisableKeepAlives 保证一请求一流，
// 避免空闲流在 yamux 会话上堆积。DisableCompression 阻止 Transport 自动添加
// Accept-Encoding: gzip——否则上游（如 proxy.go 的 StripAcceptEncodingForSSE）剔除
// Accept-Encoding 防 SSE 压缩缓冲的努力会被 Transport 重新加回。不设
// ResponseHeaderTimeout：慢目标（>30s 才返回响应头）与旧行为一致，浏览器断开时
// 由 ctx 取消完成拆除。
func NewHTTPTransport(openStream func() (*yamux.Stream, error)) http.RoundTripper {
	return &http.Transport{
		DisableKeepAlives:  true,
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			st, err := openStream()
			if err != nil {
				// 双 %w 保留内层错误链：errors.Is(err, ErrTunnelOpen) 语义不变，
				// 同时允许调用方 errors.As 提取内层类型（如 peer 认证失败的
				// 密钥指纹，#67）
				return nil, fmt.Errorf("%w: %w", ErrTunnelOpen, err)
			}
			return &streamHTTPConn{Stream: st}, nil
		},
	}
}

// WriteWSErrorResponse 写入非 101 的错误响应头（仅状态+头，不含 body），
// 用于 WS 升级失败时让发起端判定 dial 失败。
func WriteWSErrorResponse(w io.Writer, statusCode int) error {
	resp := &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}},
		Body:       http.NoBody,
	}
	return WriteResponseHeader(w, resp)
}

// StripHopProxyHeaders 已移至 pkg/httputil.StripHopProxyHeaders（X-Hop-* 前缀
// 判定的唯一实现在 httputil，供出站解析 / WS 拨号过滤 / 执行端剥离共用）。

// WriteResponseHeader 仅写入状态行与响应头（不含 body），用于 WS 升级 101 响应。
// 101 响应的 body 是协议升级后的裸字节流，由调用方单独桥接，不能走 io.Copy。
func WriteResponseHeader(w io.Writer, resp *http.Response) error {
	statusText := http.StatusText(resp.StatusCode)
	if statusText == "" {
		statusText = "status"
	}
	if _, err := fmt.Fprintf(w, "HTTP/1.1 %d %s\r\n", resp.StatusCode, statusText); err != nil {
		return err
	}
	if err := resp.Header.Write(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}

// BufioStream 包装一个 bufio.Reader（读）与 io.WriteCloser（写），实现 io.ReadWriteCloser。
// 用于 http.ReadResponse 之后：bufio 可能已缓冲协议升级后的字节，后续读取必须走 bufio，
// 否则缓冲区内的字节会丢失。
type BufioStream struct {
	BR *bufio.Reader
	WC io.WriteCloser
}

func (b *BufioStream) Read(p []byte) (int, error)  { return b.BR.Read(p) }
func (b *BufioStream) Write(p []byte) (int, error) { return b.WC.Write(p) }
func (b *BufioStream) Close() error                { return b.WC.Close() }

// ReadResponseHeader 读取状态行与响应头（不读 body），用于 WS 升级 101 响应。
// 101 响应之后的字节是协议升级后的裸帧，由调用方通过返回的 bufio.Reader 继续读取。
// br 在返回后定位于头之后的第一个 body/帧字节。
func ReadResponseHeader(br *bufio.Reader) (*http.Response, error) {
	tp := textproto.NewReader(br)
	line, err := tp.ReadLine()
	if err != nil {
		return nil, fmt.Errorf("读取状态行失败: %w", err)
	}
	// "HTTP/1.1 101 Switching Protocols"
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 2 {
		return nil, fmt.Errorf("状态行格式错误: %q", line)
	}
	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("状态码解析失败: %w", err)
	}
	mimeHeader, err := tp.ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("读取响应头失败: %w", err)
	}
	resp := &http.Response{
		StatusCode: code,
		Status:     line,
		Header:     http.Header(mimeHeader),
		Body:       http.NoBody,
	}
	return resp, nil
}
