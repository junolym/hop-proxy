package suites

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 会话管理（#80）
// =============================================================================

// sessionResponse 镜像 /api/admin/sessions 列表项
type sessionResponse struct {
	ID              int64  `json:"id"`
	ShortID         string `json:"short_id"`
	UserID          int64  `json:"user_id"`
	Username        string `json:"username"`
	Source          string `json:"source"`
	SourceRef       string `json:"source_ref"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
	LastActiveAt    string `json:"last_active_at"`
	ExpiresAt       string `json:"expires_at"`
	GrantCount      int    `json:"grant_count"`
	HasActiveGrants bool   `json:"has_active_grants"`
}

// sessionGrantResponse 镜像会话详情中的应用授权
type sessionGrantResponse struct {
	GrantRef      int64  `json:"grant_ref"`
	AppID         int64  `json:"app_id"`
	AppName       string `json:"app_name"`
	Subdomain     string `json:"subdomain"`
	CreatedAt     string `json:"created_at"`
	ExpiresAt     string `json:"expires_at"`
	LastRequestAt string `json:"last_request_at"`
	RevokedAt     string `json:"revoked_at"`
}

// listSessionsForTest 查询会话列表并断言 200
func listSessionsForTest(t *testing.T, c *harness.Client, query string) []sessionResponse {
	t.Helper()
	resp := c.Get(t, "/api/admin/sessions"+query)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "list sessions should succeed")

	var result struct {
		Data []sessionResponse `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	return result.Data
}

// newestSession 返回列表中 ID 最大的会话（自增 ID 保证最新创建的会话最大）
func newestSession(t *testing.T, sessions []sessionResponse) sessionResponse {
	t.Helper()
	require.NotEmpty(t, sessions, "sessions should not be empty")
	newest := sessions[0]
	for _, s := range sessions {
		if s.ID > newest.ID {
			newest = s
		}
	}
	return newest
}

// onlyGrant 获取会话详情中唯一的应用授权（无则失败）
func onlyGrant(t *testing.T, c *harness.Client, sessionID int64) sessionGrantResponse {
	t.Helper()
	resp := c.Get(t, fmt.Sprintf("/api/admin/sessions/%d", sessionID))
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var detail struct {
		Data struct {
			Grants []sessionGrantResponse `json:"grants"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &detail))
	require.Len(t, detail.Data.Grants, 1)
	return detail.Data.Grants[0]
}

// TestE2E_SessionManagement_LocalProxyUsageCounts 本地代理（Path 3）访问计入授权「最近使用」：
// 客户端远程校验（verify_session）通过时写入（此前仅经服务端的访问才写入）。
func TestE2E_SessionManagement_LocalProxyUsageCounts(t *testing.T) {
	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{clientID},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	ssoCookie := authorizeSSO(t, c, appID, subdomain)
	session := newestSession(t, listSessionsForTest(t, c, "?source=password&status=valid"))

	// 初始：该授权尚无使用记录
	assert.Empty(t, onlyGrant(t, c, session.ID).LastRequestAt, "grant should not be marked used before any access")

	// 经本地代理（Path 3 直连）访问：鉴权走客户端远程校验（verify_session）
	lpc := localProxyClient1()
	waitForAppSync(t, lpc, subdomain, 10*time.Second)
	resp := lpc.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookie,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "local proxy access should succeed")

	// 授权「最近使用」应已写入
	assert.NotEmpty(t, onlyGrant(t, c, session.ID).LastRequestAt, "local proxy usage should be recorded")
}

// TestE2E_SessionManagement_RevokeKicks 强制下线即时生效：
// 踢掉另一登录会话后其旧 token 立即被拒，其他会话不受影响；列表状态变为 revoked。
func TestE2E_SessionManagement_RevokeKicks(t *testing.T) {
	admin := harness.Login(t, serverURL, adminDomain)
	victim := harness.Login(t, serverURL, adminDomain)

	// 会话用户下拉应包含主用户（用户管理接口 /api/admin/users 不含管理员）
	usersResp := admin.Get(t, "/api/admin/sessions/users")
	require.Equal(t, http.StatusOK, usersResp.StatusCode)
	var userList struct {
		Data []struct {
			UserID   int64  `json:"user_id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(usersResp.Body, &userList))
	usersResp.Body.Close()
	foundMainUser := false
	for _, u := range userList.Data {
		if u.Username == "admin" {
			foundMainUser = true
		}
	}
	assert.True(t, foundMainUser, "session user filter should include the main user (admin)")

	// 新建会话均为 7 天内有活跃 → 不应落入「不活跃」
	assert.Empty(t, listSessionsForTest(t, admin, "?status=idle"), "fresh sessions should not be idle")

	// 列表应包含 victim 刚创建的密码登录会话（ID 最新）
	sessions := listSessionsForTest(t, admin, "?source=password&status=valid")
	victimSession := newestSession(t, sessions)
	assert.Len(t, victimSession.ShortID, 8, "short_id should be a prefix of the long session id")
	assert.Equal(t, "password", victimSession.Source)
	assert.Equal(t, "admin", victimSession.Username)
	assert.Equal(t, "active", victimSession.Status)

	// 会话详情可查询
	resp := admin.Get(t, fmt.Sprintf("/api/admin/sessions/%d", victimSession.ID))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var detail struct {
		Data struct {
			Session sessionResponse        `json:"session"`
			Grants  []sessionGrantResponse `json:"grants"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &detail))
	resp.Body.Close()
	assert.Equal(t, victimSession.ID, detail.Data.Session.ID)

	// 非管理员不可访问会话管理
	userClient := createTestUser(t, admin, serverURL, adminDomain)
	resp = userClient.Get(t, "/api/admin/sessions")
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "non-admin should be forbidden")
	resp.Body.Close()

	// 强制下线
	resp = admin.Post(t, fmt.Sprintf("/api/admin/sessions/%d/revoke", victimSession.ID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "revoke should succeed")
	resp.Body.Close()

	// victim 的旧 token 立即失效
	resp = victim.Get(t, "/api/auth/me")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "revoked session should be rejected immediately")
	resp.Body.Close()

	// 管理员自己的会话不受影响
	resp = admin.Get(t, "/api/auth/me")
	assert.Equal(t, http.StatusOK, resp.StatusCode, "admin session should stay valid")
	resp.Body.Close()

	// 列表中该会话状态为 revoked
	revoked := listSessionsForTest(t, admin, "?status=revoked")
	found := false
	for _, s := range revoked {
		if s.ID == victimSession.ID {
			found = true
			assert.Equal(t, "revoked", s.Status)
		}
	}
	assert.True(t, found, "revoked session should appear in revoked list")

	// 有效会话不允许删除记录（需先强制下线）
	activeSession := newestSession(t, listSessionsForTest(t, admin, "?status=valid"))
	resp = admin.Delete(t, fmt.Sprintf("/api/admin/sessions/%d", activeSession.ID))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "active session should not be deletable")
	resp.Body.Close()

	// 删除已下线会话记录（其应用授权记录一并删除）
	resp = admin.Delete(t, fmt.Sprintf("/api/admin/sessions/%d", victimSession.ID))
	require.Equal(t, http.StatusOK, resp.StatusCode, "delete ended session should succeed")
	resp.Body.Close()

	// 删除后不再出现在列表，详情返回 404
	revoked = listSessionsForTest(t, admin, "?status=revoked")
	for _, s := range revoked {
		assert.NotEqual(t, victimSession.ID, s.ID, "deleted session should no longer be listed")
	}
	resp = admin.Get(t, fmt.Sprintf("/api/admin/sessions/%d", victimSession.ID))
	assert.Equal(t, http.StatusNotFound, resp.StatusCode, "deleted session detail should be 404")
	resp.Body.Close()
}

// TestE2E_SessionManagement_GrantAttribution 面板授权归属当前登录会话：
// 会话详情可见授权明细与最近使用时间。
func TestE2E_SessionManagement_GrantAttribution(t *testing.T) {
	c := harness.Login(t, serverURL, adminDomain)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{"__host__"},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	// 面板授权（归属当前登录会话）
	ssoCookie := authorizeSSO(t, c, appID, subdomain)

	// 使用该授权访问应用（Path 1 本机直连）→ 最近使用时间立即写入（60s 节流首写）
	// 先隔 1 秒，确保"授权最近使用"与"会话创建"落在不同秒（用于校验最后活跃取值）
	time.Sleep(1100 * time.Millisecond)
	proxy := harness.NewProxyClient(serverURL, proxyDomain)
	proxyResp := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookie,
	})
	defer proxyResp.Body.Close()
	require.Equal(t, http.StatusOK, proxyResp.StatusCode)

	session := newestSession(t, listSessionsForTest(t, c, "?source=password&status=valid"))
	assert.Equal(t, 1, session.GrantCount, "grant should be attributed to the login session")
	// 外层「最后活跃」应取到授权的最近使用（晚于会话创建时间）
	assert.Greater(t, session.LastActiveAt, session.CreatedAt, "last_active should reflect grant usage")

	// 详情包含授权明细
	resp := c.Get(t, fmt.Sprintf("/api/admin/sessions/%d", session.ID))
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var detail struct {
		Data struct {
			Grants []sessionGrantResponse `json:"grants"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &detail))
	require.Len(t, detail.Data.Grants, 1)
	assert.Equal(t, appID, detail.Data.Grants[0].AppID)
	assert.Equal(t, subdomain, detail.Data.Grants[0].Subdomain)
	assert.Equal(t, t.Name(), detail.Data.Grants[0].AppName)
	assert.NotEmpty(t, detail.Data.Grants[0].LastRequestAt, "last request time should be recorded on first use")

	// 删除单条授权（删除即撤销：应用 cookie 立即失效）
	grantRef := detail.Data.Grants[0].GrantRef
	require.NotZero(t, grantRef, "grant_ref should be returned for deletion")
	delResp := c.Delete(t, fmt.Sprintf("/api/admin/sessions/%d/grants/%d", session.ID, grantRef))
	require.Equal(t, http.StatusOK, delResp.StatusCode, "delete grant should succeed")
	delResp.Body.Close()

	// 旧 cookie 不再放行
	proxyResp2 := proxy.DoGet(t, subdomain, "/", map[string]string{
		fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain): ssoCookie,
	})
	defer proxyResp2.Body.Close()
	assert.NotEqual(t, http.StatusOK, proxyResp2.StatusCode, "deleted grant should be rejected")

	// 会话聚合同步归零
	afterResp := c.Get(t, fmt.Sprintf("/api/admin/sessions/%d", session.ID))
	defer afterResp.Body.Close()
	require.Equal(t, http.StatusOK, afterResp.StatusCode)
	var after struct {
		Data struct {
			Session sessionResponse        `json:"session"`
			Grants  []sessionGrantResponse `json:"grants"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(afterResp.Body, &after))
	assert.Empty(t, after.Data.Grants)
	assert.Equal(t, 0, after.Data.Session.GrantCount)
}

// TestE2E_SessionManagement_ShareCodeSourceRef 分享码会话的授权来源：
// source_ref 记录分享码本身（可在分享码页对上、可搜索）。
func TestE2E_SessionManagement_ShareCodeSourceRef(t *testing.T) {
	c := harness.Login(t, serverURL, adminDomain)
	_, _, code := createShareCodeForTest(t, c, 1)

	// 兑换分享码（不跟随重定向）
	rc := newNoRedirectClient()
	resp := redeemShareCode(t, rc, code)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode, "redeem should redirect to app")

	// 分享码会话的授权来源 = 分享码本身
	sessions := listSessionsForTest(t, c, "?source=share_code")
	require.NotEmpty(t, sessions, "share code session should be recorded")
	newest := newestSession(t, sessions)
	assert.Equal(t, code, newest.SourceRef, "source_ref should be the share code itself")
}

// TestE2E_SessionManagement_ExpiredSessionWithActiveGrant 会话过期但授权仍生效：
// 标记 has_active_grants、不允许删除（保护应用访问）；删除授权后可正常删除会话。
func TestE2E_SessionManagement_ExpiredSessionWithActiveGrant(t *testing.T) {
	c := harness.Login(t, serverURL, adminDomain)

	// 读取并调短会话 TTL（仅影响之后的新登录；测试结束恢复）
	origTTL := ""
	if res := c.Get(t, "/api/settings"); res.StatusCode == http.StatusOK {
		var settings struct {
			Data struct {
				SessionTTL string `json:"session_ttl"`
			} `json:"data"`
		}
		if err := harness.DecodeJSON(res.Body, &settings); err == nil {
			origTTL = settings.Data.SessionTTL
		}
		res.Body.Close()
	}
	res := c.Put(t, "/api/admin/settings", map[string]interface{}{"session_ttl": "5s"})
	require.Equal(t, http.StatusOK, res.StatusCode, "shorten session ttl should succeed")
	res.Body.Close()
	t.Cleanup(func() {
		r := c.Put(t, "/api/admin/settings", map[string]interface{}{"session_ttl": origTTL})
		r.Body.Close()
	})

	// 以短 TTL 登录（其会话很快过期）
	short := harness.Login(t, serverURL, adminDomain)

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{"__host__"},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	// 短会话授权一个应用（授权 TTL 独立于会话过期）
	authorizeSSO(t, short, appID, subdomain)

	// 等待短会话过期
	time.Sleep(6 * time.Second)

	// 找到该过期会话：最新且仍有生效授权
	expired := newestSession(t, listSessionsForTest(t, c, "?source=password&status=expired_with_grants"))
	assert.True(t, expired.HasActiveGrants, "expired session with live grant should be flagged")

	// 不允许删除（删除会话不会撤销应用访问）
	delResp := c.Delete(t, fmt.Sprintf("/api/admin/sessions/%d", expired.ID))
	assert.Equal(t, http.StatusBadRequest, delResp.StatusCode, "session with active grants should not be deletable")
	delResp.Body.Close()

	// 删除该授权（删除即撤销），随后会话可删
	detailResp := c.Get(t, fmt.Sprintf("/api/admin/sessions/%d", expired.ID))
	require.Equal(t, http.StatusOK, detailResp.StatusCode)
	var detail struct {
		Data struct {
			Grants []sessionGrantResponse `json:"grants"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(detailResp.Body, &detail))
	detailResp.Body.Close()
	require.Len(t, detail.Data.Grants, 1)

	delResp = c.Delete(t, fmt.Sprintf("/api/admin/sessions/%d/grants/%d", expired.ID, detail.Data.Grants[0].GrantRef))
	require.Equal(t, http.StatusOK, delResp.StatusCode, "delete grant should succeed")
	delResp.Body.Close()

	delResp = c.Delete(t, fmt.Sprintf("/api/admin/sessions/%d", expired.ID))
	require.Equal(t, http.StatusOK, delResp.StatusCode, "session should be deletable after grants removed")
	delResp.Body.Close()
}

// TestE2E_SessionManagement_QuickLoginAttribution 快速登录归属当前登录会话：
// 快速登录不新建独立会话，应用授权挂在该登录会话下。
func TestE2E_SessionManagement_QuickLoginAttribution(t *testing.T) {
	c := harness.Login(t, serverURL, adminDomain)

	// 开启快速登录（测试结束恢复关闭，避免影响其他用例）
	resp := c.Put(t, "/api/user-settings", map[string]interface{}{"quick_login": true})
	require.Equal(t, http.StatusOK, resp.StatusCode, "enable quick login should succeed")
	resp.Body.Close()
	t.Cleanup(func() {
		r := c.Put(t, "/api/user-settings", map[string]interface{}{"quick_login": false})
		r.Body.Close()
	})

	appID, subdomain := harness.SetupAuthApp(t, c, serverURL, adminDomain, harness.AuthAppOptions{
		TargetURL:    "http://test-backend:8000",
		ClientIDs:    []string{"__host__"},
		AuthMethod:   "sso",
		AllowedUsers: "all",
	})

	// 访问 /sso 页面（模拟应用跳转）：快速登录命中 → 302 回应用并下发应用 cookie。
	// 禁止自动跟随重定向：应用域名在测试环境不可直达，只需校验首跳
	c.Client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	redirectURL := fmt.Sprintf("http://%s.%s:18080/", subdomain, proxyDomain)
	resp = c.Get(t, "/sso?redirect="+url.QueryEscape(redirectURL))
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode, "quick login should redirect back to app")

	cookieName := fmt.Sprintf("hopproxy_sso_%d_%s", appID, subdomain)
	issued := false
	for _, ck := range resp.Cookies() {
		if ck.Name == cookieName {
			issued = true
		}
	}
	require.True(t, issued, "quick login should issue SSO cookie")

	// 授权归属当前登录会话；不产生 quick_login 独立会话
	session := newestSession(t, listSessionsForTest(t, c, "?source=password&status=valid"))
	assert.Equal(t, 1, session.GrantCount, "quick login grant should be attributed to the login session")
	assert.Empty(t, listSessionsForTest(t, c, "?source=quick_login"), "quick login should not create standalone sessions")
}
