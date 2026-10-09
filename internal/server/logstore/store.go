// Package logstore 提供服务端运行日志的 JSONL 文件存储 + 查询时流式扫描。
//
// 设计要点：
//   - JSONL 文件作主存储（每行一条 JSON，便于 docker logs / tail -f 肉眼查看）
//   - 按天滚动：文件名 hopproxy.YYYYMMDD.log，保留天数、单日大小上限、落盘级别均可配
//   - 查询时流式扫描文件、边解码边过滤，内存占用与日志文件大小彻底解耦（#60：
//     此前的按日内存索引会把整天日志物化进内存，查看日志导致内存暴涨）
//   - 写入并发安全（sync.Mutex）；查询只读文件不持锁，与写入路径互不阻塞
package logstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Field 结构化日志字段（与 pkg/tunnel.Field 对应，独立定义避免循环依赖）
type Field struct {
	K string `json:"k"`
	V string `json:"v"`
}

// LogRecord 单条日志记录（JSONL 文件中的一行）
type LogRecord struct {
	ID        int64   `json:"id"`
	Ts        int64   `json:"ts"`     // 对齐后的毫秒时间戳
	Level     string  `json:"level"`  // critical / error / warning / info / debug
	Source    string  `json:"source"` // "host" 或客户端 name
	ClientID  string  `json:"client_id,omitempty"`
	Type      string  `json:"type"` // auth / proxy / share / api / ...
	Subdomain string  `json:"subdomain,omitempty"`
	RequestID string  `json:"request_id"` // 全链路请求关联 ID（请求无关日志为 "-"，#62）
	UserID    *int64  `json:"user_id,omitempty"`
	AppID     *int64  `json:"app_id,omitempty"` // 从 fields.app_id 提取，便于筛选
	Message   string  `json:"message"`
	Fields    []Field `json:"fields,omitempty"`
}

// QueryFilter 查询过滤条件（所有字段支持多选，空切片=不限）
// UserIDs/AppIDs 用 string 支持 "__empty__" 特殊值匹配空值
type QueryFilter struct {
	Date       string // 日志日期（YYYYMMDD，空 = 当天）
	Levels     []string
	Sources    []string
	Types      []string
	Subdomains []string
	RequestIDs []string // 全链路请求关联 ID（精确匹配，#62）
	UserIDs    []string // int64 字符串或 "__empty__"
	AppIDs     []string // int64 字符串或 "__empty__"
	Messages   []string // 消息类型筛选（精确匹配）
	Limit      int
	Offset     int
	AfterID    int64 // 增量查询：只返回 ID > AfterID 的日志（扫满 Limit 条即止，按时间倒序）
}

// QueryResult 查询结果
type QueryResult struct {
	Logs []LogRecord `json:"logs"`
}

// Store 日志存储
type Store struct {
	mu          sync.Mutex
	dir         string
	baseName    string // 不带日期和扩展名，如 "hopproxy"
	maxDays     int    // 最大保存天数（含当天）
	maxFileSize int64  // 单日文件大小上限（超出丢弃后续写入）
	minLevel    string // 落盘级别下限（空 = 全部落盘）

	currentFile *os.File
	currentDate string // 当天日期 YYYYMMDD
	currentSize int64
	dayFull     bool // 当天文件已达大小上限，后续写入丢弃
	nextID      int64

	// 筛选下拉去重值集合（#60：查询日志页不再全文件扫描收集去重值）。
	// 当天集合由写路径 O(1) 增量维护（启动时在 loadToday 的扫描中顺手构建）；
	// 历史日期文件不可变，首次查询时全量构建一次后常驻缓存。
	// 内存占用 = 去重后的值个数（很小），与日志文件大小无关。
	metaMu      sync.Mutex          // 保护 meta map 本身
	meta        map[string]*dayMeta // date → 当日值集合
	metaBuildMu sync.Mutex          // 串行化历史日期的惰性构建，防并发重复扫描

	// SSE 订阅者列表
	subs map[chan LogRecord]QueryFilter
}

// dayMeta 单日筛选下拉的去重值集合。
// 当天实例由写路径增量维护（dayMeta.mu 保护）；历史日期构建完成后只读。
type dayMeta struct {
	mu       sync.Mutex
	sources  map[string]struct{}
	types    map[string]struct{}
	messages map[string]struct{} // 消息类型（跳过空值）
	userIDs  map[int64]struct{}
	appIDs   map[int64]struct{}
}

// newDayMeta 创建空集合
func newDayMeta() *dayMeta {
	return &dayMeta{
		sources:  make(map[string]struct{}),
		types:    make(map[string]struct{}),
		messages: make(map[string]struct{}),
		userIDs:  make(map[int64]struct{}),
		appIDs:   make(map[int64]struct{}),
	}
}

// add 把一条记录并入各集合（构建期独占调用，或持有 dayMeta.mu 调用）
func (m *dayMeta) add(rec *LogRecord) {
	m.sources[rec.Source] = struct{}{}
	m.types[rec.Type] = struct{}{}
	if rec.Message != "" {
		m.messages[rec.Message] = struct{}{}
	}
	if rec.UserID != nil {
		m.userIDs[*rec.UserID] = struct{}{}
	}
	if rec.AppID != nil {
		m.appIDs[*rec.AppID] = struct{}{}
	}
}

// Option Store 配置项
type Option func(*Store)

// WithMaxFileSize 设置单日文件大小上限
func WithMaxFileSize(size int64) Option { return func(s *Store) { s.maxFileSize = size } }

// WithMaxDays 设置最大保存天数（含当天）
func WithMaxDays(n int) Option { return func(s *Store) { s.maxDays = n } }

// WithMinLevel 设置落盘级别下限（debug/info/warning/error/critical，空 = 全部）
func WithMinLevel(level string) Option { return func(s *Store) { s.minLevel = normalizeLevel(level) } }

// dateLayout 日志文件名中的日期格式（8 位数字）
const dateLayout = "20060102"

// 级别权重表（数值越大级别越高）
var levelOrder = map[string]int{
	"debug":    0,
	"info":     1,
	"warning":  2,
	"error":    3,
	"critical": 4,
}

// normalizeLevel 规范化级别字符串（非法值返回空 = 全部落盘）
func normalizeLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if _, ok := levelOrder[level]; ok {
		return level
	}
	return ""
}

// LevelEnabled 判断该级别是否落盘（供 slog Handler.Enabled 提前拦截）
func (s *Store) LevelEnabled(level string) bool {
	return s.levelAllowed(level)
}

// levelAllowed 统一收口的落盘级别判断：低于 minLevel 的级别不落盘、不推 SSE。
// 未知级别按允许处理（避免新级别被误丢）。
// 调用方无需持锁（minLevel 创建后不再变化）。
func (s *Store) levelAllowed(level string) bool {
	if s.minLevel == "" {
		return true
	}
	min, okMin := levelOrder[s.minLevel]
	cur, okCur := levelOrder[level]
	if !okMin || !okCur {
		return true
	}
	return cur >= min
}

// New 创建日志存储：清理过期/旧格式文件，从当天文件推算 nextID。
// dir 必须存在（调用方负责 os.MkdirAll）。
func New(dir, baseName string, opts ...Option) (*Store, error) {
	s := &Store{
		dir:         dir,
		baseName:    baseName,
		maxDays:     7,
		maxFileSize: 100 * 1024 * 1024,
		minLevel:    "",
		nextID:      1,
		meta:        make(map[string]*dayMeta),
		subs:        make(map[chan LogRecord]QueryFilter),
	}
	for _, o := range opts {
		o(s)
	}

	s.cleanupLegacyFiles()
	s.cleanupExpiredFiles()

	s.currentDate = today()
	if err := s.loadToday(); err != nil {
		return nil, fmt.Errorf("加载当天日志文件失败: %w", err)
	}
	if err := s.openCurrent(); err != nil {
		return nil, fmt.Errorf("打开当前日志文件失败: %w", err)
	}

	return s, nil
}

// today 返回当天的 8 位日期串
func today() string {
	return time.Now().Format(dateLayout)
}

// fileName 指定日期的日志文件名
func (s *Store) fileName(date string) string {
	return s.baseName + "." + date + ".log"
}

// currentDateName 当前活跃日志文件名
func (s *Store) currentDateName() string {
	return s.fileName(s.currentDate)
}

// parseDateFromName 从文件名中提取日期（不匹配返回空）
func (s *Store) parseDateFromName(name string) string {
	prefix := s.baseName + "."
	suffix := ".log"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return ""
	}
	date := name[len(prefix) : len(name)-len(suffix)]
	if len(date) != len(dateLayout) {
		return ""
	}
	if _, err := time.Parse(dateLayout, date); err != nil {
		return ""
	}
	return date
}

// cleanupLegacyFiles 删除旧版按大小轮转的日志文件（hopproxy.log / hopproxy.log.N）。
// 新方案按日期查询，旧格式文件无法被查询，留着只占磁盘，直接清理。
// 注意：仅在 New 中调用（slog.SetDefault 之前），此时 slog 仍写 stderr，
// 不会经 loghandler 递归回 Append；禁止在持锁路径或 SetDefault 之后调用。
func (s *Store) cleanupLegacyFiles() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		legacy := name == s.baseName+".log" ||
			(strings.HasPrefix(name, s.baseName+".log.") && s.parseDateFromName(name) == "")
		if !legacy {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
			slog.Warn("删除旧格式日志文件失败", "file", name, "error", err)
		} else {
			slog.Warn("已删除旧格式日志文件（新方案按天滚动，旧文件不再使用）", "file", name)
		}
	}
}

// cleanupExpiredFiles 删除超过保存天数的日志文件，返回成功删除的文件名。
// 保留最近 maxDays 天（含当天），YYYYMMDD 字典序即时间序，直接字符串比较。
//
// 严禁在本函数内调用 slog：本函数处于 Append → rotateDay 的持锁路径，
// slog 默认 handler 即本 Store，会递归回 Append 再次 Lock，与已持有的
// s.mu 形成同 goroutine 自死锁（#54 服务整体卡死的根因）。
// 删除结果由调用方在锁外补记日志；删除失败的文件静默跳过（下次清理重试）。
func (s *Store) cleanupExpiredFiles() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -(s.maxDays - 1)).Format(dateLayout)
	var deleted []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		date := s.parseDateFromName(e.Name())
		if date == "" || date >= cutoff {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, e.Name())); err == nil {
			deleted = append(deleted, e.Name())
		}
	}
	return deleted
}

// loadToday 流式扫描当天日志文件推算 nextID，O(1) 内存。
// 顺带在同一遍扫描中构建当天筛选下拉值集合（meta），启动后再无需为此全文件扫描。
// nextID 取「当天最大 ID + 1」与「当前毫秒时间戳」的较大值：
// 保证跨天/重启后 ID 仍全局单调递增（单日写入条数远小于一天的毫秒数）。
func (s *Store) loadToday() error {
	path := filepath.Join(s.dir, s.fileName(s.currentDate))
	meta := newDayMeta()
	if _, err := os.Stat(path); err == nil {
		var maxID int64
		if _, err := scanFileRecords(path, func(rec *LogRecord) bool {
			if rec.ID > maxID {
				maxID = rec.ID
			}
			meta.add(rec)
			return true
		}); err != nil {
			return err
		}
		if maxID+1 > s.nextID {
			s.nextID = maxID + 1
		}
		if now := NowMs(); now > s.nextID {
			s.nextID = now
		}
		if info, err := os.Stat(path); err == nil {
			s.currentSize = info.Size()
		}
	}
	// 无论文件是否存在都注册当天集合（文件不存在则注册空集合），
	// 保证写路径只需增量维护、查询路径永不触发当天全量扫描
	s.metaMu.Lock()
	s.meta[s.currentDate] = meta
	s.metaMu.Unlock()
	return nil
}

// scanFileRecords 流式扫描单个日志文件：逐行解码并回调 fn，fn 返回 false 提前终止。
// 内存 O(1)（每次只持有一行），损坏行（并发写入中的半行/历史脏数据）静默跳过。
// 文件不存在时返回 (false, nil)，由调用方决定语义。
func scanFileRecords(path string, fn func(rec *LogRecord) bool) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) == 0 && err != nil {
			if err == io.EOF {
				return true, nil
			}
			return true, err
		}
		jsonBytes := line
		if n := len(line); n > 0 && line[n-1] == '\n' {
			jsonBytes = line[:n-1]
		}
		var rec LogRecord
		if jerr := json.Unmarshal(jsonBytes, &rec); jerr == nil {
			if !fn(&rec) {
				return true, nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return true, nil
			}
			return true, err
		}
	}
}

// 反向扫描（tail 语义）的分块大小与跨块残行上限
const (
	reverseChunkSize    = 64 * 1024   // 单次向前读取的块大小
	maxReverseCarrySize = 1024 * 1024 // 跨块不完整行的携带上限，超出视为异常超长行丢弃重新同步
)

// scanFileReverse 从文件末尾向前分块扫描（tail 语义），按新 → 旧顺序回调完整行。
// fn 返回 false 提前终止。文件不存在时返回 (false, nil)。
// 查看最新一页日志只需读文件尾部几十 KB，CPU/内存与文件大小解耦（#60）。
// 末尾半行（并发写入中）解析失败自动跳过；异常超长行（无换行超过携带上限）丢弃并在下一换行处重新同步。
func scanFileReverse(path string, fn func(rec *LogRecord) bool) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return true, err
	}
	size := st.Size()
	if size == 0 {
		return true, nil
	}

	call := func(line []byte) bool {
		var rec LogRecord
		if json.Unmarshal(line, &rec) == nil {
			return fn(&rec)
		}
		return true
	}

	buf := make([]byte, reverseChunkSize)
	var carry []byte // 块首不完整行（该行开头位于更低偏移的块中），与下一块拼接
	off := size
	for off > 0 {
		n := len(buf)
		if int64(n) > off {
			n = int(off)
		}
		off -= int64(n)
		if _, rerr := f.ReadAt(buf[:n], off); rerr != nil && rerr != io.EOF {
			return true, rerr
		}

		var chunk []byte
		if len(carry) > 0 {
			// 不完整行的后半段（carry，高偏移）拼在本块之后
			chunk = make([]byte, 0, n+len(carry))
			chunk = append(chunk, buf[:n]...)
			chunk = append(chunk, carry...)
		} else {
			chunk = buf[:n]
		}

		// 从块尾向前解析完整行（新 → 旧）
		end := len(chunk)
		for end > 0 {
			idx := bytes.LastIndexByte(chunk[:end], '\n')
			if idx < 0 {
				break // 剩余前缀为不完整行
			}
			if line := chunk[idx+1 : end]; len(line) > 0 && !call(line) {
				return true, nil
			}
			end = idx
		}

		if end == 0 {
			carry = nil
		} else if end > maxReverseCarrySize {
			// 异常超长行：放弃该行（解析必然失败），在下一换行处重新同步
			carry = nil
		} else {
			carry = append(carry[:0], chunk[:end]...)
		}
	}
	// 文件首行（无前导换行）
	if len(carry) > 0 {
		call(carry)
	}
	return true, nil
}

// openCurrent 打开当前活跃日志文件（追加模式）
func (s *Store) openCurrent() error {
	p := filepath.Join(s.dir, s.currentDateName())
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	s.currentFile = f
	return nil
}

// rotateDay 切换到新一天的日志文件：关闭旧文件、清理过期文件、打开新文件。
// 返回本次清理删除的过期文件名，供调用方在锁外补记日志（内部严禁 slog，见 cleanupExpiredFiles）。
// 调用方需持有 s.mu
func (s *Store) rotateDay(newDate string) ([]string, error) {
	if s.currentFile != nil {
		if err := s.currentFile.Close(); err != nil {
			return nil, fmt.Errorf("关闭旧日志文件失败: %w", err)
		}
	}

	s.currentDate = newDate
	s.currentSize = 0
	s.dayFull = false

	deleted := s.cleanupExpiredFiles()

	if err := s.openCurrent(); err != nil {
		return deleted, err
	}

	// 注册新一天的空值集合（后续写入增量维护）；顺带清理被删过期文件的集合缓存
	s.metaMu.Lock()
	s.meta[newDate] = newDayMeta()
	for _, name := range deleted {
		if d := s.parseDateFromName(name); d != "" {
			delete(s.meta, d)
		}
	}
	s.metaMu.Unlock()
	return deleted, nil
}

// Append 写入一条日志记录（线程安全）。
// 调用方需提前填充好所有字段（ID 由 Store 自动分配）。
// 低于落盘级别下限或当天文件超限的记录会被静默丢弃。
func (s *Store) Append(rec *LogRecord) error {
	// 级别过滤统一收口：服务端 slog 与客户端上报日志都经过这里
	if !s.levelAllowed(rec.Level) {
		return nil
	}

	s.mu.Lock()
	cleaned, err := s.appendLocked(rec)
	s.mu.Unlock()

	// 锁外补记跨天清理结果：持锁路径内严禁 slog —— 默认 slog handler 即本
	// Store，会递归回 Append 再次 Lock，与已持有的 s.mu 自死锁（#54 根因）
	for _, name := range cleaned {
		slog.Info("已删除过期日志文件", "file", name)
	}
	return err
}

// appendLocked 持锁写入一条记录并通知订阅者。
// 返回跨天清理删除的过期文件名，供调用方在锁外补记日志。
// 调用方需持有 s.mu。
func (s *Store) appendLocked(rec *LogRecord) ([]string, error) {
	// 跨天切换日志文件
	var cleaned []string
	if t := today(); t != s.currentDate {
		var err error
		cleaned, err = s.rotateDay(t)
		if err != nil {
			// 不能用 slog 记录此错误（会递归进 Append），只返回错误
			return nil, fmt.Errorf("切换日志文件失败: %w", err)
		}
	}

	// 当天文件已达大小上限：静默丢弃（打日志会递归刷屏，直接丢弃）
	if s.dayFull {
		return cleaned, nil
	}

	rec.ID = s.nextID
	s.nextID++

	data, err := json.Marshal(rec)
	if err != nil {
		return cleaned, fmt.Errorf("序列化日志失败: %w", err)
	}
	data = append(data, '\n')

	if _, err := s.currentFile.Write(data); err != nil {
		return cleaned, fmt.Errorf("写入日志文件失败: %w", err)
	}

	s.currentSize += int64(len(data))

	// 检查单日大小上限
	if s.currentSize >= s.maxFileSize {
		s.dayFull = true
	}

	// 通知 SSE 订阅者（非阻塞，channel 满则跳过）
	for ch, f := range s.subs {
		if !matchFilter(rec, f) {
			continue
		}
		select {
		case ch <- *rec:
		default:
			// 订阅者消费太慢，跳过这条（避免阻塞写入）
		}
	}

	// 增量维护当天筛选下拉值集合（O(1)；正常路径集合已由 loadToday/rotateDay 注册，
	// 防御性兜底注册保证极端情况下也不静默丢值）
	s.metaMu.Lock()
	m := s.meta[s.currentDate]
	if m == nil {
		m = newDayMeta()
		s.meta[s.currentDate] = m
	}
	s.metaMu.Unlock()
	m.mu.Lock()
	m.add(rec)
	m.mu.Unlock()

	return cleaned, nil
}

// Subscribe 注册一个 SSE 订阅者，返回 channel 和取消函数。
// filter 为空时接收所有日志。
func (s *Store) Subscribe(filter QueryFilter) (<-chan LogRecord, func()) {
	ch := make(chan LogRecord, 256)
	s.mu.Lock()
	s.subs[ch] = filter
	s.mu.Unlock()
	cancel := func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// Dates 返回日志目录中真实存在的日期列表（从新到旧），供前端下拉选择。
func (s *Store) Dates() []string {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var dates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if date := s.parseDateFromName(e.Name()); date != "" {
			dates = append(dates, date)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))
	return dates
}

// resolveDate 归一化查询日期（空 = 当天）
func (s *Store) resolveDate(date string) string {
	if date != "" {
		return date
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentDate
}

// dayFilePath 指定日期（空 = 当天）对应的日志文件完整路径
func (s *Store) dayFilePath(date string) string {
	return filepath.Join(s.dir, s.fileName(s.resolveDate(date)))
}

// Query 按条件查询日志（时间倒序，最新在前）。
// filter.Date 指定日期（空 = 当天）。tail 语义：从文件尾反向扫描、边解码边过滤，
// 默认视图（Offset=0）只读文件尾部，CPU/内存 O(Offset+Limit)，与文件大小彻底解耦（#60）。
func (s *Store) Query(filter QueryFilter) (*QueryResult, error) {
	if filter.Limit <= 0 {
		filter.Limit = 100
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}

	path := s.dayFilePath(filter.Date)

	if filter.AfterID > 0 {
		// 增量模式：文件内 ID 随追加单调递增，正序收集 ID > AfterID 的
		// 前 Limit 条匹配，扫满即提前退出
		var matched []LogRecord
		if _, err := scanFileRecords(path, func(rec *LogRecord) bool {
			if rec.ID <= filter.AfterID {
				return true
			}
			if !matchFilter(rec, filter) {
				return true
			}
			matched = append(matched, *rec)
			return len(matched) < filter.Limit
		}); err != nil {
			return nil, err
		}
		sortByTsDesc(matched)
		if matched == nil {
			matched = []LogRecord{}
		}
		return &QueryResult{Logs: matched}, nil
	}

	// tail 模式（#60）：从文件尾反向扫描（新 → 旧），跳过最新 Offset 条匹配后收集一页，
	// 收满一页立即停扫。默认视图（Offset=0）只读文件尾部，与 tail 语义一致，
	// CPU/内存 O(Offset+Limit)。
	var logs []LogRecord
	seen := 0
	if _, err := scanFileReverse(path, func(rec *LogRecord) bool {
		if !matchFilter(rec, filter) {
			return true
		}
		seen++
		if seen <= filter.Offset {
			return true
		}
		logs = append(logs, *rec)
		return len(logs) < filter.Limit
	}); err != nil {
		return nil, err
	}
	sortByTsDesc(logs)
	if logs == nil {
		logs = []LogRecord{}
	}
	return &QueryResult{Logs: logs}, nil
}

// sortByTsDesc 按 ts 降序排序（最新在前），客户端日志批量上报时 ID 分配晚但 ts 可能更早
func sortByTsDesc(logs []LogRecord) {
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Ts > logs[j].Ts
	})
}

func matchFilter(e *LogRecord, f QueryFilter) bool {
	if len(f.Levels) > 0 && !containsOrEmpty(f.Levels, e.Level) {
		return false
	}
	if len(f.Sources) > 0 && !containsOrEmpty(f.Sources, e.Source) {
		return false
	}
	if len(f.Types) > 0 && !containsOrEmpty(f.Types, e.Type) {
		return false
	}
	if len(f.Subdomains) > 0 && !containsOrEmpty(f.Subdomains, e.Subdomain) {
		return false
	}
	if len(f.RequestIDs) > 0 && !contains(f.RequestIDs, e.RequestID) {
		return false
	}
	if len(f.Messages) > 0 && !contains(f.Messages, e.Message) {
		return false
	}
	if len(f.UserIDs) > 0 {
		// "__empty__" 匹配 nil user_id
		if contains(f.UserIDs, "__empty__") {
			if e.UserID == nil {
				return true
			}
		}
		if e.UserID == nil {
			return false
		}
		ok := false
		for _, x := range f.UserIDs {
			if x == "__empty__" {
				continue
			}
			if v, err := strconv.ParseInt(x, 10, 64); err == nil && v == *e.UserID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(f.AppIDs) > 0 {
		// "__empty__" 匹配 nil app_id
		if contains(f.AppIDs, "__empty__") {
			if e.AppID == nil {
				return true
			}
		}
		if e.AppID == nil {
			return false
		}
		ok := false
		for _, x := range f.AppIDs {
			if x == "__empty__" {
				continue
			}
			if v, err := strconv.ParseInt(x, 10, 64); err == nil && v == *e.AppID {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// containsOrEmpty 检查值是否在列表中，支持 __empty__ 特殊值匹配空字符串
func containsOrEmpty(list []string, v string) bool {
	for _, x := range list {
		if x == "__empty__" {
			if v == "" {
				return true
			}
			continue
		}
		if x == v {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// getDayMeta 获取指定日期的筛选下拉值集合。
// 当天集合在启动时（loadToday）注册、由写路径增量维护，直接命中；
// 历史日期文件不可变，首次查询全量构建一次后常驻缓存（并发查询同一日期时
// 后到者等待并直接命中，不重复扫描）。
func (s *Store) getDayMeta(date string) *dayMeta {
	s.metaMu.Lock()
	m := s.meta[date]
	s.metaMu.Unlock()
	if m != nil {
		return m
	}

	// 历史日期惰性构建：跨日期串行即可（管理后台低频场景），避免引入更复杂的并发控制
	s.metaBuildMu.Lock()
	defer s.metaBuildMu.Unlock()
	s.metaMu.Lock()
	m = s.meta[date]
	s.metaMu.Unlock()
	if m != nil {
		return m
	}

	m = newDayMeta()
	exists, err := scanFileRecords(filepath.Join(s.dir, s.fileName(date)), func(rec *LogRecord) bool {
		m.add(rec)
		return true
	})
	if err != nil || !exists {
		// 文件不存在或读取失败：返回空集合但不缓存，下次查询重试
		return m
	}
	s.metaMu.Lock()
	if s.meta[date] == nil {
		s.meta[date] = m
	} else {
		m = s.meta[date]
	}
	s.metaMu.Unlock()
	return m
}

// Sources 返回指定日期所有去重的 source 值（用于前端筛选下拉；date 空 = 当天）
func (s *Store) Sources(date string) []string {
	m := s.getDayMeta(s.resolveDate(date))
	m.mu.Lock()
	result := make([]string, 0, len(m.sources))
	for v := range m.sources {
		result = append(result, v)
	}
	m.mu.Unlock()
	sort.Strings(result)
	return result
}

// Types 返回指定日期所有去重的 type 值
func (s *Store) Types(date string) []string {
	m := s.getDayMeta(s.resolveDate(date))
	m.mu.Lock()
	result := make([]string, 0, len(m.types))
	for v := range m.types {
		result = append(result, v)
	}
	m.mu.Unlock()
	sort.Strings(result)
	return result
}

// MsgTypes 返回指定日期所有去重的 message 值（消息类型，用于前端筛选下拉）
func (s *Store) MsgTypes(date string) []string {
	m := s.getDayMeta(s.resolveDate(date))
	m.mu.Lock()
	result := make([]string, 0, len(m.messages))
	for v := range m.messages {
		result = append(result, v)
	}
	m.mu.Unlock()
	sort.Strings(result)
	return result
}

// UserIDs 返回指定日期日志中出现的所有 user_id（去重），用于前端按用户名筛选下拉
func (s *Store) UserIDs(date string) []int64 {
	m := s.getDayMeta(s.resolveDate(date))
	m.mu.Lock()
	result := make([]int64, 0, len(m.userIDs))
	for v := range m.userIDs {
		result = append(result, v)
	}
	m.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// AppIDs 返回指定日期日志中出现的所有 app_id（去重）
func (s *Store) AppIDs(date string) []int64 {
	m := s.getDayMeta(s.resolveDate(date))
	m.mu.Lock()
	result := make([]int64, 0, len(m.appIDs))
	for v := range m.appIDs {
		result = append(result, v)
	}
	m.mu.Unlock()
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// Close 关闭当前日志文件
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentFile != nil {
		return s.currentFile.Close()
	}
	return nil
}

// NowMs 返回当前毫秒时间戳（便于统一调用）
func NowMs() int64 {
	return time.Now().UnixMilli()
}

// ErrStoreClosed 存储已关闭
var ErrStoreClosed = errors.New("logstore: 已关闭")
