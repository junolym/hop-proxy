package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/libp2p/go-yamux/v4"

	"github.com/robin/hop-proxy/internal/client/agenttransport"
	"github.com/robin/hop-proxy/internal/client/apps"
	"github.com/robin/hop-proxy/internal/client/localproxy"
	"github.com/robin/hop-proxy/internal/client/logsink"
	"github.com/robin/hop-proxy/internal/client/proxy"
	"github.com/robin/hop-proxy/internal/client/tunnel"
	"github.com/robin/hop-proxy/pkg/httputil"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

var (
	buildVersion string
	buildCommit  string
)

var myClientID string

func main() {
	server := flag.String("server", os.Getenv("HP_SERVER"), "服务端地址")
	clientID := flag.String("id", os.Getenv("HP_CLIENT_ID"), "客户端 UUID")
	connect := flag.String("connect", "", "完整连接 URL")
	reconnectIntervalStr := flag.String("reconnect-interval", envOrDefault("HP_RECONNECT_INTERVAL", "10s"), "断线重连初始等待时间")
	listen := flag.String("listen", os.Getenv("HP_LISTEN"), "本地代理监听地址")
	poolSize := flag.Int("pool-size", envIntOrDefault("HP_POOL_SIZE", 4), "连接池大小")
	connMinTTL := flag.Duration("conn-min-ttl", envDurationOrDefault("HP_CONN_MIN_TTL", 5*time.Minute), "单连接最短存活时间")
	connMaxTTL := flag.Duration("conn-max-ttl", envDurationOrDefault("HP_CONN_MAX_TTL", 10*time.Minute), "单连接最长存活时间")
	connDrainGrace := flag.Duration("conn-drain-grace", envDurationOrDefault("HP_CONN_DRAIN_GRACE", 0), "drain 宽限期（0=永久等待，旧连接保留至所有 stream 结束）")
	flag.Parse()

	var wsURL string
	if *connect != "" {
		wsURL = *connect
	} else if *server != "" && *clientID != "" {
		wsURL = strings.TrimRight(*server, "/") + "/ws/" + *clientID
	} else {
		fmt.Fprintln(os.Stderr, "错误：需要提供 --connect 或 --server + --id 参数")
		flag.Usage()
		os.Exit(1)
	}

	u, err := url.Parse(wsURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") {
		fmt.Fprintf(os.Stderr, "错误：无效的 WebSocket URL: %s\n", wsURL)
		os.Exit(1)
	}

	reconnectInterval, err := time.ParseDuration(*reconnectIntervalStr)
	if err != nil {
		reconnectInterval = 10 * time.Second
	}

	slog.Info("HopProxy 客户端启动", "type", "system", "url", wsURL, "reconnect_interval", reconnectInterval, "listen", *listen)
	myClientID = strings.TrimPrefix(u.Path, "/ws/")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var tunnelOnline atomic.Bool
	var appManager *apps.Manager
	var localProxy *localproxy.LocalProxy
	var httpServer *http.Server

	peerSecret := generatePeerSecret()

	// 创建隧道客户端（yamux）
	client := tunnel.NewClient(wsURL, reconnectInterval)
	client.SetPoolConfig(*poolSize, *connMinTTL, *connMaxTTL, *connDrainGrace)

	// 初始化日志系统：slog 调用 → logsink → 异步批量上报到服务端
	// reporter 通过 client.OpenStream 开 StreamLog 流上报
	logReporter := logsink.NewReporter(func() (io.WriteCloser, error) {
		return client.OpenStream()
	})
	logSink := logsink.New(logReporter)
	slog.SetDefault(logsink.NewLogger(logSink))
	go logSink.Run(ctx)
	defer logSink.Stop()

	// agent Transport 管理器（用于通过 Noise 隧道连接安全代理 agent）
	agentTransportMgr := agenttransport.NewManager(client)

	// peer 隧道管理器：Path 4（本地代理）与 Path 6（隧道执行器）共用同一隧道池 (#58)。
	// 无本地代理的客户端也可能被配置为 peer 发起方，故无条件创建；
	// 版本号随 peer 认证上报，供对端认证日志识别拨号方版本 (#67)
	peerManager := localproxy.NewPeerManager(myClientID, buildVersion)

	// peer 隧道发送器：按 X-Hop-Proxy-Address / X-Hop-Peer-Secret 建立/复用到
	// 目标客户端的 peer 隧道并开流（此前为 nil 占位，Path 6 一直退化为本机直连，#58）
	var peerOpenStream proxy.PeerTunnelOpenStream = func(proxyAddress, peerSecret string) (*yamux.Stream, error) {
		tunnel, err := peerManager.GetTunnel(proxyAddress, proxyAddress, peerSecret, myClientID)
		if err != nil {
			return nil, err
		}
		return tunnel.OpenStream()
	}

	// 创建 WS 代理（处理来自服务端的 WS 代理请求）
	wsProxy := proxy.NewWSProxy(myClientID, peerOpenStream, agentTransportMgr)

	// 本地代理模式
	proxyEnabled := *listen != ""
	proxyListen := *listen

	if proxyEnabled {
		appManager = apps.NewManager(client)
		localProxy = localproxy.NewLocalProxyWithAgent(appManager, client, func() bool {
			return tunnelOnline.Load()
		}, myClientID, peerSecret, agentTransportMgr, peerManager)

		go appManager.StartSync(ctx, client.IsConnected)

		httpServer = &http.Server{
			Addr:    proxyListen,
			Handler: localProxy,
			// net/http 内部错误（如 superfluous WriteHeader）统一走 slog 管道
			ErrorLog:          httputil.NewServerErrorLog(),
			ReadHeaderTimeout: 10 * time.Second,  // 读请求头超时，防止 Slowloris
			ReadTimeout:       60 * time.Second,  // 读整个请求超时（含 body）
			WriteTimeout:      0,                 // 不设，流式响应/SSE 需要长时间写入
			IdleTimeout:       120 * time.Second, // Keep-Alive 空闲超时
		}
		go func() {
			slog.Debug("本地代理服务启动", "type", "system", "listen", proxyListen)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("本地代理服务错误", "type", "system", "error", err)
			}
		}()
	}

	// 设置流处理器
	client.SetHandlers(
		// HTTP 请求处理（原生 HTTP/1.1 over yamux）
		func(ctx context.Context, stream *yamux.Stream) {
			proxy.HandleProxyStream(ctx, stream, myClientID, peerOpenStream, agentTransportMgr)
		},
		// WS 请求处理（原生 HTTP 升级 over yamux）
		func(ctx context.Context, stream *yamux.Stream) {
			wsProxy.HandleWSStream(ctx, stream)
		},
		// 应用变更通知
		func() {
			if appManager != nil {
				appManager.Refresh()
			}
		},
	)

	// 每条新连接建立后重新上报版本：避免 pool_size=1 时 TTL 轮换中
	// 服务端 ConnGroup 被删除重建后版本字段丢失（#34）。
	client.SetConnEstablishedHandler(func() {
		reportProxyStatus(client, proxyEnabled, proxyListen, peerSecret)
	})

	// 监听隧道状态并上报代理状态
	go func() {
		var lastReported bool
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				connected := client.IsConnected()
				tunnelOnline.Store(connected)
				if connected != lastReported {
					lastReported = connected
					if connected {
						reportProxyStatus(client, proxyEnabled, proxyListen, peerSecret)
						if appManager != nil {
							appManager.Refresh()
						}
					}
				}
			}
		}
	}()

	client.Run(ctx)

	if localProxy != nil {
		localProxy.Close() // 内部含 peerManager.CloseAll()
	} else {
		// 无本地代理的客户端也可能持有 peer 隧道（Path 6 发起方）
		peerManager.CloseAll()
	}
	if httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envIntOrDefault(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func envDurationOrDefault(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// reportProxyStatus 上报代理状态到服务端
func reportProxyStatus(client *tunnel.Client, enabled bool, listen, peerSecret string) {
	stream, err := client.OpenStream()
	if err != nil {
		slog.Warn("上报代理状态失败", "type", "tunnel", "error", err)
		return
	}
	defer stream.Close()

	pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
		Action:     pkgTunnel.ActionProxyStatus,
		Enabled:    enabled,
		Listen:     listen,
		PeerSecret: peerSecret,
		Version:    buildVersion,
	})
	slog.Debug("已上报代理状态", "type", "tunnel", "enabled", enabled, "listen", listen)
}

func generatePeerSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
