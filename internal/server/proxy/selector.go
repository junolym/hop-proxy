package proxy

import "github.com/robin/hop-proxy/internal/server/db"

// ClientSelection 客户端选择结果
type ClientSelection struct {
	ClientID          string
	TargetURL         string
	ProxyType         string
	ProxyAddress      string
	ProxyPassword     string
	ProxyTargetClient string // peer 类型的目标客户端 ID
	PeerSecret        string // peer 密钥（目标客户端的 peer_secret）
}

// selectClient 根据策略选择一个可用的客户端及其目标地址
func (p *Proxy) selectClient(appID int64, loadBalance bool, clients []db.ClientWithTarget, appTargetURL string) ClientSelection {
	if loadBalance {
		// 负载均衡模式：round robin
		var online []db.ClientWithTarget
		for _, c := range clients {
			if p.isClientAvailable(c.ClientID) {
				online = append(online, c)
			}
		}
		if len(online) == 0 {
			return ClientSelection{}
		}
		// round robin：使用全局计数器
		idx := int(p.rrCounter.Add(1)-1) % len(online)
		c := online[idx]
		targetURL := c.TargetURL
		if targetURL == "" {
			targetURL = appTargetURL
		}
		return ClientSelection{
			ClientID:          c.ClientID,
			TargetURL:         targetURL,
			ProxyType:         c.ProxyType,
			ProxyAddress:      c.ProxyAddress,
			ProxyPassword:     c.ProxyPassword,
			ProxyTargetClient: c.ProxyTargetClient,
			PeerSecret:        c.PeerSecret,
		}
	}

	// 主备模式：按优先级顺序取第一个可用的
	for _, c := range clients {
		if p.isClientAvailable(c.ClientID) {
			targetURL := c.TargetURL
			if targetURL == "" {
				targetURL = appTargetURL
			}
			return ClientSelection{
				ClientID:          c.ClientID,
				TargetURL:         targetURL,
				ProxyType:         c.ProxyType,
				ProxyAddress:      c.ProxyAddress,
				ProxyPassword:     c.ProxyPassword,
				ProxyTargetClient: c.ProxyTargetClient,
				PeerSecret:        c.PeerSecret,
			}
		}
	}
	return ClientSelection{}
}

const maxClientRTT = 5000 // 客户端最大可接受 RTT（毫秒）

// isClientAvailable 检查客户端是否可用（__host__ 始终可用）
// 在线且最近心跳 RTT 未超时才视为可用
func (p *Proxy) isClientAvailable(clientID string) bool {
	if clientID == HostClientID {
		return true
	}
	online, rtt := p.hub.GetConnHealth(clientID)
	return online && rtt < maxClientRTT
}

// SelectClient 根据策略选择一个可用的客户端及其目标地址（公开方法）
func (p *Proxy) SelectClient(appID int64, loadBalance bool, clients []db.ClientWithTarget, appTargetURL string) ClientSelection {
	return p.selectClient(appID, loadBalance, clients, appTargetURL)
}
