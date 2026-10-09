# HopProxy Integration Tests

This directory contains the integration test suite for HopProxy. Tests run against a real Docker Compose environment with a full server + client + backend stack.

## Ground Rules

**Tests must never modify server/client source code.**

The test suite is a black-box consumer of the production binaries. If a test reveals a bug or missing capability in the server/client code, the correct process is:

1. Document the finding in `issues/` as a new issue file
2. Present the proposed change to the project owner for confirmation
3. After confirmation, a developer implements the change in the main codebase
4. Tests are updated to cover the new behavior

This ensures the test suite stays honest and that issues are tracked properly.

## Directory Structure

```
test/
├── go.mod / go.sum         # Independent Go module (separate from the main project)
├── TODO.md                 # Planned-but-not-implemented test coverage notes
│
├── docker/
│   ├── docker-compose.yml          # Main compose file (server + 2 clients + backends)
│   ├── docker-compose.override.yml # Dev override: mount local binaries instead of rebuilding
│   ├── Dockerfile.test-backend     # Image build for the Go HTTP test backend
│   └── nginx/                      # nginx static backend config and HTML files
│
├── backends/               # Test backend (own Go module: test/backends/go.mod)
│   ├── go.mod / go.sum     #   module github.com/robin/hop-proxy/test/backends
│   ├── main.go             # Go HTTP test backend (echo, headers, cookies, status codes,
│   │                       #   large files, delay, SSE streaming, WebSocket echo,
│   │                       #   WS subprotocol negotiation, SSE race endpoints)
│   └── backends/           # Independent sub-module source (used by Dockerfile.test-backend)
│
├── harness/                # Test helper library (used by all test suites)
│   ├── client.go           # HTTP client with cookie jar, CSRF token, Host header injection
│   ├── env.go              # Docker Compose lifecycle (start, stop, logs, exec)
│   ├── factory.go          # Session helpers: Setup(), Login(), IsInitialized()
│   ├── proxy.go            # Proxy path helpers: NewDirectClient / NewProxyClient / assertResponseEqual
│   ├── wait.go             # Readiness polling: WaitForServer(), WaitForSetup(), etc.
│   ├── json.go             # JSON encode/decode helpers
│   └── doc.go              # Package documentation
│
└── suites/                 # Test cases (package suites)
    ├── setup_test.go       # TestMain: automated environment lifecycle (start/teardown)
    ├── smoke_test.go       # Smoke + setup + auth + client + harness infrastructure tests
    ├── apps_test.go        # Application CRUD tests
    ├── clients_test.go     # Client management tests
    ├── auth_advanced_test.go # Advanced authentication (Bearer, Basic, token scope)
    ├── redirect_test.go    # App redirect rule tests
    ├── e2e_proxy_test.go   # P1–P6 proxy path matrix (HTTP/WS/SSE/large files)
    ├── e2e_loadbalance_test.go # Round-robin + primary/backup failover
    ├── e2e_reconnect_test.go   # Client reconnect behavior
    ├── e2e_ws_deep_test.go / e2e_ws_close_test.go / e2e_ws_sse_test.go / e2e_ws_sse_realworld_test.go / e2e_vscode_ws_test.go
    ├── e2e_sse_race_test.go / e2e_sse_truncation_test.go
    ├── e2e_auth_matrix_test.go # Auth method matrix
    ├── e2e_chunked_request_test.go # Chunked request-body normalization
    ├── e2e_p3_p5_deep_test.go / e2e_p3_p6_test.go / e2e_p4_p6_deep_test.go # Deep path combos
    ├── e2e_perf_test.go    # Throughput vs direct-connection benchmark
    └── sessions_test.go    # 会话管理：列表/强制下线/请求计数（#80）
```

## Prerequisites

- Docker and Docker Compose v2 (`docker compose` command)
- Go 1.25.0 (per `test/go.mod`; test module uses `github.com/stretchr/testify v1.10.0` and `github.com/coder/websocket v1.8.15`)

## Running Tests

### One-command run (recommended)

The test suite includes a `TestMain` in `suites/setup_test.go` that automatically manages the full Docker Compose lifecycle:

1. Builds images and starts server + backends
2. Initializes the system (creates admin user)
3. Registers two clients via the API, captures their UUIDs
4. Starts client containers with the assigned UUIDs
5. Waits for all clients to come online
6. Runs all tests
7. Tears down the environment and removes volumes

```sh
cd test
go test ./suites/ -v -timeout 600s
```

That's it. No manual `docker compose up` needed.

### Run a specific test

```sh
cd test
go test ./suites/ -v -run TestAuth_Login_Success -timeout 600s
```

The `TestMain` still runs (starts/stops the environment), but only the matching test functions execute.

### Keep the environment running between runs

If you want to iterate on tests without rebuilding the environment every time, you can start it manually and `TestMain` will detect it and skip the startup phase:

```sh
# Step 1: Start the environment manually (one-time)
docker compose -f test/docker/docker-compose.yml up -d --build

# Step 2: Initialize the system (if first run)
# The server needs to be initialized and clients registered.
# The easiest way is to let TestMain do it once:
cd test && go test ./suites/ -v -run TestSmoke_ServerVersion -timeout 300s
# TestMain will initialize the system and start clients, but won't
# tear down because the environment was already running.

# Step 3: Re-run tests quickly (environment stays up)
cd test && go test ./suites/ -v -run TestApps -timeout 120s
```

Or use pre-built local binaries (faster iteration during development):

```sh
# Build binaries first
make server client

# Start with local binaries mounted
docker compose \
  -f test/docker/docker-compose.yml \
  -f test/docker/docker-compose.override.yml \
  up -d
```

### Stop the environment

If you started the environment manually, stop it with:

```sh
# Stop containers but preserve the data volume
docker compose -f test/docker/docker-compose.yml down

# Stop and wipe all data (clean slate)
docker compose -f test/docker/docker-compose.yml down -v
```

When using the one-command run, `TestMain` automatically tears down and removes volumes after tests finish.

## Test Environment Details

| Service | Description | Address (host) |
|---|---|---|
| `server` | hop-proxy-server | `http://localhost:18080` |
| `client-1` | hop-proxy-client (UUID assigned dynamically) | `http://localhost:19091` (LocalProxy) |
| `client-2` | hop-proxy-client (UUID assigned dynamically) | `http://localhost:19092` (LocalProxy) |
| `test-backend` | Go HTTP test backend | `http://localhost:18081` (direct) / `http://test-backend:8000` (internal) |
| `test-nginx` | nginx static backend | internal only (`http://test-nginx`) |

All requests use `Host: hopproxy-admin.test` to route to the admin interface without modifying `/etc/hosts`.

Client UUIDs are assigned dynamically by the API at startup (not hardcoded). `TestMain` handles this two-phase process: start server → register clients → start client containers with the assigned UUIDs.

## Writing New Tests

### 1. Create a new file in `suites/`

Name it after the feature domain, e.g. `suites/apps_test.go`.

```go
package suites

import (
    "net/http"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"

    "github.com/robin/hop-proxy/test/harness"
)

func TestApps_Create(t *testing.T) {
    if !harness.IsInitialized(serverURL, adminDomain) {
        t.Skip("system not initialized")
    }

    c := harness.Login(t, serverURL, adminDomain)

    resp := c.Post(t, "/api/apps", map[string]interface{}{
        "name":      "my-app",
        "subdomain": "my-app",
    })
    defer resp.Body.Close()
    require.Equal(t, http.StatusOK, resp.StatusCode)

    var result struct {
        Data struct {
            ID   int    `json:"id"`
            Name string `json:"name"`
        } `json:"data"`
    }
    err := harness.DecodeJSON(resp.Body, &result)
    require.NoError(t, err)
    assert.Equal(t, "my-app", result.Data.Name)
}
```

### 2. Use the harness helpers

| Helper | Description |
|---|---|
| `harness.NewClient(serverURL, adminDomain)` | Create an HTTP client with Host header pre-set |
| `harness.Login(t, serverURL, adminDomain)` | Login as admin, return authenticated client |
| `harness.IsInitialized(serverURL, adminDomain)` | Check if the system has been initialized |
| `harness.Setup(t, serverURL, adminDomain, proxyDomain)` | Initialize the system |
| `harness.WaitForServer(serverURL, host, timeout)` | Wait until server is reachable |
| `harness.WaitForSetup(serverURL, host, timeout)` | Wait until setup endpoint is reachable |
| `harness.WaitForClientOnline(...)` | Wait until a specific client appears online |
| `harness.NewTestEnv(dockerDir())` | Access Docker Compose commands (exec, logs, etc.) |
| `harness.DecodeJSON(resp.Body, &result)` | Decode JSON response body |
| `harness.NewDirectClient()` | Bypass HopProxy, hit the test backend directly (baseline) |
| `harness.NewProxyClient(serverURL, proxyDomain)` | Route requests through HopProxy |
| `proxy.DoGet / DoPost` | Issue GET/POST through the proxy client |
| `harness.assertResponseEqual(...)` | Compare status, Content-Type, body between direct and proxy |
| `harness.AssertBodyHashEqual(...)` | SHA-256 body hash comparison (large files) |
| `harness.assertPerformance(...)` | ≥95% throughput vs direct baseline |

The `Client` methods available are:

```go
c.Get(t, "/api/path")
c.Post(t, "/api/path", body)
c.Put(t, "/api/path", body)
c.Delete(t, "/api/path")
c.SetCookie("name", "value")
c.GetCookie("name")
```

CSRF tokens are injected automatically on write requests (POST/PUT/DELETE) using the `hopproxy_csrf` cookie value set on the client.

### 3. Constants

All suites share the constants defined in `smoke_test.go`:

```go
const (
    serverURL   = "http://localhost:18080"
    adminDomain = "hopproxy-admin.test"
    proxyDomain = "hopproxy.test"
)
```

### 4. Test naming conventions

```
Test<Domain>_<Action>_<Condition>

Examples:
  TestApps_Create
  TestApps_Create_MissingName
  TestAuth_Login_WrongPassword
  TestE2E_Proxy_HTTP
```

## Extending the Test Backend

The test backend (`backends/main.go`) supports:

| Endpoint | Description |
|---|---|
| `GET /` | Returns request info as JSON |
| `POST /echo` | Returns the request body verbatim |
| `GET /headers` | Returns all request headers as JSON |
| `GET /cookies` | Returns all request cookies as JSON |
| `GET /status/{code}` | Returns the given HTTP status code |
| `GET /size/{n}MB` | Returns a file of the given size (also KB, GB) |
| `GET /random/{n}KB` | Returns random bytes (compression-friendly sizes) |
| `GET /delay?duration=1s` | Returns after the given delay |
| `GET /stream?count=5` | Sends SSE events |
| `GET /chunked` | Returns chunked transfer encoding response |
| `GET /multi-headers` | Returns repeated headers (multi-value passthrough) |
| `GET /sha256` | Returns SHA-256 hash of response body (integrity check) |
| `GET /set-cookies` | Sets cookies in response |
| `GET /health` | Health check endpoint |
| `POST /reqinfo` | Returns the framing the backend actually received (Content-Length, Transfer-Encoding, body size) — used to verify chunked request-body normalization |
| `WS  /ws` | WebSocket echo server |
| `WS  /ws/close-all` | Closes all active WS connections (cleanup helper) |
| `WS  /ws/push?count=50&size=256&type=binary|text` | Pushes N frames after connect (server push) |
| `WS  /ws/subprotocol?protocol=vscode-ws-jsonrpc` | Echo WS server that negotiates the requested subprotocol (VSCode-style) |
| `GET /stream/race?count=5&interval=0ms` | SSE race endpoint: writes bursts without Flush (truncation races) |

To add new endpoints, edit `backends/main.go` and rebuild by restarting the test environment.
