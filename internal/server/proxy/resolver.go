package proxy

import (
	"github.com/robin/hop-proxy/pkg/httputil"
)

// ExtractSubdomain 从 Host 头中提取子域名前缀
// 已移至 pkg/httputil/subdomain.go
var ExtractSubdomain = httputil.ExtractSubdomain
