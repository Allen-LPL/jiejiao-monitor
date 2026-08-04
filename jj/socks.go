package main

import (
	"compress/gzip"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// ==================== 股票监控配置结构 ====================

// StockMonitorConfig 股票监控配置
type StockMonitorConfig struct {
	Enabled                   bool              `json:"enabled"`
	BatchQuoteURL             string            `json:"batch_quote_url"`              // 批量行情URL（完整URL含md5参数）
	RealtimeQuoteURL          string            `json:"realtime_quote_url"`           // 实时行情基础URL
	RebalancingURL            string            `json:"rebalancing_url"`              // 调仓历史URL（完整URL含md5参数）
	Cookie                    string            `json:"cookie"`                       // 雪球Cookie（如需要）
	PollIntervalMs            int               `json:"poll_interval_ms"`             // 行情轮询间隔(ms)
	RebalancingPollIntervalMs int               `json:"rebalancing_poll_interval_ms"` // 调仓轮询间隔(ms)
	NotifyCooldownMinutes     int               `json:"notify_cooldown_minutes"`      // 通知冷却时间(分钟)
	DBPath                    string            `json:"db_path"`                      // SQLite数据库路径
	Thresholds                []ThresholdConfig `json:"thresholds"`                   // 多级阈值
	ReportIntervalMinutes     int               `json:"report_interval_minutes"`      // 定期盈亏播报间隔(分钟)，0=关闭
	Positions                 []PositionConfig  `json:"positions"`                    // 持仓成本/股数，用于盈亏计算
	KlineEnabled              bool              `json:"kline_enabled"`                // 是否在提醒中附带K线图
	KlineCharts               []KlineChart      `json:"kline_charts"`                 // K线图列表(分时/日K等)，空则用默认(分时+日K)
	VolChartEnabled           bool              `json:"vol_chart_enabled"`            // 是否附带自渲染的成交量变化柱状图
	ChartPublicBase           string            `json:"chart_public_base"`            // 图表公网基址(钉钉用)，如 https://yx.ysfaedu.com/jjchart
}

// PositionConfig 持仓配置
type PositionConfig struct {
	Symbol string  `json:"symbol"`
	Cost   float64 `json:"cost"`   // 成本价
	Shares int     `json:"shares"` // 持股数，0 表示仅观察
}

// ThresholdConfig 阈值配置
type ThresholdConfig struct {
	Level    string  `json:"level"`    // warning, alert, urgent
	Percent  float64 `json:"percent"`  // 触发百分比（绝对值）
	Template string  `json:"template"` // 飞书卡片模板颜色
}

// ==================== 雪球API响应结构 ====================

// BatchQuoteResponse 批量行情响应
type BatchQuoteResponse struct {
	Data struct {
		Items     []StockItem `json:"items"`
		ItemsSize int         `json:"items_size"`
	} `json:"data"`
	ErrorCode        int     `json:"error_code"`
	ErrorDescription *string `json:"error_description"`
}

// StockItem 单只股票数据
type StockItem struct {
	Market StockMarket `json:"market"`
	Quote  StockQuote  `json:"quote"`
}

// StockMarket 市场状态
type StockMarket struct {
	StatusID int    `json:"status_id"`
	Status   string `json:"status"`
	Region   string `json:"region"`
}

// StockQuote 股票行情
type StockQuote struct {
	Symbol             string  `json:"symbol"`
	Code               string  `json:"code"`
	Name               string  `json:"name"`
	Current            float64 `json:"current"`
	Percent            float64 `json:"percent"`
	Chg                float64 `json:"chg"`
	High               float64 `json:"high"`
	Low                float64 `json:"low"`
	Open               float64 `json:"open"`
	LastClose          float64 `json:"last_close"`
	Volume             int64   `json:"volume"`
	Amount             float64 `json:"amount"`
	TurnoverRate       float64 `json:"turnover_rate"`
	Amplitude          float64 `json:"amplitude"`
	Timestamp          int64   `json:"timestamp"`
	MarketCapital      float64 `json:"market_capital"`
	FloatMarketCapital float64 `json:"float_market_capital"`
	CurrentYearPercent float64 `json:"current_year_percent"`
	AvgPrice           float64 `json:"avg_price"`
}

// RealtimeQuoteResponse 实时行情响应
type RealtimeQuoteResponse struct {
	Data             []RealtimeQuote `json:"data"`
	ErrorCode        int             `json:"error_code"`
	ErrorDescription *string         `json:"error_description"`
}

// RealtimeQuote 实时行情数据
type RealtimeQuote struct {
	Symbol       string  `json:"symbol"`
	Current      float64 `json:"current"`
	Percent      float64 `json:"percent"`
	Chg          float64 `json:"chg"`
	Timestamp    int64   `json:"timestamp"`
	Volume       int64   `json:"volume"`
	Amount       float64 `json:"amount"`
	High         float64 `json:"high"`
	Low          float64 `json:"low"`
	Open         float64 `json:"open"`
	LastClose    float64 `json:"last_close"`
	AvgPrice     float64 `json:"avg_price"`
	TurnoverRate float64 `json:"turnover_rate"`
	Amplitude    float64 `json:"amplitude"`
	IsTrade      bool    `json:"is_trade"`
}

// RebalancingResponse 调仓历史响应
type RebalancingResponse struct {
	Count      int                 `json:"count"`
	Page       int                 `json:"page"`
	TotalCount int                 `json:"totalCount"`
	List       []RebalancingRecord `json:"list"`
	MaxPage    int                 `json:"maxPage"`
}

// RebalancingRecord 调仓记录
type RebalancingRecord struct {
	ID                   int64                `json:"id"`
	Status               string               `json:"status"`
	Category             string               `json:"category"`
	CreatedAt            int64                `json:"created_at"`
	UpdatedAt            int64                `json:"updated_at"`
	Cash                 float64              `json:"cash"`
	RebalancingHistories []RebalancingHistory `json:"rebalancing_histories"`
	Comment              string               `json:"comment"`
}

// RebalancingHistory 调仓详情
type RebalancingHistory struct {
	ID               int64    `json:"id"`
	StockName        string   `json:"stock_name"`
	StockSymbol      string   `json:"stock_symbol"`
	Weight           float64  `json:"weight"`
	TargetWeight     float64  `json:"target_weight"`
	PrevWeight       *float64 `json:"prev_weight"`
	PrevTargetWeight *float64 `json:"prev_target_weight"`
	Price            *float64 `json:"price"`
	Volume           float64  `json:"volume"`
	NetValue         float64  `json:"net_value"`
}

// ==================== 股票监控器 ====================

// StockMonitor 股票监控器
type StockMonitor struct {
	config            StockMonitorConfig
	db                *sql.DB
	httpClient        *http.Client
	lastRebalancingID int64 // 记录最近一次调仓ID，检测变动
	lastPriceMu       sync.Mutex
	lastPriceBySymbol map[string]float64
	pendingRecord     *RebalancingRecord
	rebalancingCount  int
	nextRebalancingAt time.Time
}

// NewStockMonitor 创建股票监控器
func NewStockMonitor(cfg StockMonitorConfig) (*StockMonitor, error) {
	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("打开SQLite数据库失败: %w", err)
	}

	monitor := &StockMonitor{
		config:            cfg,
		db:                db,
		httpClient:        &http.Client{Timeout: 10 * time.Second},
		lastPriceBySymbol: make(map[string]float64),
	}

	if err := monitor.initDB(); err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}

	return monitor, nil
}

// initDB 初始化数据库表
func (m *StockMonitor) initDB() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS stocks (
			symbol TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled INTEGER DEFAULT 1
		)`,
		`CREATE TABLE IF NOT EXISTS thresholds (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT,
			level TEXT NOT NULL,
			percent REAL NOT NULL,
			template TEXT DEFAULT 'orange'
		)`,
		`CREATE TABLE IF NOT EXISTS price_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			name TEXT,
			current REAL,
			percent REAL,
			chg REAL,
			high REAL,
			low REAL,
			open REAL,
			last_close REAL,
			volume INTEGER,
			amount REAL,
			turnover_rate REAL,
			amplitude REAL,
			timestamp INTEGER,
			source TEXT DEFAULT 'batch',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS notifications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			symbol TEXT NOT NULL,
			level TEXT NOT NULL,
			alert_type TEXT DEFAULT 'threshold',
			day_key TEXT,
			session TEXT,
			percent REAL,
			message TEXT,
			sent_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_price_history_symbol ON price_history(symbol, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_notifications_symbol ON notifications(symbol, level, sent_at)`,
	}

	for _, q := range queries {
		if _, err := m.db.Exec(q); err != nil {
			return fmt.Errorf("执行SQL失败 [%s]: %w", q[:50], err)
		}
	}

	if err := m.ensureNotificationColumns(); err != nil {
		return err
	}

	log.Println("[股票监控] 数据库初始化完成")
	return nil
}

func (m *StockMonitor) ensureNotificationColumns() error {
	rows, err := m.db.Query(`PRAGMA table_info(notifications)`)
	if err != nil {
		return fmt.Errorf("读取notifications表结构失败: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("解析notifications表结构失败: %w", err)
		}
		cols[name] = true
	}

	if !cols["alert_type"] {
		if _, err := m.db.Exec("ALTER TABLE notifications ADD COLUMN alert_type TEXT DEFAULT 'threshold'"); err != nil {
			return fmt.Errorf("增加alert_type字段失败: %w", err)
		}
	}
	if !cols["day_key"] {
		if _, err := m.db.Exec("ALTER TABLE notifications ADD COLUMN day_key TEXT"); err != nil {
			return fmt.Errorf("增加day_key字段失败: %w", err)
		}
	}
	if !cols["session"] {
		if _, err := m.db.Exec("ALTER TABLE notifications ADD COLUMN session TEXT"); err != nil {
			return fmt.Errorf("增加session字段失败: %w", err)
		}
	}

	return nil
}

// Close 关闭监控器
func (m *StockMonitor) Close() error {
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// Start 启动监控
func (m *StockMonitor) Start() {
	log.Printf("[股票监控] 启动，行情轮询间隔 %dms，调仓轮询间隔 %dms",
		m.config.PollIntervalMs, m.config.RebalancingPollIntervalMs)

	// 初始化调仓状态
	m.initRebalancingState()

	// 启动行情监控协程
	go m.monitorPrices()

	// 启动调仓监控协程
	go m.monitorRebalancing()

	// 启动定期盈亏播报协程
	if m.config.ReportIntervalMinutes > 0 {
		go m.monitorReport()
	}
}

// ==================== HTTP请求 ====================

// doRequest 发送HTTP请求并返回响应体
func (m *StockMonitor) doRequest(reqURL string) ([]byte, error) {
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	if m.config.Cookie != "" {
		req.Header.Set("Cookie", m.config.Cookie)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, gzErr := gzip.NewReader(resp.Body)
		if gzErr != nil {
			return nil, fmt.Errorf("解压gzip失败: %w", gzErr)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	return body, nil
}

// fetchBatchQuotes 获取批量行情数据
func (m *StockMonitor) fetchBatchQuotes() (*BatchQuoteResponse, error) {
	body, err := m.doRequest(m.config.BatchQuoteURL)
	if err != nil {
		return nil, fmt.Errorf("获取批量行情失败: %w", err)
	}

	var result BatchQuoteResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析批量行情JSON失败: %w", err)
	}

	if result.ErrorCode != 0 {
		desc := ""
		if result.ErrorDescription != nil {
			desc = *result.ErrorDescription
		}
		return nil, fmt.Errorf("批量行情API返回错误: code=%d, desc=%s", result.ErrorCode, desc)
	}

	return &result, nil
}

// fetchRealtimeQuote 获取单只股票实时行情
func (m *StockMonitor) fetchRealtimeQuote(symbol string) (*RealtimeQuote, error) {
	ts := time.Now().UnixMilli()
	reqURL := fmt.Sprintf("%s?symbol=%s&_=%d", m.config.RealtimeQuoteURL, symbol, ts)

	body, err := m.doRequest(reqURL)
	if err != nil {
		return nil, fmt.Errorf("获取实时行情失败 [%s]: %w", symbol, err)
	}

	var result RealtimeQuoteResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析实时行情JSON失败 [%s]: %w", symbol, err)
	}

	if result.ErrorCode != 0 {
		desc := ""
		if result.ErrorDescription != nil {
			desc = *result.ErrorDescription
		}
		return nil, fmt.Errorf("实时行情API返回错误 [%s]: code=%d, desc=%s", symbol, result.ErrorCode, desc)
	}

	if len(result.Data) == 0 {
		return nil, fmt.Errorf("实时行情无数据 [%s]", symbol)
	}

	return &result.Data[0], nil
}

// fetchRebalancing 获取调仓历史
func (m *StockMonitor) fetchRebalancing() (*RebalancingResponse, error) {
	body, err := m.doRequest(m.config.RebalancingURL)
	if err != nil {
		return nil, fmt.Errorf("获取调仓历史失败: %w", err)
	}

	var result RebalancingResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析调仓历史JSON失败: %w", err)
	}

	return &result, nil
}

// ==================== 行情监控 ====================

// monitorPrices 行情监控主循环
func (m *StockMonitor) monitorPrices() {
	interval := time.Duration(m.config.PollIntervalMs) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		m.checkPrices()
	}
}

// checkPrices 检查股票行情
func (m *StockMonitor) checkPrices() {
	// 1. 获取批量行情
	batchResp, err := m.fetchBatchQuotes()
	if err != nil {
		log.Printf("[股票监控] %v", err)
		return
	}

	for _, item := range batchResp.Data.Items {
		quote := item.Quote
		m.checkRapidChange(quote)

		// 2. 获取实时行情进行交叉验证
		realtime, err := m.fetchRealtimeQuote(quote.Symbol)
		if err != nil {
			log.Printf("[股票监控] 交叉验证失败: %v", err)
		} else {
			// 数据交叉验证：如果两个接口价格偏差超过1%，记录警告
			if realtime.Current > 0 && quote.Current > 0 {
				priceDiff := math.Abs(realtime.Current-quote.Current) / quote.Current * 100
				if priceDiff > 1.0 {
					log.Printf("[股票监控] 数据交叉验证警告 [%s]: 批量=%.2f 实时=%.2f 偏差=%.2f%%",
						quote.Symbol, quote.Current, realtime.Current, priceDiff)
				}
			}
		}

		// 3. 保存行情到数据库
		m.savePriceHistory(quote, "batch")

		// 4. 更新股票信息
		m.upsertStock(quote.Symbol, quote.Name)

		// 5. 检查阈值并发送通知
		m.checkThresholds(quote)
	}
}

// savePriceHistory 保存行情到数据库
func (m *StockMonitor) savePriceHistory(q StockQuote, source string) {
	_, err := m.db.Exec(`INSERT INTO price_history 
		(symbol, name, current, percent, chg, high, low, open, last_close, volume, amount, turnover_rate, amplitude, timestamp, source) 
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		q.Symbol, q.Name, q.Current, q.Percent, q.Chg, q.High, q.Low, q.Open, q.LastClose,
		q.Volume, q.Amount, q.TurnoverRate, q.Amplitude, q.Timestamp, source)
	if err != nil {
		log.Printf("[股票监控] 保存行情失败 [%s]: %v", q.Symbol, err)
	}
}

// upsertStock 更新或插入股票信息
func (m *StockMonitor) upsertStock(symbol, name string) {
	_, err := m.db.Exec(`INSERT INTO stocks (symbol, name) VALUES (?, ?) 
		ON CONFLICT(symbol) DO UPDATE SET name=excluded.name`, symbol, name)
	if err != nil {
		log.Printf("[股票监控] 更新股票信息失败 [%s]: %v", symbol, err)
	}
}

// checkThresholds 检查阈值并触发通知
func (m *StockMonitor) checkThresholds(q StockQuote) {
	absPercent := math.Abs(q.Percent)
	session := currentSessionName(time.Now().In(getLocation()))
	dayKey := time.Now().In(getLocation()).Format("2006-01-02")

	// 从高到低检查阈值，只触发最高级别
	for i := len(m.config.Thresholds) - 1; i >= 0; i-- {
		threshold := m.config.Thresholds[i]
		if absPercent >= threshold.Percent {
			if m.shouldNotifyByQuota(q.Symbol, threshold.Level, dayKey, session) {
				log.Printf("[股票监控] 触发阈值 [%s] %s: 涨跌幅=%.2f%% 阈值=%.2f%%",
					q.Symbol, threshold.Level, q.Percent, threshold.Percent)
				m.sendStockAlert(q, threshold)
				m.recordNotification(q.Symbol, threshold.Level, "threshold", q.Percent, session, dayKey, "")
			}
			break // 只触发最高级别
		}
	}
}

func (m *StockMonitor) shouldNotifyByQuota(symbol, level, dayKey, session string) bool {
	maxCount := levelLimit(level)
	if maxCount <= 0 {
		return false
	}

	var count int
	err := m.db.QueryRow(`SELECT COUNT(*) FROM notifications
		WHERE symbol = ? AND level = ? AND alert_type = 'threshold' AND day_key = ? AND session = ?`,
		symbol, level, dayKey, session).Scan(&count)
	if err != nil {
		log.Printf("[股票监控] 查询阈值提醒次数失败: %v", err)
		return true
	}
	return count < maxCount
}

// recordNotification 记录通知发送
func (m *StockMonitor) recordNotification(symbol, level, alertType string, percent float64, session, dayKey, message string) {
	_, err := m.db.Exec(`INSERT INTO notifications (symbol, level, alert_type, day_key, session, percent, message) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		symbol, level, alertType, dayKey, session, percent, message)
	if err != nil {
		log.Printf("[股票监控] 记录通知失败: %v", err)
	}
}

func (m *StockMonitor) checkRapidChange(q StockQuote) {
	m.lastPriceMu.Lock()
	last, ok := m.lastPriceBySymbol[q.Symbol]
	m.lastPriceBySymbol[q.Symbol] = q.Current
	m.lastPriceMu.Unlock()

	if !ok || last <= 0 || q.Current <= 0 {
		return
	}

	changePercent := (q.Current - last) / last * 100
	if math.Abs(changePercent) < 5 {
		return
	}

	threshold := ThresholdConfig{Level: "rapid", Percent: 5, Template: "red"}
	log.Printf("[股票监控] 检测到快速变动 [%s]: 上次=%.2f 当前=%.2f 变动=%.2f%%", q.Symbol, last, q.Current, changePercent)
	m.sendRapidAlert(q, threshold, changePercent, last)
	m.recordNotification(q.Symbol, threshold.Level, "rapid", changePercent, "all_day", time.Now().In(getLocation()).Format("2006-01-02"), "快速变动")
}

func (m *StockMonitor) sendRapidAlert(q StockQuote, threshold ThresholdConfig, rapidChange, lastPrice float64) {
	if !cluster.ShouldSend("股票快速变动[" + q.Symbol + "]") {
		return
	}
	for _, cfg := range config.Notifiers {
		if !cfg.Enabled {
			continue
		}
		go func(cfg NotifierConfig) {
			var err error
			switch cfg.Type {
			case "feishu":
				msg := m.buildFeishuRapidCard(q, threshold, rapidChange, lastPrice, cfg)
				err = sendWebhookJSON(cfg.Webhook, msg)
			case "dingtalk":
				msg := m.buildDingtalkRapidMessage(q, threshold, rapidChange, lastPrice, cfg)
				err = sendDingtalkWebhook(cfg, msg)
			}
			if err != nil {
				log.Printf("[股票监控] 快速变动通知 [%s] 发送失败: %v", cfg.Name, err)
			}
		}(cfg)
	}
}

func levelLimit(level string) int {
	switch level {
	case "warning":
		return 1
	case "alert", "urgent":
		return 3
	default:
		return 0
	}
}

func currentSessionName(now time.Time) string {
	if now.Hour() < 12 {
		return "morning"
	}
	return "afternoon"
}

// ==================== 定期盈亏播报 ====================

// isTradingTime A股交易时段：周一至周五 9:30-11:30、13:00-15:00
func isTradingTime(now time.Time) bool {
	if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	hm := now.Hour()*60 + now.Minute()
	return (hm >= 9*60+30 && hm <= 11*60+30) || (hm >= 13*60 && hm <= 15*60)
}

// monitorReport 定期盈亏播报主循环（仅交易时段发送）
func (m *StockMonitor) monitorReport() {
	log.Printf("[股票监控] 定期盈亏播报已启用，间隔 %d 分钟", m.config.ReportIntervalMinutes)
	// 启动时若在交易时段，先播报一次
	if isTradingTime(time.Now().In(getLocation())) {
		m.sendPeriodicReport()
	}
	ticker := time.NewTicker(time.Duration(m.config.ReportIntervalMinutes) * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		if !isTradingTime(time.Now().In(getLocation())) {
			continue
		}
		m.sendPeriodicReport()
	}
}

// positionFor 查找某股票的持仓配置
func (m *StockMonitor) positionFor(symbol string) *PositionConfig {
	for i := range m.config.Positions {
		if m.config.Positions[i].Symbol == symbol {
			return &m.config.Positions[i]
		}
	}
	return nil
}

// sendPeriodicReport 播报所有监控股票的现价与持仓盈亏
func (m *StockMonitor) sendPeriodicReport() {
	batchResp, err := m.fetchBatchQuotes()
	if err != nil {
		log.Printf("[股票监控] 盈亏播报获取行情失败: %v", err)
		return
	}
	items := batchResp.Data.Items
	if len(items) == 0 {
		return
	}
	if !cluster.ShouldSend("持仓盈亏播报") {
		return
	}

	for _, cfg := range config.Notifiers {
		if !cfg.Enabled {
			continue
		}
		go func(cfg NotifierConfig) {
			var err error
			switch cfg.Type {
			case "feishu":
				msg := m.buildFeishuReportCard(items, cfg)
				err = sendWebhookJSON(cfg.Webhook, msg)
			case "dingtalk":
				msg := m.buildDingtalkReportMessage(items, cfg)
				err = sendDingtalkWebhook(cfg, msg)
			}
			if err != nil {
				log.Printf("[股票监控] 盈亏播报 [%s] 发送失败: %v", cfg.Name, err)
			}
		}(cfg)
	}
	log.Printf("[股票监控] 已发送定期盈亏播报，共 %d 只股票", len(items))
}

// reportStockLine 生成单只股票的播报文本（含持仓盈亏），multiline
func (m *StockMonitor) reportStockLine(q StockQuote) (string, float64, bool) {
	// 标题可点击 → 打开行情图页(分时/日K/周K/成交量)
	titleText := fmt.Sprintf("%s (%s)", q.Name, q.Symbol)
	if u := chartPageURL(q.Symbol); u != "" {
		titleText = fmt.Sprintf("[%s](%s)", titleText, u)
	}
	line := fmt.Sprintf("**%s**\n现价 ¥%.2f    今日 %+.2f%%", titleText, q.Current, q.Percent)
	if desc := monthHighDesc(q.Symbol, q.Current); desc != "" {
		line += "\n" + desc
	}
	if p := m.positionFor(q.Symbol); p != nil && p.Shares > 0 && p.Cost > 0 {
		pnl := (q.Current - p.Cost) * float64(p.Shares)
		pnlPct := (q.Current - p.Cost) / p.Cost * 100
		line += fmt.Sprintf("\n持仓 %d 股 @ ¥%.3f    市值 ¥%.2f\n盈亏 **%+.2f%%**（**%+.2f 元**）",
			p.Shares, p.Cost, q.Current*float64(p.Shares), pnlPct, pnl)
		return line, pnl, true
	}
	return line, 0, false
}

// buildFeishuReportCard 构建飞书定期盈亏播报卡片
func (m *StockMonitor) buildFeishuReportCard(items []StockItem, cfg NotifierConfig) FeishuCardMessage {
	timestamp := time.Now().Unix()
	sign, _ := genFeishuSign(cfg.Secret, timestamp)
	now := time.Now().In(getLocation()).Format("2006-01-02 15:04:05")

	elements := []CardElement{}
	totalPnl := 0.0
	hasPosition := false
	for _, it := range items {
		q := it.Quote
		line, pnl, hasPos := m.reportStockLine(q)
		totalPnl += pnl
		if hasPos {
			hasPosition = true
		}
		elements = append(elements, CardElement{Tag: "div", Text: &CardText{Tag: "lark_md", Content: line}})
		elements = append(elements, klineFeishuElements(q.Symbol)...)
		elements = append(elements, volChartFeishuElements(q.Symbol, q.Name)...)
		elements = append(elements, CardElement{Tag: "hr"})
	}
	if hasPosition {
		elements = append(elements, CardElement{
			Tag:  "div",
			Text: &CardText{Tag: "lark_md", Content: fmt.Sprintf("**总盈亏：%+.2f 元**", totalPnl)},
		})
	}
	elements = append(elements, CardElement{
		Tag:      "note",
		Elements: []CardText{{Tag: "plain_text", Content: "定时播报 | " + now}},
	})

	template := "green"
	if totalPnl < 0 {
		template = "red"
	}
	return FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header:   CardHeader{Title: CardText{Content: "📊 持仓盈亏播报", Tag: "plain_text"}, Template: template},
			Elements: elements,
		},
	}
}

// buildDingtalkReportMessage 构建钉钉定期盈亏播报消息
func (m *StockMonitor) buildDingtalkReportMessage(items []StockItem, cfg NotifierConfig) DingtalkMarkdownMessage {
	now := time.Now().In(getLocation()).Format("2006-01-02 15:04:05")
	var b strings.Builder
	b.WriteString("### 📊 持仓盈亏播报\n\n")
	totalPnl := 0.0
	hasPosition := false
	for _, it := range items {
		q := it.Quote
		line, pnl, hasPos := m.reportStockLine(q)
		totalPnl += pnl
		if hasPos {
			hasPosition = true
		}
		b.WriteString(strings.ReplaceAll(line, "\n", "\n\n"))
		b.WriteString(klineDingtalkMarkdown(q.Symbol))
		b.WriteString(volChartDingtalk(q.Symbol, q.Name))
		b.WriteString("\n\n---\n\n")
	}
	if hasPosition {
		fmt.Fprintf(&b, "**总盈亏：%+.2f 元**\n\n", totalPnl)
	}
	b.WriteString("> 定时播报 | " + now)

	return DingtalkMarkdownMessage{
		MsgType:  "markdown",
		Markdown: DingtalkMarkdown{Title: "持仓盈亏播报", Text: b.String()},
	}
}

// ==================== 调仓监控 ====================

// initRebalancingState 初始化调仓状态
func (m *StockMonitor) initRebalancingState() {
	resp, err := m.fetchRebalancing()
	if err != nil {
		log.Printf("[股票监控] 初始化调仓状态失败: %v", err)
		return
	}

	if len(resp.List) > 0 {
		m.lastRebalancingID = resp.List[0].ID
		m.pendingRecord = nil
		m.rebalancingCount = 0
		m.nextRebalancingAt = time.Time{}
		log.Printf("[股票监控] 调仓监控初始化完成，最新调仓ID: %d", m.lastRebalancingID)
	}
}

// monitorRebalancing 调仓监控主循环
func (m *StockMonitor) monitorRebalancing() {
	interval := time.Duration(m.config.RebalancingPollIntervalMs) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		m.checkRebalancing()
	}
}

// checkRebalancing 检查调仓变动
func (m *StockMonitor) checkRebalancing() {
	resp, err := m.fetchRebalancing()
	if err != nil {
		log.Printf("[股票监控] %v", err)
		m.handlePendingRebalancingReminder()
		return
	}

	if len(resp.List) == 0 {
		m.handlePendingRebalancingReminder()
		return
	}

	latestID := resp.List[0].ID
	if m.lastRebalancingID > 0 && latestID != m.lastRebalancingID {
		log.Printf("[股票监控] 检测到调仓变动，旧ID=%d 新ID=%d", m.lastRebalancingID, latestID)
		m.pendingRecord = &resp.List[0]
		m.rebalancingCount = 0
		m.nextRebalancingAt = time.Now()
		m.lastRebalancingID = latestID
	}

	m.handlePendingRebalancingReminder()
}

func (m *StockMonitor) handlePendingRebalancingReminder() {
	if m.pendingRecord == nil {
		return
	}
	if m.rebalancingCount >= 5 {
		m.pendingRecord = nil
		return
	}
	now := time.Now()
	if !m.nextRebalancingAt.IsZero() && now.Before(m.nextRebalancingAt) {
		return
	}

	record := *m.pendingRecord
	m.sendRebalancingAlert(record)
	m.rebalancingCount++
	m.nextRebalancingAt = now.Add(1 * time.Minute)
	m.recordNotification("CUBE", "urgent", "rebalancing", float64(m.rebalancingCount), "all_day", time.Now().In(getLocation()).Format("2006-01-02"), "调仓变动")

	if m.rebalancingCount >= 5 {
		m.pendingRecord = nil
	}
}

// ==================== 通知发送 ====================

// sendStockAlert 发送股票涨跌提醒
func (m *StockMonitor) sendStockAlert(q StockQuote, threshold ThresholdConfig) {
	if !cluster.ShouldSend("股票阈值告警[" + q.Symbol + "]") {
		return
	}
	for _, cfg := range config.Notifiers {
		if !cfg.Enabled {
			continue
		}
		go func(cfg NotifierConfig) {
			var err error
			switch cfg.Type {
			case "feishu":
				msg := m.buildFeishuStockCard(q, threshold, cfg)
				err = sendWebhookJSON(cfg.Webhook, msg)
			case "dingtalk":
				msg := m.buildDingtalkStockMessage(q, threshold, cfg)
				err = sendDingtalkWebhook(cfg, msg)
			}
			if err != nil {
				log.Printf("[股票监控] 通知器 [%s] 发送失败: %v", cfg.Name, err)
			} else {
				log.Printf("[股票监控] 通知器 [%s] 发送成功", cfg.Name)
			}
		}(cfg)
	}
}

// sendRebalancingAlert 发送调仓变动紧急通知
func (m *StockMonitor) sendRebalancingAlert(record RebalancingRecord) {
	if !cluster.ShouldSend("组合调仓变动") {
		return
	}
	for _, cfg := range config.Notifiers {
		if !cfg.Enabled {
			continue
		}
		go func(cfg NotifierConfig) {
			var err error
			switch cfg.Type {
			case "feishu":
				msg := m.buildFeishuRebalancingCard(record, cfg)
				err = sendWebhookJSON(cfg.Webhook, msg)
			case "dingtalk":
				msg := m.buildDingtalkRebalancingMessage(record, cfg)
				err = sendDingtalkWebhook(cfg, msg)
			}
			if err != nil {
				log.Printf("[股票监控] 调仓通知器 [%s] 发送失败: %v", cfg.Name, err)
			} else {
				log.Printf("[股票监控] 调仓通知器 [%s] 发送成功", cfg.Name)
			}
		}(cfg)
	}
}

// sendWebhookJSON 发送JSON到飞书webhook
func sendWebhookJSON(webhookURL string, message any) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", webhookURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("创建请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("[webhook] 响应: %s", string(body))
	return nil
}

// sendDingtalkWebhook 发送钉钉webhook（带签名）
func sendDingtalkWebhook(cfg NotifierConfig, message any) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("序列化钉钉消息失败: %w", err)
	}

	timestamp := time.Now().UnixMilli()
	sign := genDingtalkSign(cfg.Secret, timestamp)
	webhookURL := fmt.Sprintf("%s&timestamp=%d&sign=%s", cfg.Webhook, timestamp, sign)

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", webhookURL, strings.NewReader(string(jsonData)))
	if err != nil {
		return fmt.Errorf("创建钉钉请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送钉钉请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("[钉钉] 响应: %s", string(body))
	return nil
}

// ==================== 飞书消息构建 ====================

// buildFeishuStockCard 构建飞书股票涨跌提醒卡片
func (m *StockMonitor) buildFeishuStockCard(q StockQuote, threshold ThresholdConfig, cfg NotifierConfig) FeishuCardMessage {
	timestamp := time.Now().Unix()
	sign, _ := genFeishuSign(cfg.Secret, timestamp)
	now := time.Now().Format("2006-01-02 15:04:05")

	levelText := map[string]string{
		"warning": "预警",
		"alert":   "警报",
		"urgent":  "紧急",
		"rapid":   "快速变动",
	}
	headerText := fmt.Sprintf("%s %s", levelText[threshold.Level], q.Name)

	msg := FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header: CardHeader{
				Title:    CardText{Content: headerText, Tag: "plain_text"},
				Template: threshold.Template,
			},
			Elements: []CardElement{
				{
					Tag: "div",
					Fields: []Field{
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: fmt.Sprintf("**股票**\n%s (%s)", q.Name, q.Symbol)}},
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: fmt.Sprintf("**当前价格**\n¥%.2f", q.Current)}},
					},
				},
				{
					Tag: "div",
					Fields: []Field{
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: fmt.Sprintf("**涨跌幅**\n%.2f%% (%.2f)", q.Percent, q.Chg)}},
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: fmt.Sprintf("**振幅**\n%.2f%%", q.Amplitude)}},
					},
				},
				{
					Tag: "div",
					Fields: []Field{
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: fmt.Sprintf("**最高**\n¥%.2f", q.High)}},
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: fmt.Sprintf("**最低**\n¥%.2f", q.Low)}},
					},
				},
				{
					Tag:  "div",
					Text: &CardText{Tag: "lark_md", Content: fmt.Sprintf("**成交量:** %s  |  **成交额:** %s", formatVolume(q.Volume), formatAmount(q.Amount))},
				},
				{
					Tag:      "note",
					Elements: []CardText{{Tag: "plain_text", Content: fmt.Sprintf("级别: %s | 时间: %s", threshold.Level, now)}},
				},
			},
		},
	}
	msg.Card.Elements = append(msg.Card.Elements, monthHighFeishuElements(q.Symbol, q.Current)...)
	msg.Card.Elements = append(msg.Card.Elements, klineFeishuElements(q.Symbol)...)
	msg.Card.Elements = append(msg.Card.Elements, volChartFeishuElements(q.Symbol, q.Name)...)
	return msg
}

func (m *StockMonitor) buildFeishuRapidCard(q StockQuote, threshold ThresholdConfig, rapidChange, lastPrice float64, cfg NotifierConfig) FeishuCardMessage {
	timestamp := time.Now().Unix()
	sign, _ := genFeishuSign(cfg.Secret, timestamp)
	now := time.Now().Format("2006-01-02 15:04:05")

	title := fmt.Sprintf("快速变动 %s", q.Name)
	content := fmt.Sprintf("股票: %s (%s)\n上一笔价格: %.2f\n当前价格: %.2f\n单次变动: %.2f%%\n当日涨跌幅: %.2f%%\n最高/最低: %.2f / %.2f\n成交量: %s", q.Name, q.Symbol, lastPrice, q.Current, rapidChange, q.Percent, q.High, q.Low, formatVolume(q.Volume))

	msg := FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header: CardHeader{Title: CardText{Content: title, Tag: "plain_text"}, Template: threshold.Template},
			Elements: []CardElement{
				{Tag: "div", Text: &CardText{Tag: "lark_md", Content: strings.ReplaceAll(content, "\n", "\n")}},
				{Tag: "note", Elements: []CardText{{Tag: "plain_text", Content: "类型: 快速变动 | 时间: " + now}}},
			},
		},
	}
	msg.Card.Elements = append(msg.Card.Elements, monthHighFeishuElements(q.Symbol, q.Current)...)
	msg.Card.Elements = append(msg.Card.Elements, klineFeishuElements(q.Symbol)...)
	msg.Card.Elements = append(msg.Card.Elements, volChartFeishuElements(q.Symbol, q.Name)...)
	return msg
}

// buildFeishuRebalancingCard 构建飞书调仓变动紧急通知卡片
func (m *StockMonitor) buildFeishuRebalancingCard(record RebalancingRecord, cfg NotifierConfig) FeishuCardMessage {
	timestamp := time.Now().Unix()
	sign, _ := genFeishuSign(cfg.Secret, timestamp)
	now := time.Now().Format("2006-01-02 15:04:05")
	recordTime := time.UnixMilli(record.CreatedAt).In(getLocation()).Format("2006-01-02 15:04:05")

	elements := []CardElement{
		{
			Tag:  "div",
			Text: &CardText{Tag: "lark_md", Content: fmt.Sprintf("检测到组合调仓变动\n调仓时间: %s", recordTime)},
		},
		{
			Tag: "hr",
		},
	}

	// 添加每只调仓股票的详情
	for _, h := range record.RebalancingHistories {
		action := "调整"
		if h.Weight == 0 && h.PrevWeight != nil && *h.PrevWeight > 0 {
			action = "清仓"
		} else if h.PrevWeight == nil || *h.PrevWeight == 0 {
			action = "建仓"
		} else if h.PrevWeight != nil && h.Weight > *h.PrevWeight {
			action = "加仓"
		} else if h.PrevWeight != nil && h.Weight < *h.PrevWeight {
			action = "减仓"
		}

		priceStr := "N/A"
		if h.Price != nil {
			priceStr = fmt.Sprintf("¥%.2f", *h.Price)
		}

		prevWeightStr := "N/A"
		if h.PrevWeight != nil {
			prevWeightStr = fmt.Sprintf("%.2f%%", *h.PrevWeight)
		}

		content := fmt.Sprintf("**%s %s (%s)**\n价格: %s\n权重变动: %s -> %.2f%%",
			action, h.StockName, h.StockSymbol, priceStr, prevWeightStr, h.Weight)

		elements = append(elements, CardElement{
			Tag:  "div",
			Text: &CardText{Tag: "lark_md", Content: content},
		})
	}

	elements = append(elements, CardElement{
		Tag:      "note",
		Elements: []CardText{{Tag: "plain_text", Content: fmt.Sprintf("变动提醒 | 检测时间: %s", now)}},
	})

	return FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header: CardHeader{
				Title:    CardText{Content: "组合调仓变动", Tag: "plain_text"},
				Template: "red",
			},
			Elements: elements,
		},
	}
}

// ==================== 钉钉消息构建 ====================

// buildDingtalkStockMessage 构建钉钉股票涨跌提醒消息
func (m *StockMonitor) buildDingtalkStockMessage(q StockQuote, threshold ThresholdConfig, cfg NotifierConfig) DingtalkMarkdownMessage {
	now := time.Now().Format("2006-01-02 15:04:05")

	levelText := map[string]string{
		"warning": "预警",
		"alert":   "警报",
		"urgent":  "紧急",
		"rapid":   "快速变动",
	}

	text := fmt.Sprintf(`### %s %s

股票：%s (%s)

当前价格：¥%.2f

涨跌幅：%.2f%% (%.2f)

最高：¥%.2f

最低：¥%.2f

成交量：%s

成交额：%s

---
> 级别: %s | 时间: %s`,
		levelText[threshold.Level], q.Name,
		q.Name, q.Symbol,
		q.Current,
		q.Percent, q.Chg,
		q.High,
		q.Low,
		formatVolume(q.Volume),
		formatAmount(q.Amount),
		threshold.Level, now)
	text += monthHighDingtalk(q.Symbol, q.Current)
	text += klineDingtalkMarkdown(q.Symbol)
	text += volChartDingtalk(q.Symbol, q.Name)

	return DingtalkMarkdownMessage{
		MsgType: "markdown",
		Markdown: DingtalkMarkdown{
			Title: fmt.Sprintf("%s %s", levelText[threshold.Level], q.Name),
			Text:  text,
		},
	}
}

func (m *StockMonitor) buildDingtalkRapidMessage(q StockQuote, threshold ThresholdConfig, rapidChange, lastPrice float64, cfg NotifierConfig) DingtalkMarkdownMessage {
	now := time.Now().Format("2006-01-02 15:04:05")

	text := fmt.Sprintf(`### 快速变动 %s

股票：%s (%s)

上一笔价格：%.2f

当前价格：%.2f

单次变动：%.2f%%

当日涨跌幅：%.2f%%

最高/最低：%.2f / %.2f

成交量：%s

---
> 类型: 快速变动 | 时间: %s`,
		q.Name,
		q.Name, q.Symbol,
		lastPrice,
		q.Current,
		rapidChange,
		q.Percent,
		q.High, q.Low,
		formatVolume(q.Volume),
		now)
	text += monthHighDingtalk(q.Symbol, q.Current)
	text += klineDingtalkMarkdown(q.Symbol)
	text += volChartDingtalk(q.Symbol, q.Name)

	return DingtalkMarkdownMessage{
		MsgType: "markdown",
		Markdown: DingtalkMarkdown{
			Title: fmt.Sprintf("快速变动 %s", q.Name),
			Text:  text,
		},
	}
}

// buildDingtalkRebalancingMessage 构建钉钉调仓变动紧急通知消息
func (m *StockMonitor) buildDingtalkRebalancingMessage(record RebalancingRecord, cfg NotifierConfig) DingtalkMarkdownMessage {
	now := time.Now().Format("2006-01-02 15:04:05")
	recordTime := time.UnixMilli(record.CreatedAt).In(getLocation()).Format("2006-01-02 15:04:05")

	var lines []string
	for _, h := range record.RebalancingHistories {
		action := "调整"
		if h.Weight == 0 && h.PrevWeight != nil && *h.PrevWeight > 0 {
			action = "清仓"
		} else if h.PrevWeight == nil || *h.PrevWeight == 0 {
			action = "建仓"
		} else if h.PrevWeight != nil && h.Weight > *h.PrevWeight {
			action = "加仓"
		} else if h.PrevWeight != nil && h.Weight < *h.PrevWeight {
			action = "减仓"
		}

		priceStr := "N/A"
		if h.Price != nil {
			priceStr = fmt.Sprintf("¥%.2f", *h.Price)
		}

		prevWeightStr := "N/A"
		if h.PrevWeight != nil {
			prevWeightStr = fmt.Sprintf("%.2f%%", *h.PrevWeight)
		}

		lines = append(lines, fmt.Sprintf("- **%s %s (%s)** | 价格: %s | 权重变动: %s -> %.2f%%",
			action, h.StockName, h.StockSymbol, priceStr, prevWeightStr, h.Weight))
	}

	text := fmt.Sprintf(`### 组合调仓变动

检测到调仓操作！调仓时间: %s

---

%s

---
> 变动提醒 | 检测时间: %s`, recordTime, strings.Join(lines, "\n\n"), now)

	return DingtalkMarkdownMessage{
		MsgType: "markdown",
		Markdown: DingtalkMarkdown{
			Title: "组合调仓变动",
			Text:  text,
		},
	}
}

// ==================== 工具函数 ====================

// isDuringTradingHours 判断当前是否在交易时段
func (m *StockMonitor) isDuringTradingHours() bool {
	now := time.Now().In(getLocation())
	weekday := now.Weekday()

	// 周末不交易
	if weekday == time.Saturday || weekday == time.Sunday {
		return false
	}

	hour := now.Hour()
	minute := now.Minute()
	totalMinutes := hour*60 + minute

	// 上午: 9:15 - 11:30（含集合竞价）
	// 下午: 13:00 - 15:00
	morning := totalMinutes >= 9*60+15 && totalMinutes <= 11*60+30
	afternoon := totalMinutes >= 13*60 && totalMinutes <= 15*60

	return morning || afternoon
}

// formatVolume 格式化成交量
func formatVolume(v int64) string {
	if v >= 100000000 {
		return fmt.Sprintf("%.2f亿股", float64(v)/100000000)
	}
	if v >= 10000 {
		return fmt.Sprintf("%.2f万股", float64(v)/10000)
	}
	return fmt.Sprintf("%d股", v)
}

// formatAmount 格式化成交额
func formatAmount(a float64) string {
	if a >= 100000000 {
		return fmt.Sprintf("%.2f亿", a/100000000)
	}
	if a >= 10000 {
		return fmt.Sprintf("%.2f万", a/10000)
	}
	return fmt.Sprintf("%.2f", a)
}
