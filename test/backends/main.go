package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// wsConns 管理当前活跃的 WebSocket 连接，用于 /ws/close-all 端点
var (
	wsConnsMu sync.Mutex
	wsConns   = make(map[string]*websocket.Conn)
	wsConnSeq int
)

func main() {
	addr := flag.String("addr", ":8000", "Listen address (e.g. :8000)")
	flag.Parse()

	mux := http.NewServeMux()

	// Normal JSON response
	mux.HandleFunc("/", handleRoot)

	// Echo POST body
	mux.HandleFunc("/echo", handleEcho)

	// Header echo
	mux.HandleFunc("/headers", handleHeaders)

	// Cookie check
	mux.HandleFunc("/cookies", handleCookies)

	// Status code routes
	mux.HandleFunc("/status/", handleStatus)

	// Large file download (repeated pattern, deterministic)
	mux.HandleFunc("/size/", handleLargeFile)

	// Pseudo-random binary (fixed seed, deterministic for hash verification)
	mux.HandleFunc("/random/", handleRandom)

	// Delayed response
	mux.HandleFunc("/delay", handleDelay)

	// SSE streaming
	mux.HandleFunc("/stream", handleStream)

	// Chunked transfer
	mux.HandleFunc("/chunked", handleChunked)

	// Multi-value headers (Set-Cookie + custom multi-value)
	mux.HandleFunc("/multi-headers", handleMultiHeaders)

	// SHA-256 hash of request body
	mux.HandleFunc("/sha256", handleSHA256)

	// Request transfer-encoding info (用于验证 chunked 请求体归一化)
	mux.HandleFunc("/reqinfo", handleReqInfo)

	// Set-Cookie passthrough test
	mux.HandleFunc("/set-cookies", handleSetCookies)

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// WebSocket echo
	mux.HandleFunc("/ws", handleWebSocket)

	// WebSocket control: close all active connections (用于测试后端主动断开)
	mux.HandleFunc("/ws/close-all", handleWSCloseAll)

	// WebSocket server-push: 服务端主动推送 N 帧后等待客户端发送 N 帧
	// 用于测试服务端→客户端方向的二进制流
	// URL: /ws/push?count=50&size=256&type=binary|text
	mux.HandleFunc("/ws/push", handleWSPush)

	// WebSocket subprotocol: 要求特定子协议的 WS 端点（模拟 VSCode 服务器行为）
	// URL: /ws/subprotocol?protocol=vscode-ws-jsonrpc
	// 若客户端未协商指定的子协议，立即关闭连接（返回 HTTP 400）
	// 若协商成功，echo 收到的消息，同时在响应头中反映实际协商的子协议
	mux.HandleFunc("/ws/subprotocol", handleWSSubprotocol)

	// SSE race: 发送 N 个事件后立即关闭响应（不调用 Flush），
	// 制造 StreamData 和 r.Context().Done() 的竞争条件
	// URL: /stream/race?count=5&interval=0ms
	mux.HandleFunc("/stream/race", handleStreamRace)

	// SSE echo-headers: 回显收到的 Accept-Encoding，并在请求接受 gzip 时对 SSE
	// 响应做 gzip 压缩（模拟会对 SSE 压缩的目标后端）。
	// 用于验证代理是否对 SSE 请求剔除 Accept-Encoding 从源头阻止压缩。
	// URL: /stream/echo-headers
	mux.HandleFunc("/stream/echo-headers", handleStreamEchoHeaders)

	log.Printf("Test backend listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start: %v\n", err)
		os.Exit(1)
	}
}

// handleRoot returns a JSON response with request info
func handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"method":  r.Method,
		"host":    r.Host,
		"path":    r.URL.Path,
		"query":   r.URL.RawQuery,
		"backend": "hop-proxy-test",
	})
}

// handleEcho returns the POST body as-is
func handleEcho(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB limit
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Write(body)
}

// handleHeaders returns all received request headers as JSON
func handleHeaders(w http.ResponseWriter, r *http.Request) {
	headers := make(map[string]string)
	for k, vv := range r.Header {
		if len(vv) > 0 {
			headers[k] = strings.Join(vv, ", ")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"headers": headers,
	})
}

// handleCookies returns all received cookies as JSON
func handleCookies(w http.ResponseWriter, r *http.Request) {
	cookies := make(map[string]string)
	for _, c := range r.Cookies() {
		cookies[c.Name] = c.Value
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  "ok",
		"cookies": cookies,
	})
}

// handleStatus returns a specific HTTP status code
// URL format: /status/200, /status/404, /status/500, etc.
func handleStatus(w http.ResponseWriter, r *http.Request) {
	codeStr := strings.TrimPrefix(r.URL.Path, "/status/")
	code, err := strconv.Atoi(codeStr)
	if err != nil {
		code = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":  http.StatusText(code),
		"code":    code,
		"request": r.URL.Path,
	})
}

// handleRandom returns pseudo-random binary data of the specified size.
// The output is deterministic (fixed seed) so callers can verify SHA-256 hashes
// match regardless of how many hops the data passed through.
// URL format: /random/200MB, /random/10MB, /random/100KB, etc.
func handleRandom(w http.ResponseWriter, r *http.Request) {
	sizeStr := strings.TrimPrefix(r.URL.Path, "/random/")
	nbytes, err := parseSize(sizeStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(nbytes, 10))

	// Fixed seed ensures same output every time — essential for hash comparison.
	//nolint:gosec // intentionally weak RNG; this is test data, not crypto
	rng := rand.New(rand.NewSource(42))
	buf := make([]byte, 32*1024)
	remaining := nbytes
	for remaining > 0 {
		_, _ = io.ReadFull(rng, buf)
		toWrite := int64(len(buf))
		if toWrite > remaining {
			toWrite = remaining
		}
		w.Write(buf[:toWrite])
		remaining -= toWrite
	}
}

// handleLargeFile returns a file of the specified size
// URL format: /size/1MB, /size/10MB, /size/100KB, etc.
func handleLargeFile(w http.ResponseWriter, r *http.Request) {
	sizeStr := strings.TrimPrefix(r.URL.Path, "/size/")
	bytes, err := parseSize(sizeStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(bytes, 10))

	// Write in chunks to avoid excessive memory use
	buf := make([]byte, 32*1024)
	for i := range buf {
		buf[i] = byte('A' + (i % 26))
	}
	remaining := bytes
	for remaining > 0 {
		toWrite := int64(len(buf))
		if toWrite > remaining {
			toWrite = remaining
		}
		w.Write(buf[:toWrite])
		remaining -= toWrite
	}
}

// handleDelay returns a response after a specified delay
// URL format: /delay?duration=5s
func handleDelay(w http.ResponseWriter, r *http.Request) {
	durationStr := r.URL.Query().Get("duration")
	if durationStr == "" {
		durationStr = "1s"
	}
	d, err := time.ParseDuration(durationStr)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid duration"})
		return
	}

	time.Sleep(d)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":    "ok",
		"delayed":   d.String(),
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

// handleStream sends SSE events
func handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	countStr := r.URL.Query().Get("count")
	count := 5
	if countStr != "" {
		if n, err := strconv.Atoi(countStr); err == nil && n > 0 {
			count = n
		}
	}

	intervalStr := r.URL.Query().Get("interval")
	interval := 100 * time.Millisecond
	if intervalStr != "" {
		if d, err := time.ParseDuration(intervalStr); err == nil {
			interval = d
		}
	}

	payloadSizeStr := r.URL.Query().Get("payload_size")
	payloadSize := 0
	if payloadSizeStr != "" {
		if n, err := strconv.Atoi(payloadSizeStr); err == nil && n > 0 {
			payloadSize = n
		}
	}

	for i := 0; i < count; i++ {
		eventData := map[string]interface{}{
			"index":     i,
			"timestamp": time.Now().Format(time.RFC3339Nano),
		}
		if payloadSize > 0 {
			eventData["padding"] = strings.Repeat("x", payloadSize)
		}
		data, _ := json.Marshal(eventData)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		time.Sleep(interval)
	}
	fmt.Fprintf(w, "data: {\"done\":true}\n\n")
	flusher.Flush()
}

// handleStreamEchoHeaders 发送单个 SSE 事件回显收到的 Accept-Encoding，
// 并在请求声明接受 gzip 时对响应做 gzip 压缩（模拟会对 SSE 压缩的后端）。
// 用于验证代理是否对 SSE 请求剔除 Accept-Encoding 从源头阻止压缩。
func handleStreamEchoHeaders(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	acceptEncoding := r.Header.Get("Accept-Encoding")
	data, _ := json.Marshal(map[string]string{"accept_encoding": acceptEncoding})
	payload := []byte(fmt.Sprintf("data: %s\n\n", data))

	// 若请求接受 gzip，对 SSE 响应做 gzip 压缩（模拟会对 SSE 强制压缩的后端）
	if strings.Contains(acceptEncoding, "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.WriteHeader(http.StatusOK)
		gz := gzip.NewWriter(w)
		gz.Write(payload)
		gz.Flush()
		gz.Close()
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(payload)
	flusher.Flush()
}

// handleChunked sends chunked transfer encoding response
func handleChunked(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "chunked not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")

	chunks := []string{"chunk-0", "chunk-1", "chunk-2", "chunk-3", "chunk-4"}
	for i, chunk := range chunks {
		fmt.Fprintf(w, "%s\n", chunk)
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		_ = i
	}
}

// handleWebSocket echoes WebSocket messages
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	// 注册连接，用于 /ws/close-all
	wsConnsMu.Lock()
	wsConnSeq++
	connID := strconv.Itoa(wsConnSeq)
	wsConns[connID] = conn
	wsConnsMu.Unlock()

	defer func() {
		wsConnsMu.Lock()
		delete(wsConns, connID)
		wsConnsMu.Unlock()
		conn.Close(websocket.StatusNormalClosure, "")
	}()

	conn.SetReadLimit(50 * 1024 * 1024) // 50MB，与代理 ReadLimit 对齐

	ctx := r.Context()

	for {
		msgType, reader, err := conn.Reader(ctx)
		if err != nil {
			// Normal close or context cancelled
			break
		}

		data, err := io.ReadAll(io.LimitReader(reader, 1<<20))
		if err != nil {
			break
		}

		err = conn.Write(ctx, msgType, data)
		if err != nil {
			break
		}
	}
}

// handleWSCloseAll 关闭所有当前活跃的 WebSocket 连接（用于模拟后端主动断开）
func handleWSCloseAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	wsConnsMu.Lock()
	conns := make([]*websocket.Conn, 0, len(wsConns))
	for _, c := range wsConns {
		conns = append(conns, c)
	}
	wsConnsMu.Unlock()

	for _, c := range conns {
		c.Close(websocket.StatusNormalClosure, "backend closed")
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"closed": len(conns),
	})
}

// handleMultiHeaders 返回多值 Header 响应，用于验证代理是否正确透传多值头
func handleMultiHeaders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Set-Cookie", "session=abc; Path=/")
	w.Header().Add("Set-Cookie", "tracking=xyz; Path=/")
	w.Header().Add("X-Custom-Values", "val1")
	w.Header().Add("X-Custom-Values", "val2")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":       "ok",
		"cookies":      []string{"session", "tracking"},
		"multi_header": "X-Custom-Values",
	})
}

// handleSHA256 接收请求体，返回其 SHA-256 哈希，用于大文件传输完整性验证
func handleSHA256(w http.ResponseWriter, r *http.Request) {
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r.Body, 200<<20)) // 200MB limit
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"hash": hex.EncodeToString(h.Sum(nil)),
		"size": n,
	})
}

// handleSetCookies 设置多个不同属性的 Set-Cookie 头，用于验证代理对 Set-Cookie 的透传
func handleSetCookies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Add("Set-Cookie", "test1=value1; Path=/; HttpOnly")
	w.Header().Add("Set-Cookie", "test2=value2; Path=/; Secure")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"cookies_set": 2,
	})
}

// handleReqInfo 返回后端实际收到的请求传输编码信息，
// 用于验证代理是否正确将 chunked 请求体归一化为 Content-Length 定长传输。
// 后端 Go HTTP 服务器对 chunked 请求：r.ContentLength == -1、r.TransferEncoding == ["chunked"]。
// URL: /reqinfo
func handleReqInfo(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20)) // 8MB limit，容纳大 body 完整性验证
	if err != nil {
		http.Error(w, "read failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"content_length":    r.ContentLength,
		"transfer_encoding": r.TransferEncoding,
		"body_size":         len(body),
	})
}

// parseSize parses size strings like "1MB", "10MB", "100KB", "512B"
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	// 按后缀长度降序排列，确保 "MB" 在 "B" 之前匹配
	type suffixEntry struct {
		suffix string
		mult   int64
	}
	entries := []suffixEntry{
		{"GB", 1024 * 1024 * 1024},
		{"MB", 1024 * 1024},
		{"KB", 1024},
		{"B", 1},
	}

	for _, e := range entries {
		if strings.HasSuffix(s, e.suffix) {
			numStr := strings.TrimSuffix(s, e.suffix)
			num, err := strconv.ParseInt(numStr, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("invalid size: %s", s)
			}
			return num * e.mult, nil
		}
	}

	return 0, fmt.Errorf("unknown size format: %s", s)
}

// handleWSPush 服务端主动推送 N 帧（binary 或 text），然后接收客户端回复的 N 帧
// URL: /ws/push?count=50&size=256&type=binary
// 用于验证：server→client 方向的二进制流完整性（不依赖 echo 模式）
func handleWSPush(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		log.Printf("WebSocket push upgrade failed: %v", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(50 * 1024 * 1024)

	countStr := r.URL.Query().Get("count")
	count := 10
	if n, err := strconv.Atoi(countStr); err == nil && n > 0 {
		count = n
	}

	sizeStr := r.URL.Query().Get("size")
	frameSize := 256
	if n, err := strconv.Atoi(sizeStr); err == nil && n > 0 {
		frameSize = n
	}

	msgType := websocket.MessageBinary
	if r.URL.Query().Get("type") == "text" {
		msgType = websocket.MessageText
	}

	ctx := r.Context()

	// 阶段 1：服务端主动推送 count 帧，每帧携带序号（前4字节为big-endian int32）
	buf := make([]byte, frameSize)
	for i := 0; i < count; i++ {
		// 前4字节写入序号
		buf[0] = byte(i >> 24)
		buf[1] = byte(i >> 16)
		buf[2] = byte(i >> 8)
		buf[3] = byte(i)
		// 其余字节用固定模式填充
		for j := 4; j < frameSize; j++ {
			buf[j] = byte((i + j) & 0xFF)
		}
		if err := conn.Write(ctx, msgType, buf); err != nil {
			log.Printf("WebSocket push write %d failed: %v", i, err)
			return
		}
	}

	// 阶段 2：接收客户端回复的 count 帧（echo）
	for i := 0; i < count; i++ {
		_, reader, err := conn.Reader(ctx)
		if err != nil {
			log.Printf("WebSocket push read %d failed: %v", i, err)
			return
		}
		if _, err := io.ReadAll(io.LimitReader(reader, int64(frameSize)+1)); err != nil {
			log.Printf("WebSocket push readall %d failed: %v", i, err)
			return
		}
	}
}

// handleWSSubprotocol 模拟 VSCode 服务器的 WebSocket 子协议协商行为
// URL: /ws/subprotocol?protocol=vscode-ws-jsonrpc
//
// 行为：
//   - 只接受 ?protocol 参数指定的子协议
//   - 若客户端未请求该子协议，返回 HTTP 400
//   - 若协商成功，以 JSON 响应协商结果，然后 echo 后续消息
//   - 在初始响应中携带 X-Negotiated-Subprotocol 头，供测试验证
func handleWSSubprotocol(w http.ResponseWriter, r *http.Request) {
	required := r.URL.Query().Get("protocol")
	if required == "" {
		required = "vscode-ws-jsonrpc"
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
		Subprotocols:   []string{required},
	})
	if err != nil {
		// nhooyr 在子协议协商失败时会自动返回 400/错误
		log.Printf("WebSocket subprotocol upgrade failed (required=%s): %v", required, err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	conn.SetReadLimit(50 * 1024 * 1024)

	// 发送握手确认（协商到的子协议）
	negotiated := conn.Subprotocol()
	ctx := r.Context()
	handshake := map[string]string{
		"type":        "handshake",
		"subprotocol": negotiated,
		"required":    required,
		"status":      "ok",
	}
	if negotiated == "" {
		handshake["status"] = "no_subprotocol"
	}
	data, _ := json.Marshal(handshake)
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		log.Printf("WebSocket subprotocol write handshake failed: %v", err)
		return
	}

	// Echo 后续消息
	for {
		msgType, reader, err := conn.Reader(ctx)
		if err != nil {
			break
		}
		body, err := io.ReadAll(io.LimitReader(reader, 1<<20))
		if err != nil {
			break
		}
		if err := conn.Write(ctx, msgType, body); err != nil {
			break
		}
	}
}

// handleStreamRace 模拟 SSE 截断竞争场景：
// 发送 N 个事件后**立即关闭响应**（不额外 sleep），让 client 的 Read 在最后一刻被中断。
// 不调用 Flush（让数据在 HTTP buffer 里等待），制造代理端 r.Context() 和 StreamData 的竞争。
// URL: /stream/race?count=5
func handleStreamRace(w http.ResponseWriter, r *http.Request) {
	countStr := r.URL.Query().Get("count")
	count := 5
	if n, err := strconv.Atoi(countStr); err == nil && n > 0 {
		count = n
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	// 一次性写入所有事件，不 Flush（让操作系统缓冲），然后立即返回
	// 这会在 TCP 层造成一个突发写入，代理端收到这些数据的时间窗口很小
	for i := 0; i < count; i++ {
		fmt.Fprintf(w, "data: {\"index\":%d}\n\n", i)
	}
	fmt.Fprintf(w, "data: {\"done\":true}\n\n")
	// 不 Flush，让 Go HTTP server 在 handler 返回时统一刷新并关闭
}
