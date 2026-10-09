// Package agentcrypto 提供 agent 安全代理的密钥生成与 Noise 握手封装。
//
// 使用 Noise_KN 模式（Curve25519 + ChaChaPoly + BLAKE2b）：
//   - caller（server/client）持静态私钥，作为 initiator
//   - agent 预知 caller 公钥（从 /pubkey/{uuid} 拉取），作为 responder
//   - agent 完全不持任何密钥（连临时的都不需要）
//   - 握手 2 条消息（1 RTT），通过 se DH 运算直接认证 caller，无需额外校验
package agentcrypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"

	noise "github.com/flynn/noise"
	"golang.org/x/crypto/curve25519"
)

// GenerateKeyPairHex 生成 Curve25519 密钥对，返回 (privateHex, publicHex, error)
func GenerateKeyPairHex() (string, string, error) {
	priv, pub, err := generateKeyPair()
	if err != nil {
		return "", "", err
	}
	return hex.EncodeToString(priv), hex.EncodeToString(pub), nil
}

// LoadPrivateKey 从 hex 私钥派生密钥对
func LoadPrivateKey(privHex string) (noise.DHKey, error) {
	priv, err := hex.DecodeString(privHex)
	if err != nil {
		return noise.DHKey{}, fmt.Errorf("解码私钥 hex 失败: %w", err)
	}
	if len(priv) != 32 {
		return noise.DHKey{}, fmt.Errorf("私钥长度错误: 期望 32 字节, 实际 %d", len(priv))
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return noise.DHKey{}, fmt.Errorf("派生公钥失败: %w", err)
	}
	return noise.DHKey{Private: priv, Public: pub}, nil
}

// LoadPublicKey 从 hex 公钥加载
func LoadPublicKey(pubHex string) ([]byte, error) {
	pub, err := hex.DecodeString(pubHex)
	if err != nil {
		return nil, fmt.Errorf("解码公钥 hex 失败: %w", err)
	}
	if len(pub) != 32 {
		return nil, fmt.Errorf("公钥长度错误: 期望 32 字节, 实际 %d", len(pub))
	}
	return pub, nil
}

func generateKeyPair() (priv, pub []byte, err error) {
	priv = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		return nil, nil, err
	}
	pub, err = curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return nil, nil, err
	}
	return priv, pub, nil
}

// noiseCipherSuite Noise_KN: Curve25519 + ChaChaPoly + BLAKE2b
var noiseCipherSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2b)

// NoisePrologue 协议版本标识，防止降级攻击
var NoisePrologue = []byte("hop-proxy-agent-v1")

// DialAndHandshake 作为 initiator（caller 侧）：在已建立的 TCP 连接上执行 Noise_KN 握手。
// staticKey 是 caller 的静态密钥对（私钥）。
// 返回加密后的 net.Conn。握手 1 RTT。
func DialAndHandshake(conn net.Conn, staticKey noise.DHKey) (*NoiseConn, error) {
	state, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseCipherSuite,
		Random:        rand.Reader,
		Pattern:       noise.HandshakeKN,
		Initiator:     true,
		Prologue:      NoisePrologue,
		StaticKeypair: staticKey,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Noise initiator 失败: %w", err)
	}

	// KN msg 1: -> e
	msg1, _, _, err := state.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("写 Noise msg1 失败: %w", err)
	}
	if err := writeFrame(conn, msg1); err != nil {
		return nil, fmt.Errorf("发送 Noise msg1 失败: %w", err)
	}

	// KN msg 2: <- e, ee, se（握手完成，se 运算认证 caller）
	// ReadMessage 返回 (cs1, cs2)：cs1 = initiator→responder（发送），cs2 = responder→initiator（接收）
	msg2, err := readFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("接收 Noise msg2 失败: %w", err)
	}
	_, cs1, cs2, err := state.ReadMessage(nil, msg2)
	if err != nil {
		return nil, fmt.Errorf("读 Noise msg2 失败: %w", err)
	}
	if cs1 == nil || cs2 == nil {
		return nil, fmt.Errorf("Noise 握手未完成")
	}
	return newNoiseConn(conn, cs1, cs2), nil
}

// AcceptAndHandshake 作为 responder（agent 侧）：在已接受的 TCP 连接上执行 Noise_KN 握手。
// expectedCallerPubKey 是 caller 静态公钥（从 /pubkey/{uuid} 拉取），作为 PeerStatic 预置。
// agent 完全不持任何密钥；se DH 运算会直接认证 caller 持有对应私钥，伪造者无法通过。
func AcceptAndHandshake(conn net.Conn, expectedCallerPubKey []byte) (*NoiseConn, error) {
	state, err := noise.NewHandshakeState(noise.Config{
		CipherSuite: noiseCipherSuite,
		Random:      rand.Reader,
		Pattern:     noise.HandshakeKN,
		Initiator:   false,
		Prologue:    NoisePrologue,
		PeerStatic:  expectedCallerPubKey,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Noise responder 失败: %w", err)
	}

	// KN msg 1: -> e
	msg1, err := readFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("接收 Noise msg1 失败: %w", err)
	}
	if _, _, _, err := state.ReadMessage(nil, msg1); err != nil {
		return nil, fmt.Errorf("读 Noise msg1 失败: %w", err)
	}

	// KN msg 2: <- e, ee, se（握手完成）
	// noise WriteMessage/ReadMessage 返回 (c1, c2)：
	//   c1 = initiator→responder 方向
	//   c2 = responder→initiator 方向
	// responder 的发送 cipher = c2，接收 cipher = c1
	msg2, cs1, cs2, err := state.WriteMessage(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("写 Noise msg2 失败: %w", err)
	}
	if err := writeFrame(conn, msg2); err != nil {
		return nil, fmt.Errorf("发送 Noise msg2 失败: %w", err)
	}
	if cs1 == nil || cs2 == nil {
		return nil, fmt.Errorf("Noise 握手未完成")
	}
	return newNoiseConn(conn, cs2, cs1), nil
}
