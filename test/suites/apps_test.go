package suites

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// appsResponse mirrors the API response for a single app.
type appsResponse struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Subdomain   string   `json:"subdomain"`
	TargetURL   string   `json:"target_url"`
	Enabled     bool     `json:"enabled"`
	AuthMethod  string   `json:"auth_method"`
	LoadBalance bool     `json:"load_balance"`
	ClientIDs   []string `json:"client_ids"`
}

// listAppsResponse mirrors the API response for listing apps.
type listAppsResponse struct {
	Data    []appsResponse `json:"data"`
	Message string         `json:"message"`
}

// singleAppResponse mirrors the API response for create/operations returning a single app.
type singleAppResponse struct {
	Data    appsResponse `json:"data"`
	Message string       `json:"message"`
}

// msgResponse mirrors the API response for message-only replies (update, delete).
type msgResponse struct {
	Message string `json:"message"`
}

// errResponse mirrors the API error response.
type errResponse struct {
	Error string `json:"error"`
}

// firstClientID lists clients and returns the first client ID.
// Used to obtain a real client UUID for app association.
func firstClientID(t *testing.T, c *harness.Client) string {
	t.Helper()
	resp := c.Get(t, "/api/clients")
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)
	require.NotEmpty(t, result.Data, "at least one client must be registered")
	// Skip __host__ special client
	for _, client := range result.Data {
		if client.ID != "__host__" {
			return client.ID
		}
	}
	t.Fatal("no real client found (only __host__)")
	return ""
}

// =============================================================================
// Create App
// =============================================================================

func TestApps_Create(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-create-app",
		"subdomain":  "test-create-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result singleAppResponse
	err := harness.DecodeJSON(resp.Body, &result)
	require.NoError(t, err)

	assert.Greater(t, result.Data.ID, int64(0))
	assert.Equal(t, "test-create-app", result.Data.Name)
	assert.Equal(t, "test-create-app", result.Data.Subdomain)
	assert.Equal(t, "http://test-backend:8000", result.Data.TargetURL)
	assert.True(t, result.Data.Enabled)
	assert.Equal(t, "sso", result.Data.AuthMethod)
	assert.Equal(t, []string{clientID}, result.Data.ClientIDs)

	// Cleanup
	c.Delete(t, fmt.Sprintf("/api/apps/%d", result.Data.ID))
}

// =============================================================================
// List Apps
// =============================================================================

func TestApps_List(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// Create a temporary app so the list is non-trivial.
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-list-app",
		"subdomain":  "test-list-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	appID := created.Data.ID

	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// List apps.
	listResp := c.Get(t, "/api/apps")
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var result listAppsResponse
	err := harness.DecodeJSON(listResp.Body, &result)
	require.NoError(t, err)

	// The created app must appear in the list.
	found := false
	for _, app := range result.Data {
		if app.ID == appID {
			found = true
			assert.Equal(t, "test-list-app", app.Name)
			assert.Equal(t, "test-list-app", app.Subdomain)
			break
		}
	}
	assert.True(t, found, "created app %d should appear in list", appID)
}

// =============================================================================
// Update App
// =============================================================================

func TestApps_Update(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// Create an app to update.
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-update-app",
		"subdomain":  "test-update-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	appID := created.Data.ID

	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Update name and target_url.
	updateResp := c.Put(t, fmt.Sprintf("/api/apps/%d", appID), map[string]interface{}{
		"name":       "test-update-app-renamed",
		"subdomain":  "test-update-app",
		"target_url": "http://test-nginx",
		"client_ids": []string{clientID},
	})
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	var updateResult msgResponse
	err := harness.DecodeJSON(updateResp.Body, &updateResult)
	require.NoError(t, err)
	assert.Contains(t, updateResult.Message, "成功")

	// Verify the update by listing.
	listResp := c.Get(t, "/api/apps")
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var listResult listAppsResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	for _, app := range listResult.Data {
		if app.ID == appID {
			assert.Equal(t, "test-update-app-renamed", app.Name)
			assert.Equal(t, "http://test-nginx", app.TargetURL)
			return
		}
	}
	t.Fatal("updated app not found in list")
}

// =============================================================================
// Delete App
// =============================================================================

func TestApps_Delete(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// Create an app to delete.
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-delete-app",
		"subdomain":  "test-delete-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	appID := created.Data.ID

	// Delete it.
	delResp := c.Delete(t, fmt.Sprintf("/api/apps/%d", appID))
	defer delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	var delResult msgResponse
	err := harness.DecodeJSON(delResp.Body, &delResult)
	require.NoError(t, err)
	assert.Contains(t, delResult.Message, "成功")

	// Verify it no longer appears in the list.
	listResp := c.Get(t, "/api/apps")
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var listResult listAppsResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	for _, app := range listResult.Data {
		assert.NotEqual(t, appID, app.ID, "deleted app should not appear in list")
	}
}

// =============================================================================
// Subdomain Conflict
// =============================================================================

func TestApps_Create_SubdomainConflict(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	subdomain := "test-conflict-app"

	// Create first app with the subdomain.
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-conflict-app-1",
		"subdomain":  subdomain,
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	appID := created.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Try creating a second app with the same subdomain.
	conflictResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-conflict-app-2",
		"subdomain":  subdomain,
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer conflictResp.Body.Close()

	// Server returns 400 Bad Request for subdomain conflict (not 409).
	assert.Equal(t, http.StatusBadRequest, conflictResp.StatusCode,
		"subdomain conflict should be rejected")

	var errResult errResponse
	err := harness.DecodeJSON(conflictResp.Body, &errResult)
	require.NoError(t, err)
	assert.Contains(t, errResult.Error, "子域名")
}

// =============================================================================
// Dynamic Client Association
// =============================================================================

func TestApps_ClientAssociation(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	// Collect all available client IDs.
	clientsResp := c.Get(t, "/api/clients")
	defer clientsResp.Body.Close()
	require.Equal(t, http.StatusOK, clientsResp.StatusCode)

	var clientsResult struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := harness.DecodeJSON(clientsResp.Body, &clientsResult)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(clientsResult.Data), 2,
		"at least 2 clients needed for association test")

	clientID1 := clientsResult.Data[0].ID
	clientID2 := clientsResult.Data[1].ID

	// Create app associated with client-1 only.
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-assoc-app",
		"subdomain":  "test-assoc-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID1},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	appID := created.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	// Verify initial association: only clientID1.
	assert.Equal(t, []string{clientID1}, created.Data.ClientIDs,
		"app should initially be associated with client-1 only")

	// Update app to associate with client-2 instead.
	updateResp := c.Put(t, fmt.Sprintf("/api/apps/%d", appID), map[string]interface{}{
		"name":       "test-assoc-app",
		"subdomain":  "test-assoc-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID2},
	})
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode)

	// Verify updated association via list.
	listResp := c.Get(t, "/api/apps")
	defer listResp.Body.Close()
	require.Equal(t, http.StatusOK, listResp.StatusCode)

	var listResult listAppsResponse
	require.NoError(t, harness.DecodeJSON(listResp.Body, &listResult))

	for _, app := range listResult.Data {
		if app.ID == appID {
			assert.Equal(t, []string{clientID2}, app.ClientIDs,
				"app should now be associated with client-2 only")
			return
		}
	}
	t.Fatal("app not found in list after update")
}

// =============================================================================
// Validation: Missing Required Fields
// =============================================================================

func TestApps_Create_MissingFields(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	tests := []struct {
		name string
		body map[string]interface{}
	}{
		{
			"missing_name",
			map[string]interface{}{
				"subdomain":  "test-missing-name",
				"target_url": "http://test-backend:8000",
				"client_ids": []string{clientID},
			},
		},
		{
			"missing_subdomain",
			map[string]interface{}{
				"name":       "test-missing-subdomain",
				"target_url": "http://test-backend:8000",
				"client_ids": []string{clientID},
			},
		},
		{
			"missing_target_url",
			map[string]interface{}{
				"name":       "test-missing-target",
				"subdomain":  "test-missing-target",
				"client_ids": []string{clientID},
			},
		},
		{
			"missing_client_ids",
			map[string]interface{}{
				"name":       "test-missing-clients",
				"subdomain":  "test-missing-clients",
				"target_url": "http://test-backend:8000",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := c.Post(t, "/api/apps", tc.body)
			defer resp.Body.Close()
			assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
				"missing required fields should return 400")
		})
	}
}

// =============================================================================
// Duplicate App (Clone) — #51
// =============================================================================

func TestApps_Duplicate(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)
	clientID := firstClientID(t, c)

	// 清理历史残留（前缀匹配本测试全部子域名），保证后缀推导从 -1 开始
	var preList listAppsResponse
	preResp := c.Get(t, "/api/apps")
	defer preResp.Body.Close()
	require.NoError(t, harness.DecodeJSON(preResp.Body, &preList))
	for _, app := range preList.Data {
		if strings.HasPrefix(app.Subdomain, "test-dup-app") {
			c.Delete(t, fmt.Sprintf("/api/apps/%d", app.ID))
		}
	}

	// 创建源应用
	createResp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":       "test-dup-app",
		"subdomain":  "test-dup-app",
		"target_url": "http://test-backend:8000",
		"client_ids": []string{clientID},
	})
	defer createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	var created singleAppResponse
	require.NoError(t, harness.DecodeJSON(createResp.Body, &created))
	srcID := created.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", srcID)) })

	// 给源应用加一条路由规则和一条跳转规则，验证克隆时子表被完整复制
	routeResp := c.Post(t, fmt.Sprintf("/api/apps/%d/routes", srcID), map[string]interface{}{
		"method":       "GET",
		"path_pattern": "/api/",
		"auth_method":  "none",
		"priority":     0,
	})
	defer routeResp.Body.Close()
	require.Equal(t, http.StatusOK, routeResp.StatusCode, "创建路由规则失败")

	redirectResp := c.Post(t, fmt.Sprintf("/api/apps/%d/redirects", srcID), map[string]interface{}{
		"match_type":      "exact",
		"match_path":      "/old",
		"redirect_target": "/new",
		"status_code":     302,
		"priority":        0,
	})
	defer redirectResp.Body.Close()
	require.Equal(t, http.StatusOK, redirectResp.StatusCode, "创建跳转规则失败")

	// 第一次克隆：名称与子域名推导为 -1 后缀
	dupResp := c.Post(t, fmt.Sprintf("/api/apps/%d/duplicate", srcID), map[string]interface{}{})
	defer dupResp.Body.Close()
	require.Equal(t, http.StatusOK, dupResp.StatusCode, "克隆应用失败")

	var dup singleAppResponse
	require.NoError(t, harness.DecodeJSON(dupResp.Body, &dup))
	newID := dup.Data.ID
	assert.NotEqual(t, srcID, newID)
	assert.Equal(t, "test-dup-app-1", dup.Data.Name)
	assert.Equal(t, "test-dup-app-1", dup.Data.Subdomain)
	assert.Equal(t, "http://test-backend:8000", dup.Data.TargetURL)
	assert.Equal(t, []string{clientID}, dup.Data.ClientIDs)
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", newID)) })

	// 克隆的路由/跳转规则应存在
	routesResp := c.Get(t, fmt.Sprintf("/api/apps/%d/routes", newID))
	defer routesResp.Body.Close()
	require.Equal(t, http.StatusOK, routesResp.StatusCode)
	var routes struct {
		Data []struct {
			PathPattern string `json:"path_pattern"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(routesResp.Body, &routes))
	require.Len(t, routes.Data, 1, "路由规则应被克隆")
	assert.Equal(t, "/api/", routes.Data[0].PathPattern)

	redirectsResp := c.Get(t, fmt.Sprintf("/api/apps/%d/redirects", newID))
	defer redirectsResp.Body.Close()
	require.Equal(t, http.StatusOK, redirectsResp.StatusCode)
	var redirects struct {
		Data []struct {
			MatchPath string `json:"match_path"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(redirectsResp.Body, &redirects))
	require.Len(t, redirects.Data, 1, "跳转规则应被克隆")
	assert.Equal(t, "/old", redirects.Data[0].MatchPath)

	// 第二次克隆：后缀递增为 -2
	dupResp2 := c.Post(t, fmt.Sprintf("/api/apps/%d/duplicate", srcID), map[string]interface{}{})
	defer dupResp2.Body.Close()
	require.Equal(t, http.StatusOK, dupResp2.StatusCode)

	var dup2 singleAppResponse
	require.NoError(t, harness.DecodeJSON(dupResp2.Body, &dup2))
	assert.Equal(t, "test-dup-app-2", dup2.Data.Name)
	assert.Equal(t, "test-dup-app-2", dup2.Data.Subdomain)
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", dup2.Data.ID)) })
}

// =============================================================================
// Delete Non-Existent App
// =============================================================================

func TestApps_Delete_NotFound(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	c := harness.Login(t, serverURL, adminDomain)

	resp := c.Delete(t, "/api/apps/999999")
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
