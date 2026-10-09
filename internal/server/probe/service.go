// Package probe 实现应用可用性定时探测。
//
// 探测策略（每分钟 tick）：
//   - 禁用的应用跳过
//   - 模糊匹配应用跳过（保持现状，固定显示 可用 / 离线）
//   - 从未探测过的应用：立刻探测
//   - 上次探测成功（available）：
//     · 距今 < 30min：跳过
//     · 距今 ∈ [30min, 60min)：5% 概率探测
//     · 距今 > 60min：立刻探测
//   - 上次探测非成功（error/failed/timeout/offline）：立刻探测
//
// 探测过程和详细失败原因通过日志打印。数据库只保存最后一次探测的状态。
package probe

import (
	"context"
	"log/slog"
	"math/rand"
	"time"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/proxy"
)

// Service 应用探测调度服务
type Service struct {
	db    *db.DB
	proxy *proxy.Proxy
}

// NewService 创建探测调度服务
func NewService(database *db.DB, p *proxy.Proxy) *Service {
	return &Service{db: database, proxy: p}
}

// Run 启动定时探测，每分钟执行一次策略判断。阻塞至 ctx 取消。
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	slog.Info("应用可用性探测已启动", "type", "probe", "interval", "1m")

	for {
		select {
		case <-ctx.Done():
			slog.Info("应用可用性探测已停止", "type", "probe")
			return
		case <-ticker.C:
			s.probeAll(ctx)
		}
	}
}

// probeAll 遍历所有需要探测的应用并执行探测
func (s *Service) probeAll(ctx context.Context) {
	apps, err := s.db.ListAllEnabledAppsForProbe()
	if err != nil {
		slog.Error("加载待探测应用列表失败", "type", "probe", "error", err)
		return
	}

	now := time.Now()
	for _, app := range apps {
		// 单个应用探测失败不应中断其他应用
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !s.shouldProbe(app, now) {
			continue
		}

		s.probeOne(ctx, app)
	}
}

// shouldProbe 根据策略判断应用是否需要在本次 tick 探测
func (s *Service) shouldProbe(app db.App, now time.Time) bool {
	ps := app.ProbeStatus
	if ps == nil {
		// 从未探测过 → 立刻探测
		return true
	}

	elapsed := now.Sub(ps.CheckedAt)
	switch ps.Status {
	case db.ProbeStatusAvailable:
		// 成功状态按时间衰减探测
		if elapsed < 30*time.Minute {
			return false
		}
		if elapsed < 60*time.Minute {
			// 5% 概率探测
			return rand.Intn(100) < 5
		}
		return true
	default:
		// 非成功（error/failed/timeout/offline）→ 立刻探测
		return true
	}
}

// probeOne 执行单个应用的探测并写入 DB
func (s *Service) probeOne(ctx context.Context, app db.App) {
	// 单次探测超时控制（与 Proxy.ProbeApp 内部 timeout 双保险）
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result := s.proxy.ProbeApp(probeCtx, app.ID)

	status := db.AppProbeStatus{
		Status:    result.Status,
		CheckedAt: time.Now(),
	}
	if result.StatusCode > 0 {
		status.StatusCode = result.StatusCode
	}
	if result.Detail != "" {
		status.Detail = result.Detail
	}

	if err := s.db.UpdateAppProbeStatus(app.ID, status); err != nil {
		slog.Error("写入探测状态失败", "type", "probe", "app_id", app.ID, "error", err)
	}

	// 详细结果写日志（失败时 Warn，成功时 Debug）
	if result.Status == db.ProbeStatusAvailable {
		slog.Debug("探测成功", "type", "probe",
			"app_id", app.ID,
			"subdomain", app.Subdomain,
			"status_code", result.StatusCode,
		)
	} else {
		slog.Warn("探测失败", "type", "probe",
			"app_id", app.ID,
			"subdomain", app.Subdomain,
			"status", result.Status,
			"status_code", result.StatusCode,
			"detail", result.Detail,
		)
	}
}
