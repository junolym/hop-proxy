// Package proxycfg 代理限制配置（#47）：key 命名对齐 nginx 指令。
// 全局默认值存 settings 表 proxy_config 键，应用级覆盖存 apps.proxy_config 列，
// 均为多行 "key: value" 文本（类 yaml，单列存储）。两级合并逻辑统一收口在本包。
package proxycfg

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 配置项类型
const (
	TypeSize = "size" // 字节大小：500、100k、50m、1g（0=不限）
	TypeTime = "time" // 时长：Go duration 格式（30s、5m），0=不限
)

// KeyDef 配置项元数据
type KeyDef struct {
	Key         string `json:"key"`          // 配置 key（对齐 nginx 指令名）
	Desc        string `json:"desc"`         // 说明
	Nginx       string `json:"nginx"`        // nginx 对应指令
	Type        string `json:"type"`         // size / time
	Default     string `json:"default"`      // 默认值
	AppOverride bool   `json:"app_override"` // 是否允许应用级覆盖
	Restart     bool   `json:"restart"`      // 是否需重启服务端生效（监听器级参数）
}

// keyDefs 配置项注册表（顺序即 Format 输出顺序）
var keyDefs = []KeyDef{
	{Key: "client_max_body_size", Desc: "请求体最大大小（0=不限）", Nginx: "client_max_body_size", Type: TypeSize, Default: "50m", AppOverride: true},
	{Key: "proxy_connect_timeout", Desc: "连接目标超时（0=不限）", Nginx: "proxy_connect_timeout", Type: TypeTime, Default: "30s", AppOverride: true},
	{Key: "proxy_read_timeout", Desc: "读响应超时：连接+响应头总时长，不限制 body 流式传输（0=不限）", Nginx: "proxy_read_timeout", Type: TypeTime, Default: "30s", AppOverride: true},
	{Key: "client_header_timeout", Desc: "读请求头超时（防 Slowloris）", Nginx: "client_header_timeout", Type: TypeTime, Default: "10s", Restart: true},
	{Key: "client_body_timeout", Desc: "读请求体超时（绝对时长，含头部；0=不限，可解大文件慢速上传被掐断的问题）", Nginx: "client_body_timeout", Type: TypeTime, Default: "60s", Restart: true},
	{Key: "send_timeout", Desc: "写响应超时（0=不限，SSE/流式响应需要不限）", Nginx: "send_timeout", Type: TypeTime, Default: "0", Restart: true},
	{Key: "keepalive_timeout", Desc: "Keep-Alive 空闲超时", Nginx: "keepalive_timeout", Type: TypeTime, Default: "120s", Restart: true},
	{Key: "client_header_buffer_size", Desc: "请求头上限（对应 nginx large_client_header_buffers 的请求头大小限制）", Nginx: "large_client_header_buffers", Type: TypeSize, Default: "1m", Restart: true},
}

// KeyDefs 返回配置项元数据（供 API 下发，驱动前端下拉与提示）
func KeyDefs() []KeyDef {
	// 返回副本，防止调用方修改注册表
	out := make([]KeyDef, len(keyDefs))
	copy(out, keyDefs)
	return out
}

// keyDefMap key → 元数据
var keyDefMap = func() map[string]KeyDef {
	m := make(map[string]KeyDef, len(keyDefs))
	for _, d := range keyDefs {
		m[d.Key] = d
	}
	return m
}()

// ParseSize 解析大小值：纯数字=字节，k/m/g 后缀=KiB/MiB/GiB，0=不限
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("大小值不能为空")
	}
	unit := int64(1)
	switch s[len(s)-1] {
	case 'k':
		unit, s = 1024, s[:len(s)-1]
	case 'm':
		unit, s = 1024*1024, s[:len(s)-1]
	case 'g':
		unit, s = 1024*1024*1024, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("无效的大小值: %q", s)
	}
	return n * unit, nil
}

// ParseDuration 解析时长值：Go duration 格式（30s、5m）或 0（不限）
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("时长值不能为空")
	}
	if s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("无效的时长值: %q（示例：30s、5m、1h，0 表示不限）", s)
	}
	return d, nil
}

// Parse 解析多行 "key: value" 文本为 map。
// 校验 key 是否注册、值是否符合类型、是否重复，出错返回带行号的错误。
// 空文本返回空 map（不报错）。
func Parse(text string) (map[string]string, error) {
	vals := make(map[string]string)
	if strings.TrimSpace(text) == "" {
		return vals, nil
	}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("第 %d 行格式错误，应为 key: value", i+1)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		def, ok := keyDefMap[key]
		if !ok {
			return nil, fmt.Errorf("第 %d 行：未知的配置项 %q", i+1, key)
		}
		if _, dup := vals[key]; dup {
			return nil, fmt.Errorf("第 %d 行：配置项 %q 重复", i+1, key)
		}
		var err error
		switch def.Type {
		case TypeSize:
			_, err = ParseSize(value)
		case TypeTime:
			_, err = ParseDuration(value)
		}
		if err != nil {
			return nil, fmt.Errorf("第 %d 行：%v", i+1, err)
		}
		vals[key] = value
	}
	return vals, nil
}

// ValidateForApp 校验应用级覆盖文本：只允许出现应用级 key
func ValidateForApp(text string) error {
	vals, err := Parse(text)
	if err != nil {
		return err
	}
	for key := range vals {
		if !keyDefMap[key].AppOverride {
			return fmt.Errorf("配置项 %q 不支持应用级覆盖（仅全局可配置）", key)
		}
	}
	return nil
}

// Merge 合并全局配置与应用级覆盖，返回每个 key 的最终生效值（未设置的使用默认值）。
// 解析失败的文本按空处理（写入时已校验，此处防御）。
func Merge(globalText, appText string) map[string]string {
	global, err := Parse(globalText)
	if err != nil {
		global = map[string]string{}
	}
	app, err := Parse(appText)
	if err != nil {
		app = map[string]string{}
	}
	out := make(map[string]string, len(keyDefs))
	for _, def := range keyDefs {
		v := def.Default
		if gv, ok := global[def.Key]; ok {
			v = gv
		}
		// 应用级覆盖只允许覆盖 AppOverride 的 key
		if def.AppOverride {
			if av, ok := app[def.Key]; ok {
				v = av
			}
		}
		out[def.Key] = v
	}
	return out
}

// Format 按注册表顺序将 map 序列化为多行 "key: value" 文本（只输出已设置的 key）
func Format(vals map[string]string) string {
	var b strings.Builder
	for _, def := range keyDefs {
		if v, ok := vals[def.Key]; ok {
			fmt.Fprintf(&b, "%s: %s\n", def.Key, v)
		}
	}
	return b.String()
}

// SizeOf 取生效的大小值（字节）；未设置或值非法时回退默认值。0=不限
func SizeOf(vals map[string]string, key string) int64 {
	if v, ok := vals[key]; ok {
		if n, err := ParseSize(v); err == nil {
			return n
		}
	}
	n, _ := ParseSize(keyDefMap[key].Default)
	return n
}

// DurationOf 取生效的时长；未设置或值非法时回退默认值。0=不限
func DurationOf(vals map[string]string, key string) time.Duration {
	if v, ok := vals[key]; ok {
		if d, err := ParseDuration(v); err == nil {
			return d
		}
	}
	d, _ := ParseDuration(keyDefMap[key].Default)
	return d
}
