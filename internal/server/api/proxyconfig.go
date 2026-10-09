package api

import (
	"net/http"

	"github.com/robin/hop-proxy/pkg/proxycfg"
)

// ProxyConfigHandler 代理限制配置元数据处理（#47）
type ProxyConfigHandler struct{}

func newProxyConfigHandler() *ProxyConfigHandler {
	return &ProxyConfigHandler{}
}

// Keys 返回配置项元数据（key/说明/nginx 对应/类型/默认值/是否可应用覆盖/是否需重启），
// 驱动前端下拉与提示（系统设置代理配置卡片、应用编辑覆盖）
func (h *ProxyConfigHandler) Keys(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, proxycfg.KeyDefs())
}
