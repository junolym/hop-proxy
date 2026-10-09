// Package random 提供随机数生成工具函数。
package random

import (
	"crypto/rand"
	"encoding/hex"
)

// Hex 生成 n 字节的随机十六进制字符串
func Hex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// shareCodeAlphabet 分享码字符集：大小写字母 + 数字
const shareCodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// ShareCode 生成 8 位随机分享码（大小写字母 + 数字）
func ShareCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = shareCodeAlphabet[int(b[i])%len(shareCodeAlphabet)]
	}
	return string(b)
}
