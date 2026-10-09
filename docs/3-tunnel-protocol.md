# 3 - 隧道协议与通信机制

## 概述

HopProxy 的服务端与客户端之间通过 **WebSocket 承载 yamux 多路复用流** 进行通信。每个 HTTP/WS 代理请求独占一条 yamux 流，流内按顺序读写，天然有序且自带窗口流控（背压）。协议取代了早期基于 JSON 头 + RequestID 的自定义消息协议（旧协议包已随迁移删除），无需再匹配请求 ID、无需派发循环。

```
WebSocket (wss://server/ws/<client-uuid>)
   │
   ▼ websocket.NetConn（把 WS 转成流式 net.Conn）
yamux.Session（多路复用）
   ├── 流 1: HTTP 代理请求（StreamType=0x01）
   ├── 流 2: WebSocket 代理（StreamType=0x02）
   ├── 流 3: 控制消息（StreamType=0x03）
   └── 流 N: ...
```

## 协议帧格式

### 物理层：yamux 流 + 每流首字节 StreamType

隧道上的数据不再有全局的"消息帧"。yamux 会话之上是 N 条独立流，每条流的前缀只有 **1 字节的 StreamType**，之后紧跟 gob 编码的负载：

```
┌────────────────────────┬──────────────────────────┐
│ StreamType (1 byte)    │ 负载 (gob 编码，流相关)    │
└────────────────────────┴──────────────────────────┘
```

**文件**: `pkg/tunnel/stream.go`（`WriteStreamType` / `ReadStreamType`）、`pkg/tunnel/session.go`（`NewClientSession` / `NewServerSession`）

### 设计意图

1. **每请求一流**：请求与响应在同一条 yamux 流上顺序读写，天然有序，无需 RequestID 关联、无乱序风险。
2. **窗口流控即背压**：yamux 为每条流维护独立的滑动窗口（`MaxStreamWindowSize=4MB`），目标端读取慢时窗口耗尽，写入端自然阻塞——从目标服务到浏览器形成直接背压。
3. **流隔离**：单条流的问题（慢速消费者、异常关闭）不影响其他流，无需全局分发锁。

## 流类型与消息定义

**文件**: `pkg/tunnel/stream.go`（StreamType 常量）、`pkg/tunnel/ctrl.go`（CtrlMessage 与 Action 常量）

### StreamType 表

| StreamType | 值 | 负载 | 方向 |
|-----------|----|------|------|
| `StreamHTTP` | 0x01 | gob `HTTPRequestHeader`（含 body）→ gob `HTTPResponseHeader`（`Stream=true` 时随后跟原始 body 块） | 双向 |
| `StreamWS` | 0x02 | gob `WSMeta` → 1B 状态 + gob `WSDialResult`，然后为带帧双向字节流（`[1B frameType][4B len][payload]`，保留 Text/Binary 类型） | 双向 |
| `StreamCtrl` | 0x03 | gob `CtrlMessage`（同一流上可跟可选的 gob 响应） | 双向 |

### CtrlMessage Action 表

| Action | 方向 | 用途 |
|--------|------|------|
| `get_apps` / `apps_response` | 双向 | 应用列表同步 |
| `apps_changed` | 服务端 → 客户端 | 通知应用配置变更 |
| `verify_session` / `verify_response` | 客户端 → 服务端 | 本地代理 SSO 会话远程验证 |
| `proxy_status` | 客户端 → 服务端 | 上报本地代理地址与 Peer 密钥 |
| `kick` | 服务端 → 客户端 | 管理动作：强制断开（作用于整个连接组） |
| `conn_draining` | 客户端 → 服务端 | 连接池淘汰旧连接时发送，服务端 `OpenStream` 跳过该连接 |

### 负载示例（HTTP 代理）

流首字节 `0x01` 之后是 gob 编码的 `HTTPRequestHeader`：

```go
type HTTPRequestHeader struct {
    Method   string
    Path     string
    Host     string
    Headers  map[string]string
    Body     []byte   // 非流式请求体直接内嵌
    // 代理/Peer 相关字段（proxy_type、proxy_address、proxy_secret 等）
}
```

响应为 gob `HTTPResponseHeader`（状态码、头、`Stream=true` 标记）；`Stream=true` 时响应体以原始字节块在同一流上流式拷贝（SSE/Chunked 天然支持，逐块 flush）。

## 请求-响应模型

### 同步请求-响应（每流独占）

**文件**: `internal/server/proxy/http.go`（`proxyHTTP`）、`pkg/tunnel/tunnel_transport.go`（`WriteHTTPRequest` / `ReadHTTPResponse`）

发送方打开一条 yamux 流后，写入 StreamType + gob 请求头，然后在该流上同步读取响应：

```go
stream, err := conn.OpenStream()          // 开一条新流
defer stream.Close()
pkgTunnel.WriteStreamType(stream, pkgTunnel.StreamHTTP)
pkgTunnel.WriteHTTPRequest(stream, &HTTPRequestHeader{...})
resp, err := pkgTunnel.ReadHTTPResponse(stream)   // 流上阻塞读响应
```

**为什么不需要异步回调 / pending map？**

代理场景天然是"发一个请求、拿一个响应"的同步模型。每条 yamux 流自身就是通道：流内顺序读写保证 `HTTPResponse` 先于 body 块到达，无需 RequestID 关联、无需 `pending` map、无需单 goroutine 串行分发（`streamDispatchLoop` 已成历史）。

### WebSocket 代理的双向流

**文件**: `internal/server/proxy/websocket.go`（`proxyWebSocket`）、`pkg/tunnel/ws_stream.go`（`BridgeWS`）

WS 代理在同一 yamux 流上做全双工桥接：客户端连上目标后回写 `WSDialResult`（1B 状态 + gob），随后 `BridgeWS` 用 `[1B frameType][4B len][payload]` 帧在浏览器与目标之间双向转发（保留 Text/Binary 帧类型）。

## 连接生命周期管理

### 心跳与存活检测

**文件**: `pkg/tunnel/session.go`（yamux 配置）、`internal/server/tunnel/conn.go`（`MeasureLatency`）

不再有应用层的 ping/pong 消息。存活检测由 **yamux 内置 keepalive** 负责：

- `EnableKeepAlive=true`、`KeepAliveInterval=30s`、`MeasureRTTInterval=30s`、`PingBacklog=32`
- RTT 测量：`session.Ping()`（`MeasureLatency`），供 `selectClient()` 选择延迟最优的客户端连接

### 踢出机制 (Kick)

**触发条件**：管理动作（如管理员在后台强制断开客户端），不再是"重复连接踢旧"——同 clientID 现可多连接并存（连接池），`Hub.Register` 不再踢旧连接。

**流程**：
```
Hub.Kick(clientID, reason):
  1. 取出该 clientID 的整个 ConnGroup
  2. 对组内每条连接发送 ActionKick 控制流
  3. 客户端收到后设置 kicked 标志 → 关闭整个连接池退出
```

**客户端收到 Kick 后的行为** [`tunnel.go`](internal/client/tunnel/tunnel.go)：
```go
case pkgTunnel.ActionKick:
    // 设置 kicked 标志并关闭整个连接池，Run 退出且不重连
    c.kicked.Store(true)
    c.pool.Close()
```

**为什么不重连？** 因为 Kick 意味着"该客户端已被管理员停用/身份需重新注册"。若自动重连会形成连接→被踢→重连的死循环。

### 连接池与自动重连 (客户端)

**文件**: `internal/client/tunnel/tunnel.go` + `pool.go`

客户端默认维护 **4 条 WS+yamux 连接**（`-pool-size` / `HP_POOL_SIZE`，设为 1 退化为单连接）：

```
连接池管理:
  ├─ 建连: 每条连接 = pkgTunnel.NewClientSession（WS dial → websocket.NetConn → yamux.Client）
  │
  ├─ 淘汰: 每条空闲连接随机 5–10 分钟 TTL
  │   └─ 先建立新连接，再对旧连接发送 conn_draining 并等待其上活动流结束
  │
  ├─ 选路: OpenStream 选择 NumStreams（活跃流数）最少的连接
  │
  ├─ 重连: 单条连接断开 → 指数退避 + jitter（50%–150%，最大 60 秒）重连该连接
  │
  └─ 退出: ctx 取消（SIGINT/SIGTERM）或 kicked 标志 → pool.Close()
```

**抖动 (Jitter) 的作用**：假设服务端重启，同时有大量客户端重连。没有抖动的话，客户端会在完全相同的时刻尝试重连，造成瞬间流量洪峰。±50% 的随机抖动将重连时间均匀分布在一个时间窗口内。

## 流控与帧上限（取代 WSWriter）

早期基于双通道优先级队列的 WS 写入器（控制消息优先于数据消息）已随迁移删除。yamux 模型下不再需要应用层写优先级：

- **每流独立窗口**：yamux 为每条流维护滑动窗口（`InitialStreamWindowSize=256KB`，随读取增长到 `MaxStreamWindowSize=4MB`）。慢速消费者只阻塞自己的流，不会拖累控制流——控制消息（kick、apps_changed 等）走独立的 `StreamCtrl` 流，天然不受数据流背压影响。
- **帧上限**：`MaxMessageSize=256KB` 限制单帧大小；`MaxIncomingStreams=1024` 限制对端可并发打开的流数（超限即 RST，防止开流堆积）；`StreamTypeReadTimeout=30s` 防止"开流但不发首字节"的流长期占用。

## 隧道协议安全考量

### 消息认证

隧道本身建立在 **WSS (WebSocket over TLS)** 之上，Nginx 负责 SSL 终结。因此：
- 隧道内的通信加密由 TLS 层保障
- 消息本身不做额外签名（TLS 已提供完整性保护）

### Client ID 作为凭证

客户端使用 UUID v4 作为身份标识和连接凭证：

```go
// UUID v4 = 128 位随机数
// 空间大小: 2^128 ≈ 3.4 × 10^38
// 暴力破解概率: 极低
```

**安全性分析**：
- UUID 即凭证，无独立密码。这意味着**任何知道 UUID 的人都能建立隧道连接**。
- 缓解措施：UUID 仅在管理后台展示，且管理后台自身有 JWT 认证保护。
- 建议：生产环境应确保管理后台不被未授权访问；未来可考虑增加客户端预共享密钥 (PSK)。

### 防止消息注入

- WebSocket 连接要求 Origin/Host 匹配（由 Nginx 反代保证）
- 隧道端点路径 `/ws/<UUID>` 中的 UUID 必须与数据库中的记录匹配
- `Hub.Register` 确保 UUID 存在于 clients 表中才接受连接
- 每流的 StreamType 由接收方校验（非法类型直接关闭流）

## 性能特征

### 吞吐量限制因素

| 瓶颈 | 说明 | 典型值 |
|------|------|--------|
| WebSocket 单连接带宽 | 取决于 TCP 窗口和网络条件 | ~100Mbps+ (局域网), ~10-50Mbps (公网) |
| 序列化开销 | gob 编解码（仅请求/响应头，body 原始字节流式拷贝） | 微秒级/请求 |
| 流式缓冲区 | yamux 流窗口（256KB 起步 → 4MB） | 平衡延迟和吞吐 |
| 并发请求数 | 单连接上可同时进行的请求数 | 受限于 MaxIncomingStreams=1024 与连接池规模 |

### 内存占用

每条隧道连接的固定开销来自 yamux 会话本身（窗口缓冲、backlog 队列）；每条活跃请求流额外占用一个流窗口。由于不再有全局 pending map、按请求 ID 的回调表、暂存队列或派发 channel，连接内存占用与请求数解耦，仅随实际并发流数线性增长。
