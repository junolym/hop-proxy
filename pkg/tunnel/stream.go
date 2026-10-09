package tunnel

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"time"
)

// 流类型标识（每条流的第一字节）
const (
	StreamHTTP byte = 0x01 // HTTP 代理（请求+响应）
	StreamWS   byte = 0x02 // WebSocket 代理
	StreamCtrl byte = 0x03 // 控制消息（GetApps/VerifySession/ProxyStatus/Kick/AppsChanged）
	StreamLog  byte = 0x04 // 客户端批量日志上报（LogBatch）
)

// MaxGobLength gob 消息长度上限（64MiB），防止远端声明超大长度触发 OOM。
// 覆盖 CtrlMessage 等控制消息。正常控制消息远小于此值。
const MaxGobLength = 64 * 1024 * 1024

// MaxWSFramePayload WS 帧单帧 payload 上限（64MiB），与 MaxGobLength 对齐。
// 外部 WebSocket 消息已有 50MiB SetReadLimit，此上限保护隧道内部逻辑帧。
const MaxWSFramePayload = 64 * 1024 * 1024

// WS 帧类型（用于 ws_stream.go 的帧分帧）
const (
	WSFrameBinary byte = 0 // 对应 websocket.MessageBinary
	WSFrameText   byte = 1 // 对应 websocket.MessageText
	WSFrameClose  byte = 2 // 连接关闭信号
)

// WriteStreamType 写入 1 字节流类型标识（流的第一字节）。
func WriteStreamType(w io.Writer, t byte) error {
	_, err := w.Write([]byte{t})
	return err
}

// ReadStreamType 读取 1 字节流类型标识。
func ReadStreamType(r io.Reader) (byte, error) {
	buf := make([]byte, 1)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, err
	}
	return buf[0], nil
}

// StreamTypeReadTimeout 读取流类型标识的超时，防止"开流但不发首字节"的流
// 永久占用 handler goroutine。
const StreamTypeReadTimeout = 30 * time.Second

// ReadStreamTypeWithTimeout 带超时读取流类型标识。
// 对支持 SetReadDeadline 的流（yamux.Stream）设置超时，超时即返回错误，
// 调用方应关闭流回收资源。
func ReadStreamTypeWithTimeout(r io.Reader, timeout time.Duration) (byte, error) {
	if s, ok := r.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = s.SetReadDeadline(time.Now().Add(timeout))
		defer s.SetReadDeadline(time.Time{})
	}
	return ReadStreamType(r)
}

// WriteGob 将 v gob 编码后写入 w，带 4 字节长度前缀。
// 长度前缀确保解码器不会预读后续 body 数据。
func WriteGob(w io.Writer, v interface{}) error {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return fmt.Errorf("gob 编码失败: %w", err)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(buf.Len()))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// ReadGob 从 r 读取 4 字节长度前缀 + gob 数据，解码到 v。
// 只读取恰好够 gob 解码的字节，不会预读后续数据。
// 长度超过 MaxGobLength 时返回错误，防止远端声明超大长度触发 OOM。
func ReadGob(r io.Reader, v interface{}) error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return fmt.Errorf("读取 gob 长度失败: %w", err)
	}
	length := binary.BigEndian.Uint32(lenBuf[:])
	if length > MaxGobLength {
		return fmt.Errorf("gob 长度 %d 超过上限 %d", length, MaxGobLength)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return fmt.Errorf("读取 gob 数据失败: %w", err)
	}
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(v); err != nil {
		return fmt.Errorf("gob 解码失败: %w", err)
	}
	return nil
}

// WriteGobToBytes 将 v gob 编码为带 4 字节长度前缀的字节切片。
// 用于在 yamux 之前通过原始 WS 消息发送（如 peer 认证）。
func WriteGobToBytes(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, fmt.Errorf("gob 编码失败: %w", err)
	}
	var result bytes.Buffer
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(buf.Len()))
	result.Write(lenBuf[:])
	result.Write(buf.Bytes())
	return result.Bytes(), nil
}

// ReadGobFromBytes 从带 4 字节长度前缀的字节切片解码 gob 数据。
func ReadGobFromBytes(data []byte, v interface{}) error {
	if len(data) < 4 {
		return fmt.Errorf("数据太短")
	}
	length := binary.BigEndian.Uint32(data[0:4])
	if uint32(len(data)) < 4+length {
		return fmt.Errorf("数据不完整")
	}
	return gob.NewDecoder(bytes.NewReader(data[4 : 4+length])).Decode(v)
}
