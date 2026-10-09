// Package main 实现 hop-proxy-agent：安全代理 agent 二进制。
//
// agent 部署在内网服务前，给后端 HTTP 服务套一层 Noise 加密传输层做鉴权。
// 启动时从管理服务拉取 caller 公钥（/pubkey/{uuid}），监听 TCP，
// 接受连接后执行 Noise_KN 握手并校验 caller 公钥，然后纯 TCP relay 到 --forward 目标。
//
// 参数支持命令行 flag 和环境变量两种方式（flag 优先）：
//
//	hop-proxy-agent --server=https://admin.example.com --uuid=<uuid> --listen=[ip]:port --forward=ip:port
//
// 等价的环境变量：HP_SERVER / HP_UUID / HP_LISTEN / HP_FORWARD
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/robin/hop-proxy/pkg/agentcrypto"
)

func main() {
	serverURL := flag.String("server", envOrDefault("HP_AGENT_SERVER", ""), "管理服务地址（如 https://admin.example.com），或环境变量 HP_AGENT_SERVER")
	uuid := flag.String("uuid", envOrDefault("HP_AGENT_UUID", ""), "安全代理密钥 UUID，或环境变量 HP_AGENT_UUID")
	listen := flag.String("listen", envOrDefault("HP_AGENT_LISTEN", ""), "监听地址（如 [ip]:port），或环境变量 HP_AGENT_LISTEN")
	forward := flag.String("forward", envOrDefault("HP_AGENT_FORWARD", ""), "转发目标地址（如 127.0.0.1:8080），或环境变量 HP_AGENT_FORWARD")
	flag.Parse()

	if *serverURL == "" || *uuid == "" || *listen == "" || *forward == "" {
		fmt.Fprintln(os.Stderr, "用法: hop-proxy-agent --server=<url> --uuid=<uuid> --listen=[ip]:port --forward=ip:port\n（也支持环境变量 HP_AGENT_SERVER / HP_AGENT_UUID / HP_AGENT_LISTEN / HP_AGENT_FORWARD）")
		os.Exit(1)
	}

	// 从管理服务拉取 caller 公钥
	pubKeyHex, err := fetchPublicKey(*serverURL, *uuid)
	if err != nil {
		slog.Error("拉取公钥失败", "type", "agent", "server", *serverURL, "uuid", *uuid, "error", err)
		os.Exit(1)
	}
	expectedCallerPub, err := agentcrypto.LoadPublicKey(pubKeyHex)
	if err != nil {
		slog.Error("加载公钥失败", "type", "agent", "error", err)
		os.Exit(1)
	}
	slog.Info("已加载 caller 公钥", "type", "agent", "uuid", *uuid, "public_key", pubKeyHex[:16]+"...")

	// 监听 TCP
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		slog.Error("监听失败", "type", "agent", "listen", *listen, "error", err)
		os.Exit(1)
	}
	slog.Info("agent 启动", "type", "agent", "listen", *listen, "forward", *forward)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		slog.Info("正在关闭 agent...", "type", "agent")
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			slog.Warn("接受连接失败", "type", "agent", "error", err)
			continue
		}
		go handleConn(conn, expectedCallerPub, *forward)
	}

	slog.Info("agent 已关闭", "type", "agent")
}

// handleConn 处理一条 agent 连接：Noise 握手 → TCP relay
func handleConn(conn net.Conn, expectedCallerPub []byte, forward string) {
	defer conn.Close()

	// Noise_XX 握手 + 校验 caller 公钥
	noiseConn, err := agentcrypto.AcceptAndHandshake(conn, expectedCallerPub)
	if err != nil {
		slog.Warn("Noise 握手失败", "type", "agent", "remote", conn.RemoteAddr(), "error", err)
		return
	}
	defer noiseConn.Close()

	// 连接后端
	backend, err := net.Dial("tcp", forward)
	if err != nil {
		slog.Warn("连接后端失败", "type", "agent", "forward", forward, "error", err)
		return
	}
	defer backend.Close()

	slog.Debug("agent 连接建立", "type", "agent", "remote", conn.RemoteAddr(), "forward", forward)

	// 双向 TCP relay
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(backend, noiseConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(noiseConn, backend)
		done <- struct{}{}
	}()
	<-done
}

// fetchPublicKey 从管理服务拉取 caller 公钥
func fetchPublicKey(serverURL, uuid string) (string, error) {
	url := fmt.Sprintf("%s/pubkey/%s", serverURL, uuid)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("请求管理服务失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("管理服务返回状态码: %d", resp.StatusCode)
	}

	var result struct {
		Data struct {
			UUID      string `json:"uuid"`
			PublicKey string `json:"public_key"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("解析响应失败: %w", err)
	}
	if result.Data.PublicKey == "" {
		return "", fmt.Errorf("公钥为空")
	}
	return result.Data.PublicKey, nil
}

// envOrDefault 读取环境变量，缺失时返回默认值
func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
