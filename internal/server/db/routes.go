package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// ListAppRoutes 列出某应用的所有路由规则（按 priority ASC 排序）
func (db *DB) ListAppRoutes(appID int64) ([]AppRoute, error) {
	rows, err := db.conn.Query(
		"SELECT id, app_id, client_id, method, path_pattern, auth_method, target_url, path_rewrite, priority, enabled, created_at, updated_at FROM app_routes WHERE app_id = ? ORDER BY priority ASC, id ASC",
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []AppRoute
	for rows.Next() {
		var r AppRoute
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.AppID, &r.ClientID, &r.Method, &r.PathPattern, &r.AuthMethod, &r.TargetURL, &r.PathRewrite, &r.Priority, &r.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = ParseTime(createdAt)
		r.UpdatedAt = ParseTime(updatedAt)
		routes = append(routes, r)
	}
	if routes == nil {
		routes = []AppRoute{}
	}
	return routes, rows.Err()
}

// GetActiveRoutesForApp 获取应用的所有启用的路由规则（已排序，供代理层使用）
func (db *DB) GetActiveRoutesForApp(appID int64) ([]AppRoute, error) {
	rows, err := db.conn.Query(
		"SELECT id, app_id, client_id, method, path_pattern, auth_method, target_url, path_rewrite, priority, enabled, created_at, updated_at FROM app_routes WHERE app_id = ? AND enabled = 1 ORDER BY priority ASC, id ASC",
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var routes []AppRoute
	for rows.Next() {
		var r AppRoute
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.AppID, &r.ClientID, &r.Method, &r.PathPattern, &r.AuthMethod, &r.TargetURL, &r.PathRewrite, &r.Priority, &r.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = ParseTime(createdAt)
		r.UpdatedAt = ParseTime(updatedAt)
		routes = append(routes, r)
	}
	if routes == nil {
		routes = []AppRoute{}
	}
	return routes, rows.Err()
}

// GetAppRouteByID 根据 ID 获取路由规则，同时验证是否属于指定应用。
// 当 appID <= 0 时，仅按 ID 查询（不限制 app_id），调用方需自行验证权限。
func (db *DB) GetAppRouteByID(id int64, appID int64) (*AppRoute, error) {
	var r AppRoute
	var createdAt, updatedAt string
	var err error
	if appID > 0 {
		err = db.conn.QueryRow(
			"SELECT id, app_id, client_id, method, path_pattern, auth_method, target_url, path_rewrite, priority, enabled, created_at, updated_at FROM app_routes WHERE id = ? AND app_id = ?",
			id, appID,
		).Scan(&r.ID, &r.AppID, &r.ClientID, &r.Method, &r.PathPattern, &r.AuthMethod, &r.TargetURL, &r.PathRewrite, &r.Priority, &r.Enabled, &createdAt, &updatedAt)
	} else {
		err = db.conn.QueryRow(
			"SELECT id, app_id, client_id, method, path_pattern, auth_method, target_url, path_rewrite, priority, enabled, created_at, updated_at FROM app_routes WHERE id = ?",
			id,
		).Scan(&r.ID, &r.AppID, &r.ClientID, &r.Method, &r.PathPattern, &r.AuthMethod, &r.TargetURL, &r.PathRewrite, &r.Priority, &r.Enabled, &createdAt, &updatedAt)
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = ParseTime(createdAt)
	r.UpdatedAt = ParseTime(updatedAt)
	return &r, nil
}

// CreateAppRoute 创建路由规则
func (db *DB) CreateAppRoute(route *AppRoute) error {
	result, err := db.conn.Exec(
		`INSERT INTO app_routes (app_id, client_id, method, path_pattern, auth_method, target_url, path_rewrite, priority, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		route.AppID, route.ClientID, route.Method, route.PathPattern,
		route.AuthMethod, route.TargetURL, route.PathRewrite, route.Priority, route.Enabled,
		NowUTC(), NowUTC(),
	)
	if err != nil {
		return fmt.Errorf("创建路由规则失败: %w", err)
	}
	id, _ := result.LastInsertId()
	route.ID = id
	return nil
}

// UpdateAppRoute 更新路由规则
func (db *DB) UpdateAppRoute(route *AppRoute) error {
	result, err := db.conn.Exec(
		`UPDATE app_routes SET client_id = ?, method = ?, path_pattern = ?, auth_method = ?, target_url = ?, path_rewrite = ?, priority = ?, enabled = ?, updated_at = ?
		 WHERE id = ? AND app_id = ?`,
		route.ClientID, route.Method, route.PathPattern,
		route.AuthMethod, route.TargetURL, route.PathRewrite, route.Priority, route.Enabled,
		NowUTC(), route.ID, route.AppID,
	)
	if err != nil {
		return fmt.Errorf("更新路由规则失败: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteAppRoute 删除路由规则（需验证属于该 app）
func (db *DB) DeleteAppRoute(id int64, appID int64) error {
	result, err := db.conn.Exec(
		"DELETE FROM app_routes WHERE id = ? AND app_id = ?",
		id, appID,
	)
	if err != nil {
		return fmt.Errorf("删除路由规则失败: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// routeClientMatches 来源客户端条件匹配（MatchRoute / FindRouteAuthOverride 共用）。
//
// "来源客户端"语义 (#70)：指请求进入代理体系所经的客户端本地代理——
//   - Path 3/4（本地代理/Peer 直连）：来源 = 本地代理所在的客户端自身
//   - Path 5（Client→Server 中转）：来源 = 发起转发的客户端（fromClientID）
//   - Path 2/6/1（公网 Host/隧道）：无来源客户端，clientID 传空串，
//     任何指定了来源客户端的规则均不命中
//
// route.ClientID 为空或 "*" 视为通配（不限来源）。
func routeClientMatches(routeClientID, sourceClientID string) bool {
	if routeClientID == "" || routeClientID == "*" {
		return true
	}
	return routeClientID == sourceClientID
}

// MatchRoute 在给定的路由规则列表中匹配请求，返回第一个命中的规则（按优先级顺序）
// sourceClientID 为请求的来源客户端（语义见 routeClientMatches，公网请求传空串）。
//
// 匹配逻辑：
//   - 如果一条规则的 ClientID/Method/PathPattern 全部为空或"*"，视为无效规则跳过
//   - ClientID 匹配：见 routeClientMatches
//   - Method 匹配：route.Method 为空 或 "*" 或 忽略大小写相等
//   - PathPattern 匹配：见 matchPathPattern 函数
func MatchRoute(routes []AppRoute, method, path, sourceClientID string) *AppRoute {
	for i := range routes {
		route := &routes[i]
		if !route.Enabled {
			continue
		}
		// 三者全通配则跳过无效规则
		clientWildcard := route.ClientID == "" || route.ClientID == "*"
		methodWildcard := route.Method == "" || route.Method == "*"
		pathWildcard := route.PathPattern == ""
		if clientWildcard && methodWildcard && pathWildcard {
			continue
		}
		// 来源客户端匹配
		if !routeClientMatches(route.ClientID, sourceClientID) {
			continue
		}
		// Method 匹配
		if !methodWildcard && !strings.EqualFold(route.Method, method) {
			continue
		}
		// PathPattern 匹配
		if !pathWildcard && !matchPathPattern(route.PathPattern, path) {
			continue
		}
		return route // 返回第一个命中的规则
	}
	return nil
}

// FindRouteAuthOverride 从路由规则中查找认证方式覆盖（method + path + 来源客户端）。
// 用于在 selectClient 之前确定认证方式——来源客户端在各入口处均已知：
// 公网请求无来源（空串），Path 5 为 fromClientID，本地代理为自身 clientID (#70)。
// 命中但未配置 auth_method 的规则不返回，继续向后查找。
func FindRouteAuthOverride(routes []AppRoute, method, path, sourceClientID string) string {
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		// 三者全通配则跳过无效规则
		clientWildcard := route.ClientID == "" || route.ClientID == "*"
		methodWildcard := route.Method == "" || route.Method == "*"
		pathWildcard := route.PathPattern == ""
		if clientWildcard && methodWildcard && pathWildcard {
			continue
		}
		// 来源客户端匹配
		if !routeClientMatches(route.ClientID, sourceClientID) {
			continue
		}
		// 方法匹配
		if !methodWildcard && !strings.EqualFold(route.Method, method) {
			continue
		}
		// 路径匹配
		if !pathWildcard && !matchPathPattern(route.PathPattern, path) {
			continue
		}
		if route.AuthMethod != "" {
			return route.AuthMethod // 返回第一个命中的 auth_method 覆盖
		}
	}
	return ""
}

// IsPathExempt 检查请求路径是否在豁免列表中（#68 收口为唯一实现，
// server 侧 proxy 与 client 侧 localproxy 共用，修改时两端语义必须保持一致）。
// 支持精确匹配（如 /api/health）和前缀匹配（如 /static/）。
func IsPathExempt(path string, exemptPaths []string) bool {
	if len(exemptPaths) == 0 {
		return false
	}
	for _, p := range exemptPaths {
		if p == path {
			return true // 精确匹配
		}
		if strings.HasSuffix(p, "/") && strings.HasPrefix(path, p) {
			return true // 前缀匹配：配置 /static/ 则 /static/css/app.css 也被豁免
		}
	}
	return false
}

// matchPathPattern 检查路径是否匹配模式：
//   - 空字符串 = 匹配所有路径
//   - 不以 `/` 结尾 = 精确匹配
//   - 以 `/` 结尾 = 前缀匹配
func matchPathPattern(pattern, path string) bool {
	if pattern == "" {
		return true
	}
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}
	return path == pattern
}

// RewriteRoutePath 按命中的路由规则改写请求路径 (#53)，各代理入口（server 侧
// proxy/forward、client 侧 localproxy）统一调用本函数收口。
// 改写为「前缀替换」语义，与 path_pattern 匹配模式一一对应：
//   - 精确模式（pattern 不以 / 结尾）：匹配即 path == pattern，改写结果为 path_rewrite
//   - 前缀模式（pattern 以 / 结尾）：匹配前缀替换为 path_rewrite，
//     如 pattern=/data/、rewrite=/api/ 时 /data/foo → /api/foo
//   - pattern 为空（按方法/来源客户端匹配）：path_rewrite 作为前缀拼接，
//     如 rewrite=/api 时 /foo → /api/foo
//
// 查询串（RawQuery）不在改写范围内，由调用方保留。
// route 为 nil 或未配置改写（path_rewrite 为空）时原样返回 path。
func RewriteRoutePath(route *AppRoute, path string) string {
	if route == nil || route.PathRewrite == "" {
		return path
	}
	switch {
	case route.PathPattern == "":
		return route.PathRewrite + path
	case strings.HasSuffix(route.PathPattern, "/"):
		return route.PathRewrite + strings.TrimPrefix(path, route.PathPattern)
	default:
		return route.PathRewrite
	}
}

// ApplyRoutePathRewrite 按命中的路由规则改写请求路径 (#53)，各代理入口（server 侧
// proxy/forward、client 侧 localproxy）统一调用本函数收口。
// 发生改写时输出 info 日志（含 subdomain/app_id/original/rewritten，日志页按应用筛选可见），
// 并把改写后的路径写回 r.URL.Path。
//
// 返回改写前的原始路径；未发生改写返回空字符串。调用方负责把返回值放入请求上下文
// （HopContext.OriginalPath），随 envelope 传递给下游执行端做日志关联。
// 鉴权与跳转匹配由调用方在调用前按原始路径完成。
func ApplyRoutePathRewrite(route *AppRoute, r *http.Request, subdomain string, appID int64) string {
	original := r.URL.Path
	rewritten := RewriteRoutePath(route, original)
	if rewritten == original {
		return ""
	}
	slog.Info("路由规则改写路径", "type", "proxy", "subdomain", subdomain, "app_id", appID, "original", original, "rewritten", rewritten)
	r.URL.Path = rewritten
	return original
}
