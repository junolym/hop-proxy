package agentcrypto

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"

	noise "github.com/flynn/noise"
)

// maxFramePayload 单帧最大明文长度（Noise MaxMsgLen=65535，减去 16 字节 MAC）
const maxFramePayload = noise.MaxMsgLen - 16

// NoiseConn 把握手后的 CipherState 包成 net.Conn。
// 帧格式: [4B big-endian length][ciphertext+16B MAC]
type NoiseConn struct {
	net.Conn
	enc  *noise.CipherState
	dec  *noise.CipherState
	rBuf []byte // 读缓冲（跨 Read 调用保留剩余明文）
}

func newNoiseConn(c net.Conn, enc, dec *noise.CipherState) *NoiseConn {
	return &NoiseConn{Conn: c, enc: enc, dec: dec, rBuf: make([]byte, 0, maxFramePayload)}
}

func (nc *NoiseConn) Read(p []byte) (int, error) {
	if len(nc.rBuf) == 0 {
		if err := nc.readFrame(); err != nil {
			return 0, err
		}
	}
	n := copy(p, nc.rBuf)
	nc.rBuf = nc.rBuf[n:]
	return n, nil
}

func (nc *NoiseConn) readFrame() error {
	var lenBuf [4]byte
	if _, err := io.ReadFull(nc.Conn, lenBuf[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(lenBuf[:])
	if n > noise.MaxMsgLen {
		return fmt.Errorf("帧过大: %d", n)
	}
	cipherBuf := make([]byte, n)
	if _, err := io.ReadFull(nc.Conn, cipherBuf); err != nil {
		return err
	}
	plaintext, err := nc.dec.Decrypt(nil, nil, cipherBuf)
	if err != nil {
		return fmt.Errorf("解密失败: %w", err)
	}
	nc.rBuf = append(nc.rBuf[:0], plaintext...)
	return nil
}

func (nc *NoiseConn) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maxFramePayload {
			chunk = chunk[:maxFramePayload]
		}
		ct, err := nc.enc.Encrypt(nil, nil, chunk)
		if err != nil {
			return written, err
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
		if _, err := nc.Conn.Write(lenBuf[:]); err != nil {
			return written, err
		}
		if _, err := nc.Conn.Write(ct); err != nil {
			return written, err
		}
		written += len(chunk)
		p = p[len(chunk):]
	}
	return written, nil
}

// writeFrame 写一帧握手消息（长度前缀）
func writeFrame(w io.Writer, b []byte) error {
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, err := w.Write(h[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// readFrame 读一帧握手消息
func readFrame(r io.Reader) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(h[:])
	if n > noise.MaxMsgLen {
		return nil, fmt.Errorf("帧过大: %d", n)
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}
