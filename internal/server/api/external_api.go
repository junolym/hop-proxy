package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/robin/hop-proxy/internal/server/db"
)

// ExternalAPIHandler 外部 API 处理（/external-api/，token 鉴权，只读）
type ExternalAPIHandler struct {
	db *db.DB
}

func newExternalAPIHandler(database *db.DB) *ExternalAPIHandler {
	return &ExternalAPIHandler{db: database}
}

// externalAPIAuthMiddleware 校验 Authorization: Bearer <token>，命中 app_api_token 后将 *UserClaims 放入 ctx
func externalAPIAuthMiddleware(database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if !strings.HasPrefix(auth, "Bearer ") {
				jsonError(w, http.StatusUnauthorized, "未提供认证令牌")
				return
			}
			tokenStr := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			if tokenStr == "" {
				jsonError(w, http.StatusUnauthorized, "未提供认证令牌")
				return
			}

			user, err := database.GetUserByAppAPIToken(tokenStr)
			if err != nil {
				jsonError(w, http.StatusUnauthorized, "无效的认证令牌")
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, &UserClaims{
				UserID:   user.ID,
				Username: user.Username,
				IsAdmin:  user.IsAdmin,
				Role:     user.Role,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ListApps 返回当前用户所有已启用应用（按 last_used_at 倒序），仅暴露导航所需字段
func (h *ExternalAPIHandler) ListApps(w http.ResponseWriter, r *http.Request) {
	claims := getUserFromContext(r)
	if claims == nil {
		jsonError(w, http.StatusUnauthorized, "未认证")
		return
	}

	proxyDomain, err := h.db.GetSetting("proxy_domain")
	if err != nil || proxyDomain == "" {
		slog.ErrorContext(r.Context(), "服务端未配置 proxy_domain", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "服务端未配置 proxy_domain")
		return
	}

	apps, err := h.db.ListExternalApps(claims.UserID, proxyDomain)
	if err != nil {
		slog.ErrorContext(r.Context(), "获取应用列表失败", "type", "api", "error", err)
		jsonError(w, http.StatusInternalServerError, "获取应用列表失败")
		return
	}
	jsonOK(w, apps)
}
