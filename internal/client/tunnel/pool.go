package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-yamux/v4"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// poolConn 单条隧道连接（池成员）
type poolConn struct {
	session   *yamux.Session
	createdAt time.Time
	expiresAt time.Time          // 创建时确定：now + rand(5~10min)
	draining  atomic.Bool        // true: 不再被 OpenStream 选中，等待 stream 归零后关闭
	cancel    context.CancelFunc // 取消该连接的 AcceptStream 循环
}

// connPool 连接池：维护 N 条 WebSocket+yamux 连接，随机 TTL 淘汰，最少 stream 优先选流
type connPool struct {
	url            string
	streamHandler  func(ctx context.Context, stream *yamux.Stream)
	size           int
	minTTL         time.Duration
	maxTTL         time.Duration
	drainGrace     time.Duration
	initialBackoff time.Duration
	maxBackoff     time.Duration

	mu        sync.RWMutex
	conns     []*poolConn
	closed    atomic.Bool
	ctx       context.Context
	connected atomic.Bool
	kicked    *atomic.Bool // 共享 Client 的 kicked 标志

	// onConnEstablished 在每条新连接加入池后触发（dialAndRun 内调用）。
	// 用于让上层在每次新连接建立后重新上报版本等元信息，避免 pool_size=1
	// 轮换时服务端 ConnGroup 被删除重建后版本字段丢失。
	onConnEstablished func()
}

// newConnPool 创建连接池
func newConnPool(ctx context.Context, url string, size int, minTTL, maxTTL, drainGrace, initialBackoff, maxBackoff time.Duration, kicked *atomic.Bool) *connPool {
	return &connPool{
		url:            url,
		size:           size,
		minTTL:         minTTL,
		maxTTL:         maxTTL,
		drainGrace:     drainGrace,
		initialBackoff: initialBackoff,
		maxBackoff:     maxBackoff,
		ctx:            ctx,
		kicked:         kicked,
	}
}

// OpenStream 选非 draining、stream 数最少的连接开流（最少 in-flight 策略）。
// 选流在锁内完成，开流在锁外执行（OpenStream 带超时可能阻塞，不能持锁等待）。
func (p *connPool) OpenStream() (*yamux.Stream, error) {
	p.mu.RLock()
	if p.closed.Load() || len(p.conns) == 0 {
		p.mu.RUnlock()
		return nil, fmt.Errorf("隧道未连接")
	}
	var best *poolConn
	var bestN int = -1
	for _, c := range p.conns {
		if c.draining.Load() {
			continue
		}
		if c.session.IsClosed() {
			continue
		}
		n := c.session.NumStreams()
		if best == nil || n < bestN {
			best, bestN = c, n
		}
	}
	p.mu.RUnlock()
	if best == nil {
		return nil, fmt.Errorf("所有连接正在淘汰")
	}
	ctx, cancel := context.WithTimeout(p.ctx, streamOpenTimeout)
	defer cancel()
	return best.session.OpenStream(ctx)
}

// streamOpenTimeout 客户端主动开流的超时（v4 OpenStream 需要 ctx，
// 防止对端不 ACK 新流时永久阻塞）
const streamOpenTimeout = 30 * time.Second

// IsConnected 池中是否至少有一条活跃连接
func (p *connPool) IsConnected() bool {
	return p.connected.Load()
}

// manageConn 单条连接的生命周期管理：建连 → AcceptStream 循环 → 到期/断开时触发替换或重连
func (p *connPool) manageConn(ctx context.Context) {
	backoff := p.initialBackoff
	for {
		if ctx.Err() != nil || p.closed.Load() {
			return
		}
		connCtx, cancel := context.WithCancel(ctx)
		ttl := p.randomTTL()
		connectedAt := time.Now()
		pc, err := p.dialAndRun(connCtx, ttl)
		if err != nil {
			cancel()
			slog.Error("连接服务端失败", "type", "tunnel", "error", err)
			// 重连退避（jitter 50%~150%）
			wait := backoff/2 + time.Duration(rand.Int64N(int64(backoff)))
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			backoff = time.Duration(math.Min(float64(backoff)*2, float64(p.maxBackoff)))
			continue
		}

		// TTL 到期：先建新连接替换，再 drain 旧连接
		timer := time.AfterFunc(ttl, func() {
			p.replaceConn(pc)
		})
		pc.cancel = cancel
		<-pc.session.CloseChan() // 阻塞直到连接断开（TTL 替换或网络断开）
		timer.Stop()
		cancel()

		// 如果是被 TTL 替换的（draining），不重连——新连接已由 replaceConn 启动
		if pc.draining.Load() {
			return
		}

		// 连接持续时间够长则重置退避
		if time.Since(connectedAt) >= resetBackoffThreshold {
			backoff = p.initialBackoff
		}
	}
}

// dialAndRun 建 WS+yamux 连接，加入池，启动 AcceptStream 循环
func (p *connPool) dialAndRun(ctx context.Context, ttl time.Duration) (*poolConn, error) {
	sess, err := pkgTunnel.NewClientSession(ctx, p.url)
	if err != nil {
		return nil, err
	}
	pc := &poolConn{
		session:   sess,
		createdAt: time.Now(),
		expiresAt: time.Now().Add(ttl),
	}
	p.addConn(pc)
	p.markConnected(true)
	slog.Debug("已连接到服务端", "type", "tunnel", "expires_at", pc.expiresAt.Format(time.RFC3339))

	// AcceptStream 循环（与现有 connect() 内的循环一致）
	go func() {
		defer p.removeConn(pc)
		for {
			stream, err := sess.AcceptStream()
			if err != nil {
				return
			}
			go p.streamHandler(ctx, stream)
		}
	}()

	// 通知上层：新连接已建立并注册到服务端 ConnGroup。
	// 异步触发避免阻塞 dialAndRun（reportProxyStatus 会开流，可能阻塞到 OpenStream 超时）。
	if p.onConnEstablished != nil {
		go p.onConnEstablished()
	}
	return pc, nil
}

// replaceConn TTL 到期时替换连接：先建新连接，通知服务端 drain 旧连接，等归零后关闭
func (p *connPool) replaceConn(old *poolConn) {
	// 1. 启动新连接（加入池）
	go p.manageConn(p.ctx)

	// 2. 标记旧连接 draining（客户端 OpenStream 不再选它）
	old.draining.Store(true)

	// 3. 通知服务端该连接 draining（服务端 OpenStream 不再派新流到这条连接）
	//    通过该连接本身开一条控制流发送，服务端收到即标记对应 Conn
	notifyDraining(old.session)

	// 4. 等待 in-flight stream 归零或宽限期
	//    drainGrace=0 表示不限制等待时间，永久保留旧连接直到所有 stream 自然结束
	var deadlineCh <-chan time.Time
	if p.drainGrace > 0 {
		deadline := time.NewTimer(p.drainGrace)
		defer deadline.Stop()
		deadlineCh = deadline.C
	}
	for old.session.NumStreams() > 0 {
		select {
		case <-deadlineCh: // drainGrace=0 时为 nil，永不被选中
			goto closeOld
		case <-time.After(100 * time.Millisecond):
		case <-p.ctx.Done():
			return
		}
	}
closeOld:
	// 5. 关闭旧连接（其 manageConn 检测到 draining 后退出，不重连）
	old.cancel()
}

// notifyDraining 在指定 session 上开一条控制流，通知服务端该连接正在 drain。
// 失败静默忽略（连接已不可用时无需通知）。
func notifyDraining(sess *yamux.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := sess.OpenStream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()
	pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
		Action: pkgTunnel.ActionConnDraining,
	})
}

// randomTTL 随机淘汰间隔：rand[minTTL, maxTTL)
func (p *connPool) randomTTL() time.Duration {
	span := p.maxTTL - p.minTTL
	return p.minTTL + time.Duration(rand.Int64N(int64(span)))
}

// addConn 添加连接到池
func (p *connPool) addConn(pc *poolConn) {
	p.mu.Lock()
	p.conns = append(p.conns, pc)
	p.mu.Unlock()
}

// removeConn 从池中移除连接；池空且未主动关闭时标记为未连接
func (p *connPool) removeConn(pc *poolConn) {
	p.mu.Lock()
	for i, c := range p.conns {
		if c == pc {
			p.conns = append(p.conns[:i], p.conns[i+1:]...)
			break
		}
	}
	empty := len(p.conns) == 0
	p.mu.Unlock()
	if empty && !p.closed.Load() {
		p.markConnected(false)
	}
}

// markConnected 更新连接状态
func (p *connPool) markConnected(v bool) {
	p.connected.Store(v)
}

// Close 关闭整个池：标记关闭，取消所有连接，关闭所有 session
func (p *connPool) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	p.mu.RLock()
	conns := make([]*poolConn, len(p.conns))
	copy(conns, p.conns)
	p.mu.RUnlock()
	for _, pc := range conns {
		if pc.cancel != nil {
			pc.cancel()
		}
		pc.session.Close()
	}
	p.markConnected(false)
}
