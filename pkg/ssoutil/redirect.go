package ssoutil

import (
	"fmt"
	"net/url"
	"strings"
)

// RedirectInfo 包含构建 SSO 重定向 URL 所需的信息
type RedirectInfo struct {
	AdminDomain string
	ProxyDomain string
	Scheme      string
	Host        string
	RequestURI  string
}

// BuildSSORedirectURL 构建 SSO 重定向 URL
func (info *RedirectInfo) BuildSSORedirectURL() (ssoURL string, err error) {
	requestHost := info.Host
	if idx := strings.LastIndex(requestHost, ":"); idx != -1 {
		requestHost = requestHost[:idx]
	}

	if info.ProxyDomain == "" || !strings.HasSuffix(requestHost, info.ProxyDomain) {
		return "", fmt.Errorf("无效的请求来源")
	}

	currentURL := fmt.Sprintf("%s://%s%s", info.Scheme, info.Host, info.RequestURI)

	params := url.Values{}
	params.Set("redirect", currentURL)

	ssoURL = fmt.Sprintf("%s://%s/sso?%s", info.Scheme, info.AdminDomain, params.Encode())
	return ssoURL, nil
}

// BuildSSORedirectURL 直接构建 SSO 重定向 URL
func BuildSSORedirectURL(adminDomain, proxyDomain, scheme, host, requestURI string) (string, error) {
	info := &RedirectInfo{
		AdminDomain: adminDomain,
		ProxyDomain: proxyDomain,
		Scheme:      scheme,
		Host:        host,
		RequestURI:  requestURI,
	}
	return info.BuildSSORedirectURL()
}

// ExtractScheme 从 TLS 状态和 X-Forwarded-Proto header 提取协议
func ExtractScheme(isTLS bool, xForwardedProto string) string {
	if isTLS || xForwardedProto == "https" {
		return "https"
	}
	return "http"
}

// ExtractHost 从原始 Host 和 X-Forwarded-Host header 提取主机名
func ExtractHost(originalHost, xForwardedHost string) string {
	if xForwardedHost != "" {
		return xForwardedHost
	}
	return originalHost
}
