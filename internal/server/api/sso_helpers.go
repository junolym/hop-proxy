package api

import "time"

// ssoSessionTTL 根据 app.SSOCookieMaxAge 计算 SSO 数据库会话的过期时间。
//   - -1（会话 cookie）：DB session 仍需有有限过期，使用默认 24h
//   - 0：兼容旧数据，使用默认 24h
//   - 正值：直接使用
func ssoSessionTTL(maxAge int) time.Duration {
	if maxAge <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(maxAge) * time.Second
}

// ssoCookieMaxAge 返回设置 SSO Cookie 时使用的 MaxAge 值。
//   - -1（会话 cookie 哨兵）：返回 0，不设置 Max-Age 属性，浏览器关闭即失效
//   - 0 及正值：直接返回
func ssoCookieMaxAge(maxAge int) int {
	if maxAge < 0 {
		return 0
	}
	return maxAge
}
