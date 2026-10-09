package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/robin/hop-proxy/internal/server/db"
	"github.com/robin/hop-proxy/internal/server/logstore"
)

// logsHandler 系统日志查询 handler（admin only）
type logsHandler struct {
	store *logstore.Store
	db    *db.DB

	// username 缓存：user_id → {username, expireAt}
	// 避免每条日志都查 DB（Stream 接口高频调用）
	usernameCache sync.Map // map[int64]usernameCacheEntry
}

// usernameCacheTTL 用户名缓存 TTL（用户名变更频率低，5 分钟足够）
const usernameCacheTTL = 5 * time.Minute

type usernameCacheEntry struct {
	username string
	expireAt time.Time
}

// resolveUsername 解析 user_id → username，优先走内存缓存
// id <= 0 不查 DB（0=system, -1=guest 由调用方处理）
func (h *logsHandler) resolveUsername(id int64) string {
	if id <= 0 {
		return ""
	}
	if v, ok := h.usernameCache.Load(id); ok {
		e := v.(usernameCacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.username
		}
	}
	username := ""
	if u, err := h.db.GetUserByID(id); err == nil {
		username = u.Username
	}
	h.usernameCache.Store(id, usernameCacheEntry{
		username: username,
		expireAt: time.Now().Add(usernameCacheTTL),
	})
	return username
}

func newLogsHandler(store *logstore.Store, database *db.DB) *logsHandler {
	return &logsHandler{store: store, db: database}
}

// LogDTO 前端响应结构
type LogDTO struct {
	ID        int64            `json:"id"`
	Ts        int64            `json:"ts"`
	Level     string           `json:"level"`
	Source    string           `json:"source"`
	ClientID  string           `json:"client_id,omitempty"`
	Type      string           `json:"type"`
	Subdomain string           `json:"subdomain,omitempty"`
	RequestID string           `json:"request_id"` // 全链路请求关联 ID（请求无关日志为 "-"，#62）
	UserID    *int64           `json:"user_id,omitempty"`
	Username  string           `json:"username,omitempty"`
	AppID     *int64           `json:"app_id,omitempty"`
	Message   string           `json:"message"`
	Fields    []logstore.Field `json:"fields,omitempty"`
}

// userResolver 批量解析 user_id → username，避免 N+1 查询
// 优先走 logsHandler 的 TTL 缓存，未命中再查 DB
type userResolver struct {
	h *logsHandler
}

func newUserResolver(h *logsHandler, ids []int64) *userResolver {
	// 预热缓存：批量查 DB 并填入 usernameCache
	// 同一批日志里同一 user_id 只查一次
	for _, id := range ids {
		if _, ok := h.usernameCache.Load(id); ok {
			continue
		}
		username := ""
		if u, err := h.db.GetUserByID(id); err == nil {
			username = u.Username
		}
		h.usernameCache.Store(id, usernameCacheEntry{
			username: username,
			expireAt: time.Now().Add(usernameCacheTTL),
		})
	}
	return &userResolver{h: h}
}

func (r *userResolver) lookup(id int64) string {
	if id <= 0 {
		return ""
	}
	return r.h.resolveUsername(id)
} // List GET /api/admin/logs?level=&source=&type=&subdomain=&user_id=&limit=&offset=
// 按条件查询日志，时间倒序，最新在前
// user_id 支持多选（逗号分隔）；level/source/type/subdomain 同样支持多选
func (h *logsHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}

	q := r.URL.Query()
	filter := logstore.QueryFilter{
		Date:       q.Get("date"),
		Levels:     splitMulti(q.Get("level")),
		Sources:    splitMulti(q.Get("source")),
		Types:      splitMulti(q.Get("type")),
		Subdomains: splitMulti(q.Get("subdomain")),
		Messages:   splitMulti(q.Get("message")),
		RequestIDs: splitMulti(q.Get("request_id")),
	}

	if uidStrs := splitMulti(q.Get("user_id")); len(uidStrs) > 0 {
		// 保留原始字符串，支持 "__empty__" 特殊值
		filter.UserIDs = uidStrs
	}
	if appIDStrs := splitMulti(q.Get("app_id")); len(appIDStrs) > 0 {
		filter.AppIDs = appIDStrs
	}

	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	filter.Limit = limit
	filter.Offset = offset

	// 增量查询：after_id > 0 时只返回 ID > after_id 的日志（正序，最老在前）
	if afterIDStr := q.Get("after_id"); afterIDStr != "" {
		if aid, err := strconv.ParseInt(afterIDStr, 10, 64); err == nil {
			filter.AfterID = aid
		}
	}

	result, err := h.store.Query(filter)
	if err != nil {
		slog.ErrorContext(r.Context(), "查询日志失败", "type", "system", "error", err, "filter", filter)
		jsonError(w, http.StatusInternalServerError, "查询失败")
		return
	}

	// 收集所有 user_id 批量解析 username
	// user_id 语义：0 → system（系统日志哨兵）；-1 → guest（匿名访问）；>0 → 实际用户
	var userIDs []int64
	seen := make(map[int64]bool)
	for _, l := range result.Logs {
		if l.UserID != nil && !seen[*l.UserID] {
			seen[*l.UserID] = true
			if *l.UserID > 0 {
				userIDs = append(userIDs, *l.UserID)
			}
		}
	}
	resolver := newUserResolver(h, userIDs)

	logs := make([]LogDTO, 0, len(result.Logs))
	for _, l := range result.Logs {
		var username string
		if l.UserID != nil {
			switch *l.UserID {
			case 0:
				username = "system"
			case -1:
				username = "guest"
			default:
				if *l.UserID > 0 {
					username = resolver.lookup(*l.UserID)
					if username == "" {
						username = fmt.Sprintf("user #%d", *l.UserID)
					}
				}
			}
		}
		if username == "" {
			username = "system"
		}
		logs = append(logs, LogDTO{
			ID:        l.ID,
			Ts:        l.Ts,
			Level:     l.Level,
			Source:    l.Source,
			ClientID:  l.ClientID,
			Type:      l.Type,
			Subdomain: l.Subdomain,
			RequestID: l.RequestID,
			UserID:    l.UserID,
			Username:  username,
			AppID:     l.AppID,
			Message:   l.Message,
			Fields:    l.Fields,
		})
	}

	jsonOK(w, map[string]any{
		"logs": logs,
	})
}

// Dates GET /api/admin/logs/dates — 返回日志目录中真实存在的日期列表（从新到旧）
// 供前端日志文件下拉选择；date 格式 YYYYMMDD
func (h *logsHandler) Dates(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}
	jsonOK(w, map[string]any{
		"dates": h.store.Dates(),
	})
}

// Sources GET /api/admin/logs/sources?date= — 返回指定日期所有去重的 source 值（date 空 = 当天）
func (h *logsHandler) Sources(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}
	jsonOK(w, map[string]any{
		"sources": h.store.Sources(r.URL.Query().Get("date")),
	})
}

// Types GET /api/admin/logs/types?date= — 返回指定日期所有去重的 type 值
func (h *logsHandler) Types(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}
	jsonOK(w, map[string]any{
		"types": h.store.Types(r.URL.Query().Get("date")),
	})
}

// MsgTypes GET /api/admin/logs/msg-types?date= — 返回指定日期所有去重的 message 值（消息类型）
func (h *logsHandler) MsgTypes(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}
	jsonOK(w, map[string]any{
		"messages": h.store.MsgTypes(r.URL.Query().Get("date")),
	})
}

// Levels 返回固定的日志级别列表
func (h *logsHandler) Levels(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"levels": []string{"critical", "error", "warning", "info", "debug"},
	})
}

// Users GET /api/admin/logs/users — 返回日志中出现的所有 user_id + username
// 用于前端按用户名筛选：返回 [{id, username}, ...]
// user_id 语义：0→system（哨兵），-1→guest，>0→实际用户
func (h *logsHandler) Users(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}
	ids := h.store.UserIDs(r.URL.Query().Get("date"))
	type userItem struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	users := make([]userItem, 0, len(ids)+2)
	// 始终包含 system 和 guest 两个特殊项
	hasSystem := false
	hasGuest := false
	for _, id := range ids {
		switch id {
		case 0:
			hasSystem = true
			continue
		case -1:
			hasGuest = true
			continue
		}
		username := h.resolveUsername(id)
		users = append(users, userItem{ID: id, Username: username})
	}
	prefix := make([]userItem, 0, 2)
	if hasSystem {
		prefix = append(prefix, userItem{ID: 0, Username: "system"})
	}
	if hasGuest {
		prefix = append(prefix, userItem{ID: -1, Username: "guest"})
	}
	users = append(prefix, users...)
	jsonOK(w, map[string]any{
		"users": users,
	})
}

// splitMulti 把逗号分隔的查询参数切成切片（空字符串返回 nil）
// 例如 "info,debug" → ["info", "debug"]
func splitMulti(s string) []string {
	if s == "" {
		return nil
	}
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				result = append(result, s[start:i])
			}
			start = i + 1
		}
	}
	return result
}

// Stream GET /api/admin/logs/stream — SSE 流式推送新日志
// 客户端通过 EventSource 订阅，服务端有新日志时立即推送。
// 支持筛选参数（与 List 相同），只推送符合条件的日志。
func (h *logsHandler) Stream(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		jsonError(w, http.StatusServiceUnavailable, "日志系统未初始化")
		return
	}

	// 检查是否支持 flush
	flusher, ok := w.(http.Flusher)
	if !ok {
		slog.ErrorContext(r.Context(), "不支持流式响应", "type", "api")
		jsonError(w, http.StatusInternalServerError, "不支持流式响应")
		return
	}

	q := r.URL.Query()
	filter := logstore.QueryFilter{
		Levels:     splitMulti(q.Get("level")),
		Sources:    splitMulti(q.Get("source")),
		Types:      splitMulti(q.Get("type")),
		Subdomains: splitMulti(q.Get("subdomain")),
		Messages:   splitMulti(q.Get("message")),
		RequestIDs: splitMulti(q.Get("request_id")),
	}
	if uidStrs := splitMulti(q.Get("user_id")); len(uidStrs) > 0 {
		filter.UserIDs = uidStrs
	}
	if appIDStrs := splitMulti(q.Get("app_id")); len(appIDStrs) > 0 {
		filter.AppIDs = appIDStrs
	}

	// 订阅
	ch, cancel := h.store.Subscribe(filter)
	defer cancel()

	// SSE 头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx 禁用缓冲
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// 心跳定时器（每 15 秒发一个注释行，保持连接不被代理超时断开）
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case rec, ok := <-ch:
			if !ok {
				return
			}
			// 解析 username：0→system, -1→guest, >0→查 DB（带 TTL 缓存）
			username := "system"
			if rec.UserID != nil {
				switch *rec.UserID {
				case 0:
					username = "system"
				case -1:
					username = "guest"
				default:
					if *rec.UserID > 0 {
						if name := h.resolveUsername(*rec.UserID); name != "" {
							username = name
						} else {
							username = fmt.Sprintf("user #%d", *rec.UserID)
						}
					}
				}
			}
			dto := LogDTO{
				ID: rec.ID, Ts: rec.Ts, Level: rec.Level, Source: rec.Source,
				ClientID: rec.ClientID, Type: rec.Type, Subdomain: rec.Subdomain,
				RequestID: rec.RequestID,
				UserID:    rec.UserID, Username: username, AppID: rec.AppID,
				Message: rec.Message, Fields: rec.Fields,
			}
			data, err := json.Marshal(dto)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
