// Package apps 管理客户端本地代理所需的应用列表。
package apps

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/robin/hop-proxy/internal/client/tunnel"
	"github.com/robin/hop-proxy/internal/server/db"
	subdomainpkg "github.com/robin/hop-proxy/pkg/subdomain"
	pkgTunnel "github.com/robin/hop-proxy/pkg/tunnel"
)

// Manager 管理客户端关联的应用列表
type Manager struct {
	mu          sync.RWMutex
	apps        map[string]*db.ClientAppInfo
	proxyDomain string
	adminDomain string
	client      *tunnel.Client

	// 全局子域名注册表 (#57)：所有 enabled 应用的 (id, subdomain)，
	// 用于复刻服务端解析（全局精确优先 → 全局模糊按字面量排序），
	// 防止本地模糊模式吞并其他客户端的精确子域名应用。
	// registryLoaded=false 表示旧 server 未下发注册表，回退本地模糊兜底。
	registryLoaded bool
	globalExact    map[string]int64   // 精确子域名 → appID
	globalFuzzy    []globalFuzzyEntry // 全部模糊模式（含本客户端的）
}

// globalFuzzyEntry 注册表中的模糊模式条目（已预编译）
type globalFuzzyEntry struct {
	appID   int64
	pattern string
	pat     *subdomainpkg.Pattern
	litLen  int
}

// NewManager 创建应用管理器
func NewManager(client *tunnel.Client) *Manager {
	return &Manager{
		apps:   make(map[string]*db.ClientAppInfo),
		client: client,
	}
}

// GetApp 按子域名查找应用，返回深拷贝与（模糊匹配命中的）捕获组。
//
// 解析语义与服务端 GetAppClientsWithTarget 保持一致 (#57)：
//  1. 本地精确命中 → 本地处理（它就是全局精确赢家）
//  2. 全局存在精确 app 但不在本地（如挂在其他客户端）→ 返回 nil，
//     由调用方 forwardToServer 交服务端解析——本地模糊模式不得吞并
//  3. 全局无精确 app → 全局模糊按 (字面量字符数 DESC, id ASC) 选最优，
//     赢家是本客户端的 app 才本地处理，否则返回 nil 交服务端
//  4. 未收到注册表（旧 server 兼容）→ 回退本地模糊兜底（原行为）
//
// 第二个返回值为模糊匹配命中的捕获组（精确匹配或未命中时为 nil）。
func (m *Manager) GetApp(subdomain string) (*db.ClientAppInfo, []string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// 1. 精确匹配
	if app, ok := m.apps[subdomain]; ok {
		return cloneApp(app), nil
	}

	// 2. 有全局注册表时复刻服务端解析
	if m.registryLoaded {
		// 全局存在精确 app（且不在本地，上面已排除）→ 交服务端
		if _, ok := m.globalExact[subdomain]; ok {
			return nil, nil
		}
		// 全局模糊排序选赢家
		var best *globalFuzzyEntry
		var bestCaptures []string
		for i := range m.globalFuzzy {
			e := &m.globalFuzzy[i]
			if captures := e.pat.Match(subdomain); captures != nil {
				if best == nil || e.litLen > best.litLen || (e.litLen == best.litLen && e.appID < best.appID) {
					best = e
					bestCaptures = captures
				}
			}
		}
		if best == nil {
			return nil, nil // 全局无命中 → 交服务端（404 由服务端返回）
		}
		// 赢家是本客户端的 app 才本地处理，否则交服务端
		if app, ok := m.apps[best.pattern]; ok {
			return cloneApp(app), bestCaptures
		}
		return nil, nil
	}

	// 3. 兼容旧 server（无注册表）：本地模糊兜底
	// 收集所有命中候选，按 (字面量字符数 DESC, id ASC) 选最优
	type candidate struct {
		id       int64
		key      string
		litLen   int
		captures []string
	}
	var cs []candidate
	for k, app := range m.apps {
		if !subdomainpkg.IsFuzzy(k) {
			continue
		}
		pat, err := subdomainpkg.Parse(k)
		if err != nil {
			continue
		}
		if captures := pat.Match(subdomain); captures != nil {
			cs = append(cs, candidate{id: app.ID, key: k, litLen: pat.LiteralLen(), captures: captures})
		}
	}
	if len(cs) == 0 {
		return nil, nil
	}
	best := cs[0]
	for _, c := range cs[1:] {
		if c.litLen > best.litLen || (c.litLen == best.litLen && c.id < best.id) {
			best = c
		}
	}
	return cloneApp(m.apps[best.key]), best.captures
}

// cloneApp 返回 ClientAppInfo 的深拷贝，避免调用方临时修改造成 data race
// （localproxy 在路由规则覆盖时会修改 app.TargetURL）。
func cloneApp(app *db.ClientAppInfo) *db.ClientAppInfo {
	cp := *app
	if app.Routes != nil {
		cp.Routes = append([]db.AppRoute(nil), app.Routes...)
	}
	if app.Redirects != nil {
		cp.Redirects = append([]db.AppRedirect(nil), app.Redirects...)
	}
	if app.CustomHeaders != nil {
		cp.CustomHeaders = make(map[string]string, len(app.CustomHeaders))
		for k, v := range app.CustomHeaders {
			cp.CustomHeaders[k] = v
		}
	}
	return &cp
}

func (m *Manager) GetDomains() (proxyDomain, adminDomain string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.proxyDomain, m.adminDomain
}

// ctrlRequestTimeout 控制请求响应超时，防止服务端不响应时 goroutine 永久阻塞
const ctrlRequestTimeout = 30 * time.Second

// Refresh 从服务端刷新应用列表（同步：开流 → 发 get_apps → 读响应）。
// 使用 read deadline 防止服务端不响应时 goroutine 永久阻塞。
func (m *Manager) Refresh() error {
	stream, err := m.client.OpenStream()
	if err != nil {
		return err
	}
	defer stream.Close()

	// 设置 read deadline，防止服务端不响应时永久阻塞
	_ = stream.SetReadDeadline(time.Now().Add(ctrlRequestTimeout))
	defer stream.SetReadDeadline(time.Time{})

	if err := pkgTunnel.WriteCtrlRequest(stream, &pkgTunnel.CtrlMessage{
		Action: pkgTunnel.ActionGetApps,
	}); err != nil {
		return err
	}

	resp, err := pkgTunnel.ReadCtrlMessage(stream)
	if err != nil {
		return err
	}

	if resp.Reason != "" {
		slog.Error("获取应用列表失败", "type", "tunnel", "reason", resp.Reason)
		return err
	}

	var apps []db.ClientAppInfo
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &apps); err != nil {
			slog.Error("解析应用列表失败", "type", "tunnel", "error", err)
			return err
		}
	}

	m.mu.Lock()
	m.proxyDomain = resp.ProxyDomain
	m.adminDomain = resp.AdminDomain
	m.apps = make(map[string]*db.ClientAppInfo)
	for i := range apps {
		m.apps[apps[i].Subdomain] = &apps[i]
	}
	m.parseRegistryLocked(resp.SubdomainRegistry)
	m.mu.Unlock()

	slog.Debug("应用列表已更新", "type", "tunnel", "count", len(apps), "proxy_domain", resp.ProxyDomain)
	return nil
}

// parseRegistryLocked 解析全局子域名注册表（调用方必须持有写锁）。
// registry 为空表示旧 server 未下发，保留回退行为；条目按精确/模糊分类存储。
func (m *Manager) parseRegistryLocked(registry []byte) {
	m.registryLoaded = false
	m.globalExact = nil
	m.globalFuzzy = nil
	if len(registry) == 0 {
		return
	}
	var refs []db.AppSubdomainRef
	if err := json.Unmarshal(registry, &refs); err != nil {
		slog.Error("解析全局子域名注册表失败", "type", "tunnel", "error", err)
		return
	}
	m.registryLoaded = true
	m.globalExact = make(map[string]int64, len(refs))
	for _, r := range refs {
		if subdomainpkg.IsFuzzy(r.Subdomain) {
			pat, err := subdomainpkg.Parse(r.Subdomain)
			if err != nil {
				continue // 跳过无效模式（与服务端防御逻辑一致）
			}
			m.globalFuzzy = append(m.globalFuzzy, globalFuzzyEntry{
				appID:   r.ID,
				pattern: r.Subdomain,
				pat:     pat,
				litLen:  pat.LiteralLen(),
			})
		} else {
			m.globalExact[r.Subdomain] = r.ID
		}
	}
	slog.Debug("全局子域名注册表已更新", "type", "tunnel", "refs", len(refs), "exact", len(m.globalExact), "fuzzy", len(m.globalFuzzy))
}

// StartSync 启动定期同步
func (m *Manager) StartSync(ctx context.Context, onConnected func() bool) {
	for {
		if onConnected() {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}

	m.Refresh()

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if onConnected() {
				m.Refresh()
			}
		}
	}
}
