package api

import (
	"net/http"
	"runtime"
)

// VersionHandler 版本信息处理
type VersionHandler struct {
	version string
	commit  string
}

func newVersionHandler(version, commit string) *VersionHandler {
	return &VersionHandler{version: version, commit: commit}
}

// Get 返回服务端版本信息
func (h *VersionHandler) Get(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{
		"version":    h.version,
		"commit":     h.commit,
		"go_version": runtime.Version(),
	})
}
