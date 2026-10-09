package httputil

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// NewPeerTransport 创建 peer HTTP 正向代理的 Transport
// clientID 和 secret 用于设置 ProxyConnectHeader，确保 HTTPS CONNECT 隧道请求携带 peer 认证头。
// connectTimeout 为连接 peer 的超时（#47 proxy_connect_timeout；<=0 不设限）。
func NewPeerTransport(proxyAddr, clientID, secret string, connectTimeout time.Duration) (*http.Transport, error) {
	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("解析 peer 代理地址失败: %w", err)
	}
	ts, sig := ComputePeerSignature(secret, clientID)
	connectHeader := http.Header{}
	connectHeader.Set("X-Peer-Client", clientID)
	connectHeader.Set("X-Peer-Timestamp", ts)
	connectHeader.Set("X-Peer-Secret", secret)
	connectHeader.Set("X-Peer-Signature", sig)
	t := &http.Transport{
		Proxy:              http.ProxyURL(proxyURL),
		ProxyConnectHeader: connectHeader,
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{InsecureSkipVerify: true},
		// 一次性 Transport 的 idle 连接无人复用，设置较短 idle 超时让连接及时关闭，
		// 避免每请求泄漏一个永久 idle 的 TCP fd 和 persistConn goroutine。
		IdleConnTimeout: 30 * time.Second,
	}
	if connectTimeout > 0 {
		t.DialContext = (&net.Dialer{Timeout: connectTimeout}).DialContext
	}
	return t, nil
}

// SetPeerHeadersOnRequest 在 http.Request 上设置 peer 认证头
func SetPeerHeadersOnRequest(r *http.Request, clientID, secret string) {
	ts, sig := ComputePeerSignature(secret, clientID)
	r.Header.Set("X-Peer-Client", clientID)
	r.Header.Set("X-Peer-Timestamp", ts)
	r.Header.Set("X-Peer-Secret", secret)
	r.Header.Set("X-Peer-Signature", sig)
}

// ComputePeerSignature 计算 peer 请求的 HMAC-SHA256 签名，返回 timestamp 和 signature
func ComputePeerSignature(secret, clientID string) (timestamp, signature string) {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig := hmacSHA256(secret, clientID+":"+ts)
	return ts, sig
}

// PeerSecretFingerprint 计算 peer 密钥指纹（SHA256 前 8 字节 hex，共 16 字符）。
// 用于 peer 认证失败时让服务端比对"下发密钥 vs 对端实际密钥"并定位地址上
// 实际的客户端 (#67)。非可逆、不可伪造，且不暴露客户端 UUID（UUID 是隧道
// 注册凭证，不能回传给未认证方），指纹仅在有服务端 DB 的情况下才有意义。
func PeerSecretFingerprint(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:8])
}

// HMACSHA256 计算 HMAC-SHA256
func HMACSHA256(secret, data string) string {
	return hmacSHA256(secret, data)
}

func hmacSHA256(secret, data string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
