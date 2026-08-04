package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ==================== 飞书自建应用通知器（支持应用内/电话加急，多应用轮换额度） ====================

const feishuAPIBase = "https://open.feishu.cn/open-apis"

// feishuAppClient 单个自建应用的客户端（各自的 token 缓存与 open_id 缓存）
type feishuAppClient struct {
	cred FeishuAppCred

	mu          sync.Mutex
	token       string
	tokenExpiry time.Time
	ids         map[string]string // 手机号 -> 该租户 open_id
}

type FeishuAppNotifier struct {
	config NotifierConfig
	apps   []*feishuAppClient

	mu           sync.Mutex
	phoneCalls   map[string]int  // "2026-07-05|第三次打卡" -> 已拨打次数
	backupCalled map[string]bool // 备用号每时段每天只拨一次
}

func NewFeishuAppNotifier(config NotifierConfig) *FeishuAppNotifier {
	creds := config.Apps
	if len(creds) == 0 && config.AppID != "" {
		// 兼容单应用平铺写法
		creds = []FeishuAppCred{{AppID: config.AppID, AppSecret: config.AppSecret, OpenID: config.OpenID}}
	}
	n := &FeishuAppNotifier{config: config, phoneCalls: make(map[string]int), backupCalled: make(map[string]bool)}
	for _, c := range creds {
		client := &feishuAppClient{cred: c, ids: make(map[string]string)}
		if c.OpenID != "" && config.Mobile != "" {
			client.ids[config.Mobile] = c.OpenID
		}
		if c.PhoneOpenID != "" && c.PhoneMobile != "" {
			client.ids[c.PhoneMobile] = c.PhoneOpenID
		}
		n.apps = append(n.apps, client)
	}
	return n
}

func (f *FeishuAppNotifier) Name() string {
	return f.config.Name
}

func (f *FeishuAppNotifier) phoneMax() int {
	if f.config.PhoneMaxPerSlot > 0 {
		return f.config.PhoneMaxPerSlot
	}
	return 1
}

func (f *FeishuAppNotifier) Send(msg NotifyMessage) error {
	if len(f.apps) == 0 {
		return fmt.Errorf("未配置任何 app 凭证")
	}

	urgency := msg.Urgency
	if urgency == "phone_backup" {
		// 超过备用阈值：本人照常电话加急，另外加拨备用号（每时段每天一次）
		go f.callBackup(msg)
		urgency = "phone"
	}
	appIdx := 0
	if urgency == "phone" {
		if callNo, ok := f.takePhoneSlot(msg.SlotName); ok {
			// 第 1 次电话走应用1，第 2 次走应用2，轮换消耗各租户额度
			appIdx = callNo % len(f.apps)
		} else {
			urgency = "app" // 本时段电话次数已用完，降级
		}
	}

	// 从选中的应用开始尝试，失败则轮到下一个应用兜底
	var lastErr error
	for i := 0; i < len(f.apps); i++ {
		app := f.apps[(appIdx+i)%len(f.apps)]
		if err := f.sendAndUrge(app, msg, urgency); err != nil {
			lastErr = err
			log.Printf("[%s] 应用 %s 发送失败(%v)，尝试下一个应用", f.config.Name, app.cred.AppID, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("所有应用均发送失败: %w", lastErr)
}

// sendAndUrge 通过指定应用向其目标发送消息并按等级加急。
// 电话加急优先打该应用配置的专属号码(phone_mobile)，未配置则打本人。
func (f *FeishuAppNotifier) sendAndUrge(app *feishuAppClient, msg NotifyMessage, urgency string) error {
	token, err := app.tenantToken()
	if err != nil {
		return fmt.Errorf("获取 token 失败: %w", err)
	}

	targetMobile := f.config.Mobile
	if urgency == "phone" && app.cred.PhoneMobile != "" {
		targetMobile = app.cred.PhoneMobile
	}
	if targetMobile == "" {
		return fmt.Errorf("未配置接收人手机号")
	}
	openID, err := app.openIDForMobile(token, targetMobile)
	if err != nil {
		return fmt.Errorf("解析 %s 的 open_id 失败: %w", targetMobile, err)
	}

	messageID, err := f.sendText(token, openID, f.buildText(msg))
	if err != nil {
		return fmt.Errorf("发送消息失败: %w", err)
	}

	switch urgency {
	case "app":
		if err := f.urgent(token, messageID, "urgent_app", openID); err != nil {
			log.Printf("[%s] 应用内加急失败(消息已送达): %v", f.config.Name, err)
		}
	case "phone":
		if err := f.urgent(token, messageID, "urgent_phone", openID); err != nil {
			log.Printf("[%s] 电话加急失败(消息已送达): %v", f.config.Name, err)
			if err2 := f.urgent(token, messageID, "urgent_app", openID); err2 != nil {
				log.Printf("[%s] 降级应用内加急也失败: %v", f.config.Name, err2)
			}
		} else {
			log.Printf("[%s] 已通过应用 %s 电话加急 %s (%s)", f.config.Name, app.cred.AppID, targetMobile, msg.SlotName)
		}
	}
	return nil
}

func (f *FeishuAppNotifier) buildText(msg NotifyMessage) string {
	if msg.SysAlert {
		return msg.AlertTitle + "\n" + msg.AlertBody
	}
	if msg.IsCheck {
		var b strings.Builder
		b.WriteString("📋 打卡状态查询\n")
		for _, s := range msg.Slots {
			status := "❌ 未打卡"
			if s.Checked {
				status = "✅ 已打卡"
				if s.ReportTime != nil {
					status += " " + *s.ReportTime
				}
			}
			fmt.Fprintf(&b, "%s：%s\n", s.Name, status)
		}
		return b.String()
	}
	text := fmt.Sprintf("⏰ %s\n%s 当前状态：%s", msg.Title, msg.SlotName, msg.Status)
	if msg.Remaining != "" {
		text += fmt.Sprintf("\n距时段结束还剩 %s，请尽快打卡！", msg.Remaining)
	}
	return text
}

// backupMobiles 备用联系人优先级列表（兼容旧的单个 backup_mobile 写法）
func (f *FeishuAppNotifier) backupMobiles() []string {
	if len(f.config.BackupMobiles) > 0 {
		return f.config.BackupMobiles
	}
	if f.config.BackupMobile != "" {
		return []string{f.config.BackupMobile}
	}
	return nil
}

// callBackup 按优先级给备用联系人发消息并电话加急，成功一个即止
// （不占用本人电话次数配额，每时段每天最多一次）
func (f *FeishuAppNotifier) callBackup(msg NotifyMessage) {
	mobiles := f.backupMobiles()
	if len(mobiles) == 0 {
		return
	}
	f.mu.Lock()
	key := time.Now().Format("2006-01-02") + "|" + msg.SlotName
	if f.backupCalled[key] {
		f.mu.Unlock()
		return
	}
	f.backupCalled[key] = true
	f.mu.Unlock()

	backupAfter := config.Escalation.BackupPhoneAfterMin
	if backupAfter <= 0 {
		backupAfter = 25
	}
	text := fmt.Sprintf("⚠️ 代提醒：%s 开始已超过 %d 分钟仍未打卡，请转告尽快处理！", msg.SlotName, backupAfter)

	// 按优先级逐个备用号尝试；每个号依次尝试各应用租户，任一成功即结束
	for _, mobile := range mobiles {
		for _, app := range f.apps {
			token, err := app.tenantToken()
			if err != nil {
				continue
			}
			backupID, err := app.openIDForMobile(token, mobile)
			if err != nil || backupID == "" {
				log.Printf("[%s] 应用 %s 无法解析备用号 %s: %v", f.config.Name, app.cred.AppID, mobile, err)
				continue
			}
			messageID, err := f.sendText(token, backupID, text)
			if err != nil {
				log.Printf("[%s] 应用 %s 给备用号 %s 发消息失败: %v", f.config.Name, app.cred.AppID, mobile, err)
				continue
			}
			if err := f.urgent(token, messageID, "urgent_phone", backupID); err != nil {
				log.Printf("[%s] 备用号 %s 电话加急失败: %v", f.config.Name, mobile, err)
				continue
			}
			log.Printf("[%s] 已通过应用 %s 电话通知备用号 %s (%s)", f.config.Name, app.cred.AppID, mobile, msg.SlotName)
			return
		}
	}
	// 全部失败，允许下次触发重试
	f.mu.Lock()
	delete(f.backupCalled, key)
	f.mu.Unlock()
	log.Printf("[%s] 所有备用号均未能送达（可能未加入团队或数据权限未放开）", f.config.Name)
}

// resolveMobileOpenID 按手机号在指定租户内解析 open_id（无缓存的一次性查询）
func resolveMobileOpenID(token, mobile string) (string, error) {
	body, _ := json.Marshal(map[string][]string{"mobiles": {mobile}})
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			UserList []struct {
				UserID string `json:"user_id"`
			} `json:"user_list"`
		} `json:"data"`
	}
	if err := postFeishuJSON(feishuAPIBase+"/contact/v3/users/batch_get_id?user_id_type=open_id", token, body, &result); err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", fmt.Errorf("code=%d msg=%s", result.Code, result.Msg)
	}
	for _, u := range result.Data.UserList {
		if u.UserID != "" {
			return u.UserID, nil
		}
	}
	return "", fmt.Errorf("手机号未匹配到用户(未注册或未加入团队)")
}

// takePhoneSlot 返回本时段第几次拨打(从0开始)；超出上限返回 false
func (f *FeishuAppNotifier) takePhoneSlot(slotName string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := time.Now().Format("2006-01-02") + "|" + slotName
	n := f.phoneCalls[key]
	if n >= f.phoneMax() {
		return 0, false
	}
	f.phoneCalls[key] = n + 1
	return n, true
}

func (c *feishuAppClient) tenantToken() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}

	body, _ := json.Marshal(map[string]string{
		"app_id":     c.cred.AppID,
		"app_secret": c.cred.AppSecret,
	})
	var result struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := postFeishuJSON(feishuAPIBase+"/auth/v3/tenant_access_token/internal", "", body, &result); err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", fmt.Errorf("code=%d msg=%s", result.Code, result.Msg)
	}
	c.token = result.Token
	// 提前 5 分钟过期，避免边界失效
	c.tokenExpiry = time.Now().Add(time.Duration(result.Expire-300) * time.Second)
	return c.token, nil
}

// openIDForMobile 解析手机号在该租户的 open_id（带缓存）
func (c *feishuAppClient) openIDForMobile(token, mobile string) (string, error) {
	c.mu.Lock()
	if id := c.ids[mobile]; id != "" {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	id, err := resolveMobileOpenID(token, mobile)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.ids[mobile] = id
	c.mu.Unlock()
	log.Printf("[飞书应用 %s] %s open_id 解析成功: %s", c.cred.AppID, mobile, id)
	return id, nil
}

func (f *FeishuAppNotifier) sendText(token, openID, text string) (string, error) {
	content, _ := json.Marshal(map[string]string{"text": text})
	body, _ := json.Marshal(map[string]string{
		"receive_id": openID,
		"msg_type":   "text",
		"content":    string(content),
	})
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := postFeishuJSON(feishuAPIBase+"/im/v1/messages?receive_id_type=open_id", token, body, &result); err != nil {
		return "", err
	}
	if result.Code != 0 {
		return "", fmt.Errorf("code=%d msg=%s", result.Code, result.Msg)
	}
	return result.Data.MessageID, nil
}

// urgent kind: urgent_app | urgent_sms | urgent_phone
func (f *FeishuAppNotifier) urgent(token, messageID, kind, openID string) error {
	url := fmt.Sprintf("%s/im/v1/messages/%s/%s?user_id_type=open_id", feishuAPIBase, messageID, kind)
	body, _ := json.Marshal(map[string][]string{"user_id_list": {openID}})

	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return fmt.Errorf("解析响应失败: %s", string(respBody))
	}
	if result.Code != 0 {
		return fmt.Errorf("code=%d msg=%s", result.Code, result.Msg)
	}
	return nil
}

func postFeishuJSON(url, token string, body []byte, out interface{}) error {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return json.Unmarshal(respBody, out)
}
