package api

import (
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/robin/hop-proxy/internal/server"
	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/loghandler"
	"github.com/robin/hop-proxy/pkg/httputil"
	"github.com/robin/hop-proxy/pkg/random"
)

type contextKey string

const userContextKey contextKey = "user"
const authSessionContextKey contextKey = "auth_session"

// UserClaims JWT 载荷
type UserClaims struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	IsAdmin     bool   `json:"is_admin"`
	Role        string `json:"role"`
	SessionID   string `json:"sid,omitempty"` // 登录会话标识（#80，服务端记录 auth_sessions）
	TOTPPending bool   `json:"totp_pending"`  // 是否为 TOTP 待验证状态的临时 token
	jwt.RegisteredClaims
}

// getUserFromContext 从请求上下文获取当前用户信息
func getUserFromContext(r *http.Request) *UserClaims {
	claims, _ := r.Context().Value(userContextKey).(*UserClaims)
	return claims
}

// getAuthSessionFromContext 从请求上下文获取当前登录会话（authMiddleware 注入，#80）
func getAuthSessionFromContext(r *http.Request) *db.AuthSession {
	s, _ := r.Context().Value(authSessionContextKey).(*db.AuthSession)
	return s
}

// csrfMiddleware 使用双提交 Cookie 模式验证 CSRF Token。
// 仅对写操作（POST/PUT/DELETE）生效，GET/HEAD/OPTIONS 跳过。
func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		headerToken := r.Header.Get("X-CSRF-Token")
		cookie, err := r.Cookie(server.CookieCSRF)
		if err != nil || cookie.Value == "" || headerToken == "" || headerToken != cookie.Value {
			jsonError(w, http.StatusForbidden, "CSRF 校验失败")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// adminMiddleware 管理员权限中间件（需先经过 authMiddleware）
func adminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := getUserFromContext(r)
		if claims == nil || !claims.IsAdmin {
			jsonError(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guestBlockMiddleware 访客拦截中间件，禁止访客访问管理功能
func guestBlockMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := getUserFromContext(r)
		if claims != nil && claims.Role == "guest" {
			slog.WarnContext(r.Context(), "访客权限拒绝", "type", "api")
			jsonError(w, http.StatusForbidden, "访客用户无权访问此功能")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// recoveryMiddleware 全局 panic 恢复中间件，防止 panic 导致进程崩溃
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				slog.ErrorContext(r.Context(), "请求处理 panic", "type", "api", "error", err, "method", r.Method, "path", r.URL.Path)
				http.Error(w, "内部服务器错误", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// gzipWriter gzip 响应写入器，实现 http.ResponseWriter 接口
type gzipWriter struct {
	http.ResponseWriter
	gw *gzip.Writer
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	return g.gw.Write(b)
}

func (g *gzipWriter) Close() {
	g.gw.Close()
}

// compressionMiddleware 对 API JSON 响应启用 gzip 压缩
type compressionMiddleware struct {
	next http.Handler
}

func (m *compressionMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 不压缩 WebSocket 升级请求
	if strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		m.next.ServeHTTP(w, r)
		return
	}
	// 不压缩 SSE 流（EventSource 不支持 gzip，且缓冲会破坏实时性）
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		m.next.ServeHTTP(w, r)
		return
	}
	// 仅在客户端支持 gzip 时压缩
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		m.next.ServeHTTP(w, r)
		return
	}

	gw := gzip.NewWriter(w)
	defer gw.Close()

	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Del("Content-Length") // gzip 后长度会变

	m.next.ServeHTTP(&gzipWriter{ResponseWriter: w, gw: gw}, r)
}

// loggingMiddleware 请求日志中间件：为每个管理 API 请求生成全链路 request_id、
// 注入日志元数据（type=api + request_id，供 handler 内 slog.*Context 自动携带，
// #62/#78），并记录状态码/响应大小/耗时（>=400 记 Warn，否则 Debug，
// 避免轮询类接口刷屏）。
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// 管理 API 请求独立生成 request_id（与代理链路隔离）
		ctx := loghandler.WithContext(r.Context(), loghandler.LogMeta{
			Type:      "api",
			RequestID: random.RequestID(),
		})
		// StatusRecorder 已实现 Unwrap/Flush，兼容 /ws 隧道升级与 SSE 日志流
		rec := httputil.NewStatusRecorder(w)
		next.ServeHTTP(rec, r.WithContext(ctx))

		level := slog.LevelDebug
		if rec.Status() >= 400 {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "HTTP 请求",
			"type", "api",
			"method", r.Method,
			"path", r.URL.Path,
			"host", r.Host,
			"status", rec.Status(),
			"size", rec.BytesWritten(),
			"duration", time.Since(start), // loghandler.slogAttrToField 会格式化 Duration
		)
	})
}

// authMiddleware JWT 认证中间件
func authMiddleware(database *db.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var tokenStr string

			// 优先从 httpOnly Cookie 读取
			if cookie, err := r.Cookie(server.CookieSession); err == nil && cookie.Value != "" {
				tokenStr = cookie.Value
			}

			// 兼容 Bearer Token（API 调用场景）
			if tokenStr == "" {
				auth := r.Header.Get("Authorization")
				if strings.HasPrefix(auth, "Bearer ") {
					tokenStr = strings.TrimPrefix(auth, "Bearer ")
				}
			}

			if tokenStr == "" {
				jsonError(w, http.StatusUnauthorized, "未提供认证令牌")
				return
			}

			// 获取 JWT 密钥
			secret, err := database.GetSetting("jwt_secret")
			if err != nil {
				slog.ErrorContext(r.Context(), "获取 JWT 密钥失败", "type", "api", "error", err)
				jsonError(w, http.StatusInternalServerError, "获取 JWT 密钥失败")
				return
			}

			// 解析验证 Token
			claims := &UserClaims{}
			token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("不支持的签名方法: %v", t.Header["alg"])
				}
				return []byte(secret), nil
			})
			if err != nil || !token.Valid {
				jsonError(w, http.StatusUnauthorized, "无效的认证令牌")
				return
			}

			// 登录会话校验（#80）：sid 必须存在且会话有效（未退出/未被强制下线/未过期），
			// 使"会话管理"页的强制下线即时生效；无 sid 的 token 一律不予放行。
			if claims.TOTPPending || claims.SessionID == "" {
				jsonError(w, http.StatusUnauthorized, "会话已失效，请重新登录")
				return
			}
			authSession, err := database.GetAuthSessionBySID(claims.SessionID)
			if err != nil || !authSession.Active() {
				jsonError(w, http.StatusUnauthorized, "会话已失效，请重新登录")
				return
			}
			// 节流更新最后活跃时间（#80）
			database.TouchAuthSession(claims.SessionID)

			// 将用户信息与登录会话放入上下文；同时补记 user_id 到日志元数据
			// （type/request_id 由 loggingMiddleware 注入，#62）
			ctx := context.WithValue(r.Context(), userContextKey, claims)
			ctx = context.WithValue(ctx, authSessionContextKey, authSession)
			meta := loghandler.FromContext(ctx)
			meta.UserID = &claims.UserID
			ctx = loghandler.WithContext(ctx, meta)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
