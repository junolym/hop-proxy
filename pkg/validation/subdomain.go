// Package validation 提供输入验证工具函数。
package validation

import (
	"fmt"
	"regexp"

	"github.com/robin/hop-proxy/pkg/subdomain"
)

// SubdomainRegex 子域名校验正则：仅允许小写字母、数字和连字符
var SubdomainRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`)

// ValidSubdomain 检查子域名是否有效（仅允许小写字母、数字和连字符，不允许 * 或 /）。
// 适用于普通子域名校验，不适用于含通配符的模糊匹配模式。
func ValidSubdomain(s string) bool {
	return SubdomainRegex.MatchString(s)
}

// ValidSubdomainPattern 校验子域名模式是否有效。
// 模式可为：
//   - 普通子域名：仅小写字母、数字和连字符（沿用 SubdomainRegex）
//   - 模糊匹配模式：包含 `*` 或 `/`，需能成功编译为正则
//
// 返回 nil 表示有效，非 nil 错误携带具体原因。
func ValidSubdomainPattern(s string) error {
	if s == "" {
		return fmt.Errorf("子域名不能为空")
	}
	if !subdomain.IsFuzzy(s) {
		if !SubdomainRegex.MatchString(s) {
			return fmt.Errorf("子域名仅允许小写字母、数字和连字符")
		}
		return nil
	}
	if _, err := subdomain.Parse(s); err != nil {
		return err
	}
	return nil
}
