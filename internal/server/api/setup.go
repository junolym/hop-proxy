package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/pkg/password"
	"github.com/robin/hop-proxy/pkg/random"
)

// SetupHandler 引导配置处理
type SetupHandler struct {
	db *db.DB
}

func newSetupHandler(database *db.DB) *SetupHandler {
	return &SetupHandler{db: database}
}

// Status 查询是否已初始化
func (h *SetupHandler) Status(w http.ResponseWriter, r *http.Request) {
	initialized, err := h.db.IsInitialized()
	if err != nil {
		slog.ErrorContext(r.Context(), "检查初始化状态失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "检查初始化状态失败")
		return
	}
	jsonOK(w, map[string]bool{"initialized": initialized})
}

// Init 执行初始化
func (h *SetupHandler) Init(w http.ResponseWriter, r *http.Request) {
	// 检查是否已初始化
	initialized, _ := h.db.IsInitialized()
	if initialized {
		jsonError(w, http.StatusBadRequest, "系统已初始化")
		return
	}

	var req struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		SiteName    string `json:"site_name"`
		AdminDomain string `json:"admin_domain"`
		ProxyDomain string `json:"proxy_domain"`
	}
	if err := decodeJSON(r, &req); err != nil {
		jsonError(w, http.StatusBadRequest, server.ErrInvalidRequest)
		return
	}

	// 校验必填字段
	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	req.SiteName = strings.TrimSpace(req.SiteName)
	req.AdminDomain = strings.TrimSpace(req.AdminDomain)
	req.ProxyDomain = strings.TrimSpace(req.ProxyDomain)

	if req.Username == "" || req.Password == "" || req.AdminDomain == "" || req.ProxyDomain == "" {
		jsonError(w, http.StatusBadRequest, "用户名、密码、管理域名和代理域名不能为空")
		return
	}
	if len(req.Password) < 6 {
		jsonError(w, http.StatusBadRequest, server.ErrPasswordTooShort)
		return
	}
	if req.SiteName == "" {
		req.SiteName = "HopProxy"
	}

	// 哈希密码
	hash, err := password.HashPassword(req.Password)
	if err != nil {
		slog.ErrorContext(r.Context(), "密码加密失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "密码加密失败")
		return
	}

	// 生成 JWT 密钥
	jwtSecret := random.Hex(32)

	// 执行初始化
	if err := h.db.Initialize(req.Username, hash, req.SiteName, req.AdminDomain, req.ProxyDomain, jwtSecret); err != nil {
		slog.ErrorContext(r.Context(), "初始化失败: ", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "初始化失败: "+err.Error())
		return
	}

	slog.InfoContext(r.Context(), "系统初始化完成", "type", "api", "admin_username", req.Username)
	jsonMsg(w, "初始化成功")
}
