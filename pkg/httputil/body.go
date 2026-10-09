package httputil

import (
	"bytes"
	"io"
	"net/http"
)

// RequestBodyNormalizeLimit 请求 body 归一化上限（1MB）。
// body 长度未知（chunked / 无 Content-Length）时，不超过此大小的 body 会被读入内存
// 并改用 Content-Length 定长传输；超过此大小保持流式（chunked），避免大上传全量缓冲。
const RequestBodyNormalizeLimit int64 = 1 * 1024 * 1024

// NormalizeRequestBody 将 body 长度未知的请求归一化为 Content-Length 定长传输。
//
// 部分目标服务（如 Proxmox VE 的 termproxy）不支持 chunked 请求体，收到
// Transfer-Encoding: chunked 会直接返回 501 "chunked transfer encoding not supported"。
// 透明代理应尽可能以 Content-Length 发送请求体，仅在无法得知长度时才回退到 chunked。
//
// 规则：
//   - body 为 nil / NoBody 或已定长（ContentLength ≥ 0 且非 chunked）时不处理；
//   - body ≤ limit 时全量读入，设置 ContentLength，替换 Body 为可重读缓冲区
//     （空 body 置为 NoBody，避免 Go 重新按 chunked 发送）；
//   - body > limit 时保持流式，用 MultiReader 拼接已读部分与剩余流，仍按 chunked 发送。
func NormalizeRequestBody(req *http.Request, limit int64) error {
	if req.Body == nil || req.Body == http.NoBody {
		return nil
	}
	if req.ContentLength >= 0 && len(req.TransferEncoding) == 0 {
		return nil // 已定长
	}

	data, err := io.ReadAll(io.LimitReader(req.Body, limit+1))
	if err != nil {
		return err
	}

	if int64(len(data)) > limit {
		// 超过阈值：保持流式（chunked）传输，拼接已读部分与剩余流
		req.ContentLength = -1
		req.TransferEncoding = []string{"chunked"}
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), req.Body))
		return nil
	}

	// 小 body：改为 Content-Length 定长传输
	req.Header.Del("Transfer-Encoding")
	req.TransferEncoding = nil
	req.ContentLength = int64(len(data))
	if len(data) == 0 {
		req.Body = http.NoBody
	} else {
		req.Body = io.NopCloser(bytes.NewReader(data))
	}
	return nil
}
