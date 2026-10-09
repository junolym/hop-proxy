package random

import (
	"strings"

	"github.com/google/uuid"
)

// RequestID 生成请求关联 ID（#62）：复用 uuid 生成器，取 '-' 分割的最后一节
// （12 位 hex）。全链路日志靠它关联——只在请求入口生成（服务端/客户端代理入口、
// 探测），其余各跳经 X-Hop-Request-ID 带外头透传，不用完整 uuid 避免太长浪费。
func RequestID() string {
	s := uuid.New().String()
	if i := strings.LastIndex(s, "-"); i >= 0 {
		return s[i+1:]
	}
	return s
}
