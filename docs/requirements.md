# HopProxy 项目需求文档

## 1. 项目概述

HopProxy 是一个自建的内网穿透与反向代理系统。通过在远程服务器上运行服务端、在内网服务器上运行客户端，实现将内网 HTTP 服务暴露到公网子域名访问的能力。

### 核心场景

用户拥有运行在内网（无公网 IP）的 HTTP 服务，希望通过公网子域名访问这些服务。

### 数据流

```
外部用户浏览器
    │
    │ HTTPS (*.example.com)
    ▼
外部反向代理 (Nginx/Caddy, 非本项目范围)
    │
    │ HTTP
    ▼
HopProxy 服务端 (根据 Host 头路由)
    │
    │ WebSocket 隧道
    ▼
HopProxy 客户端 (运行在内网)
    │
    │ HTTP
    ▼
内网目标服务 (如 http://localhost:3000)
```

## 2. 系统架构

### 2.1 组件

| 组件 | 说明 | 运行环境 |
|------|------|----------|
| 服务端 (server) | HTTP 服务 + 管理后台 API + WebSocket 隧道端点 + 子域名请求路由 | 公网 Linux 服务器 |
| 客户端 (client) | 连接服务端 WebSocket、代理本地 HTTP 请求 | 内网 Linux 服务器 |
| 管理前端 (web) | 管理页面 SPA，管理客户端和应用 | 浏览器中运行，由服务端托管静态文件 |

### 2.2 连接模型

- **一对多**：一个服务端可同时连接多个客户端
- 每个客户端拥有一个 UUID，既是客户端 ID 也是连接凭证
- **同一 clientID 可多连接并存（连接池）**：客户端默认维持 4 条 WS+yamux 连接（`HP_POOL_SIZE` 可调，设 1 退化为单连接），服务端 Hub 以 ConnGroup 管理，`OpenStream` 在组内选路，不再互踢
- 管理员可主动踢出客户端（管理动作，作用于整个连接组）；被踢客户端关闭连接池退出、不自动重连

### 2.3 端口与域名规划

服务端监听**单个 HTTP 端口**，同时承载：

- **管理后台**：通过管理域名访问（如 `proxy.example.com`）
- **管理 API**：管理域名下的 `/api/` 路径
- **WebSocket 隧道**：管理域名下的 `/ws/` 路径，供客户端连接
- **代理请求**：通过子域名访问（如 `app1.proxy.example.com`），根据 Host 头路由到对应客户端

服务端自行解析 HTTP 请求的 Host 头来区分管理流量和代理流量，不依赖外部反向代理做子域名分发。外部反向代理（Nginx 等）只负责 TLS 终结和通配符域名转发。

## 3. 功能需求

### 3.1 首次引导配置

服务端首次访问时（数据库为空），自动进入引导配置流程：

#### 3.1.1 引导步骤

1. **创建管理员账户**：第一个注册的用户自动成为管理员
   - 输入用户名、密码
   - 后续不再开放注册（单用户系统，后续版本可扩展多用户）

2. **站点设置**：
   - 管理域名（如 `proxy.example.com`，用于管理后台访问）
   - 代理域名（如 `proxy.example.com`，子域名的上级域名，即 `*.proxy.example.com` 的请求会被路由）
   - 站点名称（显示在管理页面标题）

#### 3.1.2 引导完成后

- 站点设置写入数据库，后续可在管理页面「站点设置」中修改
- 跳转到登录页面

### 3.2 站点设置（管理页面可修改）

| 设置项 | 说明 | 示例 |
|--------|------|------|
| 站点名称 | 显示在页面标题和导航栏 | `My Proxy` |
| 管理域名 | 管理后台的访问域名 | `proxy.example.com` |
| 代理域名 | 子域名的上级域名 | `proxy.example.com` |
| JWT 密钥 | 自动生成，支持手动重置（重置后所有会话失效） | - |
| 会话有效期 | JWT Token 过期时间 | `24h` |

> 注意：管理域名和代理域名可以相同（通过 Host 头中是否包含子域名前缀来区分），也可以不同（如管理用 `admin.example.com`，代理用 `*.tunnel.example.com`）。

### 3.3 用户认证

- 服务端管理页面需要用户名 + 密码登录
- 初期单用户（管理员），由首次引导流程创建
- 登录后颁发 JWT Token，前端通过 Bearer Token 调用 API
- 会话有效期可配置，默认 24 小时
- 支持修改密码

### 3.4 客户端管理

#### 3.4.1 添加客户端

管理页面可添加客户端，需要填写：

| 字段 | 说明 | 要求 |
|------|------|------|
| 名称 | 客户端备注名 | 必填，任意字符串 |

创建后系统自动生成：
- 客户端 UUID（同时作为连接凭证，不再使用单独的 Token）
- 显示完整的连接命令，方便复制

#### 3.4.2 客户端 ID 与连接凭证

- 客户端 ID 使用 UUID v4
- **UUID 即凭证**：客户端直接使用此 UUID 连接服务端，不需要额外的 Token
- 提供有效的 UUID 即可建立连接
- 支持重新生成 UUID（旧 UUID 立即失效，已连接的客户端会被断开）

#### 3.4.3 连接模型（连接池）

- 同一客户端 UUID 可同时维持**多条 WebSocket 连接**（连接池，默认 4 条，`HP_POOL_SIZE` 可调，设 1 退化为单连接）
- 服务端以 ConnGroup 形式注册同一 clientID 的多条连接，`OpenStream` 在组内轮询选路
- 空闲连接按随机 5–10 分钟 TTL 淘汰（先建新后 drain 旧，淘汰时发送 `conn_draining` 控制流通知服务端）
- 管理员可主动踢出客户端（作用于整个连接组）；被踢客户端设置 kicked 标志后关闭连接池退出，不自动重连
- 正常断线（网络问题等非踢出场景）客户端按连接自动重连（指数退避 + 抖动）

#### 3.4.4 客户端列表

- 显示所有客户端及其状态（在线/离线）
- 显示每个客户端关联的应用数量
- 支持删除客户端（关联的应用不删除，但变为无客户端状态）
- 支持重新生成 UUID

#### 3.4.5 资源限制

- 每个用户最多 100 个客户端

### 3.5 应用管理（反向代理）

初期只支持一种应用类型：**HTTP 反向代理**。

#### 3.5.1 应用与客户端的关系

- **多对多**：一个应用关联一个客户端，但一个客户端可关联多个应用
- 创建应用时必须选择一个已有的客户端（需要先创建客户端才能创建应用）
- 应用可以修改关联的客户端（切换到其他客户端）
- 删除客户端时，其关联的应用保留但变为**未关联客户端**状态（不可用）

#### 3.5.2 添加应用

| 字段 | 说明 | 要求 |
|------|------|------|
| 名称 | 应用备注名 | 必填，任意字符串 |
| 子域名 | 用于外部访问的子域名前缀 | 必填，全局唯一，仅允许小写字母、数字和连字符 |
| 目标地址 | 客户端可访问的内网地址 | 必填，如 `http://localhost:3000` 或 `http://192.168.1.100:8080` |
| 关联客户端 | 通过哪个客户端连接 | 必填，从已有客户端中选择 |

#### 3.5.3 应用列表

- 显示所有应用及其关联的客户端
- 显示应用状态：
  - **可用**：关联了在线的客户端
  - **客户端离线**：关联了客户端但客户端不在线
  - **未关联客户端**：客户端被删除，需要重新关联
- 显示完整的外部访问地址
- 支持编辑（名称、子域名、目标地址、关联客户端）和删除

#### 3.5.4 资源限制

- 每个用户最多 100 个应用

#### 3.5.5 子域名路由

- 服务端收到请求时，解析 Host 头提取子域名前缀
- 根据子域名查找对应的应用配置
- 如找到，将请求通过 WebSocket 隧道转发给应用关联的客户端
- 客户端收到后，向目标地址发起实际 HTTP 请求，并将响应回传

### 3.6 请求代理流程

1. 外部用户访问 `https://app1.proxy.example.com/path`
2. 外部反向代理（Nginx）将请求转发到服务端
3. 服务端解析 Host 头，提取子域名 `app1`
4. 查找子域名 `app1` 对应的应用配置，找到关联的客户端和目标地址
5. 服务端通过 WebSocket 隧道将 HTTP 请求（方法、路径、头、Body）序列化后发送给客户端
6. 客户端反序列化请求，向目标地址（如 `http://localhost:3000/path`）发起 HTTP 请求
7. 客户端将目标服务的 HTTP 响应（状态码、头、Body）序列化后通过隧道回传给服务端
8. 服务端将响应返回给外部用户

### 3.7 WebSocket 代理支持

除了普通 HTTP 请求，还需支持 WebSocket 代理：

- 当外部用户通过子域名发起 WebSocket 升级请求时
- 服务端识别到 `Upgrade: websocket` 头
- 在隧道中建立一个双向数据流通道
- 实现外部用户 <-> 服务端 <-> 隧道 <-> 客户端 <-> 内网服务 的全双工 WebSocket 代理

## 4. 技术选型

| 领域 | 选型 | 说明 |
|------|------|------|
| 语言 | Go | 服务端和客户端分别编译为独立二进制 |
| HTTP 框架 | 标准库 `net/http` | Go 标准库已足够强大 |
| WebSocket | `github.com/coder/websocket` | 成熟的 Go WebSocket 库（隧道承载层） |
| 数据库 | SQLite | 轻量，单文件，通过 `modernc.org/sqlite`（纯 Go 实现，无 CGO 依赖） |
| ORM | 不使用 | 直接使用 `database/sql`，保持简单 |
| 前端框架 | Vue 3 + TypeScript | 前后端分离，构建后的静态文件嵌入到服务端二进制中 |
| 认证 | JWT | 管理页面登录认证 |
| 构建 | Go embed | 前端静态文件嵌入服务端二进制 |

## 5. 数据模型

### 5.1 用户表 (users)

| 字段 | 类型 | 说明 |
|------|------|------|
| id | INTEGER PK | 主键 |
| username | TEXT UNIQUE | 用户名 |
| password_hash | TEXT | 密码哈希 (bcrypt) |
| is_admin | BOOLEAN | 是否管理员 |
| created_at | DATETIME | 创建时间 |
| updated_at | DATETIME | 更新时间 |

### 5.2 站点设置表 (settings)

| 字段 | 类型 | 说明 |
|------|------|------|
| key | TEXT PK | 设置项键名 |
| value | TEXT | 设置项值 |
| updated_at | DATETIME | 更新时间 |

预置键名：
- `site_name`：站点名称
- `admin_domain`：管理域名
- `proxy_domain`：代理域名（子域名的上级域名）
- `jwt_secret`：JWT 签名密钥（首次引导自动生成）
- `session_ttl`：会话有效期（默认 `24h`）
- `initialized`：是否已完成初始化引导（`true`/`false`）

### 5.3 客户端表 (clients)

| 字段 | 类型 | 说明 |
|------|------|------|
| id | TEXT PK | 客户端 UUID（同时作为连接凭证） |
| user_id | INTEGER FK | 所属用户 |
| name | TEXT | 备注名 |
| created_at | DATETIME | 创建时间 |
| updated_at | DATETIME | 更新时间 |

> 注意：不再有单独的 `token` 字段，UUID 即凭证。

### 5.4 应用表 (apps)

| 字段 | 类型 | 说明 |
|------|------|------|
| id | INTEGER PK | 主键 |
| user_id | INTEGER FK | 所属用户 |
| client_id | TEXT FK NULL | 关联客户端（可为空，客户端被删除后为 NULL） |
| name | TEXT | 应用备注名 |
| subdomain | TEXT UNIQUE | 子域名前缀 |
| target_url | TEXT | 目标地址 |
| enabled | BOOLEAN | 是否启用 |
| created_at | DATETIME | 创建时间 |
| updated_at | DATETIME | 更新时间 |

## 6. API 设计

> **说明**：本文档为历史 MVP 需求文档。下列 6.1–6.6 为 MVP 期的核心端点；系统演进后实际路由远多于下表，**权威路由表以 `internal/server/api/router.go` 为准**。按当前代码（router.go）补充的常用端点见 6.7。

### 6.1 初始化引导

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| GET | `/api/setup/status` | 查询是否已初始化 | 无 |
| POST | `/api/setup/init` | 执行初始化（创建管理员 + 站点设置） | 无（仅未初始化时可用） |

### 6.2 认证

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| POST | `/api/auth/login` | 登录，返回 JWT | 无 |
| POST | `/api/auth/password` | 修改密码 | 需要 |
| GET | `/api/auth/me` | 获取当前用户信息 | 需要 |

### 6.3 站点设置

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| GET | `/api/settings` | 获取站点设置 | 需要 |
| PUT | `/api/settings` | 更新站点设置 | 需要（管理员） |

### 6.4 客户端管理

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| GET | `/api/clients` | 客户端列表（含在线状态） | 需要 |
| POST | `/api/clients` | 添加客户端 | 需要 |
| PUT | `/api/clients/:id` | 编辑客户端 | 需要 |
| DELETE | `/api/clients/:id` | 删除客户端 | 需要 |
| POST | `/api/clients/:id/regenerate` | 重新生成 UUID（返回新 UUID） | 需要 |

### 6.5 应用管理

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| GET | `/api/apps` | 应用列表 | 需要 |
| POST | `/api/apps` | 添加应用 | 需要 |
| PUT | `/api/apps/:id` | 编辑应用 | 需要 |
| DELETE | `/api/apps/:id` | 删除应用 | 需要 |

### 6.6 WebSocket 隧道

| 路径 | 说明 |
|------|------|
| `/ws/<client-uuid>` | 客户端连接隧道端点 |

### 6.7 实际路由补充（按 `internal/server/api/router.go`）

| 方法 | 路径 | 说明 | 认证 |
|------|------|------|------|
| GET | `/api/version` | 版本信息 | 无 |
| POST | `/api/auth/totp` | TOTP 二次验证 | 无 |
| GET | `/api/clients/available-peers` | 可用的 peer 客户端列表 | 登录 |
| GET | `/api/clients/{id}/ping` | 客户端连通性 ping | 登录 |
| GET | `/api/clients/{id}/conns` | 客户端连接详情（ConnGroup 各连接状态） | 登录 |
| GET/POST | `/api/clients/{id}/proxies` | 代理配置（SOCKS5/Shadowsocks/Peer）CRUD | 登录 |
| PUT/DELETE | `/api/proxies/{id}` | 代理配置更新/删除 | 登录 |
| GET/POST | `/api/apps/{id}/routes` | 应用路由规则 CRUD | 登录 |
| PUT/DELETE | `/api/routes/{rid}` | 路由规则更新/删除 | 登录 |
| GET/POST | `/api/apps/{id}/redirects` | 应用跳转规则 CRUD | 登录 |
| PUT/DELETE | `/api/redirects/{rid}` | 跳转规则更新/删除 | 登录 |
| GET/PUT | `/api/user-settings` | 当前用户偏好设置 | 登录 |
| GET | `/api/totp/status` / `/api/totp/setup` | TOTP 状态/启用前配置 | 登录 |
| POST | `/api/totp/enable` / `/api/totp/disable` | 启用/关闭 TOTP | 登录 |
| GET/POST/PUT/DELETE | `/api/access-tokens` | API 访问令牌 CRUD | 登录 |
| GET/POST/PUT | `/api/admin/users` | 多用户管理（管理员） | 管理员 |
| GET/PUT | `/api/settings` | 站点设置（更新需管理员） | 登录/管理员 |

## 7. 隧道协议设计

服务端与客户端之间通过 WebSocket 承载 **yamux 多路复用流** 进行通信。

### 7.1 传输模型

- WebSocket → `websocket.NetConn`（流式 `net.Conn`）→ `yamux.Session` → N 条独立流
- **每个 HTTP/WS 代理请求独占一条 yamux 流**，流内顺序读写、天然有序，窗口流控提供背压
- 每流首字节为 `StreamType`（HTTP=0x01 / WS=0x02 / Ctrl=0x03），负载为 gob 编码
- 控制消息经 `StreamCtrl` 流传输，Action 包括：`get_apps`/`apps_response`/`apps_changed`/`verify_session`/`verify_response`/`proxy_status`/`kick`/`conn_draining`/`diag_request`/`diag_response`
- 心跳由 yamux 内置 keepalive 承担（`KeepAliveInterval=30s`），RTT 经 `session.Ping()` 测量
- 旧的自定义"JSON 头 + 二进制 Body 帧"协议及其包已随迁移删除

> 详细协议说明见 [3 - 隧道协议与通信机制](3-tunnel-protocol.md)。

## 8. 配置

### 8.1 服务端配置

服务端采用**纯 Web 配置**方式，无配置文件。启动参数仅通过命令行 flag 或环境变量传入：

| 参数 | 环境变量 | 说明 | 默认值 |
|------|----------|------|--------|
| `--listen` | HP_LISTEN | 监听地址 | `:8080` |
| `--db` | HP_DB_PATH | SQLite 数据库路径 | `./data/hopproxy.db` |

所有业务配置（域名、JWT 密钥、会话有效期等）均在首次引导或管理页面中设置，存储在数据库中。

### 8.2 客户端配置

通过命令行参数或环境变量：

| 参数 | 环境变量 | 说明 |
|------|----------|------|
| `--server` | HP_SERVER | 服务端地址（如 `wss://proxy.example.com`） |
| `--id` | HP_CLIENT_ID | 客户端 UUID |
| `--pool-size` | HP_POOL_SIZE | 连接池大小（默认 4；设 1 退化为单连接） |
| `--reconnect-interval` | HP_RECONNECT_INTERVAL | 断线重连初始等待时间（默认 10s） |
| `--listen` | HP_LISTEN | 本地代理监听地址（可选，启用本地代理模式） |

客户端也可以直接用完整的连接 URL：
```bash
./hop-proxy-client --connect "wss://proxy.example.com/ws/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
```

## 9. 部署方式

### 9.1 服务端

```bash
# 独立二进制，包含前端静态文件，直接启动即可
./hop-proxy-server

# 可选指定监听地址和数据库路径
./hop-proxy-server --listen :9090 --db /var/lib/hopproxy/data.db
```

前面套 Nginx，配置通配符域名 `*.proxy.example.com` 和主域名 `proxy.example.com` 都转发到服务端端口。

### 9.2 客户端

```bash
# 独立二进制，轻量稳定，不频繁更新
./hop-proxy-client --connect "wss://proxy.example.com/ws/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
```

建议通过 systemd 管理，实现开机自启和自动重启。

客户端设计为**轻量、稳定、听从服务端指令**的模式：
- 客户端只负责执行隧道协议，不包含业务逻辑决策
- 应用配置（子域名、目标地址等）全部由服务端管理，客户端通过隧道协议接收指令
- 客户端迭代频率低，一旦隧道协议稳定后基本无需更新

## 10. 项目结构（规划）

服务端和客户端为**两个独立的二进制**（独立的 `main.go`），共享 `protocol` 包。

理由：客户端定位为轻量稳定的隧道执行器，隧道协议稳定后基本无需更新；服务端包含业务逻辑、管理页面等，迭代频繁。分开编译避免客户端因服务端更新而需要重新部署。

```
hop-proxy/
├── cmd/
│   ├── server/             # 服务端入口 main.go → 编译为 hop-proxy-server
│   └── client/             # 客户端入口 main.go → 编译为 hop-proxy-client
├── internal/
│   ├── server/             # 服务端核心逻辑（仅服务端引用）
│   │   ├── api/            # REST API 处理
│   │   ├── tunnel/         # WebSocket 隧道管理
│   │   ├── proxy/          # HTTP 请求代理
│   │   └── db/             # 数据库操作
│   └── client/             # 客户端核心逻辑（仅客户端引用）
│       ├── tunnel/         # WebSocket 隧道连接
│       └── proxy/          # 本地 HTTP 请求代理
├── pkg/
│   └── tunnel/            # 隧道协议定义（yamux 流 + StreamType + gob，服务端客户端共用）
├── web/                    # 前端项目（Vue 3 + TypeScript）
│   ├── src/
│   └── dist/               # 构建输出，嵌入到服务端二进制
├── docs/
│   └── requirements.md     # 本文档
├── go.mod
└── go.sum
```

## 11. 非功能性需求

- **性能**：单个服务端至少支持 100 个并发客户端连接
- **可靠性**：客户端正常断线自动重连（指数退避，连接池逐连接），被踢出则关闭连接池退出
- **安全性**：UUID 作为凭证使用 v4 随机生成（128 位随机）；管理 API 需 JWT 认证；密码 bcrypt 存储
- **可观测性**：结构化日志输出，包含关键操作的日志记录
- **跨平台**：服务端和客户端均支持 Linux（主要目标），Go 天然支持交叉编译
- **资源限制**：每用户最多 100 个客户端、100 个应用

## 12. 初期范围（MVP）

初期 MVP 聚焦核心功能：

- [x] 首次引导配置（管理员注册 + 站点设置）
- [x] 用户登录认证
- [x] 站点设置管理
- [x] 客户端管理（增删、UUID 生成、连接互斥）
- [x] 应用管理（HTTP 反向代理配置，关联客户端）
- [x] WebSocket 隧道建立和维护
- [x] HTTP 请求代理转发
- [x] WebSocket 代理转发
- [x] 管理页面（Vue 3 + TypeScript SPA）
- [ ] ~~TCP 端口映射~~（后续版本）
- [ ] ~~多用户支持~~（后续版本）
- [ ] ~~访问统计/监控面板~~（后续版本）
