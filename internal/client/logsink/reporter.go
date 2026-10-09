package logsink

import (
	"encoding/json"
	"fmt"
	"io"

	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// Reporter 通过 yamux StreamLog 流上报日志批次到服务端
type Reporter struct {
	// openStream 函数：返回可写的 yamux 流
	// 由调用方注入（通常是 tunnel.Client.OpenStream）
	openStream func() (io.WriteCloser, error)
}

// NewReporter 创建上报器
// openStream 由调用方注入：通常是 client.OpenStream
func NewReporter(openStream func() (io.WriteCloser, error)) *Reporter {
	return &Reporter{openStream: openStream}
}

// Send 上报一批日志
//   - 开流失败（隧道断开）→ 返回错误，调用方保留 buffer
//   - 写入失败 → 返回错误，调用方保留 buffer
//   - 成功 → 关闭流，返回 nil
func (r *Reporter) Send(batch *pkgTunnel.LogBatch) error {
	if r.openStream == nil {
		return fmt.Errorf("reporter 未初始化")
	}

	stream, err := r.openStream()
	if err != nil {
		return fmt.Errorf("开流失败: %w", err)
	}
	defer stream.Close()

	if err := pkgTunnel.WriteStreamType(stream, pkgTunnel.StreamLog); err != nil {
		return fmt.Errorf("写 StreamType 失败: %w", err)
	}

	if err := pkgTunnel.WriteLogBatch(stream, batch); err != nil {
		return fmt.Errorf("写 LogBatch 失败: %w", err)
	}

	return nil
}

// jsonMarshal 包装 json.Marshal，避免在 sink.go 顶部 import json
func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}
