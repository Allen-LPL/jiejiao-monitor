package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ==================== K线图 ====================
//
// 每只股票附带两张图：分时(时线) + 日K。
// 钉钉：markdown 内嵌图片 URL(东财 HTTPS)。
// 飞书：卡片图片需 img_key，先下载再经任一有 im:resource 权限的自建应用上传换取；
//       全部应用都无权限时降级为卡片内 "查看图" 链接。

// KlineChart 一种K线图配置
type KlineChart struct {
	Label    string `json:"label"`
	Template string `json:"template"` // %s = 东财 secid，如 1.688110
}

var defaultKlineCharts = []KlineChart{
	{Label: "分时", Template: "https://webquotepic.eastmoney.com/GetPic.aspx?nid=%s&imageType=r&unitWidth=-6&ef=&formula=RSI&AT=1"},
	{Label: "日K", Template: "https://webquotepic.eastmoney.com/GetPic.aspx?nid=%s&imageType=KXL&type=&unitWidth=-6&ef=&formula=RSI&AT=1"},
}

func klineCharts() []KlineChart {
	if len(config.StockMonitor.KlineCharts) > 0 {
		return config.StockMonitor.KlineCharts
	}
	return defaultKlineCharts
}

// emSecID 将 SH688110 -> 1.688110、SZ002436 -> 0.002436（东财 secid，沪1深0）
func emSecID(symbol string) string {
	s := strings.ToUpper(strings.TrimSpace(symbol))
	switch {
	case strings.HasPrefix(s, "SH"):
		return "1." + s[2:]
	case strings.HasPrefix(s, "SZ"), strings.HasPrefix(s, "BJ"):
		return "0." + s[2:]
	default:
		return "1." + s
	}
}

func chartURL(c KlineChart, symbol string) string {
	return fmt.Sprintf(c.Template, emSecID(symbol))
}

// ---------- 飞书 img_key 缓存与上传 ----------

type imgKeyCache struct {
	key string
	at  time.Time
}

var (
	klineMu       sync.Mutex
	klineImgKeys  = make(map[string]imgKeyCache) // "symbol|label" -> img_key
	feishuTokenMu sync.Mutex
	feishuTokens  = make(map[string]feishuTokenCache) // app_id -> token
)

type feishuTokenCache struct {
	token  string
	expiry time.Time
}

// feishuAppCreds 返回所有配置的飞书自建应用凭证(用于逐个尝试上传)
func feishuAppCreds() [][2]string {
	var out [][2]string
	for _, n := range config.Notifiers {
		if n.Type != "feishu_app" || !n.Enabled {
			continue
		}
		if len(n.Apps) > 0 {
			for _, a := range n.Apps {
				out = append(out, [2]string{a.AppID, a.AppSecret})
			}
		} else if n.AppID != "" {
			out = append(out, [2]string{n.AppID, n.AppSecret})
		}
	}
	return out
}

func feishuAppToken(appID, appSecret string) (string, error) {
	feishuTokenMu.Lock()
	defer feishuTokenMu.Unlock()
	if c, ok := feishuTokens[appID]; ok && time.Now().Before(c.expiry) {
		return c.token, nil
	}
	body, _ := json.Marshal(map[string]string{"app_id": appID, "app_secret": appSecret})
	var res struct {
		Code   int    `json:"code"`
		Msg    string `json:"msg"`
		Token  string `json:"tenant_access_token"`
		Expire int    `json:"expire"`
	}
	if err := postFeishuJSON(feishuAPIBase+"/auth/v3/tenant_access_token/internal", "", body, &res); err != nil {
		return "", err
	}
	if res.Code != 0 {
		return "", fmt.Errorf("code=%d msg=%s", res.Code, res.Msg)
	}
	feishuTokens[appID] = feishuTokenCache{token: res.Token, expiry: time.Now().Add(time.Duration(res.Expire-300) * time.Second)}
	return res.Token, nil
}

// chartImgKey 返回某图的飞书 img_key，失败返回 ""(3分钟缓存)
func chartImgKey(c KlineChart, symbol string) string {
	cacheKey := symbol + "|" + c.Label
	klineMu.Lock()
	if v, ok := klineImgKeys[cacheKey]; ok && time.Since(v.at) < 3*time.Minute {
		key := v.key
		klineMu.Unlock()
		return key
	}
	klineMu.Unlock()

	creds := feishuAppCreds()
	if len(creds) == 0 {
		return ""
	}

	// 下载图片
	imgReq, _ := http.NewRequest("GET", chartURL(c, symbol), nil)
	imgReq.Header.Set("User-Agent", userAgent)
	imgResp, err := (&http.Client{Timeout: 10 * time.Second}).Do(imgReq)
	if err != nil {
		log.Printf("[K线] 下载图片失败 [%s %s]: %v", symbol, c.Label, err)
		return ""
	}
	imgData, err := io.ReadAll(imgResp.Body)
	imgResp.Body.Close()
	if err != nil || len(imgData) == 0 {
		log.Printf("[K线] 读取图片失败 [%s %s]", symbol, c.Label)
		return ""
	}

	// 逐个应用尝试上传，用第一个有 im:resource 权限的
	var lastErr error
	for _, cr := range creds {
		token, err := feishuAppToken(cr[0], cr[1])
		if err != nil {
			lastErr = err
			continue
		}
		key, err := uploadFeishuImage(token, imgData)
		if err != nil {
			lastErr = err
			continue
		}
		klineMu.Lock()
		klineImgKeys[cacheKey] = imgKeyCache{key: key, at: time.Now()}
		klineMu.Unlock()
		return key
	}
	log.Printf("[K线] 飞书上传失败 [%s %s]: %v(需某个自建应用开通 im:resource)", symbol, c.Label, lastErr)
	return ""
}

func uploadFeishuImage(token string, imgData []byte) (string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("image_type", "message")
	fw, err := w.CreateFormFile("image", "chart.png")
	if err != nil {
		return "", err
	}
	fw.Write(imgData)
	w.Close()

	req, err := http.NewRequest(http.MethodPost, feishuAPIBase+"/im/v1/images", &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var res struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			ImageKey string `json:"image_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &res); err != nil {
		return "", fmt.Errorf("解析失败: %s", string(respBody))
	}
	if res.Code != 0 {
		return "", fmt.Errorf("code=%d msg=%s", res.Code, res.Msg)
	}
	return res.Data.ImageKey, nil
}

// klineFeishuElements 返回附加到飞书卡片的K线元素(分时+日K)。
// 有 img_key 则内嵌图片，否则降级为 markdown 链接。
func klineFeishuElements(symbol string) []CardElement {
	if !config.StockMonitor.KlineEnabled {
		return nil
	}
	var els []CardElement
	for _, c := range klineCharts() {
		if key := chartImgKey(c, symbol); key != "" {
			els = append(els,
				CardElement{Tag: "div", Text: &CardText{Tag: "lark_md", Content: "**" + c.Label + "**"}},
				CardElement{Tag: "img", ImgKey: key, Alt: &CardText{Tag: "plain_text", Content: symbol + " " + c.Label}, Mode: "fit_horizontal"},
			)
		} else {
			els = append(els, CardElement{Tag: "div", Text: &CardText{Tag: "lark_md",
				Content: fmt.Sprintf("[📈 查看 %s %s图](%s)", symbol, c.Label, chartURL(c, symbol))}})
		}
	}
	return els
}

// klineDingtalkMarkdown 返回附加到钉钉markdown的K线图片语法(分时+日K)
func klineDingtalkMarkdown(symbol string) string {
	if !config.StockMonitor.KlineEnabled {
		return ""
	}
	var b strings.Builder
	for _, c := range klineCharts() {
		fmt.Fprintf(&b, "\n\n**%s**\n\n![%s %s](%s)\n", c.Label, symbol, c.Label, chartURL(c, symbol))
	}
	return b.String()
}
