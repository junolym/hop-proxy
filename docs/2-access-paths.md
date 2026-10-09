# 2 - 多路径请求代理机制（六种代理路径深度分析）

## 概述

HopProxy 的核心竞争力在于：**根据网络拓扑、应用配置和请求来源，自动选择最优的代理路径**。本文档逐一剖析每种路径的完整数据流、触发条件、关键代码位置和设计意图。

所有隧道内传输均基于 **yamux 流模型**：每个请求独占一条 yamux 流，流首字节为 `StreamType`（HTTP=0x01 / WS=0x02 / Ctrl=0x03），负载为 gob 编码。详见 [3 - 隧道协议与通信机制](3-tunnel-protocol.md)。

## 路径总览

```
                        请求到达
                           │
              ┌────────────┼────────────────────┐
              ▼            ▼                    ▼
        服务端接收     客户端本地代理接收      (其他)
        (Nginx转入)    (DNS指向内网)
              │            │
              │       ┌────┴────┐
              │       │         │
              │    子域名命中  未命中
              │       │         │
              │       ▼         ▼
              │   本地/Peer   forwardToServer()
              │       │         │
              │       └────┬────┘
              │            │
              ▼            ▼
        selectClient() ◄──┘
              │
        ┌─────┼──────────────────┐
        ▼     ▼                  ▼
   __host__  在线客户端      Peer配置
        │       │                  │
        ▼       ▼                  ▼
   路径一   路径二/六           路径四(本地)
                                   │
                            隧道客户端收到 Peer 配置
                                   │
                                   ▼
                              路径六(隧道)
```

---

## 路径一：服务端本地反向代理 (Server-Side Local Proxy)

### 触发条件

应用在 `app_clients` 表中关联的 `client_id` 为特殊值 `"__host__"` 时触发。

**含义**：目标服务运行在 HopProxy Server 所在的同一台机器上。

### 数据流

```
用户浏览器
  │
  │ GET https://myapp.proxy.example.com/api/data
  │ Host: myapp.proxy.example.com
  ▼
Nginx (SSL 终结)
  │ proxy_pass http://127.0.0.1:8080
  │ proxy_set_header Host $host  ← 保留原始域名
  ▼
HopProxy Server (:8080)
  │
  ├─ main.go → isAdminRequest()? NO (Host 是 *.proxy.domain, 不是 admin_domain)
  │
  ├─ Proxy.ServeHTTP()  [proxy/proxy.go]
  │   ├─ ExtractSubdomain("myapp.proxy.example.com") → "myapp"
  │   ├─ GetAppClientsWithTarget("myapp")
  │   │   → app{ID:1}, clients=[{ClientID:"__host__", TargetURL:"http://localhost:3000"}]
  │   │
  │   ├─ CheckSSOAuth(w, r, db, app)  [proxy/sso.go]
  │   │   → 检查 hopproxy_sso_1 Cookie → 有效 ✓
  │   │
  │   ├─ selectClient(appID, loadBalance, clients)  [proxy/selector.go]
  │   │   → 返回 {ClientID:"__host__", TargetURL:"http://localhost:3000"}
  │   │
  │   ├─ selection.ClientID == HostClientID ("__host__")? YES  [proxy/proxy.go]
  │   │
  │   ├─ IsWebSocketUpgrade(r)? NO
  │   │   → proxyLocalHTTP(w, r, targetURL, customHeaders, ...)  [proxy/local.go]
  │   │       → httputil.DoProxyRequest(targetURL + r)
  │   │       → 直接 http.Get("http://localhost:3000/api/data")
  │   │       → 复制响应头和 Body → 写回 w
  │   │
  │   └─ 响应返回给用户
  │
  ▼
响应写回用户浏览器
```

### 关键代码位置

| 步骤 | 文件 | 说明 |
|------|------|------|
| 特殊常量定义 | `proxy/proxy.go` | `HostClientID = "__host__"` |
| 判断逻辑 | `proxy/proxy.go` | `selection.ClientID == HostClientID` |
| 本地 HTTP 反代 | `proxy/local.go` | `httputil.DoProxyRequest()` 直接调用 |
| 本地 WS 反代 | `proxy/local_ws.go` | WebSocket 双向转发 |

### 支持的高级特性

即使不走隧道，本地反代也支持：

| 特性 | 实现方式 | 说明 |
|------|----------|------|
| Peer 代理转发 | `local.go` 中检测 `ProxyType=="peer"` | 本机也可以通过 Peer 代理到其他客户端 |
| SOCKS5/Shadowsocks | 通过 `proxydial` 包 | 支持链式代理 |
| SSE 流式响应 | `httputil.DoProxyRequest` 内置 | 逐块读取并写入，支持 keepalive 心跳 |
| gzip 透明解压 | 自动解压 gzip 响应体 | 防止 SSE 响应被压缩导致缓冲延迟 |
| 自定义 Header 合并 | `GetAppCustomHeaders()` | 应用级自定义头注入 |

### 设计意图

当目标服务与 Server 同机部署时，没有必要让请求走「Server→隧道→Client→Server 本地」这样的绕行路径。直接本地反代消除了：
- WebSocket 隧道的序列化/反序列化开销
- 网络往返延迟（即使在本机，WebSocket 也有协议开销）
- 隧道连接断开的风险点

典型使用场景：Server 上运行的监控面板（Grafana）、CI 工具（Jenkins）、管理 API 网关等。

---

## 路径二：隧道代理 (Tunnel Proxy) — 经典内网穿透模式

### 触发条件

公网用户通过子域名访问，且应用的关联客户端为普通远程客户端（非 `__host__`）时触发。

**这是 HopProxy 最核心、最常用的代理路径。**

### 数据流 — HTTP 请求

```
用户浏览器
  │
  │ GET https://api-app.proxy.example.com/users
  │ Host: api-app.proxy.example.com
  ▼
Nginx → Server (:8080)
  │
  ├─ isAdminRequest()? NO
  ├─ Proxy.ServeHTTP()
  │   ├─ subdomain = "api-app"
  │   ├─ GetAppClientsWithTarget("api-app")
  │   │   → app{ID:5, AuthMethod:"sso"}, clients=[{ClientID:"client-aaa", TargetURL:"http://10.0.1.50:8080"}]
  │   │
  │   ├─ CheckSSOAuth() → SSO Cookie 有效 ✓
  │   ├─ selectClient() → {ClientID:"client-aaa", TargetURL:"http://10.0.1.50:8080"}
  │   │
  │   ├─ ClientID != "__host__" + 非 WebSocket 升级
  │   │
  │   ├─ 预读 Body (支持 Failover 重试时复用)
  │   │
  │   └─ proxyHTTP(w, r, "client-aaa", targetURL, headers, body)  [proxy/http.go]
  │       │
  │       ├─ 打开一条 yamux 流:
  │       │   stream, _ := connGroup.OpenStream()  // 连接池内选 NumStreams 最少的连接
  │       │
  │       ├─ 写入流首字节 + gob 请求头:
  │       │   WriteStreamType(stream, StreamHTTP)
  │       │   WriteHTTPRequest(stream, &HTTPRequestHeader{
  │       │     Method: "GET", Path: "/users", Host: "api-app.proxy.example.com",
  │       │     Headers: {...}, Body: nil,
  │       │   })
  │       │
  │       └─ 在同一流上阻塞读取响应:
  │           ReadHTTPResponse(stream)  → 流内顺序保证 Response 先于 body 块
  │
  │  ◄══════════ yamux 流 (WebSocket 隧道内) ══════════► │
  │                                                      │
  ▼                                                     ▼
Server Conn                                           Client tunnel.Client
(OpenStream)                                        (AcceptStream 循环)
                                                          │
                                                   读取流首字节 0x01
                                                          │
                                                   → HTTPHandler
                                                          │
                                                   proxy.HandleHTTPRequest
                                                   [client/proxy/http.go]
                                                          │
                                                   ├─ ReadHTTPRequest → 构建 *http.Request
                                                   │   目标: http://10.0.1.50:8080/users
                                                   │
                                                   ├─ 发起真实 HTTP 请求
                                                   │
                                                   └─ 回写响应 (同一流):
                                                       ├─ WriteHTTPResponse{
                                                       │     Status: 200, Headers: {...},
                                                       │     Stream: true   ← 流式标记
                                                       │   }
                                                       └─ WriteHTTPResponseWithBody
                                                           流式拷贝 body 块到流上

  ◄══════════ yamux 流 (响应回传) ══════════► │
                                              │
Server proxyHTTP 收到响应                       │
  ├─ ReadHTTPResponse → 写状态码+头到浏览器       ▼
  └─ Stream=true: 从流上拷贝原始 body 块     用户
      逐块 Write (SSE/Chunked 原生 flush)    浏览器
                                             收到
                                            响应
```

### 数据流 — WebSocket 请求

WebSocket 代理的关键时序（Phase 01.4 修复）：**服务端先经隧道连接目标，用目标协商的 `ws_subprotocol` 完成对浏览器的 WebSocket 升级**——保证两端协商到同一个子协议（VSCode 等严格校验子协议的客户端不会白屏）。

```
用户浏览器 → Server (:8080)
  │ Upgrade: websocket, Connection: Upgrade
  ▼
Proxy.ServeHTTP()
  ├─ ... (子域名查找, SSO 认证, selectClient 同上)
  │
  ├─ IsWebSocketUpgrade(r)? YES
  │
  └─ proxyWebSocket(w, r, clientID, targetURL, ...)  [proxy/websocket.go]
      │
      ├─ 打开 yamux 流, 写入 StreamWS + gob WSMeta:
      │   stream, _ := connGroup.OpenStream()
      │   WriteStreamType(stream, StreamWS)
      │   WriteWSMeta(stream, &WSMeta{Path, Headers, Subprotocols: 浏览器协商列表})
      │
      │  ◄════ 隧道 ════► │
      │                    ▼
      │            Client HandleWSOpen
      │            [client/proxy/websocket.go]
      │              ├─ websocket.Dial(目标, 携带浏览器子协议列表)
      │              └─ 回写 WSDialResult (1B 状态 + gob, 含目标最终协商的 ws_subprotocol)
      │
      │  ◄════ 隧道 (WSDialResult) ════► │
      │                    │
      │  读取 WSDialResult:               │
      │  ├─ 成功(101) → 提取目标协商的 ws_subprotocol
      │  │
      │  └─ websocket.Accept(w, r, Subprotocols: [ws_subprotocol])
      │      → 用目标协商的子协议升级浏览器, 两端一致
      │      → 预注册 handler + buffer 防早期数据丢失
      │
      │       ══════ 全双工数据转发 (BridgeWS) ════════
      │       [1B frameType][4B len][payload] 帧, 保留 Text/Binary 类型
      │  用户 → Server ──流──> Client ──> 目标 WS
      │  目标 WS → Client ──流──> Server ──> 用户
      │  任一方关闭 → 对端关闭对应连接
```

### 关键代码位置

| 步骤 | 文件 | 说明 |
|------|------|------|
| 隧道 HTTP 代理入口 | `internal/server/proxy/http.go` | `proxyHTTP` — OpenStream + gob 请求/响应 |
| 隧道 WS 代理入口 | `internal/server/proxy/websocket.go` | `proxyWebSocket` — 先连目标、后升级浏览器（见下方时序） |
| 客户端处理 HTTP 请求 | `internal/client/proxy/http.go` | `HandleHTTPRequest` |
| 客户端处理 WS 打开 | `internal/client/proxy/websocket.go` | `HandleWSOpen` — dial 目标 + `BridgeWS` |
| 隧道连接 (服务端) | `internal/server/tunnel/conn.go` | `Conn.OpenStream` / `Run` (AcceptStream) |
| 隧道连接 (客户端) | `internal/client/tunnel/tunnel.go` | 连接池 + AcceptStream 循环 |
| 流编解码 | `pkg/tunnel/` | `WriteHTTPRequest` / `ReadHTTPResponse` / `BridgeWS` |

### 流式传输机制详解

所有 HTTP 响应（无论大小）都采用**统一的流式模式**：

```go
// 客户端侧 [client/proxy/http.go]
// 响应以 HTTPResponseHeader{Stream: true} 开始
WriteHTTPResponse(stream, &HTTPResponseHeader{Status: 200, Headers: {...}, Stream: true})

// 然后 WriteHTTPResponseWithBody 把 body 分块拷到同一流上
// (流式读取 → 写块 → 流式读取 → 写块 ... 直到 EOF)
```

**为什么全部用流式？**

1. **SSE (Server-Sent Events)**: SSE 要求服务端持续推送数据，必须边读边写。非流式模式下会等整个响应读完才发回，SSE 的实时性完全丧失。
2. **Chunked Transfer**: 很多 Go HTTP handler 使用 `http.Flusher` 逐步输出，流式模式可以逐 flush 转发。
3. **大文件**: 50MB 限制内的文件不需要一次性加载到内存。
4. **统一实现**: 不需要判断「这个响应是否需要流式」，全部走流式代码路径，减少分支复杂度。

**有序性保证**: 请求与响应在同一条 yamux 流上顺序读写——`HTTPResponseHeader` 永远先于 body 块到达，无需额外的派发循环或暂存队列。

---

## 路径三：客户端本地代理 (Client Local Access)

### 触发条件

客户端以 `--listen :<port>` 启动，并且内网 DNS 将 `*.proxy_domain` 解析到该客户端 IP 地址。

**前提**：需要在内网 DNS 服务器（或各机器的 hosts 文件）中配置通配符解析。

### 数据流

```
内网用户浏览器
  │
  │ GET http://dashboard.proxy.example.com/status
  │ Host: dashboard.proxy.example.com
  │ (DNS 解析: *.proxy.example.com → 192.168.1.10 即客户端 A 的 IP)
  ▼
客户端 A LocalProxy (:8080)  [localproxy/proxy.go]
  │
  ├─ ServeHTTP(w, r)
  │
  ├─ 路径优先级判断:
  │   │
  │   ├─ r.URL.Path == "/ws-peer"?
  │   │   → handleWSPeer() → 作为 Peer 隧道的服务端
  │   │   (见路径四 / Peer 系统)
  │   │
  │   └─ 以上都不是 → 标准本地代理流程:
  │       │
  │       ├─ ExtractSubdomain("dashboard.proxy.example.com") → "dashboard"
  │       │
  │       ├─ appManager.GetApp("dashboard")
  │       │   → AppInfo{TargetURL:"http://localhost:9090", AuthMethod:"sso", ...}
  │       │   (从本地缓存获取, 缓存由 AppManager 定期同步)
  │       │
  │       ├─ ★ checkAuth(w, r, appInfo)  [localproxy/auth.go]
  │       │   │
  │       │   ├─ SSO Cookie 存在?
  │       │   │   → 检查本地 Session 缓存 (session.go, TTL 1分钟)
  │       │   │   ├─ 缓存命中且有效 → ✓ 通过
  │       │   │   ├─ 缓存命中但过期 + 隧道在线 → 远程验证 (verify_session 控制流)
  │       │   │   └─ 缓存命中但过期 + 隧道离线 → ✓ 通过 (离线容灾: 已验证过的 session 继续有效)
  │       │   │
  │       │   ├─ Access Token? (Bearer / Basic)
  │       │   │   → 验证 token 有效性
  │       │   │
  │       │   └─ 都没有?
  │       │       → 302 重定向到 https://admin_domain/sso?redirect=<当前URL>
  │       │
  │       ├─ 非 WebSocket:
  │       │   → proxyLocalHTTP(w, r, appInfo)  [localproxy/local_proxy.go]
  │       │       → httputil.ReverseProxy(appInfo.TargetURL) 直接反代
  │       │       → 响应写回用户
  │       │
  │       └─ WebSocket:
  │           → proxyLocalWebSocket(w, r, appInfo)
  │               → websocket.Accept() → 连接目标 WS → 双向转发
  │
  ▼
响应返回给内网用户 (零额外跳转, 最低延迟)
```

### 本地未命中时的透明转发

```
  ├─ appManager.GetApp("unknown-app") → nil (未找到)
  │
  └─ forwardToServer(w, r)  [localproxy/forward.go]
      │
      ├─ 在主隧道上打开一条 yamux 流 (StreamHTTP/StreamWS):
      │   stream, _ := client.OpenStream()
      │   WriteHTTPRequest(stream, &HTTPRequestHeader{
      │     Method: r.Method, Path: r.URL.Path, Host: r.Host,
      │     Headers: r.Header (含 X-Forwarded-Proto),
      │     Body: r.Body,
      │   })   // 不含 TargetURL, 由服务端根据子域名决定
      │
      ├─ 服务端 HandleForwardedRequest()  [proxy/forward.go]
      │   → 和「路径二」完全相同的处理:
      │     子域名解析 → SSO → selectClient() → 隧道到目标客户端
      │
      └─ 响应沿原路返回:
          server → 主隧道流 → 客户端A → 用户 (完全无感知)
```

**设计意图**：内网可能有多个客户端分别运行不同的服务，每个客户端只缓存与自己关联的应用配置。当用户访问一个不在当前客户端上的应用时，自动回退到服务端中转——对用户来说，URL 不变、行为不变、只是延迟稍高一些。

### SSO 本地缓存的特殊考量

**文件**: `internal/client/localproxy/session.go`

```
Session 缓存策略:

缓存结构: map[sessionToken]*SessionCacheEntry
         ├── UserID
         ├── AppID
         ├── ExpiresAt (原始过期时间)
         └── CachedAt   (缓存写入时间)

TTL: 1 分钟 (硬编码)

验证流程:
  1. 从 Cookie 提取 session token
  2. 查本地缓存:
     ├─ 命中 + 未过期 → 直接通过 (零网络开销)
     ├─ 命中 + 已过期 + 隧道在线 → 发送 verify_session 控制流到服务端验证
     │   └─ 有效则更新缓存, 无效则删除
     └─ 未命中 + 隧道在线 → 发送 verify_session
```

**离线容灾机制**：隧道离线时，已缓存的 Session 即使超过 TTL 也视为有效——如果服务端不可达，应该尽量保持可用性而非拒绝访问。安全影响有限：缓存的 Session 本身是之前验证通过的。

---

## 路径四：Peer 对等代理 (Client-to-Client via Peer Tunnel) — 本地发起

### 触发条件

1. 客户端 A 以 `--listen` 启动本地代理
2. 用户从客户端 A 的本地代理发起请求
3. 请求的子域名命中本地应用配置
4. 该应用的 `ProxyType == "peer"`

### 数据流 — Peer 隧道建立阶段

```
                    密钥协调 (启动时一次性)
                    ═══════════════════════

  客户端 B 启动:
  ├─ generatePeerSecret() → 32字节随机数 secret_B
  ├─ 连接服务端成功后:
  │   └─ reportProxyStatus({PeerSecret: secret_B, ListenAddress: ":8081"})
  │       → 服务端存入 clients.peer_secret
  │
  服务端 → 客户端 A (apps_changed / get_apps):
  └─ GetClientApps() 返回包含 peer_secret 的 ClientAppInfo


                    首次请求时按需建立隧道
                    ════════════════════════════

  客户端 A 收到请求 (subdomain="db-service"):
  ├─ appManager.GetApp("db-service")
  │   → AppInfo{ProxyType:"peer", ProxyAddress:"192.168.1.11:8081",
  │             TargetClientID:"client-b", PeerSecret:secret_B}
  │
  ├─ PeerManager.GetTunnel(
  │     "client-b",           // 目标客户端 ID
  │     "192.168.1.11:8081",  // Peer 监听地址
  │     secret_B,             // B 的 Peer 密钥
  │     "client-a"            // 自己的客户端 ID
  │   )
  │   │
  │   ├─ 缓存中有已建立的隧道?
  │   │   → YES → 直接返回
  │   │   → NO → 建立新隧道:
  │   │       │
  │   │       ├─ websocket.Dial("ws://192.168.1.11:8081/ws-peer")
  │   │       │
  │   │       ├─ 发送原始 gob 认证消息 (yamux 升级之前):
  │   │       │   peer_auth{
  │   │       │     ClientID: "client-a",
  │   │       │     Timestamp: unix_now,
  │   │       │     Signature: HMAC-SHA256(secret_B, "client-a:timestamp"),
  │   │       │   }
  │   │       │
  │   │       ├─ 等待 peer_auth_resp {Success: true/false} (超时则失败)
  │   │       │   ├─ false → 认证失败, 返回错误
  │   │       │   └─ true  → 升级为 yamux 会话:
  │   │       │       NewClientSessionFromWS(ws)  [pkg/tunnel/session.go]
  │   │       │
  │   │       └─ 进入 AcceptStream 循环, 按 StreamType 分发请求
  │   │
  │   └─ 返回可用的 PeerTunnel 实例
```

### 数据流 — HTTP 请求通过 Peer 隧道

```
用户 → 客户端A LocalProxy (:8080)
  │ GET http://db-service.proxy.example.com/query
  ▼
ServeHTTP() → 子域名 "db-service" 命中
  │
  ├─ checkAuth() → SSO 通过 ✓
  │
  ├─ app.ProxyType == "peer"?
  │   → proxyViaPeerTunnel(w, r, appInfo)  [localproxy/peer_client.go]
  │       │
  │       ├─ 打开 peer yamux 流:
  │       │   stream, _ := peerTunnel.OpenStream()
  │       │   WriteStreamType(stream, StreamHTTP)
  │       │   WriteHTTPRequest(stream, &HTTPRequestHeader{
  │       │     Method: "GET", Path: "/query", Host: "db-service.proxy.example.com",
  │       │     Headers: {...}, Body: nil,
  │       │   })
  │       │
  │       │  ◄════ peer yamux 流 (内网直连) ════► │
  │       │                                      │
  │       ▼                                     ▼
  │   客户端B /ws-peer 端点                    AcceptStream 循环
  │   handlePeerStream → handlePeerHTTPStream    │
  │   [localproxy/peer_handler.go]               │
  │       ├─ ReadHTTPRequest → 构建 *http.Request │
  │       ├─ 向目标服务发起真实请求                │
  │       └─ 流式回写响应 (Stream=true + body 块) │
  │                                              │
  │  ◄════ peer yamux 流 (响应) ════► │
  │                                    │
  │  客户端A 收到响应序列                 ▼
  │  → WriteHeader + 分块 Write      用户
  │                                  浏览器
  ▼
用户看到查询结果
```

### 数据流 — WebSocket 请求通过 Peer 隧道

```
用户 → 客户端A LocalProxy
  │ WS 升级请求: /ws/realtime
  ▼
IsWebSocketUpgrade? YES
  │
  └─ proxyLocalWebSocketViaPeer(w, r, appInfo)
      [localproxy/peer_client.go]
      │
      ├─ 打开 peer yamux 流 (StreamWS + WSMeta)
      │   → 客户端B handlePeerWSStream 连接目标 WS, 回写 WSDialResult
      │
      │  ◄════ peer yamux 流 ════► │
      │  (先连目标, 以目标协商的 ws_subprotocol 升级浏览器)
      │
      │       ═════ 全双工 WS 数据转发 (BridgeWS) ═════
      │  用户→A→[StreamWS 流]→B→目标WS
      │  目标WS→B→[StreamWS 流]→A→用户
      │
      ▼
```

### 关键代码位置

| 组件 | 文件 | 说明 |
|------|------|------|
| Peer 请求发起 (Path 4) | `localproxy/peer_client.go` | `proxyViaPeerTunnel` / `proxyLocalWebSocketViaPeer` |
| Peer 服务端 (/ws-peer) | `localproxy/peer_handler.go` | `handleWSPeer` — gob 认证 + yamux 升级 + 流分发 |
| Peer 隧道管理 | `localproxy/peer_manager.go` / `peer_tunnel.go` | 懒创建 + 复用 + 自动重连 |
| Peer 签名工具 | `pkg/httputil/peer.go` | `HMACSHA256` + `ComputePeerSignature` |
| yamux 升级 | `pkg/tunnel/session.go` | `NewClientSessionFromWS` / `NewServerSessionFromWS` |

---

## 路径五：客户端到服务端透明中转 (Client → Server Relay)

### 触发条件

客户端本地代理 (`--listen`) 收到请求，但子域名未匹配任何本地应用配置时自动触发。

这是**路径三的降级方案**，属于系统自动行为，无需额外配置。

### 数据流

已在「路径三」的「本地未命中时的透明转发」章节详述。补充几个设计要点：

### 设计意图

1. **内网多客户端场景**：假设有 3 个内网客户端（A/B/C），各自运行不同服务。内网用户的 DNS 可能将 `*.proxy.domain` 指向客户端 A。当用户想访问运行在 C 上的服务时：
   - 请求到达 A → A 本地没有该应用 → `forwardToServer()` → Server → 隧道到 C → 目标
   - 用户全程使用同一个 URL，不需要知道服务跑在哪台机器上

2. **部分服务迁移**：某个服务从 A 迁移到 B 后，A 上的本地缓存可能暂时还有旧配置。如果旧配置被清除但新配置还没同步过来，请求会自动 fallback 到服务端——保证可用性优于性能。

### 与路径二的关系

| 维度 | 路径二 (Server→Tunnel→Client) | 路径五 (Client→Server Relay) |
|------|-------------------------------|------------------------------|
| 入口 | 服务端 Proxy.ServeHTTP() | 客户端 LocalProxy.ServeHTTP() |
| 来源 | 公网用户 (经 Nginx) | 内网用户 (经 DNS) |
| 请求写入方 | 服务端 | 客户端 |
| 流构建差异 | 含 TargetURL 相关信息 | **不含** (由服务端查库决定) |
| SSO 执行方 | 服务端 | 服务端 (客户端不执行) |
| 最终路径相同 | ✅ | ✅ (最终都是 Server → Tunnel → 目标 Client) |

### 流式响应的中转

客户端收到服务端的流式响应后，逐块写入原始 ResponseWriter。由于请求/响应都在同一条 yamux 流上顺序读写，中转过程完全透明——包括 SSE 的实时推送效果都能保留。

---

## 路径六：隧道+Peer 组合代理 (Tunnel → Client A → Peer → Client B)

### 触发条件

1. **公网用户**发起请求（进入路径二的流程）
2. `selectClient()` 选中的客户端 (如 Client A) 的 `app_clients` 记录中包含 Peer 配置
3. 即：`ProxyType="peer"`, `ProxyAddress="B-host:port"`, `PeerSecret=B's_secret`

### 适用场景

目标服务运行在客户端 B 上，但从公网无法直接连接 B（B 在内网）。应用关联的是客户端 A（可能是作为网关角色），而实际目标在 B。

### 数据流

```
公网用户
  │ GET https://cross-app.proxy.example.com/data
  ▼
Nginx → Server (:8080)
  │
  ├─ Proxy.ServeHTTP() → SSO → selectClient()
  │   → clients=[{ClientID:"client-a", ProxyType:"peer",
  │               ProxyAddress:"192.168.1.11:8081", PeerSecret:secret_B}]
  │   → selection = {ClientID:"client-a", ProxyType:"peer", ...}
  │
  ├─ ClientID != "__host__" + 非 WS 升级
  │
  └─ proxyHTTP(w, r, "client-a", ..., proxyType="peer", ...)
      │
      ├─ 主隧道上开流, 写入 gob 请求头并携带 Peer 配置:
      │   WriteHTTPRequest(stream, &HTTPRequestHeader{
      │     Method: "GET", Path: "/data", Host: "cross-app.proxy.example.com",
      │     Headers: {
      │       proxy_type:    "peer",        ← ★ 关键: 携带 Peer 配置
      │       proxy_address: "192.168.1.11:8081",
      │       proxy_secret:  secret_B,
      │     },
      │   })
      │
      │  ◄════ 主隧道 yamux 流 ════► │
      │                                ▼
      │                        客户端A AcceptStream → HTTPHandler
      │                        [client/proxy/http.go]
      │                                │
      │                        ├─ 发现请求头 proxy_type == "peer"
      │                        │   && peerSender != nil
      │                        │
      │                        └─ 经 peer yamux 隧道转发给客户端 B:
      │                            peerTunnel.OpenStream()
      │                            → WriteHTTPRequest (透传)
      │                                │
      │                            │  ◄═══ peer yamux 流 (内网) ═══► │
      │                            │                                 ▼
      │                            │                          客户端B /ws-peer
      │                            │                          handlePeerHTTPStream()
      │                            │                          → 请求目标 → 流式响应回传
      │                            │
      │                            │  ◄═══ peer yamux 流 (响应) ═══► │
      │                            │                                  │
      │                            ▼                                 │
      │                        客户端A 收到响应                        │
      │                        → 通过主隧道流回传给服务端              │
      │                                                              │
      │  ◄════ 主隧道 (最终响应) ════► │                             │
      │                                ▼                             ▼
      │                          Server 写回用户                    用户
      │                                                        看到结果
  ▼
```

### 关键代码位置

| 步骤 | 文件 | 说明 |
|------|------|------|
| 服务端注入 Peer 配置 | `internal/server/proxy/http.go` | 将 proxy_type/address/secret 编入请求头 |
| 客户端 Peer Sender 设置 | `cmd/client/main.go` | `localProxy.SendViaPeerTunnel` 回调注册 |
| 客户端判断 Peer 类型 | `internal/client/proxy/http.go` | 请求头 `proxy_type == "peer"` |
| 客户端 Peer 发送 | `internal/client/localproxy/peer_client.go` | 经 peer yamux 流转发 |
| WS 版本的 Peer 转发 | `internal/client/proxy/websocket.go` + `peer_client.go` | WS 请求同理 |

### 设计意图

这种组合模式允许灵活的网络拓扑：

```
公网 → Server → Client A (网关/DMZ角色)
                     ├─ 本地服务 → 直接处理
                     ├─ Client B (生产内网) → Peer 直连
                     └─ Client C (测试环境) → Peer 直连
```

Client A 可以是一个暴露在半可信网络中的客户端（如 DMZ 区），它不需要知道每个服务的具体地址，只需要知道 Peer 客户端是谁。实际的业务服务完全隐藏在更深层的内网客户端中。

---

## 六种路径对比总结

| # | 路径名称 | 入口点 | 目标选择 | 是否经过隧道 | 延迟 | 典型适用场景 |
|---|---------|--------|---------|:-----------:|:----:|-------------|
| 一 | 服务端本地反代 | `proxy/local.go` | `__host__` 特殊值 | **否** | ★★★★★ | 目标服务与 Server 同机 |
| 二 | 隧道代理 | `proxy/http.go` | `selectClient()` | **主隧道** | ★★★☆☆ | 公网访问内网服务 (**主要模式**) |
| 三 | 客户端本地代理 | `localproxy/proxy.go` | `appManager` 本地缓存 | **否** | ★★★★★ | 内网用户访问同客户端上的服务 |
| 四 | Peer 代理 (本地发起) | `localproxy/peer_client.go` | `PeerManager` | **Peer 隧道** | ★★★★☆ | 内网用户跨客户端访问 |
| 五 | 服务端中转 (本地回退) | `localproxy/forward.go` | 服务端 `selectClient()` | **主隧道** | ★★☆☆☆ | 本地无此应用时的透明降级 |
| 六 | 隧道+Peer 组合 | `proxy/http.go` → `client/proxy/http.go` | `selectClient(A)` → PeerSender | **主隧道+Peer** | ★★★☆☆ | 公网访问跨客户端的目标服务 |

### 选择决策树

```
请求到达
  │
  ├─ 来自 Nginx (公网)?
  │   └─ → 路径一 (__host__) / 路径二 (隧道) / 路径六 (隧道+Peer)
  │
  └─ 来源于本地监听 (内网)?
      │
      ├─ /ws-peer 端点?
      │   └─ → Peer 隧道服务端 (被动接受, gob 认证 + yamux 升级)
      │
      ├─ 子域名命中本地应用?
      │   ├─ ProxyType == "peer"? → 路径四 (Peer 代理)
      │   └─ 其他? → 路径三 (本地直接反代)
      │
      └─ 子域名未命中?
          └─ → 路径五 (forwardToServer 透明中转)
```
