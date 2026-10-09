package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// EnsureWSSSecureTLS 确保 wss:// 连接跳过 TLS 证书验证（兼容自签证书，如 PVE）。
// 仅在未设置自定义 HTTPClient 时生效（避免覆盖 SOCKS5/Shadowsocks 代理的 Transport 配置）。
func EnsureWSSSecureTLS(opts *websocket.DialOptions, targetURL string) {
	if strings.HasPrefix(targetURL, "wss://") && opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	}
}

// WriteWSFrame 在流上写入一帧 WS 消息。
// 格式: [1B frameType(0=Binary/1=Text/2=Close)][4B BE payloadLen][payload]
func WriteWSFrame(w io.Writer, frameType byte, payload []byte) error {
	var header [5]byte
	header[0] = frameType
	binary.BigEndian.PutUint32(header[1:5], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadWSFrame 从流读取一帧 WS 消息。
// 返回 frameType(0=Binary/1=Text/2=Close) 和 payload。
// payloadLen 超过 MaxWSFramePayload 时返回错误，防止远端声明超大长度触发 OOM。
func ReadWSFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	frameType := header[0]
	payloadLen := binary.BigEndian.Uint32(header[1:5])
	if payloadLen > MaxWSFramePayload {
		return 0, nil, fmt.Errorf("WS 帧 payload 长度 %d 超过上限 %d", payloadLen, MaxWSFramePayload)
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return frameType, payload, nil
}

// BridgeWS 在 yamux 流和 WebSocket 连接之间双向桥接。
//
// goroutine A: ws.Read → WriteWSFrame(stream)   浏览器/目标 → 流
// goroutine B: ReadWSFrame(stream) → ws.Write   流 → 浏览器/目标
//
// 任一方向出错或关闭时，关闭另一方向并退出。
// WS 帧类型（Text/Binary）通过 WSFrame 前缀保留，不使用 NetConn（避免类型检查关闭连接）。
func BridgeWS(ctx context.Context, stream io.ReadWriteCloser, ws *websocket.Conn) {
	done := make(chan struct{})

	// goroutine A: WS → stream
	go func() {
		defer close(done)
		for {
			msgType, data, err := ws.Read(ctx)
			if err != nil {
				slog.Debug("BridgeWS: WS→stream 退出", "type", "proxy", "error", err, "data_len", len(data))
				WriteWSFrame(stream, WSFrameClose, nil)
				return
			}
			frameType := WSFrameBinary
			if msgType == websocket.MessageText {
				frameType = WSFrameText
			}
			if err := WriteWSFrame(stream, frameType, data); err != nil {
				slog.Debug("BridgeWS: WS→stream 写入失败", "type", "proxy", "error", err)
				return
			}
		}
	}()

	// goroutine B: stream → WS
	for {
		frameType, payload, err := ReadWSFrame(stream)
		if err != nil {
			slog.Debug("BridgeWS: stream→WS 退出", "type", "proxy", "error", err, "frame_type", frameType)
			break
		}
		if frameType == WSFrameClose {
			slog.Debug("BridgeWS: 收到 Close 帧", "type", "proxy")
			break
		}
		wsMsgType := websocket.MessageBinary
		if frameType == WSFrameText {
			wsMsgType = websocket.MessageText
		}
		writeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := ws.Write(writeCtx, wsMsgType, payload); err != nil {
			cancel()
			slog.Debug("BridgeWS: stream→WS 写入失败", "type", "proxy", "error", err)
			break
		}
		cancel()
	}

	ws.Close(websocket.StatusNormalClosure, "")
	stream.Close()
	<-done
}
