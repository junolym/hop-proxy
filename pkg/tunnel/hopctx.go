// HopContext：HopProxy 全链路请求上下文的唯一 wire 表示。
//
// # 协议
//
// 所有请求级上下文字段打包为一个 protobuf 编码、base64url 编码的单一请求头
// （X-Hop-Ctx，schema 见 ./tunnelpb/hopctx.proto）。规则是"读即 pop，转发即重编码"：
//
//  1. 需要读取上下文的位置调用 PopHopContext：一次完成"取值 + 剥离"
//  2. 需要把请求转发到下一跳（Path 5 首跳 / Path 6 中继 / 隧道发起端）时，
//     从内存上下文 ApplyTo 重新编码写入出站请求
//  3. 最终执行端 pop 后不再编码 —— envelope 永不落到后端服务
//
// 没有"只读不剥"或"先剥后设回"的中间态：任何读过上下文又需要继续传递的
// 位置必须显式重编码，漏编码会让下游立即失败（缺 TargetURL → 400/502），
// 而不是静默丢字段。
//
// # 兼容性约束
//
// protobuf 用字段号寻址：**字段号一旦发布不得变更/复用**（新增字段用未使用过的
// 号，删除字段保留 reserved）。字段增删向后兼容（未知字段被忽略）。协议版本号见
// Version 字段：不匹配时 PopHopContext 返回明确错误，提示对端可能未同步升级。
//
// 本文件是 X-Hop-* 头名称字面量的唯一允许位置（Makefile check-wire 守护）。
package tunnel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/robin/hop-proxy/pkg/proxycfg"
	"github.com/robin/hop-proxy/pkg/tunnel/tunnelpb"
)

const (
	// HopCtxHeader 全链路请求上下文头（唯一的请求方向带外头）
	HopCtxHeader = "X-Hop-Ctx"
	// HopCtxVersion 当前协议版本
	HopCtxVersion = 1
	// maxHopCtxValueLen base64 串长度上限（8KB），防止恶意超长头部触发解码开销
	maxHopCtxValueLen = 8 * 1024
)

// hopPeerSecretFPHeader peer 密钥指纹响应头（方向：执行端 → 服务端，不入 envelope）。
// 仅用于 #67 的密钥不匹配定位，写回浏览器前由 StreamResponse 剥离。
const hopPeerSecretFPHeader = "X-Hop-Peer-Secret-FP"

// ErrHopCtxMissing 请求未携带上下文头（公网入口的正常情况；中继跳收到则说明对端未升级）
var ErrHopCtxMissing = errors.New("缺少 " + HopCtxHeader)

// ErrHopCtxInvalid 上下文头存在但解码/版本校验失败
var ErrHopCtxInvalid = errors.New("X-Hop-Ctx 无效")

// HopContext 全链路请求上下文（wire 表示）。
// 由发起端构造，经隧道各跳 pop/re-encode 传递，最终执行端消费。
type HopContext struct {
	Version int // 协议版本（HopCtxVersion）

	// 日志关联
	RequestID    string // 全链路请求关联 ID（入口生成，各跳透传）
	Subdomain    string
	AppID        int64
	OriginalPath string // 路径改写前的原始路径（#53）

	// 路由与出站
	TargetURL    string // 目标地址（带外传递，不占用请求行）
	ProxyType    string // 出站代理类型（peer/socks5/shadowsocks）
	ProxyAddress string // 出站代理地址
	PeerSecret   string // peer 认证密钥（目标客户端的 peer_secret）
	AgentKeyUUID string // agent Transport 密钥 UUID

	// 代理限制配置（#47；LimitsSet 保证"未设置"与"显式不限(0)"可区分）
	LimitsSet      bool
	MaxBodySize    int64         // 请求体/WS 消息上限（字节，0=不限）
	ConnectTimeout time.Duration // 连接目标超时（0=不限）
	ReadTimeout    time.Duration // 读响应（连接+响应头）超时（0=不限）
}

// NewHopContext 构造带当前协议版本的上下文（发起端入口使用）。
func NewHopContext(requestID string) HopContext {
	return HopContext{Version: HopCtxVersion, RequestID: requestID}
}

// SetLimits 从代理限制配置生效值（proxycfg.Merge 产出）解析并填入限流三件套，
// 标记 LimitsSet（区别于"显式不限"与"未设置"）。
func (h *HopContext) SetLimits(cfg map[string]string) {
	h.MaxBodySize = proxycfg.SizeOf(cfg, "client_max_body_size")
	h.ConnectTimeout = proxycfg.DurationOf(cfg, "proxy_connect_timeout")
	h.ReadTimeout = proxycfg.DurationOf(cfg, "proxy_read_timeout")
	h.LimitsSet = true
}

// ApplyTo 把上下文编码写入请求头（覆盖已有值）。用于"转发前重新序列化"。
// LimitsSet 为 false（如仅携带 RequestID 的中继跳上下文）时填入默认限制值，
// 保证 wire 上限制字段总是有效值。
func (h HopContext) ApplyTo(header http.Header) error {
	if h.Version == 0 {
		h.Version = HopCtxVersion
	}
	if !h.LimitsSet {
		h.MaxBodySize = proxycfg.SizeOf(nil, "client_max_body_size")
		h.ConnectTimeout = proxycfg.DurationOf(nil, "proxy_connect_timeout")
		h.ReadTimeout = proxycfg.DurationOf(nil, "proxy_read_timeout")
	}
	encoded, err := encodeHopContext(h)
	if err != nil {
		slog.Error("HopContext 编码失败", "type", "proxy", "request_id", h.RequestID, "error", err)
		return err
	}
	header.Set(HopCtxHeader, encoded)
	return nil
}

// PopHopContext 从请求头取出上下文并立即剥离（"读出来立刻就 pop"）。
// 缺失返回 ErrHopCtxMissing（公网入口无入站上下文属正常）；存在但格式/版本
// 非法返回 ErrHopCtxInvalid（调用方应快速失败并提示对端可能未升级）。
func PopHopContext(header http.Header) (HopContext, error) {
	raw := header.Get(HopCtxHeader)
	header.Del(HopCtxHeader)
	if raw == "" {
		return HopContext{}, ErrHopCtxMissing
	}
	if len(raw) > maxHopCtxValueLen {
		return HopContext{}, fmt.Errorf("%w: 长度 %d 超过上限 %d", ErrHopCtxInvalid, len(raw), maxHopCtxValueLen)
	}
	hop, err := decodeHopContext(raw)
	if err != nil {
		return HopContext{}, err
	}
	if hop.Version != HopCtxVersion {
		return HopContext{}, fmt.Errorf("%w: 版本 %d 不受支持（本端支持 %d，对端可能未同步升级）",
			ErrHopCtxInvalid, hop.Version, HopCtxVersion)
	}
	return hop, nil
}

// LogAttrs 构造代理链路日志的公共字段（类型固定 proxy）。
func (h HopContext) LogAttrs() []any {
	return h.LogAttrsWithType("proxy")
}

// LogAttrsWithType 同 LogAttrs，但覆盖日志类型（如 SSO 相关日志用 auth）。
func (h HopContext) LogAttrsWithType(typ string) []any {
	attrs := make([]any, 0, 10)
	attrs = append(attrs, "type", typ)
	if h.RequestID != "" {
		attrs = append(attrs, "request_id", h.RequestID)
	}
	if h.Subdomain != "" {
		attrs = append(attrs, "subdomain", h.Subdomain)
	}
	if h.AppID != 0 {
		attrs = append(attrs, "app_id", h.AppID)
	}
	if h.OriginalPath != "" {
		attrs = append(attrs, "original_path", h.OriginalPath)
	}
	return attrs
}

// SetPeerSecretFP 设置 peer 密钥指纹响应头（执行端 → 服务端，#67）。
func SetPeerSecretFP(header http.Header, fp string) {
	if fp != "" {
		header.Set(hopPeerSecretFPHeader, fp)
	}
}

// PeerSecretFP 读取 peer 密钥指纹响应头。
func PeerSecretFP(header http.Header) string {
	return header.Get(hopPeerSecretFPHeader)
}

// encodeHopContext protobuf 编码 + base64url（无 padding，仅 A-Za-z0-9-_ 字符，HTTP 头安全）
func encodeHopContext(h HopContext) (string, error) {
	msg := &tunnelpb.HopContext{
		Version:          int32(h.Version),
		RequestId:        h.RequestID,
		Subdomain:        h.Subdomain,
		AppId:            h.AppID,
		OriginalPath:     h.OriginalPath,
		TargetUrl:        h.TargetURL,
		ProxyType:        h.ProxyType,
		ProxyAddress:     h.ProxyAddress,
		PeerSecret:       h.PeerSecret,
		AgentKeyUuid:     h.AgentKeyUUID,
		LimitsSet:        h.LimitsSet,
		MaxBodySize:      h.MaxBodySize,
		ConnectTimeoutMs: h.ConnectTimeout.Milliseconds(),
		ReadTimeoutMs:    h.ReadTimeout.Milliseconds(),
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("protobuf 编码失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// decodeHopContext base64url 解码 + protobuf 解码
func decodeHopContext(raw string) (HopContext, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return HopContext{}, fmt.Errorf("%w: base64 解码失败", ErrHopCtxInvalid)
	}
	var msg tunnelpb.HopContext
	if err := proto.Unmarshal(data, &msg); err != nil {
		return HopContext{}, fmt.Errorf("%w: protobuf 解码失败", ErrHopCtxInvalid)
	}
	return HopContext{
		Version:        int(msg.Version),
		RequestID:      msg.RequestId,
		Subdomain:      msg.Subdomain,
		AppID:          msg.AppId,
		OriginalPath:   msg.OriginalPath,
		TargetURL:      msg.TargetUrl,
		ProxyType:      msg.ProxyType,
		ProxyAddress:   msg.ProxyAddress,
		PeerSecret:     msg.PeerSecret,
		AgentKeyUUID:   msg.AgentKeyUuid,
		LimitsSet:      msg.LimitsSet,
		MaxBodySize:    msg.MaxBodySize,
		ConnectTimeout: time.Duration(msg.ConnectTimeoutMs) * time.Millisecond,
		ReadTimeout:    time.Duration(msg.ReadTimeoutMs) * time.Millisecond,
	}, nil
}
