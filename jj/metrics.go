package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ==================== 近1个月最高点回撤指标 ====================
//
// 取近1个月(自然30天)的日K最高价，找出区间最高点，
// 计算当前价相对该最高点的增减百分比(通常为负=回撤)。

type monthHighEntry struct {
	high float64
	date string
	at   time.Time
}

var (
	monthHighMu    sync.Mutex
	monthHighCache = make(map[string]monthHighEntry) // symbol -> 近1月最高
)

// sinaSymbol 将 SH688110 -> sh688110（新浪格式）
func sinaSymbol(symbol string) string {
	return strings.ToLower(strings.TrimSpace(symbol))
}

// monthHigh 返回某股票近1个月自然日内的最高价及其日期(10分钟缓存)
// 数据源：新浪日K(Go可直连、免鉴权)。东财数据接口对Go会RST，故不用。
func monthHigh(symbol string) (float64, string, bool) {
	monthHighMu.Lock()
	if e, ok := monthHighCache[symbol]; ok && time.Since(e.at) < 10*time.Minute {
		monthHighMu.Unlock()
		return e.high, e.date, true
	}
	monthHighMu.Unlock()

	// 取最近40个交易日，再按自然日期过滤出近31天
	url := fmt.Sprintf("https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s&scale=240&datalen=40",
		sinaSymbol(symbol))

	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[指标] 获取近1月K线失败 [%s]: %v", symbol, err)
		return 0, "", false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var arr []struct {
		Day  string `json:"day"`
		High string `json:"high"`
	}
	if err := json.Unmarshal(body, &arr); err != nil || len(arr) == 0 {
		log.Printf("[指标] 近1月K线无数据 [%s]", symbol)
		return 0, "", false
	}

	cutoff := time.Now().In(getLocation()).AddDate(0, 0, -31).Format("2006-01-02")
	var maxHigh float64
	var maxDate string
	for _, k := range arr {
		day := k.Day
		if len(day) > 10 {
			day = day[:10]
		}
		if day < cutoff { // 只看近1个月自然天数
			continue
		}
		h, err := strconv.ParseFloat(k.High, 64)
		if err != nil {
			continue
		}
		if h > maxHigh {
			maxHigh = h
			maxDate = day
		}
	}
	if maxHigh <= 0 {
		return 0, "", false
	}

	monthHighMu.Lock()
	monthHighCache[symbol] = monthHighEntry{high: maxHigh, date: maxDate, at: time.Now()}
	monthHighMu.Unlock()
	return maxHigh, maxDate, true
}

// monthHighDesc 返回展示文本，如 "近1月最高 ¥209.55(06-15) · 当前距最高 -15.62%"
// current 为当前价。无数据返回 ""。
func monthHighDesc(symbol string, current float64) string {
	high, date, ok := monthHigh(symbol)
	if !ok || high <= 0 || current <= 0 {
		return ""
	}
	pct := (current - high) / high * 100
	md := date
	if len(date) >= 10 {
		md = date[5:] // MM-DD
	}
	return fmt.Sprintf("近1月最高 ¥%.2f(%s) · 当前距最高 %+.2f%%", high, md, pct)
}

// monthHighFeishuElements 飞书卡片中的近1月回撤元素(无数据返回nil)
func monthHighFeishuElements(symbol string, current float64) []CardElement {
	desc := monthHighDesc(symbol, current)
	if desc == "" {
		return nil
	}
	return []CardElement{{Tag: "div", Text: &CardText{Tag: "lark_md", Content: "📉 " + desc}}}
}

// monthHighDingtalk 钉钉markdown中的近1月回撤行(无数据返回"")
func monthHighDingtalk(symbol string, current float64) string {
	desc := monthHighDesc(symbol, current)
	if desc == "" {
		return ""
	}
	return "\n\n📉 " + desc
}
