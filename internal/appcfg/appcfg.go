// Package appcfg 收口应用配置的模板展开（#50）。
//
// 应用/路由目标地址、自定义 Header 值、跳转目标三类配置模板，
// 全部经本包统一展开（子域名捕获组 + 请求级内置变量，见 pkg/vars），
// 各代理入口（server 侧 proxy/forward、client 侧 localproxy、probe 探测）
// 不再各自拼装展开逻辑。
package appcfg

import (
	"strings"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/vars"
)

// ResolveTargetURL 解析最终目标地址（#50）：
// 路由规则命中且配置了目标地址（routeTarget 非空）时覆盖应用级目标，
// 两个层级均做捕获组 + 内置变量展开。
func ResolveTargetURL(appTargetURL, routeTarget string, captures []string, v *vars.RequestVars) string {
	if routeTarget != "" {
		return vars.Expand(routeTarget, captures, v)
	}
	return vars.Expand(appTargetURL, captures, v)
}

// ExpandCustomHeaders 展开自定义 Header 值（key 不展开，仅展开 value）。
// 无任何占位符时原样返回入参 map，避免每请求分配。
func ExpandCustomHeaders(headers map[string]string, captures []string, v *vars.RequestVars) map[string]string {
	need := false
	for _, val := range headers {
		if strings.ContainsAny(val, "$*") {
			need = true
			break
		}
	}
	if !need {
		return headers
	}
	out := make(map[string]string, len(headers))
	for k, val := range headers {
		out[k] = vars.Expand(val, captures, v)
	}
	return out
}

// ExpandRedirects 展开跳转规则的跳转目标（redirect_target），
// 返回新的切片、不修改入参。无任何占位符时原样返回入参切片。
// 展开顺序：先本函数处理捕获组 + 内置变量，
// 再由 db.MatchRedirect 做路径正则 ExpandString（两阶段串联）。
func ExpandRedirects(redirects []db.AppRedirect, captures []string, v *vars.RequestVars) []db.AppRedirect {
	need := false
	for i := range redirects {
		if strings.ContainsAny(redirects[i].RedirectTarget, "$*") {
			need = true
			break
		}
	}
	if !need {
		return redirects
	}
	out := make([]db.AppRedirect, len(redirects))
	for i, r := range redirects {
		r.RedirectTarget = vars.Expand(r.RedirectTarget, captures, v)
		out[i] = r
	}
	return out
}
