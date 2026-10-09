package suites

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// externalAppItem 镜像 /external-api/apps 返回的应用条目
type externalAppItem struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Subdomain  string `json:"subdomain"`
	URL        string `json:"url"`
	LastUsedAt string `json:"last_used_at"`
}

// enableAppAPI 调用 PUT /api/user-settings 打开应用 API，返回 token
func enableAppAPI(t *testing.T, c *harness.Client) string {
	t.Helper()
	resp := c.Put(t, "/api/user-settings", map[string]interface{}{"app_api_enabled": true})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "enable app api should succeed")
	var result struct {
		Data struct {
			AppAPIToken string `json:"app_api_token"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	// 重复启用可能返回空 token（已存在时）；再读一次拿到完整 token
	if result.Data.AppAPIToken != "" {
		return result.Data.AppAPIToken
	}
	return getAppAPIToken(t, c)
}

// getAppAPIToken 从 GET /api/user-settings 读取当前完整 token
func getAppAPIToken(t *testing.T, c *harness.Client) string {
	t.Helper()
	resp := c.Get(t, "/api/user-settings")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var result struct {
		Data struct {
			AppAPIToken string `json:"app_api_token"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &result))
	require.NotEmpty(t, result.Data.AppAPIToken, "app api token should be set")
	return result.Data.AppAPIToken
}

// externalAPIGet 用 Bearer token 调 /external-api/apps
func externalAPIGet(t *testing.T, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, serverURL+"/external-api/apps", nil)
	require.NoError(t, err)
	req.Host = adminDomain
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// TestExternalAPI_FullFlow 覆盖启用 → 鉴权 → 列表 → 重置 → 关闭全流程
func TestExternalAPI_FullFlow(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// 准备一个已启用的应用
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-external-api-app",
		"subdomain":  "test-external-api-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)
	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	appID := created.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// 清理：测试结束时关闭应用 API，避免污染其他用例
	t.Cleanup(func() {
		c.Put(t, "/api/user-settings", map[string]interface{}{"app_api_enabled": false})
	})

	// 1. 未启用时调用 /external-api/apps 应返回 401
	resp := externalAPIGet(t, "any-token")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "should reject before enable")

	// 2. 启用应用 API，拿到 token
	token := enableAppAPI(t, c)
	require.NotEmpty(t, token)

	// 3. 无 token / 错误 token / 正确 token
	noTokenResp := externalAPIGet(t, "")
	defer noTokenResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, noTokenResp.StatusCode)

	wrongResp := externalAPIGet(t, "deadbeef")
	defer wrongResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, wrongResp.StatusCode)

	okResp := externalAPIGet(t, token)
	defer okResp.Body.Close()
	require.Equal(t, http.StatusOK, okResp.StatusCode)
	var list struct {
		Data []externalAppItem `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(okResp.Body, &list))

	// 4. 列表中应包含刚创建的应用，URL 形如 https://test-external-api-app.<proxyDomain>
	var found *externalAppItem
	for i := range list.Data {
		if list.Data[i].ID == appID {
			found = &list.Data[i]
			break
		}
	}
	require.NotNil(t, found, "created app should appear in external-api list")
	assert.Equal(t, "test-external-api-app", found.Name)
	assert.Equal(t, "test-external-api-app", found.Subdomain)
	assert.Equal(t, "https://test-external-api-app."+proxyDomain, found.URL)
	// 不应暴露后端 target_url 等敏感字段

	// 5. 重置 token：旧 token 立即失效
	resetResp := c.Put(t, "/api/user-settings", map[string]interface{}{"reset_app_api_token": true})
	defer resetResp.Body.Close()
	require.Equal(t, http.StatusOK, resetResp.StatusCode)
	var resetResult struct {
		Data struct {
			AppAPIToken string `json:"app_api_token"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resetResp.Body, &resetResult))
	newToken := resetResult.Data.AppAPIToken
	require.NotEmpty(t, newToken)
	require.NotEqual(t, token, newToken, "reset should produce a different token")

	oldResp := externalAPIGet(t, token)
	defer oldResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, oldResp.StatusCode, "old token should be invalid after reset")

	newResp := externalAPIGet(t, newToken)
	defer newResp.Body.Close()
	assert.Equal(t, http.StatusOK, newResp.StatusCode, "new token should work")

	// 6. 关闭应用 API：token 立即失效
	disableResp := c.Put(t, "/api/user-settings", map[string]interface{}{"app_api_enabled": false})
	defer disableResp.Body.Close()
	require.Equal(t, http.StatusOK, disableResp.StatusCode)

	afterDisableResp := externalAPIGet(t, newToken)
	defer afterDisableResp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, afterDisableResp.StatusCode, "token should be invalid after disable")
}
