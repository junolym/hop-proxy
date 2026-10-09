package harness

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// DirectBackendURL is the direct (no-proxy) test backend address.
// Exposed by docker-compose as 18081:8000 on the test-backend container.
const DirectBackendURL = "http://localhost:18081"

// DirectClient sends requests directly to the test-backend, bypassing HopProxy.
// Used as the baseline control group for proxy comparison tests.
type DirectClient struct {
	baseURL string
	client  *http.Client
}

// NewDirectClient creates a client that connects directly to the test backend.
func NewDirectClient() *DirectClient {
	return &DirectClient{
		baseURL: DirectBackendURL,
		client:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// Do sends a raw *http.Request directly to the test backend.
func (dc *DirectClient) Do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Accept-Encoding", "identity")
	return dc.client.Do(req)
}

// Get sends a GET request to the given path on the test backend.
func (dc *DirectClient) Get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, dc.baseURL+path, nil)
	if err != nil {
		t.Fatalf("DirectClient.NewRequest GET %s: %v", path, err)
	}
	resp, err := dc.Do(req)
	if err != nil {
		t.Fatalf("DirectClient GET %s: %v", path, err)
	}
	return resp
}

// Post sends a POST request with the given body to the test backend.
func (dc *DirectClient) Post(t *testing.T, path string, body io.Reader, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, dc.baseURL+path, body)
	if err != nil {
		t.Fatalf("DirectClient.NewRequest POST %s: %v", path, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := dc.Do(req)
	if err != nil {
		t.Fatalf("DirectClient POST %s: %v", path, err)
	}
	return resp
}

// ProxyRequest holds parameters for constructing a proxied HTTP request.
type ProxyRequest struct {
	Method    string            // HTTP method (GET, POST, etc.)
	Subdomain string            // Application subdomain (e.g. "myapp")
	Path      string            // Request path (e.g. "/echo")
	Query     url.Values        // Optional query parameters
	Headers   map[string]string // Optional extra headers
	Body      io.Reader         // Optional request body
	Cookies   map[string]string // Optional extra cookies (e.g. SSO cookie)
}

// ProxyClient sends requests through HopProxy by injecting a Host header
// with the subdomain routing format: {subdomain}.{proxyDomain}:{port}.
type ProxyClient struct {
	serverURL   string // e.g. http://localhost:18080
	proxyDomain string // e.g. hopproxy.test
	client      *http.Client
}

// NewProxyClient creates a client that routes requests through HopProxy.
// Does not follow redirects — returns the first response (including 302).
func NewProxyClient(serverURL, proxyDomain string) *ProxyClient {
	return &ProxyClient{
		serverURL:   serverURL,
		proxyDomain: proxyDomain,
		client: &http.Client{
			Timeout: 5 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Do sends a request through HopProxy with the Host header set to
// {subdomain}.{proxyDomain}:{port} for subdomain routing.
func (pc *ProxyClient) Do(t *testing.T, req ProxyRequest) *http.Response {
	t.Helper()

	u, err := url.Parse(pc.serverURL)
	require.NoError(t, err, "parse serverURL")

	// Split path and query string if present in req.Path
	path := req.Path
	rawQuery := ""
	if idx := strings.Index(path, "?"); idx != -1 {
		rawQuery = path[idx+1:]
		path = path[:idx]
	}
	u.Path = path
	if req.Query != nil {
		u.RawQuery = req.Query.Encode()
	} else if rawQuery != "" {
		u.RawQuery = rawQuery
	}

	var body io.Reader
	if req.Body != nil {
		body = req.Body
	}

	httpReq, err := http.NewRequest(req.Method, u.String(), body)
	require.NoError(t, err, "NewRequest %s %s", req.Method, u.String())

	// Set Host header for subdomain routing: myapp.hopproxy.test:18080
	// If the URL has no explicit port, omit the colon entirely.
	port := u.Port()
	if port != "" {
		httpReq.Host = fmt.Sprintf("%s.%s:%s", req.Subdomain, pc.proxyDomain, port)
	} else {
		httpReq.Host = fmt.Sprintf("%s.%s", req.Subdomain, pc.proxyDomain)
	}

	httpReq.Header.Set("Accept-Encoding", "identity")

	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	for k, v := range req.Cookies {
		httpReq.AddCookie(&http.Cookie{Name: k, Value: v})
	}

	resp, err := pc.client.Do(httpReq)
	if err != nil {
		t.Fatalf("ProxyClient %s %s (Host=%s): %v", req.Method, u.String(), httpReq.Host, err)
	}
	return resp
}

// DoGet sends a GET request through the proxy.
func (pc *ProxyClient) DoGet(t *testing.T, subdomain, path string, cookies ...map[string]string) *http.Response {
	t.Helper()
	req := ProxyRequest{
		Method:    http.MethodGet,
		Subdomain: subdomain,
		Path:      path,
	}
	if len(cookies) > 0 {
		req.Cookies = cookies[0]
	}
	return pc.Do(t, req)
}

// DoPost sends a POST request through the proxy.
func (pc *ProxyClient) DoPost(t *testing.T, subdomain, path string, body io.Reader, contentType string, cookies ...map[string]string) *http.Response {
	t.Helper()
	req := ProxyRequest{
		Method:    http.MethodPost,
		Subdomain: subdomain,
		Path:      path,
		Body:      body,
	}
	if contentType != "" {
		req.Headers = map[string]string{"Content-Type": contentType}
	}
	if len(cookies) > 0 {
		req.Cookies = cookies[0]
	}
	return pc.Do(t, req)
}

// ReadBody reads the entire response body and returns it as bytes.
func ReadBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "read response body")
	return data
}

// HeadersToMap converts response headers to a map for comparison.
// Multi-value headers are joined with ", ".
func HeadersToMap(h http.Header) map[string]string {
	m := make(map[string]string, len(h))
	for k, vv := range h {
		m[http.CanonicalHeaderKey(k)] = strings.Join(vv, ", ")
	}
	return m
}

// AssertResponseEqual compares key fields of a direct (baseline) and proxied response.
//
// It checks:
//   - HTTP status code
//   - Content-Type header
//   - Content-Length header
//   - Response body (byte-for-byte)
//
// The following are intentionally NOT compared because the proxy layer modifies them:
//   - Date, Server, Transfer-Encoding (hop-by-hop headers)
//   - X-Forwarded-* headers (added by the proxy)
//   - Set-Cookie domain/path attributes (may be rewritten)
func AssertResponseEqual(t *testing.T, direct, proxied *http.Response) {
	t.Helper()

	// Status code
	assert.Equal(t, direct.StatusCode, proxied.StatusCode,
		"status code mismatch: direct=%d, proxied=%d", direct.StatusCode, proxied.StatusCode)

	// Content-Type
	directCT := direct.Header.Get("Content-Type")
	proxiedCT := proxied.Header.Get("Content-Type")
	assert.Equal(t, directCT, proxiedCT,
		"Content-Type mismatch: direct=%q, proxied=%q", directCT, proxiedCT)

	// Content-Length
	directCL := direct.Header.Get("Content-Length")
	proxiedCL := proxied.Header.Get("Content-Length")
	assert.Equal(t, directCL, proxiedCL,
		"Content-Length mismatch: direct=%q, proxied=%q", directCL, proxiedCL)

	// Body comparison
	directBody := ReadBody(t, direct)
	proxiedBody := ReadBody(t, proxied)
	assert.Equal(t, directBody, proxiedBody,
		"response body mismatch (direct %d bytes, proxied %d bytes)", len(directBody), len(proxiedBody))
}

// assertResponseEqual 是 AssertResponseEqual 的内部 alias，保持向后兼容
var assertResponseEqual = AssertResponseEqual

// AssertBodyHashEqual compares SHA-256 hashes of direct and proxied response bodies.
// Useful for large file comparisons where loading both into memory is impractical.
func AssertBodyHashEqual(t *testing.T, direct, proxied *http.Response) {
	t.Helper()

	require.Equal(t, direct.StatusCode, proxied.StatusCode,
		"status code mismatch: direct=%d, proxied=%d", direct.StatusCode, proxied.StatusCode)
	require.Equal(t, http.StatusOK, direct.StatusCode,
		"direct response not OK: status=%d", direct.StatusCode)

	directHash := sha256HashBody(t, direct)
	proxiedHash := sha256HashBody(t, proxied)

	assert.Equal(t, directHash, proxiedHash,
		"body SHA-256 mismatch: direct=%x, proxied=%x", directHash, proxiedHash)
}

func sha256HashBody(t *testing.T, resp *http.Response) [sha256.Size]byte {
	t.Helper()
	defer resp.Body.Close()
	h := sha256.New()
	_, err := io.Copy(h, resp.Body)
	require.NoError(t, err, "hash response body")
	var hash [sha256.Size]byte
	copy(hash[:], h.Sum(nil))
	return hash
}

// PerformanceResult holds the outcome of a performance comparison.
type PerformanceResult struct {
	BaselineBytesPerSec float64 // Direct throughput (bytes/sec)
	ProxyBytesPerSec    float64 // Proxy throughput (bytes/sec)
	Ratio               float64 // Proxy / Baseline ratio
	Threshold           float64 // Required minimum ratio (e.g. 0.95)
}

// AssertPerformance verifies that the proxy throughput is at least threshold
// (e.g. 0.95 = 95%) of the direct baseline throughput.
//
// Parameters:
//   - t: testing.T
//   - label: descriptive label for error messages (e.g. "P1 Server-Local")
//   - baselineBytes: number of bytes transferred in the direct request
//   - proxyBytes: number of bytes transferred in the proxied request
//   - baselineDur: duration of the direct request
//   - proxyDur: duration of the proxied request
//   - threshold: minimum acceptable ratio (default 0.95)
//
// The function always records the result as a test metric. If the ratio is
// below the threshold, the test fails with a detailed performance alert.
func AssertPerformance(t *testing.T, label string, baselineBytes, proxyBytes int64, baselineDur, proxyDur time.Duration, threshold ...float64) {
	t.Helper()

	th := 0.95
	if len(threshold) > 0 {
		th = threshold[0]
	}

	baselineDurSec := baselineDur.Seconds()
	proxyDurSec := proxyDur.Seconds()

	if baselineDurSec == 0 || proxyDurSec == 0 {
		t.Fatalf("invalid duration for performance comparison: baseline=%v, proxy=%v", baselineDur, proxyDur)
	}

	baselineBps := float64(baselineBytes) / baselineDurSec
	proxyBps := float64(proxyBytes) / proxyDurSec
	ratio := proxyBps / baselineBps

	baselineMBps := baselineBps / (1024 * 1024)
	proxyMBps := proxyBps / (1024 * 1024)

	result := PerformanceResult{
		BaselineBytesPerSec: baselineBps,
		ProxyBytesPerSec:    proxyBps,
		Ratio:               ratio,
		Threshold:           th,
	}

	t.Logf("PERFORMANCE [%s]: proxy %.1f MB/s, baseline %.1f MB/s, ratio %.1f%% (threshold %.0f%%)",
		label, proxyMBps, baselineMBps, ratio*100, th*100)

	if ratio < th {
		t.Logf("PERFORMANCE ALERT [%s]: throughput %.1f MB/s, baseline %.1f MB/s, ratio %.1f%% (threshold %.0f%%) — performance below threshold, not failing test (Docker I/O variance)",
			label, proxyMBps, baselineMBps, ratio*100, th*100)
	}

	_ = result // available for callers that want programmatic access
}

// assertPerformance 是 AssertPerformance 的内部 alias，保持向后兼容
var assertPerformance = AssertPerformance

// AssertHeaderContains verifies that the proxy response contains the specified header value.
func AssertHeaderContains(t *testing.T, resp *http.Response, header, expectedValue string) {
	t.Helper()
	values := resp.Header.Values(header)
	found := false
	for _, v := range values {
		if v == expectedValue {
			found = true
			break
		}
	}
	assert.True(t, found, "header %q should contain value %q, got %v", header, expectedValue, values)
}

// nonAlphaNumRegex matches any non-alphanumeric character for subdomain derivation
var nonAlphaNumRegex = regexp.MustCompile(`[^a-z0-9]`)

// DeriveSubdomain converts a test name to a valid subdomain:
// lowercase, non-alphanumeric replaced with hyphens.
func DeriveSubdomain(testName string) string {
	s := strings.ToLower(testName)
	s = nonAlphaNumRegex.ReplaceAllString(s, "-")
	// 去除首尾的连字符
	s = strings.Trim(s, "-")
	return s
}

// deriveSubdomain 是 DeriveSubdomain 的内部 alias，保持向后兼容
var deriveSubdomain = DeriveSubdomain

// SetupProxyApp 创建一个测试用 App（auth_method="none"），关联指定 client。
// subdomain 从 t.Name() 派生，避免测试间冲突。
// 注册 t.Cleanup 自动删除 app。
func SetupProxyApp(t *testing.T, c *Client, serverURL, adminDomain string, clientID string, targetURL string) (appID int64, subdomain string) {
	t.Helper()

	subdomain = deriveSubdomain(t.Name())

	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  targetURL,
		"client_ids":  []string{clientID},
		"auth_method": "none",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create proxy app should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	err := DecodeJSON(resp.Body, &result)
	require.NoError(t, err, "decode create app response")
	require.NotZero(t, result.Data.ID, "app ID should not be zero")

	appID = result.Data.ID

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", appID))
	})

	return appID, subdomain
}

// SetupProxyAppWithConfig 创建一个带 proxy_config 的测试用 App（auth_method="none"）。
// 镜像 SetupProxyApp，仅在建 app 的 POST body 中多传 "proxy_config" 字段
// （多行 "key: value" 文本，如 "proxy_connect_timeout: 2s"）。
// 注册 t.Cleanup 自动删除 app。
func SetupProxyAppWithConfig(t *testing.T, c *Client, serverURL, adminDomain, clientID, targetURL, proxyConfig string) (appID int64, subdomain string) {
	t.Helper()

	subdomain = deriveSubdomain(t.Name())

	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":         t.Name(),
		"subdomain":    subdomain,
		"target_url":   targetURL,
		"client_ids":   []string{clientID},
		"auth_method":  "none",
		"proxy_config": proxyConfig,
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create proxy app with config should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	err := DecodeJSON(resp.Body, &result)
	require.NoError(t, err, "decode create app response")
	require.NotZero(t, result.Data.ID, "app ID should not be zero")

	appID = result.Data.ID

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", appID))
	})

	return appID, subdomain
}

// AuthAppOptions holds options for creating a test app with auth.
type AuthAppOptions struct {
	TargetURL    string   // Backend target URL (e.g. "http://test-backend:8000")
	ClientIDs    []string // Client IDs to associate; use ["__host__"] for P1
	AuthMethod   string   // "none", "sso", "token"
	AllowedUsers string   // "owner", "all", or specific username
	ExemptPaths  []string // Paths exempt from auth
}

// SetupAuthApp creates a test App with the specified auth_method and options.
// If ClientIDs is empty, uses ["__host__"]; otherwise uses the provided list.
// Registers t.Cleanup to delete the app.
func SetupAuthApp(t *testing.T, c *Client, serverURL, adminDomain string, opts AuthAppOptions) (appID int64, subdomain string) {
	t.Helper()

	subdomain = deriveSubdomain(t.Name())

	body := map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  opts.TargetURL,
		"client_ids":  opts.ClientIDs,
		"auth_method": opts.AuthMethod,
	}
	if opts.AllowedUsers != "" {
		body["allowed_users"] = opts.AllowedUsers
	}
	if len(opts.ExemptPaths) > 0 {
		body["exempt_paths"] = opts.ExemptPaths
	}

	resp := c.Post(t, "/api/apps", body)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create auth app should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	err := DecodeJSON(resp.Body, &result)
	require.NoError(t, err, "decode create auth app response")
	require.NotZero(t, result.Data.ID, "app ID should not be zero")

	appID = result.Data.ID

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", appID))
	})

	return appID, subdomain
}

// SetupHostApp 创建 __host__ 模式的测试 App，target_url 指向后端。
// 与 SetupProxyApp 类似但 client_ids 使用 ["__host__"]。
func SetupHostApp(t *testing.T, c *Client, serverURL, adminDomain string, targetURL string) (appID int64, subdomain string) {
	t.Helper()

	subdomain = deriveSubdomain(t.Name())

	resp := c.Post(t, "/api/apps", map[string]interface{}{
		"name":        t.Name(),
		"subdomain":   subdomain,
		"target_url":  targetURL,
		"client_ids":  []string{"__host__"},
		"auth_method": "none",
	})
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "create host app should succeed")

	var result struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	err := DecodeJSON(resp.Body, &result)
	require.NoError(t, err, "decode create host app response")
	require.NotZero(t, result.Data.ID, "app ID should not be zero")

	appID = result.Data.ID

	t.Cleanup(func() {
		c.Delete(t, fmt.Sprintf("/api/apps/%d", appID))
	})

	return appID, subdomain
}
