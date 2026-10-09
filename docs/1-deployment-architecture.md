# 1 - 部署架构与组件职责

## 整体部署拓扑

```
                          ┌─────────────────────┐
                          │   DNS (通配符解析)    │
                          │  *.proxy.example.com │
                          │  proxy.example.com   │
                          └──────────┬──────────┘
                                     │
                    ┌────────────────┼────────────────┐
                    ▼                ▼                ▼
             ┌────────────┐  ┌──────────────┐  ┌────────────┐
             │ 公网用户    │  │ 内网用户 A    │  │ 内网用户 B  │
             │ (外部网络)  │  │ (VLAN-10)    │  │ (VLAN-20)  │
             └─────┬──────┘  └──────┬───────┘  └──────┬─────┘
                   │                │                 │
                   ▼                │                 │
        ┌──────────────────┐       │                 │
        │ Nginx / Caddy     │       │                 │
        │ (SSL 终结)         │       │                 │
        │ 443 → :8080        │       │                 │
        └────────┬──────────┘       │                 │
                 │                  │                 │
                 ▼                  ▼                 ▼
        ┌─────────────────────────────────────────────────┐
        │              HopProxy 服务端 (:8080)              │
        │                                                  │
        │  ┌──────────────────────────────────────────┐   │
        │  │ Host 路由分发器                            │   │
        │  │                                          │   │
        │  │ Host == admin_domain?                     │   │
        │  │  ├─ YES → 管理流量                        │   │
        │  │  │   ├─ /api/* → API Handler             │   │
        │  │  │   ├─ /ws/*  → Tunnel Endpoint         │   │
        │  │  │   └─ 其他  → 前端 SPA                  │   │
        │  │  │                                        │   │
        │  │  └─ NO → 代理流量 (*.proxy.domain)        │   │
        │  │      → Proxy.ServeHTTP()                  │   │
        │  └──────────────────────────────────────────┘   │
        │                      ▲                         │
        │                      │ forwardToServer()       │
        └──────────────────────┼─────────────────────────┘
                               │ WebSocket Tunnel
                    ┌──────────┴──────────┐
                    ▼                     ▼
           ┌──────────────┐      ┌──────────────┐
           │ 客户端 Alpha  │      │ 客户端 Beta   │
           │ (:8080 本地)  │      │ (:8081 本地)  │
           │              │      │              │
           │ ┌──────────┐ │      │ ┌──────────┐ │
           │ │LocalProxy│ │      │ │LocalProxy│ │
           │ │          │ │      │ │          │ │
           │ │ 本地反代  │ │      │ │ Peer WS  │ │
           │ │ + Peer    │ │      │ │ 端点     │ │
           │ │ + 中转    │ │      │ │ + 本地反代│ │
           │ └──────────┘ │      │ └──────────┘ │
           └──────┬───────┘      └──────┬───────┘
                  │                     │
                  ▼                     ▼
           ┌──────────────┐     ┌──────────────┐
           │ 目标服务 A    │     │ 目标服务 B    │
           │ (:3000)      │     │ (:5000)       │
           └──────────────┘     └──────────────┘
```

## 组件一：服务端 (Server)

### 核心职责

服务端是整个系统的**协调中心**和**流量入口**，承担以下角色：

#### 1.1 单端口多路复用路由器

**文件**: `cmd/server/main.go`（主路由分发逻辑）

服务端监听单个 HTTP 端口（默认 `:8080`），根据请求的 `Host` 头将流量分发到不同处理器：

```go
// main.go - 主路由分发逻辑（简化）
handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    if isAdminRequest(database, r) {
        // 管理域名流量
        if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/") {
            apiRouter.ServeHTTP(w, r)
            return
        }
        // 前端静态文件（含多入口路由：/sso、/login、/guest、SPA fallback）
        staticFS.ServeHTTP(w, r)
    } else {
        // 代理子域名流量
        proxyHandler.ServeHTTP(w, r)
    }
})
```

**路由规则详解**：

| Host 匹配 | URL 路径前缀 | 处理方式 |
|-----------|-------------|----------|
| `admin_domain` | `/api/*` | REST API Handler（需 JWT 认证） |
| `admin_domain` | `/ws/*` | WebSocket 隧道端点（客户端连接） |
| `admin_domain` | `/sso*` | SSO 授权页面（需登录态） |
| `admin_domain` | `/login*` | 登录页面 |
| `admin_domain` | `/guest` | 访客提示页 |
| `admin_domain` | 其他 | 前端 SPA（Vue Router 接管） |
| `*.proxy_domain` | **任意** | 子域名代理路由 |

> **为什么不用子路径区分管理/代理流量？**
>
> 因为代理的目标应用可能使用任意路径结构。用 `Host` 头做第一层分发是最可靠的方式——管理域名的请求永远不需要被代理，代理域名的请求也永远不会命中管理接口。

#### 1.2 WebSocket 隧道管理器 (Hub)

**文件**: `internal/server/tunnel/hub.go`

Hub 是客户端连接的注册中心，维护 `clientID → ConnGroup` 的映射表（同一 clientID 的多条连接组成一个连接组）：

```go
type Hub struct {
    mu      sync.RWMutex
    conns   map[string]*ConnGroup  // clientID → 连接组 (连接池多连接并存)
}
```

关键操作：
- **Register(clientID, conn)**：新连接加入该 clientID 的 ConnGroup。**不踢旧连接**——同一 clientID 的多条连接（客户端连接池）并存，`OpenStream` 在组内按 Round Robin 选路。
- **Unregister(clientID, conn)**：从连接组中移除指定连接；组空时删除 map 项。
- **Kick(clientID, reason)**：管理动作，对整个连接组发送 `kick` 控制流。
- **GetConn(clientID)**：返回 `*ConnGroup`，调用方通过 `OpenStream()`（组内轮询）/ `Count()` / `NumStreams()` 获取连接信息。
- **GetConnHealth(clientID)**：查询连接在线状态和最近 RTT。
- **NotifyAppsChanged()**：广播应用配置变更通知给所有在线客户端。

**连接池共存设计意图**：客户端可同时维持多条 WS+yamux 连接（默认 4 条，随机 5–10 分钟 TTL 淘汰）。同一 UUID 的连接不再互斥，`Register` 不踢旧连接；管理员需要强制断开时使用管理动作 `Kick`（作用于整个连接组）。这避免了旧模型"两个客户端抢同一个身份反复互踢"的问题。

#### 1.3 子域名代理引擎 (Proxy)

**文件**: `internal/server/proxy/proxy.go`

Proxy 是代理流量的核心处理器，`ServeHTTP()` 方法执行完整的请求处理管线：

```
ServeHTTP 入口 (proxy/proxy.go)
  │
  ├─ 1. 提取子域名 (ExtractSubdomain)
  │     从 Host 头中去掉 proxy_domain 后缀
  │     例: "app1.proxy.example.com" → "app1"
  │
  ├─ 2. Body 大小限制 (MaxBytesReader, 50MB)
  │     防止恶意大请求导致 OOM
  │
  ├─ 3. 应用查找 (GetAppClientsWithTarget)
  │     查询 app + 关联的客户端列表及各自目标地址
  │     未找到 → SSO 重定向（不泄露子域名是否存在的信息）
  │
  ├─ 4. SSO 认证检查 (CheckSSOAuth)
  │     根据 auth_method 配置决定是否需要认证
  │     exempt_paths 配置可豁免特定路径
  │
  ├─ 5. 客户端选择 (selectClient)
  │     load_balance=true → Round Robin 轮询
  │     load_balance=false → 主备模式（取第一个在线的）
  │
  ├─ 6. 路径分派
  │     ├─ ClientID == "__host__" → proxyLocalHTTP / proxyLocalWebSocket
  │     ├─ WebSocket 升级 → proxyWebSocket（隧道 WS 代理）
  │     └─ 普通 HTTP → proxyHTTP（隧道 HTTP 代理，支持 Failover 重试）
  │
  └─ 7. 异步更新应用最后使用时间
```

#### 1.4 数据管理层 (SQLite)

**文件**: `internal/server/db/`

采用纯 Go SQLite 实现 (`modernc.org/sqlite`)，无 CGO 依赖：

- **22 个增量迁移版本**（`CurrentMigrationVersion = 22`）：从 v1 到 v22，每次新增功能时添加迁移而非重建表
- **核心数据模型**：users, settings, clients, apps, app_clients, client_proxies, sso_sessions, access_tokens
- **自动清理**：过期 SSO Session 自动删除；不活跃应用定时禁用

### 定时任务

**文件**: `cmd/server/main.go`（定时任务部分）

- **不活跃应用清理器**：每日凌晨 3 点运行，禁用超过 `inactive_days` 未被访问的应用
  - 设计目的：防止长期不用的子域名占用资源或成为安全盲区

### HTTP 服务器调优参数

**文件**: `cmd/server/main.go`（HTTP 服务器配置）

```go
server := &http.Server{
    ReadHeaderTimeout: 10 * time.Second,  // 读请求头超时，防 Slowloris 攻击
    ReadTimeout:       60 * time.Second,  // 读完整请求超时（含 body）
    WriteTimeout:      0,                 // 不设限制！SSE/流式响应需要长时间写入
    IdleTimeout:       120 * time.Second, // Keep-Alive 连接空闲超时
}
```

**注意 `WriteTimeout: 0` 的设计意图**：SSE (Server-Sent Events)、WebSocket、大文件下载等场景下，服务端可能需要持续数分钟甚至更久的写入时间。设置 WriteTimeout 会导致长连接被误杀。

## 组件二：客户端 (Client)

### 核心职责

客户端是**隧道端点**和**本地代理执行器**，有两种运行模式：

### 2.1 纯隧道模式（无 `--listen`）

最简模式，仅承担隧道连接和请求执行：

```
启动流程:
  1. 解析连接参数 (--connect 或 --server + --id, --pool-size / HP_POOL_SIZE 默认 4)
  2. 生成 Peer 密钥 (crypto/rand 32 字节)
  3. 创建 tunnel.Client 并配置连接池 (SetPoolConfig: 默认 4 条 WS+yamux 连接,
     随机 5–10min TTL 淘汰, 1 条退化为单连接)
  4. 注册流处理回调 (SetHandlers):
     - StreamHTTP (0x01) → HandleHTTPRequest → 请求目标 → 回写响应
     - StreamWS   (0x02) → HandleWSOpen → 连接目标 WS → BridgeWS 双向转发
     - StreamCtrl (0x03) → ActionAppsChanged(重拉应用列表) / ActionKick(退出) 等控制流
  5. 启动连接池 (Run): 每条连接独立建连 + AcceptStream 循环 + 指数退避重连
```

### 2.2 双模模式（带 `--listen`）

在隧道基础上额外启动本地 HTTP 代理：

```
额外组件:
  ├── AppManager: 从服务端同步应用列表到本地缓存
  ├── LocalProxy:  监听本地端口，处理内网用户请求
  │   ├── 子域名命中本地应用 → 直接反代 / Peer 代理
  │   └─ 子域名未命中 → forwardToServer() 经主隧道转发
  ├── PeerManager: 管理 Peer 隧道的创建/重连/销毁
  └── /ws-peer 端点: 作为 Peer 隧道的接受方（被其他客户端连接）
```

**文件**: `cmd/client/main.go`

### 隧道连接生命周期

**文件**: `internal/client/tunnel/tunnel.go` + `pool.go`

```
连接建立 (每条连接):
  pkgTunnel.NewClientSession(wss://server/ws/<uuid>)
  → WebSocket → websocket.NetConn → yamux.Client
  → 进入 AcceptStream 循环

流分发 (每条连接各自 AcceptStream):
  for {
    stream, _ := session.AcceptStream()
    读取流首字节 StreamType:
      0x01 (StreamHTTP) → HTTPHandler
      0x02 (StreamWS)   → WSHandler
      0x03 (StreamCtrl) → 控制流:
          ActionAppsChanged → 重新拉取应用列表
          ActionKick        → 设置 kicked 标志 → 关闭整个连接池退出（不重连!）
  }

心跳: 由 yamux 内置 keepalive 承担 (KeepAliveInterval=30s, PingBacklog=32),
      无应用层 ping/pong 消息

断线处理 (连接池, 每条连接独立):
  单条连接读取错误（网络断开等）:
  → 指数退避等待 (初始间隔可配, 最大 60s)
  → + 50% 随机抖动 (防惊群: 多客户端同时断线同时重连)
  → 重新 Dial 该连接
  → 如果连接存活 >30s 后才断开 → 重置退避计数器
    （区别正常运行的偶然断线 vs 启动阶段的配置错误）
```

### 状态上报机制

客户端定期通过隧道向服务端上报自身状态：

```go
// 上报内容:
type ProxyStatus struct {
    ProxyEnabled  bool   // 是否启用了 --listen
    ListenAddress string // 监听地址 (如 ":8080")
    PeerSecret    string // Peer 认证密钥 (32字节随机数)
}
```

服务端将 `peer_secret` 存入 `clients` 表，供其他客户端在建立 Peer 连接时获取。

## 组件三：管理前端 (Web)

### 技术栈与架构

- **框架**: Vue 3 Composition API + TypeScript
- **状态管理**: Pinia
- **构建工具**: Vite
- **样式**: Tailwind CSS
- **部署方式**: Vite 构建输出 → Go `//go:embed` 嵌入 → 单个二进制文件

### 前端多入口路由

**文件**: `cmd/server/main.go`（前端多入口路由）

虽然前端是 SPA，但为了 SEO 和直接访问体验，服务端实现了基于路径的多入口路由：

| URL 路径 | 返回的 HTML | 条件 |
|----------|------------|------|
| `/sso*` | `sso.html` | 需要已登录 session |
| `/login*` | `login.html` | 无条件 |
| `/guest` | `guest.html` | 无条件 |
| 其他 (`/`, `/apps`, ...) | `index.html` | Vue Router 接管客户端路由 |

SSO 页面的特殊处理逻辑（`main.go` 前端路由部分）：
1. 检查用户是否已登录（session Cookie 有效）
2. 未登录 → 302 到 `/login?next=<原URL>`
3. 已登录 + 有 `redirect` 参数 → 检查目标应用的 SSO Cookie 是否已有效 → 若有效则直接跳转（避免多余授权确认步骤）

## Nginx 反向代理配置要点

### 标准 HTTPS 反代配置

```nginx
server {
    listen 443 ssl;
    server_name proxy.example.com *.proxy.example.com;

    ssl_certificate     /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    # TLS 优化
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers HIGH:!aNULL:!MD5;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;

    location / {
        proxy_pass http://127.0.0.1:8080;

        # 核心：传递原始 Host 头
        proxy_set_header Host $host;

        # 客户端真实 IP 传递
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        # 原始协议信息（用于 SSO 重定向 URL 构建等）
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSocket 支持（三个缺一不可）
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        # 超时配置（与服务端的 WriteTimeout=0 对齐）
        proxy_read_timeout 86400s;   # 允许 SSE/长连接
        proxy_send_timeout 86400s;
    }
}
```

### 关键配置解释

| 配置项 | 为什么需要 |
|--------|-----------|
| `proxy_set_header Host $host` | **至关重要**。服务端通过 Host 头区分管理和代理流量。必须传递原始域名（如 `app1.proxy.example.com`），不能替换为 `127.0.0.1:8080` |
| `Upgrade` + `Connection "upgrade"` | WebSocket 代理必需。Nginx 默认会移除这些头，导致隧道连接和 WS 代理升级失败 |
| `proxy_read_timeout 86400s` | 服务端 `WriteTimeout=0` 不限制写入时间（SSE/WS 场景），Nginx 默认 60s 超时会提前断开长连接 |
| `ssl_server_name *.example.com` | 通配符证书覆盖所有子域名，无需为每个应用单独申请证书 |

### 可选：HTTP → HTTPS 强制跳转

```nginx
server {
    listen 80;
    server_name proxy.example.com *.proxy.example.com;
    return 301 https://$host$request_uri;
}
```
