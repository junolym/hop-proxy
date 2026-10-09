package db

import (
	"database/sql"
	"fmt"
	"regexp"
)

// ListAppRedirects 列出某应用的所有跳转规则（按 priority ASC 排序）
func (db *DB) ListAppRedirects(appID int64) ([]AppRedirect, error) {
	rows, err := db.conn.Query(
		"SELECT id, app_id, match_type, match_path, match_include_query, redirect_target, status_code, priority, enabled, created_at, updated_at FROM app_redirects WHERE app_id = ? ORDER BY priority ASC, id ASC",
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var redirects []AppRedirect
	for rows.Next() {
		var r AppRedirect
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.AppID, &r.MatchType, &r.MatchPath, &r.MatchIncludeQuery, &r.RedirectTarget, &r.StatusCode, &r.Priority, &r.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = ParseTime(createdAt)
		r.UpdatedAt = ParseTime(updatedAt)
		redirects = append(redirects, r)
	}
	if redirects == nil {
		redirects = []AppRedirect{}
	}
	return redirects, rows.Err()
}

// GetActiveRedirectsForApp 获取应用启用的跳转规则（已按 priority ASC 排序，供代理层使用）
func (db *DB) GetActiveRedirectsForApp(appID int64) ([]AppRedirect, error) {
	rows, err := db.conn.Query(
		"SELECT id, app_id, match_type, match_path, match_include_query, redirect_target, status_code, priority, enabled, created_at, updated_at FROM app_redirects WHERE app_id = ? AND enabled = 1 ORDER BY priority ASC, id ASC",
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var redirects []AppRedirect
	for rows.Next() {
		var r AppRedirect
		var createdAt, updatedAt string
		if err := rows.Scan(&r.ID, &r.AppID, &r.MatchType, &r.MatchPath, &r.MatchIncludeQuery, &r.RedirectTarget, &r.StatusCode, &r.Priority, &r.Enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = ParseTime(createdAt)
		r.UpdatedAt = ParseTime(updatedAt)
		redirects = append(redirects, r)
	}
	if redirects == nil {
		redirects = []AppRedirect{}
	}
	return redirects, rows.Err()
}

// GetAppRedirectByID 根据 ID 获取跳转规则，appID <= 0 时不限制 app_id
func (db *DB) GetAppRedirectByID(id int64, appID int64) (*AppRedirect, error) {
	var r AppRedirect
	var createdAt, updatedAt string
	var err error
	if appID > 0 {
		err = db.conn.QueryRow(
			"SELECT id, app_id, match_type, match_path, match_include_query, redirect_target, status_code, priority, enabled, created_at, updated_at FROM app_redirects WHERE id = ? AND app_id = ?",
			id, appID,
		).Scan(&r.ID, &r.AppID, &r.MatchType, &r.MatchPath, &r.MatchIncludeQuery, &r.RedirectTarget, &r.StatusCode, &r.Priority, &r.Enabled, &createdAt, &updatedAt)
	} else {
		err = db.conn.QueryRow(
			"SELECT id, app_id, match_type, match_path, match_include_query, redirect_target, status_code, priority, enabled, created_at, updated_at FROM app_redirects WHERE id = ?",
			id,
		).Scan(&r.ID, &r.AppID, &r.MatchType, &r.MatchPath, &r.MatchIncludeQuery, &r.RedirectTarget, &r.StatusCode, &r.Priority, &r.Enabled, &createdAt, &updatedAt)
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt = ParseTime(createdAt)
	r.UpdatedAt = ParseTime(updatedAt)
	return &r, nil
}

// CreateAppRedirect 创建跳转规则
func (db *DB) CreateAppRedirect(r *AppRedirect) error {
	result, err := db.conn.Exec(
		`INSERT INTO app_redirects (app_id, match_type, match_path, match_include_query, redirect_target, status_code, priority, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.AppID, r.MatchType, r.MatchPath, r.MatchIncludeQuery, r.RedirectTarget, r.StatusCode, r.Priority, r.Enabled,
		NowUTC(), NowUTC(),
	)
	if err != nil {
		return fmt.Errorf("创建跳转规则失败: %w", err)
	}
	id, _ := result.LastInsertId()
	r.ID = id
	return nil
}

// UpdateAppRedirect 更新跳转规则
func (db *DB) UpdateAppRedirect(r *AppRedirect) error {
	result, err := db.conn.Exec(
		`UPDATE app_redirects SET match_type = ?, match_path = ?, match_include_query = ?, redirect_target = ?, status_code = ?, priority = ?, enabled = ?, updated_at = ?
		 WHERE id = ? AND app_id = ?`,
		r.MatchType, r.MatchPath, r.MatchIncludeQuery, r.RedirectTarget, r.StatusCode, r.Priority, r.Enabled,
		NowUTC(), r.ID, r.AppID,
	)
	if err != nil {
		return fmt.Errorf("更新跳转规则失败: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteAppRedirect 删除跳转规则（需验证属于该 app）
func (db *DB) DeleteAppRedirect(id int64, appID int64) error {
	result, err := db.conn.Exec(
		"DELETE FROM app_redirects WHERE id = ? AND app_id = ?",
		id, appID,
	)
	if err != nil {
		return fmt.Errorf("删除跳转规则失败: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MatchRedirect 在跳转规则列表中匹配请求路径，返回跳转目标与状态码。
//
// 两阶段匹配（精确优先于正则）：
//  1. 第一遍遍历所有 MatchType=="exact" 的规则（已按 priority 排序），matchStr == matchPath 即命中
//  2. 第二遍遍历所有 MatchType=="regex" 的规则，正则匹配 matchStr；命中后用 ExpandString 展开
//     redirectTarget 中的 $1 / ${1} 占位符
//
// matchStr 取值：默认为 path；当规则的 MatchIncludeQuery=true 时为 path?query（query 为空则仍为 path），
// 用于避免「同 path 不同 query」导致跳转死循环（如 / → /?token=xxx）。
// 正则编译失败的规则被跳过（防御）。未命中返回 ok=false。
func MatchRedirect(redirects []AppRedirect, path, query string) (target string, statusCode int, ok bool) {
	// 第一遍：精确匹配
	for i := range redirects {
		r := &redirects[i]
		if !r.Enabled || r.MatchType != "exact" {
			continue
		}
		matchStr := path
		if r.MatchIncludeQuery {
			matchStr = pathWithQuery(path, query)
		}
		if r.MatchPath == matchStr {
			return r.RedirectTarget, normalizeStatusCode(r.StatusCode), true
		}
	}
	// 第二遍：正则匹配
	for i := range redirects {
		r := &redirects[i]
		if !r.Enabled || r.MatchType != "regex" {
			continue
		}
		re, err := regexp.Compile(r.MatchPath)
		if err != nil {
			continue
		}
		matchStr := path
		if r.MatchIncludeQuery {
			matchStr = pathWithQuery(path, query)
		}
		idx := re.FindStringSubmatchIndex(matchStr)
		if idx == nil {
			continue
		}
		expanded := re.ExpandString(nil, r.RedirectTarget, matchStr, idx)
		return string(expanded), normalizeStatusCode(r.StatusCode), true
	}
	return "", 0, false
}

// pathWithQuery 拼接 path 与 query，query 为空时仅返回 path
func pathWithQuery(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

// normalizeStatusCode 规范化状态码，非法值回退为 302
func normalizeStatusCode(code int) int {
	if code == 301 || code == 302 {
		return code
	}
	return 302
}
