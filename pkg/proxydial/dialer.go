package proxydial

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/shadowsocks/go-shadowsocks2/core"
	"golang.org/x/net/proxy"
)

const (
	// ProxyTypeSOCKS5 SOCKS5 代理类型
	ProxyTypeSOCKS5 = "socks5"
	// ProxyTypeShadowsocks Shadowsocks 代理类型
	ProxyTypeShadowsocks = "shadowsocks"
	// DefaultTimeout 默认连接超时
	DefaultTimeout = 30 * time.Second
)

// Dialer 代理拨号器接口
type Dialer interface {
	Dial(network, addr string) (net.Conn, error)
}

// NewDialerSimple 根据代理类型创建拨号器。
// connectTimeout 为拨号超时（#47 proxy_connect_timeout；<=0 用 DefaultTimeout）。
func NewDialerSimple(proxyType, proxyAddress, proxyPassword string, connectTimeout time.Duration) (Dialer, error) {
	if connectTimeout <= 0 {
		connectTimeout = DefaultTimeout
	}
	if proxyType == "" || proxyAddress == "" {
		return &directDialer{timeout: connectTimeout}, nil
	}

	var dialer Dialer
	var err error
	switch proxyType {
	case ProxyTypeSOCKS5:
		dialer, err = newSOCKS5Dialer(proxyAddress, proxyPassword, connectTimeout)
	case ProxyTypeShadowsocks:
		dialer, err = newShadowsocksDialer(proxyAddress, proxyPassword, connectTimeout)
	default:
		return nil, fmt.Errorf("不支持的代理类型: %s", proxyType)
	}
	if err != nil {
		return nil, err
	}
	return dialer, nil
}

// directDialer 直连拨号器
type directDialer struct {
	timeout time.Duration
}

func (d *directDialer) Dial(network, addr string) (net.Conn, error) {
	return net.DialTimeout(network, addr, d.timeout)
}

// socks5Dialer SOCKS5 代理拨号器
type socks5Dialer struct {
	dialer proxy.Dialer
}

func newSOCKS5Dialer(address, password string, connectTimeout time.Duration) (Dialer, error) {
	var auth *proxy.Auth
	if password != "" {
		// SOCKS5 密码格式为 username:password
		var username, pwd string
		for i, c := range password {
			if c == ':' {
				username = password[:i]
				pwd = password[i+1:]
				break
			}
		}
		if username == "" && pwd == "" {
			// 如果没有冒号，整个作为密码，用户名为空
			pwd = password
		}
		auth = &proxy.Auth{
			User:     username,
			Password: pwd,
		}
	}

	// 创建带超时的前置拨号器
	forward := &net.Dialer{
		Timeout: connectTimeout,
	}

	dialer, err := proxy.SOCKS5("tcp", address, auth, forward)
	if err != nil {
		return nil, fmt.Errorf("创建 SOCKS5 拨号器失败: %w", err)
	}

	return &socks5Dialer{dialer: dialer}, nil
}

func (d *socks5Dialer) Dial(network, addr string) (net.Conn, error) {
	return d.dialer.Dial(network, addr)
}

// shadowsocksDialer Shadowsocks 代理拨号器
type shadowsocksDialer struct {
	address string
	ciph    core.Cipher
	timeout time.Duration
}

// newShadowsocksDialer 创建 Shadowsocks 拨号器
// password 格式为 method:password，如 aes-256-gcm:mypassword
func newShadowsocksDialer(address, password string, connectTimeout time.Duration) (Dialer, error) {
	method, pass, err := parseShadowsocksPassword(password)
	if err != nil {
		return nil, err
	}

	ciph, err := core.PickCipher(method, nil, pass)
	if err != nil {
		return nil, fmt.Errorf("创建 Shadowsocks 加密失败: %w", err)
	}

	return &shadowsocksDialer{
		address: address,
		ciph:    ciph,
		timeout: connectTimeout,
	}, nil
}

// parseShadowsocksPassword 解析 method:password 格式
func parseShadowsocksPassword(password string) (method, pass string, err error) {
	idx := strings.Index(password, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("Shadowsocks 密码格式错误，应为 method:password（如 aes-256-gcm:mypassword）")
	}
	return password[:idx], password[idx+1:], nil
}

func (d *shadowsocksDialer) Dial(network, addr string) (net.Conn, error) {
	// 1. 先直连到 Shadowsocks 服务器
	conn, err := net.DialTimeout("tcp", d.address, d.timeout)
	if err != nil {
		return nil, fmt.Errorf("连接 Shadowsocks 服务器失败: %w", err)
	}
	// 2. 将 conn 包装为 Shadowsocks 加密连接
	conn = d.ciph.StreamConn(conn)
	return conn, nil
}

// IsProxyConfigured 检查是否配置了代理
func IsProxyConfigured(proxyType, proxyAddress string) bool {
	return proxyType != "" && proxyAddress != ""
}
