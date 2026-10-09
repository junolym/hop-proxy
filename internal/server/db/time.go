package db

import "time"

// NowUTC 返回当前 UTC 时间的 RFC3339 格式字符串
func NowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// ParseTime 解析 RFC3339 格式的时间字符串
func ParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}
