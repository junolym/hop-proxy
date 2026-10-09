# HopProxy

自建内网穿透与反向代理系统。通过公网服务器运行服务端、内网机器运行客户端，将内网 HTTP/WebSocket 服务暴露到公网子域名访问，同时为内网用户提供零延迟的本地直连路径。

## 核心设计目标

**同一个子域名，无论从公网还是内网访问，都能正确到达目标服务。**

这是 HopProxy 与其他内网穿透工具最本质的区别。传统穿透工具只解决「公网如何访问内网」的问题，而 HopProxy 同时解决「内网用户如何高效访问」和「跨客户端服务如何互通」两个问题，形成**三层访问路径**：

| 层级 | 路径 | 延迟 | 适用场景 |
|------|------|------|----------|
| 第一层 | 本地直连（Local Proxy） | 最低 | 内网用户访问同机或 Peer 直达的目标 |
| 第二层 | Peer 隧道直连 | 低 | 跨内网机器的服务互访，不经过公网 |
| 第三层 | 服务端中转（Tunnel Relay） | 中等 | 公网用户访问，或本地无匹配应用时透明回退 |

## 架构总览

```
┌──────────────────────────────────────────────────────────────────┐
│                         公网 / 内网 用户                           │
│                          (浏览器/API 客户端)                       │
└─────────────┬────────────────────────────────┬───────────────────┘
              │ HTTPS (通配符域名)               │ HTTP (DNS → 内网)
              ▼                                 ▼
┌──────────────────────────┐      ┌────────────────────────────────┐
│  Nginx / Caddy           │      │  客户端 A 本地代理 (:8080)     │
│  (SSL 终结 + 反向代理)    │      │  ┌──────────────────────────┐ │
│                          │      │  │ 子域名命中?               │ │
│  *.proxy.example.com ────┼─────►│  │  ├─ 是 → 直接反代/Peer   │ │
│  proxy.example.com ──────┤      │  │  └─ 否 → forwardToServer │ │
└─────────────┬────────────┘      │  └──────────┬───────────────┘ │
              │                    │             │ WebSocket 隧道   │
              ▼                    └─────────────┼─────────────────┘
┌──────────────────────────────────────────────┐                 │
│          HopProxy 服务端 (:8080)              ◄─────────────────┘
│                                              │
│  ┌──────────────────────────────────────┐   │ WebSocket 隧道
│  │ Host 头路由分发器                       │   │
│  │                                       │   │
│  │ 管理域名的流量:                        │   │
│  │  ├─ /api/*  → REST API Handler        │   │
│  │  ├─ /ws/*   → WebSocket 隧道端点       │◄──┼── 客户端 B 连接隧道
│  │  └─ 其他    → 前端 SPA 静态文件         │   │
│  │                                       │   │
│  │ 子域名流量 (*.proxy.domain):          │   │
│  │  → Proxy.ServeHTTP()                  │   │
│  │    ├─ SSO 认证检查                     │   │
│  │    ├─ selectClient() 选择目标          │   │
│  │    ├─ __host__ → 本机直接反代          │   │
│  │    ├─ 在线客户端 → 隧道代理转发         ├──►│── 客户端 C 连接隧道
│  │    └─ Peer 配置 → 通过客户端转发给 Peer │   │
│  └──────────────────────────────────────┘   │
│                                              │
│  数据层: SQLite (纯 Go, 无 CGO)              │
└──────────────────────────────────────────────┘
```

### 组件职责

| 组件 | 二进制 | 运行位置 | 核心职责 |
|------|--------|----------|----------|
| **服务端** | `hop-proxy-server` | 公网 Linux 服务器 | 单端口路由分发、子域名解析、SSO 认证、WebSocket 隧道管理、管理后台 API + 前端 SPA |
| **客户端** | `hop-proxy-client` | 内网 Linux 服务器 | 维护与服务端的持久 WebSocket 隧道、执行代理请求（HTTP/WS）、可选启动本地代理监听内网请求 |
| **管理前端** | 嵌入服务端二进制 | 浏览器 | Vue 3 SPA，管理客户端/应用/设置 |

> **设计决策：服务端与客户端分离编译**
>
> 客户端的定位是轻量稳定的隧道执行器——一旦隧道协议稳定，基本无需更新。服务端包含业务逻辑、管理界面、数据库迁移等，迭代频繁。分开编译避免客户端因服务端功能更新而被迫重新部署。

### 单端口架构

服务端仅监听一个 HTTP 端口，通过解析请求的 `Host` 头区分流量类型：

- **管理域名** (`proxy.example.com`) → 管理后台 API + 前端页面 + WebSocket 隧道端点 (`/ws/<uuid>`)
- **代理子域名** (`*.proxy.example.com`) → 子域名提取 → 应用查找 → SSO 认证 → 代理转发

外部仅需部署一个 Nginx（或 Caddy），配置通配符 SSL 证书统一反代到服务端口即可。

## 技术栈

| 领域 | 选型 | 说明 |
|------|------|------|
| 语言 | Go 1.22+ | 服务端 & 客户端分别编译为独立二进制，交叉编译无依赖 |
| HTTP 框架 | 标准库 `net/http` | 足够强大，无需引入第三方框架 |
| WebSocket | `nhooyr.io/websocket` | 现代 Go WS 库，基于 `net/http` 的 Context 友好接口 |
| 数据库 | SQLite (`modernc.org/sqlite`) | 纯 Go 实现，零 CGO 依赖，单文件存储，20 个增量迁移版本 |
| 认证 | JWT (`golang-jwt/jwt/v5`) + httpOnly Cookie | 管理员会话使用 httpOnly Cookie 存储 JWT |
| 前端 | Vue 3 + TypeScript + Tailwind CSS + Pinia | SPA 构建，`embed` 打包进服务端二进制 |
| 构建 | Go `//go:embed` | 单个二进制即可完成全部部署 |

## 六种请求代理路径

HopProxy 的核心能力在于支持多种代理路径，系统根据网络拓扑和应用配置自动选择最优路径：

### 路径一：服务端本地反向代理 (Server-Side Local Proxy)

当应用的关联客户端 ID 为特殊值 `__host__` 时，服务端直接向目标地址发起 HTTP 请求，**无需经过任何隧道**。

**适用场景**：目标服务与 HopProxy Server 运行在同一台机器上（如 Server 上运行的监控面板、CI 工具等）。

**延迟特征**：最低（仅一次本地 HTTP 往返）。

### 路径二：隧道代理 (Tunnel Proxy)

经典的内网穿透模式。服务端将请求序列化后通过 WebSocket 隧道发送给远程客户端，客户端执行实际的 HTTP 请求并将响应回传。

**适用场景**：公网用户访问内网服务的**主要方式**。

**延迟特征**：中等（用户→Server→隧道→Client→目标→原路返回）。

**关键特性**：
- 全量流式传输（SSE/大文件/Chunked Transfer 原生支持）
- 主备自动故障转移 (Failover)
- Round Robin 负载均衡（多客户端关联同一应用）
- WebSocket 全双工代理

### 路径三：客户端本地代理 (Client Local Proxy)

客户端启用 `--listen` 参数后，在本地开启 HTTP 代理服务。内网 DNS 将 `*.proxy_domain` 解析到该客户端 IP，实现**内网请求不经过公网服务器**。

**适用场景**：内网用户访问内部服务，追求最低延迟。

**关键特性**：
- 子域名命中本地应用 → 直接反代（零额外延迟）
- 未命中 → 透明转发到服务端（用户完全无感知）
- SSO 双层检查：本地 Session 缓存（1分钟 TTL）+ 隧道远程验证

### 路径四：Peer 对等代理 (Client-to-Client via Peer Tunnel)

客户端之间通过 WebSocket 建立**内网直接通信隧道**，请求不经过服务端。

**适用场景**：目标服务运行在 Peer 客户端 B 所在机器上，但用户从主客户端 A 的本地代理发起请求。

**安全机制**：HMAC-SHA256 签名认证 + 时间戳防重放（5分钟窗口）+ 密钥启动轮换 + 认证头清理。

**延迟特征**：低（内网直连，不经公网）。

### 路径五：客户端到服务端中转 (Client → Server Relay)

客户端本地代理收到无法本地处理的请求时（子域名未匹配），将其封装后通过主隧道转发给服务端，由服务端走标准隧道代理流程处理。

**触发条件**：路径三的回退机制——本地没有该应用配置时的透明降级。

**用户体验**：用户完全不感知请求是本地处理还是经服务端中转。

### 路径六：隧道+Peer 组合 (Tunnel → Client A → Peer → Client B)

公网用户访问的应用，其目标服务位于另一个客户端上。服务端通过隧道将请求发给关联的主客户端，主客户端再通过 Peer 隧道转发给实际持有目标服务的 Peer 客户端。

**数据链路**：用户 → Nginx → Server → [主隧道] → Client A → [Peer 隧道] → Client B → 目标服务

> 详细的数据流图、代码位置和技术细节参见 [`docs/2-access-paths.md`](docs/2-access-paths.md)。

## 快速开始

### 编译

```bash
# 需要 Go 1.22+ 和 Node.js 18+
make all
```

产物在 `bin/` 目录：`hop-proxy-server` 和 `hop-proxy-client`。

### 部署服务端

```bash
# 默认监听 :8080，数据库存储在 ./data/hopproxy.db
./hop-proxy-server

# 自定义参数
./hop-proxy-server --listen :9090 --db /var/lib/hopproxy/data.db
```

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| `--listen` | `HP_LISTEN` | `:8080` | 监听地址 |
| `--db` | `HP_DB_PATH` | `./data/hopproxy.db` | SQLite 数据库路径 |

首次访问管理域名时进入引导流程（创建管理员账户 + 配置站点信息）。

### Nginx 配置示例

```nginx
server {
    listen 443 ssl;
    server_name proxy.example.com *.proxy.example.com;

    ssl_certificate     /path/to/cert.pem;
    ssl_certificate_key /path/to/key.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;

        # WebSocket 支持（隧道连接和 WS 代理都需要）
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

### 部署客户端

```bash
# 方式一：完整连接 URL
./hop-proxy-client --connect "wss://proxy.example.com/ws/<client-uuid>"

# 方式二：分开指定
./hop-proxy-client --server wss://proxy.example.com --id <client-uuid>

# 方式三：启用本地代理（内网直连 + Peer 支持）
./hop-proxy-client --connect "wss://..." --listen :8080
```

| 参数 | 环境变量 | 默认值 | 说明 |
|------|----------|--------|------|
| `--connect` | — | — | 完整 WebSocket 连接 URL |
| `--server` | `HP_SERVER` | — | 服务端地址 |
| `--id` | `HP_CLIENT_ID` | — | 客户端 UUID（管理页面创建） |
| `--listen` | — | — | 启用本地代理并指定监听地址 |
| `--reconnect-interval` | `HP_RECONNECT_INTERVAL` | `10s` | 断线重连初始等待时间 |
| `--pool-size` | `HP_POOL_SIZE` | `4` | 到服务端的并发 WS 连接数（连接池大小）；`1` 退化为单连接 |
| `--conn-min-ttl` | `HP_CONN_MIN_TTL` | `5m` | 单连接最短存活时间，到点触发替换 |
| `--conn-max-ttl` | `HP_CONN_MAX_TTL` | `10m` | 单连接最长存活时间，到点触发替换 |
| `--conn-drain-grace` | `HP_CONN_DRAIN_GRACE` | `0` | drain 宽限期：旧连接替换后等待在途 stream 结束的时间；`0`=永久等待（不打断长连接），`5m`=最多等 5 分钟超时强关 |

> **连接池**：客户端默认维护 4 条到服务端的 WebSocket 连接（多 TCP 连接多路复用），缓解公网丢包导致的 TCP 队头阻塞。每条连接随机 5~10 分钟到期替换——先建新连接，再 drain 旧连接（不再派新流，等在途请求结束后关闭）。`HP_POOL_SIZE=1` 可退化为单连接模式。详见 [`issues/2026-07-30-yamux连接池方案.md`](issues/2026-07-30-yamux连接池方案.md)。

### 使用流程

1. 部署服务端，完成首次引导（管理员注册 + 域名配置）
2. 管理页面添加客户端，获取 UUID
3. 内网服务器运行客户端，用 UUID 连接服务端
4. 管理页面添加应用，配置子域名 + 目标地址 + 关联客户端
5. 通过 `https://<subdomain>.proxy.example.com` 访问

## 项目结构

```
hop-proxy/
├── cmd/
│   ├── server/main.go            # 服务端入口（路由分发、定时任务、优雅关闭）
│   └── client/main.go            # 客户端入口（参数解析、组件组装、消息分发）
├── internal/
│   ├── server/
│   │   ├── api/                  # REST API 层
│   │   │   ├── router.go         # 路由定义（API + WS 隧道端点）
│   │   │   ├── auth.go           # 登录/JWT/密码修改
│   │   │   ├── apps.go           # 应用 CRUD
│   │   │   ├── clients.go        # 客户端管理
│   │   │   ├── sso.go            # SSO 授权/撤销
│   │   │   ├── middleware.go      # JWT/CSRF/限流中间件
│   │   │   └── ...
│   │   ├── db/                   # SQLite 数据层
│   │   │   ├── migrations.go     # 20 个增量迁移版本
│   │   │   ├── models.go         # 数据模型定义
│   │   │   └── *.go              # 各实体 CRUD 操作
│   │   ├── tunnel/               # WebSocket 隧道管理层
│   │   │   ├── hub.go            # 连接注册中心 (clientID → Conn)
│   │   │   ├── conn.go           # 单连接生命周期（心跳/RTT/消息分发）
│   │   │   └── handler.go        # 隧道 HTTP 端点 + 消息处理
│   │   └── proxy/                # 请求代理引擎
│   │       ├── proxy.go          # ServeHTTP 入口 + 客户端选择逻辑
│   │       ├── selector.go       # 主备 Failover / Round Robin 策略
│   │       ├── http.go           # 隧道 HTTP 代理
│   │       ├── forward.go        # 处理客户端转发来的请求
│   │       ├── local.go          # __host__ 本地反代
│   │       ├── local_ws.go       # __host__ 本地 WS 反代
│   │       ├── websocket.go      # 隧道 WS 代理
│   │       └── sso.go            # SSO 认证检查 + 重定向
│   └── client/
│       ├── tunnel/tunnel.go      # 隧道客户端（自动重连/心跳/踢出处理）
│       ├── proxy/http.py         # 执行隧道 HTTP 代理请求
│       ├── proxy/websocket.go    # 执行隧道 WS 代理请求
│       ├── localproxy/proxy.go   # **本地代理核心**（子域名路由/认证/转发决策）
│       ├── localproxy/forward.go # 转发器到服务端
│       ├── localproxy/peer_tunnel.go   # Peer WS 隧道实现
│       ├── localproxy/peer_manager.go  # 多 Peer 隧道管理
│       ├── apps/manager.go       # 应用列表同步管理
├── pkg/                         # 公共库（server/client 共享）
│   ├── protocol/                # 隧道二进制协议（编解码 + 消息类型定义）
│   ├── httputil/                # HTTP 工具集（代理/SSE/Peer签名/子域名等）
│   ├── proxydial/               # SOCKS5/Shadowsocks 拨号器
│   ├── wsutil/                  # WebSocket 优先级队列写入器
│   ├── random/                  # 加密随机数生成
│   ├── password/                # bcrypt 密码哈希
│   ├── validation/              # 输入验证
│   └── ssoutil/                # SSO URL 构建
├── web/                         # Vue 3 + TS 前端 SPA
├── docs/                        # 项目文档
│   ├── 0-overview.md            # 项目总览与设计哲学
│   ├── 1-deployment-architecture.md  # 部署架构详解
│   ├── 2-access-paths.md        # 六种代理路径深度分析
│   ├── 3-tunnel-protocol.md     # 隧道协议规范
│   ├── 4-sso-security.md        # SSO 安全体系
│   ├── 5-peer-system.md         # Peer 对等通信系统
│   └── 6-feature-matrix.md      # 功能矩阵与业界对标
├── Makefile
├── go.mod
└── go.sum
```

## 文档索引

| 文档 | 内容 |
|------|------|
| [项目总览与设计哲学](docs/0-overview.md) | 定位、解决的问题、核心设计理念、设计决策原因 |
| [部署架构与组件职责](docs/1-deployment-architecture.md) | 三大组件详细职责、单端口路由机制、Nginx 配置 |
| [六种请求代理路径](docs/2-access-paths.md) | 每种路径的完整数据流图、触发条件、代码定位 |
| [隧道协议与通信机制](docs/3-tunnel-protocol.md) | 二进制协议格式、消息类型、流式传输、心跳/重连/踢出 |
| [SSO 认证与安全体系](docs/4-sso-security.md) | 双层认证架构、Session 缓存、审计日志、纵深防御 |
| [Peer 对等通信系统](docs/5-peer-system.md) | 隧道建立流程、密钥协调、双模式代理、安全管理 |
| [功能矩阵与业界对标](docs/6-feature-matrix.md) | 功能属性分类、定位象限图、Cloudflare/开源替代映射 |

## 许可证

MIT
