package tunnel

import (
	"fmt"
	"io"
)

// LogEntry 单条日志条目（客户端 → 服务端）。
// 所有时间戳为毫秒级 Unix 时间；Ts 是客户端本地时间，服务端按 LogBatch.ClientNow
// 与服务端接收时间估算 offset 后对齐。
type LogEntry struct {
	Ts        int64   // 客户端本地毫秒时间戳
	Level     string  // critical / error / warning / info / debug
	Type      string  // auth / proxy / share / api / tunnel / ... 开放扩展
	Subdomain string  // 关联子域名（可空）
	RequestID string  // 全链路请求关联 ID（请求无关日志为 "-"，#62）
	UserID    *int64  // 关联用户 ID（可空）
	Message   string  // 日志正文
	Fields    []Field // 结构化键值对
}

// Field 结构化日志字段（仅 string 类型，避免 gob 编码复杂度）
type Field struct {
	K string
	V string
}

// LogBatch 客户端批量日志上报载荷。
// 流格式: [1B StreamType=0x04][WriteGob LogBatch]
// 服务端无需响应，读取完毕即关闭流。
type LogBatch struct {
	// ClientNow 客户端发送本批时的本地毫秒时间戳。
	// 服务端用它和自身接收时间估算每个客户端的时钟偏移 offset，
	// 该批所有 LogEntry.Ts 对齐时统一加 offset。
	ClientNow int64
	// Logs 一批日志条目。
	// 单批上限由客户端 flush 触发条件控制：满 100 条 / 累计 64KB / 满 1 秒。
	Logs []LogEntry
}

// LogBatchMaxBytes 单批 gob 编码后的字节上限。
// 与 yamux MaxMessageSize=256KB 对齐，留余量给 gob 帧头。
const LogBatchMaxBytes = 200 * 1024

// WriteLogBatch 在流上写入批量日志。
// 调用方负责先写 1 字节 StreamType（通常通过 WriteStreamType）。
func WriteLogBatch(w io.Writer, b *LogBatch) error {
	return WriteGob(w, b)
}

// ReadLogBatch 从流读取批量日志。
// 长度上限由 ReadGob 的 MaxGobLength 保护（64MiB），实际单批应远小于此。
func ReadLogBatch(r io.Reader) (*LogBatch, error) {
	var b LogBatch
	if err := ReadGob(r, &b); err != nil {
		return nil, fmt.Errorf("读取 LogBatch 失败: %w", err)
	}
	return &b, nil
}
