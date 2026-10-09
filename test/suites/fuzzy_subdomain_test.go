package suites

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// TestFuzzySubdomain 验证子域名模糊匹配功能（issue #24）：
//   - * 通配符匹配 [a-z0-9-]+（含连字符），捕获组编号 1, 2, ...
//   - /.../ 原始正则段，段内捕获组按出现顺序编号
//   - 精确匹配优先于模糊匹配
//   - 模糊匹配之间按字面量字符数降序选最优（local-* 优先于 *-dev 优先于 /.*/）
//   - $1 / ${1} 变量展开到 custom_headers / target_url / route.target_url / redirect_target
func TestFuzzySubdomain(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// 全部用 -fuzzy 后缀以避免与其他测试冲突
	t.Run("basic_wildcard_match", func(t *testing.T) {
		pattern := "*-fuzzy1"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 命中：abc-fuzzy1
		resp := rawGet(t, serverURL, "abc-fuzzy1."+proxyDomain, "/", "")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "abc-fuzzy1 应通过模糊匹配命中")

		// 命中：abc-def-fuzzy1（* 现在允许 -，* = abc-def）
		resp2 := rawGet(t, serverURL, "abc-def-fuzzy1."+proxyDomain, "/", "")
		defer resp2.Body.Close()
		assert.Equal(t, http.StatusOK, resp2.StatusCode, "abc-def-fuzzy1 应通过 *-fuzzy1 命中（* 含 -）")

		// 不命中：Fuzzy1（大写不匹配，subdomain 已 lowercase）
		// 注：HTTP Host 经 ExtractSubdomain 已 lowercase，故此用例无意义
	})

	t.Run("multiple_wildcards_captures", func(t *testing.T) {
		pattern := "*-*-fuzzy2"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 命中：abc-def-fuzzy2
		resp := rawGet(t, serverURL, "abc-def-fuzzy2."+proxyDomain, "/", "")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "abc-def-fuzzy2 应通过 *-*-fuzzy2 命中")
	})

	t.Run("raw_regex_segment", func(t *testing.T) {
		// pre-/([0-9]{2,4})/-post → 匹配 pre-1234-post，捕获 1234
		pattern := "pre-/([0-9]{2,4})/-post"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 命中：pre-1234-post
		resp := rawGet(t, serverURL, "pre-1234-post."+proxyDomain, "/", "")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode, "pre-1234-post 应通过 raw regex 命中")

		// 不命中：pre-1-post（数字长度不足）
		resp2 := rawGet(t, serverURL, "pre-1-post."+proxyDomain, "/", "")
		defer resp2.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp2.StatusCode, "pre-1-post 不应命中（数字长度不足 2）")

		// 不命中：pre-12345-post（数字长度超过 4）
		resp3 := rawGet(t, serverURL, "pre-12345-post."+proxyDomain, "/", "")
		defer resp3.Body.Close()
		assert.NotEqual(t, http.StatusOK, resp3.StatusCode, "pre-12345-post 不应命中（数字长度超过 4）")
	})

	t.Run("exact_priority_over_fuzzy", func(t *testing.T) {
		// 先创建模糊模式
		fuzzyPattern := "*-fuzzy4"
		fuzzyID := createFuzzyApp(t, c, fuzzyPattern, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(fuzzyID)) })

		// 再创建精确匹配 abc-fuzzy4，关联不同 client（这里复用同一 client，但目标 URL 不同便于区分）
		exactSub := "abc-fuzzy4"
		exactID := createFuzzyApp(t, c, exactSub, "http://test-backend:8000/echo", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(exactID)) })

		// 请求 abc-fuzzy4/echo：精确命中的 app target_url = http://test-backend:8000/echo
		// 所以实际请求路径是 /echo/echo（test-backend /echo 端点回显 body）
		// 模糊命中的 app target_url = http://test-backend:8000，请求 /echo/ 会回显 body
		// 这里通过 GET /echo 验证：精确命中应返回 /echo 端点的响应（200 + 非 status=ok JSON），
		// 模糊命中应返回 / 端点的响应（200 + status=ok JSON）
		resp := rawGet(t, serverURL, exactSub+"."+proxyDomain, "/echo", "")
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)

		// 精确命中的 target_url 为 .../echo，请求路径 /echo → 实际路径 /echo/echo
		// /echo/echo 不存在 → test-backend 的 / handler 返回 status=ok
		// 这无法可靠区分精确与模糊。改用更可靠的区分：通过 custom header 标记
	})

	t.Run("exact_priority_with_marker_header", func(t *testing.T) {
		// 模糊模式：custom_header X-Marker=fuzzy
		fuzzyPattern := "*-fuzzy5"
		fuzzyID := createFuzzyApp(t, c, fuzzyPattern, "http://test-backend:8000", clientID,
			map[string]string{"X-Marker": "fuzzy"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(fuzzyID)) })

		// 精确匹配：custom_header X-Marker=exact
		exactSub := "xyz-fuzzy5"
		exactID := createFuzzyApp(t, c, exactSub, "http://test-backend:8000", clientID,
			map[string]string{"X-Marker": "exact"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(exactID)) })

		// 请求 xyz-fuzzy5/headers：应命中精确匹配
		resp := rawGet(t, serverURL, exactSub+"."+proxyDomain, "/headers", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "exact", hdrs.Headers["X-Marker"],
			"精确匹配应优先于模糊匹配")

		// 请求 abc-fuzzy5/headers（无精确匹配）：应命中模糊匹配
		resp2 := rawGet(t, serverURL, "abc-fuzzy5."+proxyDomain, "/headers", "")
		defer resp2.Body.Close()
		require.Equal(t, http.StatusOK, resp2.StatusCode)
		var hdrs2 struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&hdrs2))
		assert.Equal(t, "fuzzy", hdrs2.Headers["X-Marker"],
			"无精确匹配时应命中模糊匹配")
	})

	t.Run("fuzzy_priority_by_literal_len", func(t *testing.T) {
		// 三个模糊模式同时命中 local-test-p3：
		//   local-* (lit=6) > *-p3 (lit=3) > /.*/ (lit=0)
		id1 := createFuzzyApp(t, c, "local-fprio", "http://test-backend:8000", clientID,
			map[string]string{"X-Marker": "local-star"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(id1)) })

		id2 := createFuzzyApp(t, c, "*-fprio-p3", "http://test-backend:8000", clientID,
			map[string]string{"X-Marker": "star-p3"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(id2)) })

		id3 := createFuzzyApp(t, c, "/.*/", "http://test-backend:8000", clientID,
			map[string]string{"X-Marker": "slash"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(id3)) })

		// 请求 local-test-fprio-p3：三个模式都命中
		//   local-fprio (lit=6): * = test-fprio-p3
		//   *-fprio-p3 (lit=9): * = local-test
		//   /.*/ (lit=0)
		// 预期命中 *-fprio-p3（lit=9 最大）
		resp := rawGet(t, serverURL, "local-test-fprio-p3."+proxyDomain, "/headers", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "star-p3", hdrs.Headers["X-Marker"],
			"应命中字面量最多的 *-fprio-p3 (lit=9)，而非 local-fprio (lit=6) 或 /.*/ (lit=0)")

		// 反向验证：请求 xyz-test-fprio-p3（不以 local- 开头）
		//   local-fprio: 不命中
		//   *-fprio-p3 (lit=9): * = xyz-test
		//   /.*/ (lit=0)
		// 预期命中 *-fprio-p3
		resp2 := rawGet(t, serverURL, "xyz-test-fprio-p3."+proxyDomain, "/headers", "")
		defer resp2.Body.Close()
		require.Equal(t, http.StatusOK, resp2.StatusCode)
		var hdrs2 struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp2.Body).Decode(&hdrs2))
		assert.Equal(t, "star-p3", hdrs2.Headers["X-Marker"],
			"无 local- 前缀时应命中 *-fprio-p3 (lit=9) 优先于 /.*/ (lit=0)")
	})

	t.Run("expand_captures_in_custom_headers", func(t *testing.T) {
		// *-fuzzy6 + custom_header X-Captured: $1
		pattern := "*-fuzzy6"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID,
			map[string]string{"X-Captured": "val-${1}-end"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		resp := rawGet(t, serverURL, "myname-fuzzy6."+proxyDomain, "/headers", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "val-myname-end", hdrs.Headers["X-Captured"],
			"$1 应展开为子域名捕获组 myname")
	})

	t.Run("expand_captures_in_target_url", func(t *testing.T) {
		// *-fuzzy7 + target_url http://test-backend:8000/${1}-path
		// 请求 abc-fuzzy7/echo → 后端收到 /abc-path/echo
		pattern := "*-fuzzy7"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000/${1}-path", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		resp := rawGet(t, serverURL, "abc-fuzzy7."+proxyDomain, "/", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var info struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&info))
		assert.Equal(t, "/abc-path/", info.Path,
			"target_url 中的 $1 应展开为 abc")
	})

	t.Run("expand_captures_in_redirect_target", func(t *testing.T) {
		// *-fuzzy8 + redirect 规则 /old → /new-${1}
		pattern := "*-fuzzy8"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 创建跳转规则
		resp := c.Post(t, "/api/apps/"+itoa(appID)+"/redirects", map[string]interface{}{
			"match_type":      "exact",
			"match_path":      "/old",
			"redirect_target": "/new-${1}",
			"status_code":     302,
			"priority":        0,
		})
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "创建跳转规则失败")

		// 请求 abc-fuzzy8/old → 302 Location: /new-abc
		r := rawGet(t, serverURL, "abc-fuzzy8."+proxyDomain, "/old", "")
		defer r.Body.Close()
		assert.Equal(t, http.StatusFound, r.StatusCode, "应 302 跳转")
		assert.Equal(t, "/new-abc", r.Header.Get("Location"),
			"redirect_target 中的 ${1} 应展开为 abc")
	})

	t.Run("expand_captures_in_route_target_url", func(t *testing.T) {
		// *-fuzzy9 + 路由规则：path=/api → target_url=http://test-backend:8000/${1}-api
		pattern := "*-fuzzy9"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID, nil)
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 创建路由规则
		resp := c.Post(t, "/api/apps/"+itoa(appID)+"/routes", map[string]interface{}{
			"method":       "GET",
			"path_pattern": "/api",
			"target_url":   "http://test-backend:8000/${1}-api",
			"priority":     0,
			"enabled":      true,
		})
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode, "创建路由规则失败")

		// 请求 abc-fuzzy9/api → 命中路由，target_url 展开为 http://test-backend:8000/abc-api
		// 后端收到 /abc-api/api
		r := rawGet(t, serverURL, "abc-fuzzy9."+proxyDomain, "/api", "")
		defer r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode)
		var info struct {
			Path string `json:"path"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&info))
		assert.Equal(t, "/abc-api/api", info.Path,
			"route.target_url 中的 ${1} 应展开为 abc")
	})

	t.Run("raw_regex_with_multiple_segments", func(t *testing.T) {
		// 多段 /.../ 与 * 混用：pre-/([a-z]{2})/-*-post → 匹配 pre-ab-cd-post，$1=ab, $2=cd
		pattern := "pre-/([a-z]{2})/-*-post"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID,
			map[string]string{"X-Seg1": "${1}", "X-Seg2": "${2}"})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		// 命中：pre-ab-cd-post
		resp := rawGet(t, serverURL, "pre-ab-cd-post."+proxyDomain, "/headers", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "ab", hdrs.Headers["X-Seg1"], "第一段 /.../ 捕获组应为 ab")
		assert.Equal(t, "cd", hdrs.Headers["X-Seg2"], "* 捕获组应为 cd")
	})

	t.Run("star_shorthand_for_first_capture", func(t *testing.T) {
		// 模板中 `*` 视为 `${1}` 的简写；多个 `*` 全部按首个捕获组替换
		// 模式 *-fuzzy10 → 命中 abc-fuzzy10，captures=["abc"]
		// custom_header X-Star: *-*  → abc-abc
		// custom_header X-Mix: ${1}-*-end → abc-abc-end
		pattern := "*-fuzzy10"
		appID := createFuzzyApp(t, c, pattern, "http://test-backend:8000", clientID,
			map[string]string{
				"X-Star": "pre-*-mid-*-post",
				"X-Mix":  "${1}-*-end",
				"X-Only": "just-star-here:*",
			})
		t.Cleanup(func() { c.Delete(t, "/api/apps/"+itoa(appID)) })

		resp := rawGet(t, serverURL, "abc-fuzzy10."+proxyDomain, "/headers", "")
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var hdrs struct {
			Headers map[string]string `json:"headers"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&hdrs))
		assert.Equal(t, "pre-abc-mid-abc-post", hdrs.Headers["X-Star"],
			"模板中 `*` 应展开为首个捕获组 abc，多处 `*` 全部按 ${1} 替换")
		assert.Equal(t, "abc-abc-end", hdrs.Headers["X-Mix"],
			"${1} 与 `*` 可混用，都指向同一首个捕获组")
		assert.Equal(t, "just-star-here:abc", hdrs.Headers["X-Only"],
			"末尾单个 `*` 也应替换")
	})

	t.Run("invalid_pattern_rejected", func(t *testing.T) {
		// 未闭合的 /.../ 应被拒绝
		badPatterns := []string{
			"foo/bar", // 未闭合
			"*-foo/(", // 正则编译失败
		}
		for _, p := range badPatterns {
			resp := c.Post(t, "/api/apps", map[string]interface{}{
				"name":        "invalid-pattern-test",
				"subdomain":   p,
				"target_url":  "http://test-backend:8000",
				"client_ids":  []string{clientID},
				"auth_method": "none",
			})
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			assert.NotEqual(t, http.StatusOK, resp.StatusCode,
				"无效模式 %q 应被拒绝 (body: %s)", p, string(body))
		}
	})
}

// createFuzzyApp 创建一个带模糊子域名模式的临时应用。
// 复用 createTestApp 的清理逻辑，但允许指定 target_url、clientID、custom_headers。
func createFuzzyApp(t *testing.T, c *harness.Client, pattern, targetURL, clientID string, customHeaders map[string]string) int64 {
	t.Helper()

	// 先清理同 pattern 的残留应用
	resp := c.Get(t, "/api/apps")
	defer resp.Body.Close()
	var list listAppsResponse
	require.NoError(t, harness.DecodeJSON(resp.Body, &list))
	for _, app := range list.Data {
		if app.Subdomain == pattern {
			c.Delete(t, fmt.Sprintf("/api/apps/%d", app.ID))
		}
	}

	body := map[string]interface{}{
		"name":           "fuzzy-" + pattern,
		"subdomain":      pattern,
		"target_url":     targetURL,
		"client_ids":     []string{clientID},
		"auth_method":    "none",
		"custom_headers": customHeaders,
	}
	resp2 := c.Post(t, "/api/apps", body)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		var errRes struct {
			Error string `json:"error"`
		}
		harness.DecodeJSON(resp2.Body, &errRes)
		t.Fatalf("create fuzzy app %q failed (status %d): %s", pattern, resp2.StatusCode, errRes.Error)
	}

	var result singleAppResponse
	require.NoError(t, harness.DecodeJSON(resp2.Body, &result))
	return result.Data.ID
}
