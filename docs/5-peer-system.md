# 5 - Peer 对等通信系统

## 概述

Peer 系统是 HopProxy 中最精巧的子系统之一。它允许**客户端之间建立直接的 WebSocket 隧道**，实现内网跨机器的服务访问，而请求不经过公网服务端。

### 解决的问题

在多机器的内网环境中，服务往往分布在不同的物理机或虚拟机上：

```
机器 A (192.168.1.10): 运行前端应用 + HopProxy 客户端 A
机器 B (192.168.1.11): 运行数据库管理面板 (端口 5000) + HopProxy 客户端 B
```

当机器 A 上的用户需要访问机器 B 上的服务时，传统方案：
- **直接访问**：需要知道 `192.168.1.11:5000`，暴露了内网拓扑
- **经服务端中转**：A → 公网 Server → 隧道 → B，延迟高且浪费带宽

Peer 方案：A 通过 Peer 隧道直连 B（走内网），使用统一子域名 `db-panel.proxy.example.com` 访问，**低延迟 + 不暴露真实地址 + 统一入口**。

## 架构总览

```
                    ┌──────────────────────────────┐
                    │         服务端 (协调角色)       │
                    │                              │
                    │  ├─ 存储 clients.peer_secret   │
                    │  ├─ 应用配置中的 ProxyType/     │
                    │  │   ProxyAddress/ProxySecret   │
                    │  └─ 转发非 Peer 的普通请求       │
                    └──────┬──────────────┬───────────┘
                           │              │
                   主隧道连接        主隧道连接
                           │              │
              ┌────────────▼──┐  ┌────────▼──────────┐
              │  主客户端 A    │  │  Peer 客户端 B     │
              │  --listen     │  │  --listen :8081    │
              │  :8080        │  │                    │
              │               │  │                    │
              │  LocalProxy   │  │  LocalProxy        │
              │    │          │  │    │               │
              │    ├─ 本地应用  │  │    ├─ 本地反代      │
              │    │  → 直接反代│  │    │               │
              │    │           │  │    │               │
              │    └─ Peer 应用│  │    └─ /ws-peer 端点 │
              │       │        │  │       │            │
              │       ▼        │  │       ▼            │
              │  PeerManager   │  │  Peer Session      │
              │    │           │  │  Server (接受方)    │
              │    ▼           │  │       │            │
              │  PeerTunnel ───┼──┼───────┘            │
              │  (WS 直连 +    │                       │
              │   yamux 流,    │                       │
              │   内网不走公网) │                       │
              └───────────────┘ └──────────────────────┘

              A → ws://B:8081/ws-peer  (内网 WebSocket 直连)
              → gob peer_auth 认证 → yamux 会话 → 每请求一条 yamux 流
```

## 核心组件

### 5.1 PeerTunnel — 单条 Peer 连接

**文件**: `internal/client/localproxy/peer_tunnel.go`

封装了一条到目标客户端的 Peer 连接的完整生命周期。底层是 **yamux 会话**（WebSocket 握手 + gob 认证后升级），请求经 yamux 流收发——不再有 per-request 的 pending map 或回调表。

```go
type PeerTunnel struct {
    ws         *websocket.Conn  // 底层 WS 连接
    session    *yamux.Session   // 认证后升级的 yamux 会话
    targetID   string           // 目标客户端 ID
    targetAddr string           // 目标地址 host:port
    secret     string           // 目标的 peer_secret
    clientID   string           // 自己的客户端 ID
}
```

#### 建立流程 (Connect)

```
1. websocket.Dial("ws://<targetAddr>/ws-peer")
   → TCP+WS 握手完成
   │
2. 构建认证消息 (原始 gob 编码, yamux 升级之前):
   timestamp = time.Now().Unix()
   signature = HMAC-SHA256(
       key: secret,
       data: clientID + ":" + strconv.FormatInt(timestamp, 10)
   )
   msg = peer_auth{
       ClientID:  clientID,
       Timestamp: timestamp,
       Signature: hex(signature),
   }
   data = WriteGobToBytes(msg)   // 原始 gob WS 消息
   ws.Write(data)
   │
3. 等待响应 (带超时):
   ├─ peer_auth_resp{Success: true}  → ✅ 认证通过
   └─ peer_auth_resp{Success: false} 或超时 → ❌ 认证失败, 关闭连接
   │
4. 升级为 yamux 会话:
   session = NewClientSessionFromWS(ctx, ws)   // pkg/tunnel/session.go
   │
5. 进入 AcceptStream 循环 (按 StreamType 分发, 无独立 readLoop/heartbeatLoop/dispatchLoop):
   每条请求 = 一条 yamux 流; 心跳由 yamux keepalive 承担 (30s)
```

#### 请求传输 (与主隧道协议一致)

Peer 隧道复用 `pkg/tunnel` 的流协议，与主隧道完全一致：

| 流类型 | 用途 |
|---------------------------|------|
| `StreamHTTP` (0x01) | HTTP 代理请求（gob `HTTPRequestHeader` → 流式响应） |
| `StreamWS` (0x02) | WebSocket 代理（gob `WSMeta` → `WSDialResult` → `BridgeWS`） |
| `StreamCtrl` (0x03) | 控制消息（按需） |

**设计意图：复用同一套 yamux 编解码实现**（`pkg/tunnel/`），避免维护两套序列化逻辑。

### 5.2 PeerManager — 多隧道管理器

**文件**: `internal/client/localproxy/peer_manager.go`

管理到多个不同目标客户端的 Peer 隧道实例。

```go
type PeerManager struct {
    mu      sync.RWMutex
    tunnels map[string]*PeerTunnel // key: "targetClientID@targetAddr"
}
```

#### 核心方法: GetTunnel()

```go
func (pm *PeerManager) GetTunnel(
    targetID, targetAddr, secret, selfID string,
) (*PeerTunnel, error) {
    key := targetID + "@" + targetAddr

    // 1. 先查缓存 (读锁)
    pm.mu.RLock()
    t, ok := pm.tunnels[key]
    pm.mu.RUnlock()
    if ok && t.IsAlive() {
        return t, nil // 缓存命中, 直接复用
    }

    // 2. 双重检查锁 (写锁)
    pm.mu.Lock()
    defer pm.mu.Unlock()
    if t, ok = pm.tunnels[key]; ok && t.IsAlive() {
        return t, nil // 另一个 goroutine 可能已创建
    }

    // 3. 创建新隧道
    t = NewPeerTunnel(targetID, targetAddr, secret, selfID)
    if err := t.Connect(); err != nil {
        return nil, err
    }
    pm.tunnels[key] = t
    return t, nil
}
```

**关键特性 — 懒创建 (Lazy Creation)**:

Peer 隧道不在启动时全部建立，而是**首次需要时按需创建**。原因：
- 一个客户端可能有十几个 Peer 配置，但实际使用的可能只有 2-3 个
- 减少启动时的连接风暴
- 未使用的 Peer 配置不会消耗资源

**自动重连机制**:

```
yamux 会话关闭 / WS 断开:
  │
  └→ 触发 onClose 回调
     │
     └→ PeerManager.onReconnect(targetID, targetAddr, secret, selfID)
          │
          └→ 指数退避重连:
               backoff = min(5s * 2^attempt, 60s)
               sleep(backoff)
               NewPeerTunnel().Connect() 重试
```

### 5.3 Peer 服务端 (/ws-peer 端点)

**文件**: `internal/client/localproxy/peer_handler.go`

每个启用 `--listen` 的客户端都会同时作为 Peer 隧道的**接受方** (Server 角色)，监听 `/ws-peer` 路径。

#### 请求路由判断

在 `LocalProxy.ServeHTTP()` 中，`/ws-peer` 路径具有最高优先级：

```go
func (lp *LocalProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // 优先级 1: Peer 隧道端点
    if r.URL.Path == "/ws-peer" {
        lp.handleWSPeer(w, r)
        return
    }
    // 优先级 2+: 正常本地代理逻辑...
}
```

#### Peer 隧道接受流程 (handleWSPeer)

```
1. websocket.Accept(w, r) → 升级为 WS 连接
   │
2. 读取第一条消息, 必须是 gob 编码的 peer_auth (yamux 之前):
   │
3. 验证签名:
   │   ├─ 解析 ClientID, Timestamp, Signature
   │   │
   │   ├─ 时间戳检查:
   │   │   ├─ |now - timestamp| < 300s (5分钟窗口)?  ← 防重放
   │   │   └─ 允许 60 秒时钟偏差 (容忍 NTP 不同步)
   │   │
   │   ├─ 查找发起方的 peer_secret:
   │   │   从本地 AppManager 的客户端列表中查找 ClientID 对应的密钥
   │   │
   │   └─ HMAC-SHA256 验证:
   │       expected = HMAC-SHA256(secret, clientID + ":" + timestamp)
   │       constant-time compare(expected == received)  ← 防时序攻击
   │
4. 验证结果:
   ├─ 成功 → 发送 peer_auth_resp{Success: true}
   │        → 升级为 yamux 会话 (NewServerSessionFromWS)
   │        → 进入 AcceptStream 循环 (handlePeerStream)
   │
   └─ 失败 → 发送 peer_auth_resp{Success: false}
             → 关闭连接
```

#### 流分发循环 (handlePeerStream)

```
for {
    stream, _ := session.AcceptStream()
    读取流首字节 StreamType:
      ├─ StreamHTTP (0x01) → handlePeerHTTPStream   // 每条流独立 goroutine
      ├─ StreamWS   (0x02) → handlePeerWSStream
      └─ 其他 → 关闭流
}
```

每个 Peer 请求在独立 goroutine 中处理，避免单个慢请求阻塞整个 Peer 隧道。

### 5.4 Peer HTTP 代理执行

**文件**: `internal/client/localproxy/peer_handler.go`（`handlePeerHTTPStream`）

收到 Peer 隧道的 HTTP 请求后，执行实际的代理调用：

```go
func handlePeerHTTPStream(ctx context.Context, stream *yamux.Stream) {
    // 1. 读取 gob HTTPRequestHeader (含方法、路径、Host、Headers、Body)
    req, err := pkgTunnel.ReadHTTPRequest(stream)

    // 2. 从请求头构建 *http.Request, 向目标服务发起请求
    resp, err := httputil.DoProxyRequest(req)

    // 3. 流式回写 (与主隧道一致)
    pkgTunnel.WriteHTTPResponse(stream, &HTTPResponseHeader{
        Status: resp.Status, Headers: respHeaders, Stream: true,
    })
    // Stream=true → WriteHTTPResponseWithBody 流式拷贝 body
}
```

**Peer 认证的层级**：peer 连接的身份认证在 **WS 握手阶段**完成（gob `peer_auth` / `peer_auth_resp`，yamux 升级之前）。请求本身不再携带认证头——这与早期"每个请求带 X-Peer-* 签名头"的模型不同，认证头仅用于服务端/诊断直连场景（见 5.6）。

### 5.5 Peer WebSocket 代理执行

**文件**: `internal/client/localproxy/peer_handler.go`（`handlePeerWSStream`）

WebSocket 的 Peer 代理比 HTTP 稍复杂，因为涉及三个 WS 连接的同时管理：

```
handlePeerWSStream(ctx, stream):
  │
  ├─ 1. 读取 gob WSMeta (含目标路径、请求头、子协议列表)
  │
  ├─ 2. websocket.Dial(目标服务的 WS 地址)
  │      ├─ 成功 → 得到 targetConn, 记录目标协商的 ws_subprotocol
  │      └─ 失败 → 回写 WSDialResult 错误, 结束
  │
  ├─ 3. 回写 WSDialResult (1B 状态 + gob, 含 ws_subprotocol)
  │
  └─ 4. 双向桥接 (BridgeWS):
        目标 WS → 流: [1B frameType][4B len][payload]
        流 → 目标 WS: 解帧写入
        任一方关闭 → 清理两端连接
```

### 5.6 Peer 签名辅助与 HTTP 正向代理风格路由

**文件**: `pkg/httputil/peer.go`

Peer 请求的**标准入口**已统一为 `/ws-peer` + yamux 流。除此之外，`pkg/httputil/peer.go` 仍保留一套 **HTTP 正向代理风格的签名辅助**，用于服务端与诊断组件以 HTTP 代理方式直连 peer 客户端的场景：

| 辅助 | 用途 |
|------|------|
| `NewPeerTransport(proxyAddr, clientID, secret)` | 创建以 peer 客户端为 HTTP 代理的 `http.Transport`，`ProxyConnectHeader` 携带 X-Peer-* 签名头（HTTPS CONNECT 隧道） |
| `SetPeerHeadersOnRequest(r, clientID, secret)` | 在明文 HTTP 请求上直接附加 X-Peer-Client / X-Peer-Timestamp / X-Peer-Signature 头 |
| `ComputePeerSignature` / `HMACSHA256` | HMAC-SHA256 签名计算（握手与头签名共用） |

**使用方**：
- 服务端 `internal/server/proxy/proxy.go` 的 `GetPeerTransport` 缓存（按 `proxyAddr|serverName` 复用 TCP 连接池，30 分钟淘汰）

> 注意：peer 客户端本地代理（localproxy）不再提供独立的 X-Peer-* 头接收端点（原 `handlePeerRequest` / `handlePeerConnect` 接收逻辑已随 yamux 迁移移除）——所有 peer 代理流量经 `/ws-peer` 握手认证后由 yamux 流承载。

## 安全体系

### 5.7 认证机制: HMAC-SHA256 + 时间戳

```
签名算法:
  Signature = HEX(HMAC-SHA256(
      Key:    peer_secret (32字节随机数, 目标客户端的),
      Data:   client_id + ":" + unix_timestamp
  ))

验证过程 (接收方):
  1. 从 gob peer_auth 消息提取 ClientID, Timestamp, Signature
  2. 检查时间戳: |now - timestamp| <= 5 分钟 (防重放窗口)
  3. 允许 ±60 秒时钟偏差 (NTP 容差)
  4. 查找 ClientID 对应的 peer_secret
  5. 计算 expected_sig = HMAC-SHA256(secret, client_id + ":" + timestamp)
  6. constant-time compare (防止时序攻击)
```

### 安全属性分析

| 攻击向量 | 防护措施 |
|---------|----------|
| 重放攻击 | 时间戳 5 分钟窗口过期后签名失效 |
| 中间人篡改 | HMAC 完整性保护，任何修改导致签名不匹配 |
| 密钥窃取 | 密钥仅存在于内存和 DB |
| 时序攻击 | 使用 `crypto/subtle.ConstantTimeCompare` 做签名比较 |
| 跨域伪造 | Peer 监听地址通常仅绑定内网 IP，外部无法直达 |

### 5.8 密钥管理与轮换

```
密钥生命周期:

  客户端 B 启动:
  ├─ crypto/rand 生成 32 字节随机数 → peer_secret
  │
  ├─ 连接服务端成功后:
  │   reportProxyStatus({PeerSecret: peer_secret})
  │   → 存入 clients.peer_secret (DB)
  │
  ├─ 其他客户端 (如 A) 查询应用列表时:
  │   GetClientApps() 返回包含 peer_secret 的 ClientAppInfo
  │   → A 用此密钥建立 Peer 连接
  │
  └─ 客户端 B 重启:
      ├─ 生成全新的 peer_secret (旧的立即失效!)
      ├─ 上报新密钥到服务端
      └─ 旧 Peer 连接自然断开 → A 自动用新密钥重建
```

**每次重启生成新密钥**的设计意味着：
- 密钥有效期最多等于客户端连续运行时间
- 定期重启客户端可实现「软性密钥轮换」
- 即使旧密钥泄露，重启后自动失效

### 5.9 网络隔离

```
推荐部署拓扑:

  客户端 B --listen :8081
    ↑ 仅绑定内网 IP (如 192.168.1.11)
    │
    ├─ 内网用户/Peer 可达 ✓
    │
    └─ 公网不可达 ✓ (防火墙/Nginx 不转发该端口)
```

Peer 监听端口 (`--listen` 指定的地址) 应仅绑定内网 IP，不暴露到公网。即使攻击者获得了有效的 Peer 签名，也无法从外部网络建立 Peer 连接。

## 性能特征

### 延迟对比

| 路径 | 典型 RTT (局域网环境) | 说明 |
|------|----------------------|------|
| 本地直连 | < 1ms | 同机进程间通信 |
| Peer 隧道 | 1-3ms | 内网 WS 一跳 |
| 服务端中转 | 20-100ms+ | 内网→公网→内网 (取决于公网链路质量) |

Peer 隧道的延迟接近本地直连，比服务端中转快 1-2 个数量级。

### 连接池与复用

PeerManager 维护的隧道是**长连接**，一旦建立后持续复用：

```
第一次请求: 建立连接 (WS 握手 + gob 认证 + yamux 升级)
后续请求: 直接开流发送 (~0ms 额外开销)
空闲保持: yamux keepalive 保活 (30s)
断线恢复: 自动重连 (指数退避 5s→60s)
```

### 并发能力

单条 Peer 隧道上可同时进行多个请求/WS 会话（每条请求独占一条 yamux 流，流间互不阻塞），与主隧道的并发模型一致。
