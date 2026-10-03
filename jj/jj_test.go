package main

import (
	"encoding/json"
	"testing"
)

// TestSignResponseUnmarshal 校验成功与登录失效响应都能进入业务码判断。
// 生产故障回归：401 的 data 是 []string，不能因此阻断 code/msg 解析。
func TestSignResponseUnmarshal(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantCode   int
		wantMsg    string
		wantRecord int
	}{
		{
			name:       "成功响应保留签到记录",
			payload:    `{"code":200,"msg":"操作成功","data":[{"pkId":1,"createTime":"2026-10-03 08:47:09","isValid":"1"}]}`,
			wantCode:   200,
			wantMsg:    "操作成功",
			wantRecord: 1,
		},
		{
			name:       "登录失效字符串数组仍可解析业务码",
			payload:    `{"msg":"无法访问系统资源，请您重新登录后再试","code":401,"data":["请求访问失败"]}`,
			wantCode:   401,
			wantMsg:    "无法访问系统资源，请您重新登录后再试",
			wantRecord: 0,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var got SignResponse
			if err := json.Unmarshal([]byte(test.payload), &got); err != nil {
				t.Fatalf("解析响应失败: %v", err)
			}
			if got.Code != test.wantCode || got.Msg != test.wantMsg || len(got.Data) != test.wantRecord {
				t.Fatalf("解析结果=%+v，期望 code=%d msg=%q records=%d", got, test.wantCode, test.wantMsg, test.wantRecord)
			}
		})
	}
}

// TestIsAuthExpired 校验登录态失效判定（决定是否发 token 过期告警）
func TestIsAuthExpired(t *testing.T) {
	cases := []struct {
		code int
		msg  string
		want bool
	}{
		{200, "操作成功", false},              // 正常
		{401, "无法访问系统资源，请您重新登录后再试", true}, // 新后端(RuoYi)
		{105, "账号异常，请重新登录", true},         // 旧后端
		{500, "系统内部错误", false},            // 其它错误不算登录态失效
		{0, "请登录后再操作", true},              // 仅凭关键字兜底
	}
	for _, c := range cases {
		if got := isAuthExpired(c.code, c.msg); got != c.want {
			t.Errorf("isAuthExpired(%d, %q)=%v，期望 %v", c.code, c.msg, got, c.want)
		}
	}
}

// TestFindSignBySlot 校验按时段窗口匹配当日签到（新版接口的核心检测逻辑）
func TestFindSignBySlot(t *testing.T) {
	loc := getLocation()
	// 模拟当日两条有效签到（08:47 和 14:45）+ 一条无效签到
	records := []SignRecord{
		{CreateTime: "2026-08-03 14:45:31", IsValid: "1", SignTypeStr: "APP签到"},
		{CreateTime: "2026-08-03 08:47:09", IsValid: "1", SignTypeStr: "APP签到"},
		{CreateTime: "2026-08-03 09:00:00", IsValid: "0", SignTypeStr: "APP签到"}, // 无效，应忽略
	}
	slot1 := TimeSlotConfig{StartHour: 8, StartMinute: 45, EndHour: 9, EndMinute: 15, Name: "第一次"}
	slot2 := TimeSlotConfig{StartHour: 14, StartMinute: 45, EndHour: 15, EndMinute: 15, Name: "第二次"}
	slot3 := TimeSlotConfig{StartHour: 20, StartMinute: 45, EndHour: 21, EndMinute: 15, Name: "第三次"}

	if rec := findSignBySlot(records, slot1, loc); rec == nil || rec.CreateTime != "2026-08-03 08:47:09" {
		t.Errorf("slot1 应匹配 08:47:09，实际 %+v", rec)
	}
	if rec := findSignBySlot(records, slot2, loc); rec == nil || rec.CreateTime != "2026-08-03 14:45:31" {
		t.Errorf("slot2 应匹配 14:45:31，实际 %+v", rec)
	}
	if rec := findSignBySlot(records, slot3, loc); rec != nil {
		t.Errorf("slot3 无签到应为未打卡，实际 %+v", rec)
	}
}

// TestFindSignBySlot_IgnoresInvalid 无效签到即便落在窗口内也不算打卡
func TestFindSignBySlot_IgnoresInvalid(t *testing.T) {
	loc := getLocation()
	records := []SignRecord{
		{CreateTime: "2026-08-03 09:00:00", IsValid: "0"}, // 窗口内但无效
	}
	slot1 := TimeSlotConfig{StartHour: 8, StartMinute: 45, EndHour: 9, EndMinute: 15}
	if rec := findSignBySlot(records, slot1, loc); rec != nil {
		t.Errorf("无效签到不应计入打卡，实际 %+v", rec)
	}
}

// TestFindSignBySlot_PicksEarliest 窗口内多条时取最早一条作为打卡时间
func TestFindSignBySlot_PicksEarliest(t *testing.T) {
	loc := getLocation()
	records := []SignRecord{
		{CreateTime: "2026-08-03 09:05:00", IsValid: "1"},
		{CreateTime: "2026-08-03 08:50:00", IsValid: "1"},
	}
	slot1 := TimeSlotConfig{StartHour: 8, StartMinute: 45, EndHour: 9, EndMinute: 15}
	if rec := findSignBySlot(records, slot1, loc); rec == nil || rec.CreateTime != "2026-08-03 08:50:00" {
		t.Errorf("应取最早 08:50:00，实际 %+v", rec)
	}
}
