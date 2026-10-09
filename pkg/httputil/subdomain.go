package httputil

import "strings"

// ExtractSubdomain 从 Host 头中提取子域名前缀
// 例如：host="app1.proxy.example.com", proxyDomain="proxy.example.com" -> "app1"
func ExtractSubdomain(host, proxyDomain string) string {
	if proxyDomain == "" {
		return ""
	}

	// 去掉端口号
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}

	host = strings.ToLower(host)
	proxyDomain = strings.ToLower(proxyDomain)

	// 检查 host 是否以 .proxyDomain 结尾
	suffix := "." + proxyDomain
	if strings.HasSuffix(host, suffix) {
		sub := strings.TrimSuffix(host, suffix)
		if sub != "" {
			return sub
		}
	}

	return ""
}
