package main

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==================== 配置结构定义 ====================

// Config 应用配置
type Config struct {
	AttendanceAPI     string             `json:"attendance_api"`
	AuthToken         string             `json:"auth_token"`
	AttendanceHeaders map[string]string  `json:"attendance_headers"` // 签到接口自定义请求头(device-sn/app-version-*/user-agent 等)
	APIKey            string             `json:"api_key"`
	HTTPPort          string             `json:"http_port"`
	Notifiers         []NotifierConfig   `json:"notifiers"`
	TimeSlots         []TimeSlotConfig   `json:"time_slots"`
	Escalation        EscalationConfig   `json:"escalation"`
	StockMonitor      StockMonitorConfig `json:"stock_monitor"`
	Cluster           ClusterConfig      `json:"cluster"`
}

// EscalationConfig 打卡提醒升级策略（距时段开始的分钟数）
type EscalationConfig struct {
	AppUrgentAfterMin   int `json:"app_urgent_after_min"`   // 超过后应用内加急，默认 10
	PhoneAfterMin       int `json:"phone_after_min"`        // 超过后电话加急，默认 15
	BackupPhoneAfterMin int `json:"backup_phone_after_min"` // 超过后加拨备用号，默认 25
}

// NotifierConfig 通知器配置
type NotifierConfig struct {
	Type    string `json:"type"`    // feishu, dingtalk, feishu_app, wechat 等
	Name    string `json:"name"`    // 显示名称
	Enabled bool   `json:"enabled"` // 是否启用
	Webhook string `json:"webhook"` // webhook 地址
	Secret  string `json:"secret"`  // 签名密钥
	// feishu_app 自建应用专用
	AppID           string          `json:"app_id,omitempty"`
	AppSecret       string          `json:"app_secret,omitempty"`
	OpenID          string          `json:"open_id,omitempty"` // 接收人 open_id，为空时通过 mobile/email 解析
	Mobile          string          `json:"mobile,omitempty"`
	Email           string          `json:"email,omitempty"`
	Apps            []FeishuAppCred `json:"apps,omitempty"`               // 多应用轮换（额度分摊），优先于平铺的 app_id/app_secret
	PhoneMaxPerSlot int             `json:"phone_max_per_slot,omitempty"` // 每时段每天最多电话次数，默认 1
	BackupMobile    string          `json:"backup_mobile,omitempty"`      // 备用联系人手机号（须已加入某个应用所在团队）
	BackupMobiles   []string        `json:"backup_mobiles,omitempty"`     // 备用联系人优先级列表，按顺序尝试，成功一个即止
	BackupOpenID    string          `json:"backup_open_id,omitempty"`
}

// FeishuAppCred 飞书自建应用凭证
type FeishuAppCred struct {
	AppID       string `json:"app_id"`
	AppSecret   string `json:"app_secret"`
	OpenID      string `json:"open_id,omitempty"`       // 本人在该租户的 open_id（消息/应用内加急目标）
	PhoneMobile string `json:"phone_mobile,omitempty"`  // 该应用电话加急的专属目标号码，空则打本人
	PhoneOpenID string `json:"phone_open_id,omitempty"` // 电话目标的 open_id 预缓存
}

// TimeSlotConfig 打卡时段配置
type TimeSlotConfig struct {
	StartHour   int    `json:"start_hour"`
	StartMinute int    `json:"start_minute"`
	EndHour     int    `json:"end_hour"`
	EndMinute   int    `json:"end_minute"`
	STimePrefix string `json:"s_time_prefix"`
	Name        string `json:"name"`
}

// ==================== 通知器接口定义 ====================

// Notifier 通知器接口
type Notifier interface {
	Name() string
	Send(message NotifyMessage) error
}

// NotifyMessage 通知消息结构
type NotifyMessage struct {
	Title      string
	SlotName   string
	Status     string // "未打卡" 或 "已打卡"
	ReportTime string
	Template   string // "warning" 或 "success" 或 "error"
	Slots      []SlotStatus
	IsCheck    bool   // 是否为查询接口触发的通知
	Urgency    string // ""/"app"/"phone"，仅 feishu_app 通知器使用
	Remaining  string // 距时段结束剩余时间描述
	SysAlert   bool   // 系统级告警(如登录态失效)，走独立卡片样式
	AlertTitle string // 系统告警标题
	AlertBody  string // 系统告警正文(lark_md)
}

// ==================== 飞书通知器 ====================

type FeishuNotifier struct {
	config NotifierConfig
}

func NewFeishuNotifier(config NotifierConfig) *FeishuNotifier {
	return &FeishuNotifier{config: config}
}

func (f *FeishuNotifier) Name() string {
	return f.config.Name
}

// genFeishuSign 生成飞书签名
func genFeishuSign(secret string, timestamp int64) (string, error) {
	stringToSign := fmt.Sprintf("%v", timestamp) + "\n" + secret
	h := hmac.New(sha256.New, []byte(stringToSign))
	h.Write([]byte{})
	return base64.StdEncoding.EncodeToString(h.Sum(nil)), nil
}

func (f *FeishuNotifier) Send(msg NotifyMessage) error {
	timestamp := time.Now().Unix()
	sign, _ := genFeishuSign(f.config.Secret, timestamp)

	var message any
	switch {
	case msg.SysAlert:
		message = f.buildSysAlertCard(msg, timestamp, sign)
	case msg.IsCheck:
		message = f.buildCheckResultCard(msg, timestamp, sign)
	default:
		message = f.buildReminderCard(msg, timestamp, sign)
	}

	return f.doSend(message)
}

// buildSysAlertCard 构建系统级告警卡片(如登录态失效)
func (f *FeishuNotifier) buildSysAlertCard(msg NotifyMessage, timestamp int64, sign string) FeishuCardMessage {
	now := time.Now().In(getLocation()).Format("2006-01-02 15:04:05")
	return FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header: CardHeader{
				Title:    CardText{Content: msg.AlertTitle, Tag: "plain_text"},
				Template: "red",
			},
			Elements: []CardElement{
				{Tag: "div", Text: &CardText{Tag: "lark_md", Content: msg.AlertBody}},
				{Tag: "note", Elements: []CardText{{Tag: "plain_text", Content: "告警时间: " + now}}},
			},
		},
	}
}

func (f *FeishuNotifier) buildReminderCard(msg NotifyMessage, timestamp int64, sign string) FeishuCardMessage {
	now := time.Now().Format("2006-01-02 15:04:05")
	return FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header: CardHeader{
				Title:    CardText{Content: "⚠️ 打卡提醒", Tag: "plain_text"},
				Template: "orange",
			},
			Elements: []CardElement{
				{
					Tag: "div",
					Fields: []Field{
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: "**📌 时段**\n" + msg.SlotName}},
						{IsShort: true, Text: CardText{Tag: "lark_md", Content: "**📍 状态**\n" + msg.Status}},
					},
				},
				{
					Tag:  "div",
					Text: &CardText{Tag: "lark_md", Content: "⏰ **请及时打卡！**"},
				},
				{
					Tag:      "note",
					Elements: []CardText{{Tag: "plain_text", Content: "提醒时间: " + now}},
				},
			},
		},
	}
}

func (f *FeishuNotifier) buildCheckResultCard(msg NotifyMessage, timestamp int64, sign string) FeishuCardMessage {
	now := time.Now().Format("2006-01-02 15:04:05")
	checkedCount := 0
	uncheckedCount := 0

	var fields []Field
	for _, s := range msg.Slots {
		var statusText string
		if s.Checked {
			checkedCount++
			statusText = fmt.Sprintf("✅ **%s**\n已打卡 %s", s.Name, *s.ReportTime)
		} else {
			uncheckedCount++
			statusText = fmt.Sprintf("❌ **%s**\n未打卡", s.Name)
		}
		fields = append(fields, Field{
			IsShort: false,
			Text:    CardText{Tag: "lark_md", Content: statusText},
		})
	}

	template := "green"
	if uncheckedCount > 0 {
		template = "red"
	}

	return FeishuCardMessage{
		Timestamp: strconv.FormatInt(timestamp, 10),
		Sign:      sign,
		MsgType:   "interactive",
		Card: FeishuCard{
			Header: CardHeader{
				Title:    CardText{Content: "📋 打卡状态查询", Tag: "plain_text"},
				Template: template,
			},
			Elements: []CardElement{
				{
					Tag:  "div",
					Text: &CardText{Tag: "lark_md", Content: fmt.Sprintf("**统计:** 已打卡 %d 项，未打卡 %d 项", checkedCount, uncheckedCount)},
				},
				{
					Tag: "hr",
				},
				{
					Tag:    "div",
					Fields: fields,
				},
				{
					Tag:      "note",
					Elements: []CardText{{Tag: "plain_text", Content: "查询时间: " + now}},
				},
			},
		},
	}
}

func (f *FeishuNotifier) doSend(message any) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("序列化飞书消息失败: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", f.config.Webhook, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建飞书请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送飞书通知失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("[飞书] 通知发送成功，响应: %s", string(body))
	return nil
}

// ==================== 钉钉通知器 ====================

type DingtalkNotifier struct {
	config NotifierConfig
}

func NewDingtalkNotifier(config NotifierConfig) *DingtalkNotifier {
	return &DingtalkNotifier{config: config}
}

func (d *DingtalkNotifier) Name() string {
	return d.config.Name
}

// genDingtalkSign 生成钉钉签名
func genDingtalkSign(secret string, timestamp int64) string {
	stringToSign := fmt.Sprintf("%d\n%s", timestamp, secret)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(stringToSign))
	signData := base64.StdEncoding.EncodeToString(h.Sum(nil))
	return url.QueryEscape(signData)
}

func (d *DingtalkNotifier) Send(msg NotifyMessage) error {
	var message any
	switch {
	case msg.SysAlert:
		message = d.buildSysAlertMessage(msg)
	case msg.IsCheck:
		message = d.buildCheckResultMessage(msg)
	default:
		message = d.buildReminderMessage(msg)
	}
	return d.doSend(message)
}

// buildSysAlertMessage 构建系统级告警消息(如登录态失效)
func (d *DingtalkNotifier) buildSysAlertMessage(msg NotifyMessage) DingtalkMarkdownMessage {
	now := time.Now().In(getLocation()).Format("2006-01-02 15:04:05")
	text := fmt.Sprintf("### %s\n\n%s\n\n---\n> 告警时间: %s", msg.AlertTitle, msg.AlertBody, now)
	return DingtalkMarkdownMessage{
		MsgType:  "markdown",
		Markdown: DingtalkMarkdown{Title: msg.AlertTitle, Text: text},
	}
}

// DingtalkMarkdownMessage 钉钉 Markdown 消息
type DingtalkMarkdownMessage struct {
	MsgType  string           `json:"msgtype"`
	Markdown DingtalkMarkdown `json:"markdown"`
	At       DingtalkAtConfig `json:"at,omitempty"`
}

type DingtalkMarkdown struct {
	Title string `json:"title"`
	Text  string `json:"text"`
}

type DingtalkAtConfig struct {
	AtMobiles []string `json:"atMobiles,omitempty"`
	IsAtAll   bool     `json:"isAtAll"`
}

func (d *DingtalkNotifier) buildReminderMessage(msg NotifyMessage) DingtalkMarkdownMessage {
	now := time.Now().Format("2006-01-02 15:04:05")
	text := fmt.Sprintf(`### ⚠️ 打卡提醒

📌 时段：%s

📍 状态：%s

⏰ 请及时打卡！

---
> 提醒时间: %s`, msg.SlotName, msg.Status, now)

	return DingtalkMarkdownMessage{
		MsgType: "markdown",
		Markdown: DingtalkMarkdown{
			Title: "打卡提醒",
			Text:  text,
		},
	}
}

func (d *DingtalkNotifier) buildCheckResultMessage(msg NotifyMessage) DingtalkMarkdownMessage {
	now := time.Now().Format("2006-01-02 15:04:05")
	checkedCount := 0
	uncheckedCount := 0

	var statusLines []string
	for _, s := range msg.Slots {
		if s.Checked {
			checkedCount++
			statusLines = append(statusLines, fmt.Sprintf("✅ %s - 已打卡 %s", s.Name, *s.ReportTime))
		} else {
			uncheckedCount++
			statusLines = append(statusLines, fmt.Sprintf("❌ %s - 未打卡", s.Name))
		}
	}

	statusIcon := "📋"
	if uncheckedCount > 0 {
		statusIcon = "⚠️"
	}

	text := fmt.Sprintf(`### %s 打卡状态查询

统计：已打卡 %d 项，未打卡 %d 项

---

%s

---
> 查询时间: %s`, statusIcon, checkedCount, uncheckedCount, strings.Join(statusLines, "\n\n"), now)

	return DingtalkMarkdownMessage{
		MsgType: "markdown",
		Markdown: DingtalkMarkdown{
			Title: "打卡状态查询",
			Text:  text,
		},
	}
}

// DingtalkResponse 钉钉 API 响应
type DingtalkResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

func (d *DingtalkNotifier) doSend(message any) error {
	jsonData, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("序列化钉钉消息失败: %w", err)
	}

	// 生成签名
	timestamp := time.Now().UnixMilli()
	sign := genDingtalkSign(d.config.Secret, timestamp)

	// 构建带签名的 URL
	webhookURL := fmt.Sprintf("%s&timestamp=%d&sign=%s", d.config.Webhook, timestamp, sign)

	log.Printf("[钉钉] 发送请求: URL=%s, Body=%s", webhookURL, string(jsonData))

	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", webhookURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建钉钉请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送钉钉通知失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	log.Printf("[钉钉] 响应: %s", string(body))

	// 解析响应检查是否成功
	var dtResp DingtalkResponse
	if err := json.Unmarshal(body, &dtResp); err == nil {
		if dtResp.ErrCode != 0 {
			return fmt.Errorf("钉钉返回错误: errcode=%d, errmsg=%s", dtResp.ErrCode, dtResp.ErrMsg)
		}
	}

	log.Printf("[钉钉] 通知发送成功")
	return nil
}

// ==================== 通知管理器 ====================

type NotifyManager struct {
	notifiers []Notifier
}

func NewNotifyManager(configs []NotifierConfig) *NotifyManager {
	manager := &NotifyManager{}
	for _, cfg := range configs {
		if !cfg.Enabled {
			log.Printf("通知器 [%s] 已禁用，跳过", cfg.Name)
			continue
		}
		switch cfg.Type {
		case "feishu":
			manager.notifiers = append(manager.notifiers, NewFeishuNotifier(cfg))
			log.Printf("通知器 [%s] 已加载", cfg.Name)
		case "dingtalk":
			manager.notifiers = append(manager.notifiers, NewDingtalkNotifier(cfg))
			log.Printf("通知器 [%s] 已加载", cfg.Name)
		case "feishu_app":
			manager.notifiers = append(manager.notifiers, NewFeishuAppNotifier(cfg))
			log.Printf("通知器 [%s] 已加载 (支持加急)", cfg.Name)
		case "wechat":
			// TODO: 实现微信通知器
			log.Printf("通知器 [%s] 类型 wechat 暂未实现", cfg.Name)
		default:
			log.Printf("未知通知器类型: %s", cfg.Type)
		}
	}
	return manager
}

func (m *NotifyManager) SendAll(msg NotifyMessage) {
	if !cluster.ShouldSend("打卡提醒[" + msg.SlotName + "]") {
		return
	}
	for _, n := range m.notifiers {
		go func(notifier Notifier) {
			if err := notifier.Send(msg); err != nil {
				log.Printf("通知器 [%s] 发送失败: %v", notifier.Name(), err)
			}
		}(n)
	}
}

// ==================== 飞书消息结构体 ====================

type FeishuCardMessage struct {
	Timestamp string     `json:"timestamp"`
	Sign      string     `json:"sign"`
	MsgType   string     `json:"msg_type"`
	Card      FeishuCard `json:"card"`
}

type FeishuCard struct {
	Header   CardHeader    `json:"header"`
	Elements []CardElement `json:"elements"`
}

type CardHeader struct {
	Title    CardText `json:"title"`
	Template string   `json:"template"`
}

type CardText struct {
	Content string `json:"content"`
	Tag     string `json:"tag"`
}

type CardElement struct {
	Tag      string     `json:"tag"`
	Content  *CardText  `json:"content,omitempty"`
	Text     *CardText  `json:"text,omitempty"`
	Fields   []Field    `json:"fields,omitempty"`
	Elements []CardText `json:"elements,omitempty"`
	ImgKey   string     `json:"img_key,omitempty"`
	Alt      *CardText  `json:"alt,omitempty"`
	Mode     string     `json:"mode,omitempty"`
}

type Field struct {
	IsShort bool     `json:"is_short"`
	Text    CardText `json:"text"`
}

// ==================== 业务数据结构 ====================

// SignResponse 当日签到列表接口响应
// GET /api/mob/signIn/onlineSigns/listByDay?searchDay=YYYY-MM-DD
type SignResponse struct {
	Code int          `json:"code"`
	Msg  string       `json:"msg"`
	Data []SignRecord `json:"data"`
}

// UnmarshalJSON 兼容业务错误响应中 data 类型与成功响应不一致的情况。
// 成功时 data 必须是签到记录数组；失败时保留 code/msg 供登录态失效判断。
func (r *SignResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	r.Code = wire.Code
	r.Msg = wire.Msg
	r.Data = nil
	if wire.Code != 200 || len(wire.Data) == 0 || string(wire.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(wire.Data, &r.Data); err != nil {
		return fmt.Errorf("解析签到记录失败: %w", err)
	}
	return nil
}

// SignRecord 单条签到记录（data 为当日实际发生的签到，而非预期时段）
type SignRecord struct {
	PkID        int64  `json:"pkId"`
	CreateTime  string `json:"createTime"`  // 签到时间 "2006-01-02 15:04:05"
	SignTypeStr string `json:"signTypeStr"` // 如 "APP签到"
	Status      string `json:"status"`
	IsValid     string `json:"isValid"`     // "1"=有效签到
	IsOutTime   string `json:"isOutTime"`   // "1"=超时
	IsOutBounds string `json:"isOutBounds"` // "1"=越界
	Address     string `json:"address"`
	Xm          string `json:"xm"` // 姓名
}

type CheckResponse struct {
	Code      int                `json:"code"`
	Message   string             `json:"message"`
	Timestamp string             `json:"timestamp"`
	Data      *CheckResponseData `json:"data,omitempty"`
}

type CheckResponseData struct {
	Slots []SlotStatus `json:"slots"`
}

type SlotStatus struct {
	Name       string  `json:"name"`
	StartTime  string  `json:"start_time"`
	EndTime    string  `json:"end_time"`
	Checked    bool    `json:"checked"`
	ReportTime *string `json:"report_time,omitempty"`
}

// ==================== 全局变量 ====================

var (
	loc           *time.Location
	locOnce       sync.Once
	config        *Config
	notifyManager *NotifyManager
	cluster       *Cluster
)

const userAgent = "Mozilla/5.0 (Linux; Android 14; 23127PN0CC Build/UKQ1.230804.001; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/126.0.6478.71 Mobile Safari/537.36 uni-app Html5Plus/1.0 (Immersed/38.666668)"

// ==================== 初始化函数 ====================

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 用环境变量(.env)展开 ${VAR} 占位符，密钥不入库
	data = expandConfigEnv(data)

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return &cfg, nil
}

func getLocation() *time.Location {
	locOnce.Do(func() {
		var err error
		loc, err = time.LoadLocation("Asia/Shanghai")
		if err != nil {
			log.Printf("加载时区失败，使用本地时区: %v", err)
			loc = time.Local
		}
	})
	return loc
}

// ==================== 主函数 ====================

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Println("打卡检查服务启动...")

	// 先加载 .env(密钥不入库)，再加载配置
	loadDotEnv(".env")

	// 加载配置
	var err error
	config, err = loadConfig("config.json")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置加载成功，共 %d 个通知渠道，%d 个打卡时段", len(config.Notifiers), len(config.TimeSlots))

	// 初始化通知管理器
	notifyManager = NewNotifyManager(config.Notifiers)

	// 初始化集群/故障转移
	cluster = NewCluster(config.Cluster)
	cluster.Start()

	// 启动股票监控
	if config.StockMonitor.Enabled {
		stockMonitor, err := NewStockMonitor(config.StockMonitor)
		if err != nil {
			log.Printf("[股票监控] 初始化失败: %v", err)
		} else {
			stockMonitor.Start()
		}
	} else {
		log.Println("[股票监控] 未启用")
	}

	location := getLocation()

	http.HandleFunc("/api/check", handleCheckAttendance)
	http.HandleFunc("/chart/vol", handleVolChart)
	http.HandleFunc("/chart/page", handleChartPage)
	go func() {
		log.Printf("HTTP 服务器启动，监听端口 %s", config.HTTPPort)
		if err := http.ListenAndServe(config.HTTPPort, nil); err != nil {
			log.Fatalf("HTTP 服务器启动失败: %v", err)
		}
	}()

	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	checkAttendance(location)

	for range ticker.C {
		checkAttendance(location)
	}
}

// ==================== 业务逻辑 ====================

func checkAttendance(loc *time.Location) {
	now := time.Now().In(loc)
	currentMinutes := now.Hour()*60 + now.Minute()

	for _, slot := range config.TimeSlots {
		slotStart := slot.StartHour*60 + slot.StartMinute
		slotEnd := slot.EndHour*60 + slot.EndMinute

		if currentMinutes >= slotStart && currentMinutes <= slotEnd {
			if now.Minute()%2 == 0 {
				log.Printf("当前时间 %s，在 %s 时间段内，开始检查打卡状态...", now.Format("15:04:05"), slot.Name)
				performCheck(slot)
			}
			return
		}
	}
}

func performCheck(slot TimeSlotConfig) {
	resp, err := requestAttendanceAPI()
	if err != nil {
		log.Printf("请求签到API失败: %v", err)
		return
	}

	// 接口异常(登录态失效等)：不能判定打卡状态，跳过打卡提醒以免误报未打卡；
	// 若为登录态失效则单独发系统告警提示刷新 token。
	if resp.Code != 200 {
		reason := fmt.Sprintf("code=%d %s", resp.Code, resp.Msg)
		log.Printf("[签到] 接口异常，跳过打卡判定: %s", reason)
		if isAuthExpired(resp.Code, resp.Msg) {
			sendTokenExpiredAlert(reason)
		}
		return
	}

	rec := findSignBySlot(resp.Data, slot, getLocation())
	if rec == nil {
		log.Printf("%s 未签到，准备发送通知...", slot.Name)
		sendNotification(slot)
	} else {
		log.Printf("%s 已签到，时间: %s (%s)", slot.Name, rec.CreateTime, rec.SignTypeStr)
	}
}

// findSignBySlot 在当日签到记录中查找落在该时段窗口 [start,end] 内的有效签到，
// 返回最早的一条；不存在则返回 nil。
func findSignBySlot(records []SignRecord, slot TimeSlotConfig, loc *time.Location) *SignRecord {
	slotStart := slot.StartHour*60 + slot.StartMinute
	slotEnd := slot.EndHour*60 + slot.EndMinute

	var match *SignRecord
	for i := range records {
		if records[i].IsValid != "1" { // 仅统计有效签到
			continue
		}
		t, err := time.ParseInLocation("2006-01-02 15:04:05", records[i].CreateTime, loc)
		if err != nil {
			log.Printf("[签到] 解析签到时间失败 [%s]: %v", records[i].CreateTime, err)
			continue
		}
		m := t.Hour()*60 + t.Minute()
		if m >= slotStart && m <= slotEnd {
			if match == nil || records[i].CreateTime < match.CreateTime {
				match = &records[i]
			}
		}
	}
	return match
}

func requestAttendanceAPI() (*SignResponse, error) {
	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	// 追加当日 searchDay 参数(Asia/Shanghai)
	day := time.Now().In(getLocation()).Format("2006-01-02")
	sep := "?"
	if strings.Contains(config.AttendanceAPI, "?") {
		sep = "&"
	}
	reqURL := fmt.Sprintf("%s%ssearchDay=%s", config.AttendanceAPI, sep, day)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}

	// 鉴权与固定头
	req.Header.Set("Authorization", config.AuthToken)
	req.Header.Set("Accept-Encoding", "gzip")
	// 应用自定义头(device-sn / app-version-* / user-agent / referer 等)
	for k, v := range config.AttendanceHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("解压gzip失败: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	var result SignResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析JSON失败: %w (原始响应: %.200s)", err, string(body))
	}

	log.Printf("[签到] %s 返回 code=%d，记录数=%d", day, result.Code, len(result.Data))
	return &result, nil
}

func sendNotification(slot TimeSlotConfig) {
	urgency, remaining := slotUrgency(slot)
	msg := NotifyMessage{
		Title:     "打卡提醒",
		SlotName:  slot.Name,
		Status:    "未打卡",
		Template:  "warning",
		IsCheck:   false,
		Urgency:   urgency,
		Remaining: remaining,
	}
	notifyManager.SendAll(msg)
}

// ==================== 登录态失效告警 ====================

var (
	tokenAlertMu   sync.Mutex
	lastTokenAlert time.Time
)

// tokenAlertCooldown 登录态失效告警冷却时间。检测每 2 分钟一次，冷却期内只发一条；
// 三个时段相隔约 6 小时，因此每个时段至多提醒一次，直到 token 被刷新。
const tokenAlertCooldown = 30 * time.Minute

// isAuthExpired 判断签到接口返回是否为登录态失效(需刷新 token)
func isAuthExpired(code int, msg string) bool {
	// 新后端(RuoYi)返回 401；旧后端返回 105 "账号异常"；兜底匹配"登录"关键字
	return code == 401 || code == 105 || strings.Contains(msg, "登录")
}

// sendTokenExpiredAlert 发送登录态失效系统告警(带冷却，避免每 2 分钟刷屏)
func sendTokenExpiredAlert(reason string) {
	tokenAlertMu.Lock()
	if time.Since(lastTokenAlert) < tokenAlertCooldown {
		tokenAlertMu.Unlock()
		return
	}
	lastTokenAlert = time.Now()
	tokenAlertMu.Unlock()

	now := time.Now().In(getLocation()).Format("2006-01-02 15:04:05")
	msg := NotifyMessage{
		SysAlert:   true,
		AlertTitle: "🔑 打卡监控登录态失效",
		AlertBody: fmt.Sprintf(
			"签到接口返回：**%s**\n\n打卡状态检测已暂停，**期间不会再发送未打卡提醒**。\n请尽快重新抓取 App 的 `authorization` 头并更新 config.json 的 `auth_token` 后重启服务。\n\n检测时间：%s",
			reason, now),
		Urgency: "app", // 应用内加急提醒本人，不触发电话
	}
	notifyManager.SendAll(msg)
}

// slotUrgency 根据距时段开始的时间决定加急等级：
// 超过 app_urgent_after_min 分钟 -> 应用内加急；
// 超过 phone_after_min 分钟 -> 电话加急；
// 超过 backup_phone_after_min 分钟 -> 电话加急并加拨备用号
func slotUrgency(slot TimeSlotConfig) (string, string) {
	now := time.Now()
	nowMin := now.Hour()*60 + now.Minute()
	start := slot.StartHour*60 + slot.StartMinute
	end := slot.EndHour*60 + slot.EndMinute
	elapsed := nowMin - start
	remaining := fmt.Sprintf("%d 分钟", end-nowMin)

	appAfter, phoneAfter, backupAfter := config.Escalation.AppUrgentAfterMin, config.Escalation.PhoneAfterMin, config.Escalation.BackupPhoneAfterMin
	if appAfter <= 0 {
		appAfter = 10
	}
	if phoneAfter <= 0 {
		phoneAfter = 15
	}
	if backupAfter <= 0 {
		backupAfter = 25
	}

	switch {
	case elapsed >= backupAfter:
		return "phone_backup", remaining
	case elapsed >= phoneAfter:
		return "phone", remaining
	case elapsed >= appAfter:
		return "app", remaining
	default:
		return "", remaining
	}
}

func sendCheckResultNotification(slots []SlotStatus) {
	msg := NotifyMessage{
		Title:   "打卡状态查询",
		Slots:   slots,
		IsCheck: true,
	}
	notifyManager.SendAll(msg)
}

func handleCheckAttendance(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONResponse(w, http.StatusMethodNotAllowed, CheckResponse{
			Code:      405,
			Message:   "仅支持 GET 方法",
			Timestamp: time.Now().In(getLocation()).Format("2006-01-02 15:04:05"),
		})
		return
	}

	// 密钥鉴权
	key := r.URL.Query().Get("key")
	if key == "" {
		key = r.Header.Get("X-API-Key")
	}
	if key != config.APIKey {
		log.Printf("API 鉴权失败，来源 IP: %s", r.RemoteAddr)
		writeJSONResponse(w, http.StatusUnauthorized, CheckResponse{
			Code:      401,
			Message:   "密钥无效或缺失",
			Timestamp: time.Now().In(getLocation()).Format("2006-01-02 15:04:05"),
		})
		return
	}

	resp, err := requestAttendanceAPI()
	if err != nil {
		log.Printf("API 请求失败: %v", err)
		writeJSONResponse(w, http.StatusInternalServerError, CheckResponse{
			Code:      500,
			Message:   "获取打卡状态失败",
			Timestamp: time.Now().In(getLocation()).Format("2006-01-02 15:04:05"),
		})
		return
	}

	if resp.Code != 200 {
		message := fmt.Sprintf("签到接口异常: code=%d %s", resp.Code, resp.Msg)
		if isAuthExpired(resp.Code, resp.Msg) {
			message = "签到后台登录态失效，需刷新 auth_token"
		}
		writeJSONResponse(w, http.StatusBadGateway, CheckResponse{
			Code:      resp.Code,
			Message:   message,
			Timestamp: time.Now().In(getLocation()).Format("2006-01-02 15:04:05"),
		})
		return
	}

	loc := getLocation()
	slots := make([]SlotStatus, 0, len(config.TimeSlots))
	hasUnchecked := false

	for _, slot := range config.TimeSlots {
		status := SlotStatus{
			Name:      slot.Name,
			StartTime: fmt.Sprintf("%02d:%02d", slot.StartHour, slot.StartMinute),
			EndTime:   fmt.Sprintf("%02d:%02d", slot.EndHour, slot.EndMinute),
			Checked:   false,
		}

		if rec := findSignBySlot(resp.Data, slot, loc); rec != nil {
			status.Checked = true
			signTime := rec.CreateTime
			status.ReportTime = &signTime
		} else {
			hasUnchecked = true
		}

		slots = append(slots, status)
	}

	if hasUnchecked {
		go sendCheckResultNotification(slots)
	}

	writeJSONResponse(w, http.StatusOK, CheckResponse{
		Code:      200,
		Message:   "success",
		Timestamp: time.Now().In(getLocation()).Format("2006-01-02 15:04:05"),
		Data: &CheckResponseData{
			Slots: slots,
		},
	})
}

func writeJSONResponse(w http.ResponseWriter, statusCode int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(data)
}
