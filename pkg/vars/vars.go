// Package vars 提供应用配置模板的统一变量展开（#50）。
//
// 一次扫描同时处理两类占位符：
//   - 子域名捕获组：`$n` / `${n}` / `*`（语义迁移自原 subdomain.Expand）
//   - 命名内置变量：`${host}` / `${subdomain}` 等（见 RequestVars）
//
// 未知的变量名原样输出，存量配置（仅使用捕获组）行为不变。
package vars

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/ssoutil"
)

// RequestVars 请求级内置变量集合（#50），由各代理入口在鉴权后统一构造，
// 再交给 Expand / appcfg 展开应用配置模板。
type RequestVars struct {
	Host      string // ${host}：原始请求完整域名（如 app1.proxy.example.com）
	Subdomain string // ${subdomain}：命中的子域名（模糊匹配时为实际命中的串）
	Proto     string // ${proto}：浏览器侧协议 http/https
	RemoteIP  string // ${remote_ip}：最原始客户端 IP（信任 X-Forwarded-For 链）
	Method    string // ${method}：请求方法
	Path      string // ${path}：请求路径
	Query     string // ${query}：原始 query（不含 ?）
	UserID    int64  // ${user_id}：鉴权后用户 ID（匿名/未鉴权为 0）
	UserName  string // ${user_name}：鉴权后用户名（未知为空）
	AppID     int64  // ${app_id}：应用 ID
	AppName   string // ${app_name}：应用名称
}

// FromRequest 从 HTTP 请求提取请求级内置变量（#50）。
// 只提取请求上下文可得的字段；鉴权用户（UserID/UserName）与应用信息
// （AppID/AppName）由调用方在鉴权后填充。
func FromRequest(r *http.Request, subdomain string) *RequestVars {
	return &RequestVars{
		Host:      ssoutil.ExtractHost(r.Host, r.Header.Get("X-Forwarded-Host")),
		Subdomain: subdomain,
		Proto:     ssoutil.ExtractScheme(r.TLS != nil, r.Header.Get("X-Forwarded-Proto")),
		RemoteIP:  httputil.ClientIP(r),
		Method:    r.Method,
		Path:      r.URL.Path,
		Query:     r.URL.RawQuery,
	}
}

// Expand 将模板中的占位符替换为捕获组或内置变量值，一次性扫描、无正则。
//
// 捕获组占位符（语义迁移自原 subdomain.Expand）：
//   - `$n`（n 为 1-9）→ captures[n-1]
//   - `${n}` / `${nn}` → captures[n-1]（支持多位数字）
//   - `*` → captures[0]（即等价于 `${1}`，多处 `*` 全部按首个捕获组替换，
//     这是有意的简化：模板作者不需要区分多个捕获组的编号）
//   - 超出捕获组范围或 `$` 后非数字：原样输出
//   - 不存在 `$0`（与正则惯例不同，子域名匹配只暴露捕获组，不暴露完整匹配）
//   - `*` 仅在 len(captures) > 0 时才被解释为占位符；captures 为空时保持字面量
//
// 命名内置变量：
//   - `${name}` → v 中对应字段（name 见 RequestVars 注释）
//   - v 为 nil、未知名或值未知：原样输出整段 `${...}`
//
// 该函数与 regexp.Regexp.ExpandString 互不兼容：本函数对模板做一次性扫描，
// 不依赖任何外部 regex 上下文。调用方若需将子域名捕获与路径正则捕获叠加，
// 应先用 Expand 替换子域名捕获与内置变量，再调用 ExpandString 处理路径正则捕获
// （注意：若捕获值本身含 `$`，会被 ExpandString 二次解释，属已知边界情况）。
func Expand(template string, captures []string, v *RequestVars) string {
	if !containsPlaceholder(template) {
		return template
	}
	var sb strings.Builder
	i := 0
	for i < len(template) {
		c := template[i]
		if c != '$' && c != '*' {
			sb.WriteByte(c)
			i++
			continue
		}
		// `*` 简写为 ${1}：captures 为空时保留字面量
		if c == '*' {
			if len(captures) > 0 {
				sb.WriteString(captures[0])
			} else {
				sb.WriteByte('*')
			}
			i++
			continue
		}
		// 遇到 $：尝试解析 $n 或 ${n} / ${name}
		if i+1 >= len(template) {
			sb.WriteByte('$')
			i++
			continue
		}
		// ${n} / ${name} 形式
		if template[i+1] == '{' {
			j := i + 2
			for j < len(template) && template[j] != '}' {
				j++
			}
			if j >= len(template) {
				// 没有闭合 }，原样输出 $
				sb.WriteByte('$')
				i++
				continue
			}
			inner := template[i+2 : j]
			if num, ok := parseDigits(inner); ok {
				// 数字占位符：越界原样输出整段 ${n}
				if num < 1 || num > len(captures) {
					sb.WriteString(template[i : j+1])
				} else {
					sb.WriteString(captures[num-1])
				}
				i = j + 1
				continue
			}
			// 命名内置变量：未知名原样输出整段 ${name}
			if val, ok := v.lookup(inner); ok {
				sb.WriteString(val)
			} else {
				sb.WriteString(template[i : j+1])
			}
			i = j + 1
			continue
		}
		// $n 形式（n 为 1-9，仅单位数；多位数必须用 ${} 形式）
		if template[i+1] >= '1' && template[i+1] <= '9' {
			num := int(template[i+1] - '0')
			if num > len(captures) {
				sb.WriteString(template[i : i+2])
			} else {
				sb.WriteString(captures[num-1])
			}
			i += 2
			continue
		}
		// $ 后非数字非 {：原样输出 $
		sb.WriteByte('$')
		i++
	}
	return sb.String()
}

// lookup 按变量名查找内置变量值，未知名返回 ok=false。
func (v *RequestVars) lookup(name string) (string, bool) {
	if v == nil {
		return "", false
	}
	switch name {
	case "host":
		return v.Host, true
	case "subdomain":
		return v.Subdomain, true
	case "proto":
		return v.Proto, true
	case "remote_ip":
		return v.RemoteIP, true
	case "method":
		return v.Method, true
	case "path":
		return v.Path, true
	case "query":
		return v.Query, true
	case "user_id":
		return strconv.FormatInt(v.UserID, 10), true
	case "user_name":
		return v.UserName, true
	case "app_id":
		return strconv.FormatInt(v.AppID, 10), true
	case "app_name":
		return v.AppName, true
	}
	return "", false
}

// containsPlaceholder 判断模板是否包含任意占位符起始字符（$ 或 *）。
func containsPlaceholder(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '$' || s[i] == '*' {
			return true
		}
	}
	return false
}

// parseDigits 解析纯数字字符串，返回数字与是否成功。
func parseDigits(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0, false // 防止溢出
		}
	}
	return n, true
}
