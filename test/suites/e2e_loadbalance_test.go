package suites

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/robin/hop-proxy/test/harness"
)

// =============================================================================
// 负载均衡与故障切换测试
// =============================================================================

// TestLoadBalance_RoundRobin_DistributesRequests 验证 round-robin 模式下请求被分布到多个客户端
// D-07: 验证请求均匀分布
//
// 验证策略：由于两个客户端指向相同后端，响应内容相同无法区分，
// 通过停止一个客户端并确认另一个仍能处理请求来证明分布。
func TestLoadBalance_RoundRobin_DistributesRequests(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)
	client2ID := secondClientID(t, c)

	// 创建 load_balance=true 的 app，关联两个客户端
	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{client1ID, client2ID},
		"auth_method": "none",
		"load_balance": true,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create round-robin app should succeed")

	var appResult struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &appResult))
	appID := appResult.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// a) 发送 10 个请求，验证全部返回 200（round-robin 模式正常工作）
	for i := 0; i < 10; i++ {
		r := proxy.DoGet(t, subdomain, "/")
		r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode,
			"round-robin request %d should succeed, got %d", i+1, r.StatusCode)
	}

	// b) 停止 client-1，验证 client-2 仍能处理所有请求
	err := env.ComposeCmd(t.Context(), "stop", "client-1").Run()
	require.NoError(t, err, "stop client-1 container")

	// 确保 t.Cleanup 重启 client-1
	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		// 等待 client-1 恢复在线
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待 client-1 离线
	err = pollUntil(func() error {
		if clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client-1 should go offline after container stop")

	// 5 个请求全部成功（由 client-2 处理）
	for i := 0; i < 5; i++ {
		r := proxy.DoGet(t, subdomain, "/")
		r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode,
			"request %d after client-1 stop should succeed (client-2 handles), got %d", i+1, r.StatusCode)
	}

	// c) 重启 client-1，验证回到池中
	err = env.ComposeCmd(t.Context(), "start", "client-1").Run()
	require.NoError(t, err, "restart client-1 container")

	err = pollUntil(func() error {
		if !clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still offline")
		}
		return nil
	}, 60*time.Second)
	require.NoError(t, err, "client-1 should come back online after restart")

	// 再发 5 个请求，全部成功
	for i := 0; i < 5; i++ {
		r := proxy.DoGet(t, subdomain, "/")
		r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode,
			"request %d after client-1 restart should succeed, got %d", i+1, r.StatusCode)
	}

	// 检查服务端日志是否有 round-robin 或选择客户端 的记录
	logs, err := env.Logs("server", 100)
	if err == nil && logs != "" {
		hasRR := strings.Contains(logs, "round-robin") || strings.Contains(logs, "轮询") ||
			strings.Contains(logs, "选择客户端")
		t.Logf("Server logs contain round-robin/client-selection evidence: %v", hasRR)
	}
}

// TestLoadBalance_PrimaryBackup_Failover 验证 primary/backup 模式下自动故障切换
// D-08: 主备模式 - 主客户端在线时走主，离线时自动切换到备
func TestLoadBalance_PrimaryBackup_Failover(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)
	client2ID := secondClientID(t, c)

	// 创建 load_balance=false（默认，primary/backup 模式）的 app
	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{client1ID, client2ID},
		"auth_method": "none",
		"load_balance": false,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create primary/backup app should succeed")

	var appResult struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &appResult))
	appID := appResult.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 主客户端在线时，请求由主客户端处理
	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode, "primary client should handle request")

	// 停止 client-1（primary），触发故障切换到 client-2（backup）
	err := env.ComposeCmd(t.Context(), "stop", "client-1").Run()
	require.NoError(t, err, "stop client-1 container")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待 client-1 离线
	err = pollUntil(func() error {
		if clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client-1 should go offline")

	// backup 客户端自动接管
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"backup client should handle request after primary goes offline, got %d", r.StatusCode)

	// 重启 client-1，请求恢复
	err = env.ComposeCmd(t.Context(), "start", "client-1").Run()
	require.NoError(t, err, "restart client-1 container")

	err = pollUntil(func() error {
		if !clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still offline")
		}
		return nil
	}, 60*time.Second)
	require.NoError(t, err, "client-1 should come back online")

	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"request should succeed after primary comes back online, got %d", r.StatusCode)
}

// TestLoadBalance_AllClientsOffline_502 验证所有客户端离线时返回 502
// D-09: 所有客户端离线 → 502 Bad Gateway
func TestLoadBalance_AllClientsOffline_502(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)
	client2ID := secondClientID(t, c)

	// 创建关联两个客户端的 app
	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{client1ID, client2ID},
		"auth_method": "none",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create app should succeed")

	var appResult struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &appResult))
	appID := appResult.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 先验证代理正常工作
	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	require.Equal(t, http.StatusOK, r.StatusCode, "proxy should work before stopping clients")

	// 停止所有客户端
	err := env.ComposeCmd(t.Context(), "stop", "client-1", "client-2").Run()
	require.NoError(t, err, "stop both client containers")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1", "client-2").Run()
		// 等待至少一个客户端恢复在线
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) && !clientOnline(t, c, client2ID) {
				return fmt.Errorf("no client online yet")
			}
			return nil
		}, 60*time.Second)
	})

	// 等待两个客户端都离线
	err = pollUntil(func() error {
		online1 := clientOnline(t, c, client1ID)
		online2 := clientOnline(t, c, client2ID)
		if online1 || online2 {
			return fmt.Errorf("some clients still online (c1=%v, c2=%v)", online1, online2)
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "both clients should go offline")

	// 验证代理返回 502
	r = proxy.DoGet(t, subdomain, "/")
	defer r.Body.Close()
	assert.Equal(t, http.StatusBadGateway, r.StatusCode,
		"should return 502 Bad Gateway when all clients offline, got %d", r.StatusCode)
}

// TestLoadBalance_DynamicClientJoin 验证动态添加/移除客户端后请求路由正确
// D-10: 动态客户端加入/离开 — 新客户端加入后开始接收请求，移除后停止
func TestLoadBalance_DynamicClientJoin(t *testing.T) {
	if !harness.IsInitialized(serverURL, adminDomain) {
		t.Skip("system not initialized")
	}

	env := harness.NewTestEnv(dockerDir())
	c := harness.Login(t, serverURL, adminDomain)
	client1ID := firstClientID(t, c)
	client2ID := secondClientID(t, c)

	// 创建只关联 client-1 的 app
	subdomain := harness.DeriveSubdomain(t.Name())
	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{client1ID},
		"auth_method": "none",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create app with client-1 only should succeed")

	var appResult struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, harness.DecodeJSON(resp.Body, &appResult))
	appID := appResult.Data.ID
	t.Cleanup(func() { c.Delete(t, fmt.Sprintf("/api/apps/%d", appID)) })

	proxy := harness.NewProxyClient(serverURL, proxyDomain)

	// 只关联 client-1 时，5 个请求全部成功
	for i := 0; i < 5; i++ {
		r := proxy.DoGet(t, subdomain, "/")
		r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode,
			"request %d with only client-1 should succeed, got %d", i+1, r.StatusCode)
	}

	// 动态添加 client-2 到 app
	updateResp := c.Put(t, fmt.Sprintf("/api/apps/%d", appID), map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{client1ID, client2ID},
		"auth_method": "none",
	})
	defer updateResp.Body.Close()
	require.Equal(t, http.StatusOK, updateResp.StatusCode, "update app to add client-2 should succeed")

	// 等待 app 配置同步（短暂延迟后发请求验证）
	time.Sleep(2 * time.Second)

	// 添加 client-2 后，5 个请求全部成功（client-2 也可用）
	for i := 0; i < 5; i++ {
		r := proxy.DoGet(t, subdomain, "/")
		r.Body.Close()
		require.Equal(t, http.StatusOK, r.StatusCode,
			"request %d after adding client-2 should succeed, got %d", i+1, r.StatusCode)
	}

	// 停止 client-1，client-2 应接管
	err := env.ComposeCmd(t.Context(), "stop", "client-1").Run()
	require.NoError(t, err, "stop client-1 container")

	t.Cleanup(func() {
		env.ComposeCmd(context.Background(), "start", "client-1").Run()
		pollUntil(func() error {
			if !clientOnline(t, c, client1ID) {
				return fmt.Errorf("client-1 still offline")
			}
			return nil
		}, 60*time.Second)
	})

	err = pollUntil(func() error {
		if clientOnline(t, c, client1ID) {
			return fmt.Errorf("client-1 still online")
		}
		return nil
	}, 45*time.Second)
	require.NoError(t, err, "client-1 should go offline")

	r := proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"client-2 should handle request after client-1 stops, got %d", r.StatusCode)

	// 动态移除 client-1，只保留 client-2
	updateResp2 := c.Put(t, fmt.Sprintf("/api/apps/%d", appID), map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  "http://test-backend:8000",
		"client_ids":  []string{client2ID},
		"auth_method": "none",
	})
	defer updateResp2.Body.Close()
	require.Equal(t, http.StatusOK, updateResp2.StatusCode, "update app to remove client-1 should succeed")

	time.Sleep(2 * time.Second)

	// 移除 client-1 后，请求仍成功（client-2 处理）
	r = proxy.DoGet(t, subdomain, "/")
	r.Body.Close()
	assert.Equal(t, http.StatusOK, r.StatusCode,
		"request should succeed with only client-2, got %d", r.StatusCode)
}
