package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// ==================== 成交量变化块状图 ====================
// 自渲染：从新浪日K取近1个月每日成交量，画成柱状图(A股红涨绿跌)。
// 飞书：上传换 img_key 内嵌；钉钉：走公网URL(chart_public_base + /vol?symbol=)。

type volBar struct {
	day   string
	close float64
	vol   float64
}

// sinaVolBars 取某股票近1个月(自然31天)的日成交量
func sinaVolBars(symbol string) []volBar {
	url := fmt.Sprintf("https://money.finance.sina.com.cn/quotes_service/api/json_v2.php/CN_MarketData.getKLineData?symbol=%s&scale=240&datalen=40",
		sinaSymbol(symbol))
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		log.Printf("[成交量图] 获取失败 [%s]: %v", symbol, err)
		return nil
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var arr []struct {
		Day    string `json:"day"`
		Close  string `json:"close"`
		Volume string `json:"volume"`
	}
	if json.Unmarshal(body, &arr) != nil {
		return nil
	}
	cutoff := time.Now().In(getLocation()).AddDate(0, 0, -31).Format("2006-01-02")
	var out []volBar
	for _, k := range arr {
		d := k.Day
		if len(d) > 10 {
			d = d[:10]
		}
		if d < cutoff {
			continue
		}
		c, _ := strconv.ParseFloat(k.Close, 64)
		v, _ := strconv.ParseFloat(k.Volume, 64)
		out = append(out, volBar{day: d, close: c, vol: v})
	}
	return out
}

// ---------- PNG 渲染 ----------

func drawText(img *image.RGBA, x, y int, s string, c color.Color) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(c), Face: basicfont.Face7x13, Dot: fixed.P(x, y)}
	d.DrawString(s)
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.Color) {
	draw.Draw(img, image.Rect(x0, y0, x1, y1), image.NewUniform(c), image.Point{}, draw.Src)
}

// renderVolChartPNG 渲染成交量柱状图
func renderVolChartPNG(symbol, name string) []byte {
	bars := sinaVolBars(symbol)
	if len(bars) == 0 {
		return nil
	}
	const W, H = 760, 380
	const left, right, top, bottom = 60, 20, 44, 40
	plotW := W - left - right
	plotH := H - top - bottom

	img := image.NewRGBA(image.Rect(0, 0, W, H))
	fillRect(img, 0, 0, W, H, color.White)

	// 标题(图内用ASCII，中文说明由飞书卡片标题承载；basicfont不含中文)
	_ = name
	title := fmt.Sprintf("%s  Volume - last 1 month (x10k lots, red=up green=down)", symbol)
	drawText(img, left, 24, title, color.RGBA{30, 30, 30, 255})

	var maxVol float64
	for _, b := range bars {
		if b.vol > maxVol {
			maxVol = b.vol
		}
	}
	if maxVol <= 0 {
		return nil
	}

	// 坐标轴
	axis := color.RGBA{180, 180, 180, 255}
	fillRect(img, left, top, left+1, top+plotH, axis)          // Y
	fillRect(img, left, top+plotH, left+plotW, top+plotH+1, axis) // X

	// Y轴刻度(0 / 半 / 峰值，单位万手 = 股/1e6)
	grid := color.RGBA{235, 235, 235, 255}
	for i := 1; i <= 2; i++ {
		yv := maxVol * float64(i) / 2
		y := top + plotH - int(float64(plotH)*float64(i)/2)
		fillRect(img, left, y, left+plotW, y+1, grid)
		drawText(img, 4, y+4, fmt.Sprintf("%.0f", yv/1e6), color.RGBA{140, 140, 140, 255})
	}

	// 柱子
	n := len(bars)
	slot := float64(plotW) / float64(n)
	barW := int(slot * 0.62)
	if barW < 2 {
		barW = 2
	}
	redUp := color.RGBA{220, 60, 60, 255}   // 涨
	greenDn := color.RGBA{30, 160, 90, 255}  // 跌
	prevClose := bars[0].close
	for i, b := range bars {
		h := int(float64(plotH) * b.vol / maxVol)
		x0 := left + int(float64(i)*slot) + int((slot-float64(barW))/2)
		y0 := top + plotH - h
		c := redUp
		if b.close < prevClose {
			c = greenDn
		}
		prevClose = b.close
		fillRect(img, x0, y0, x0+barW, top+plotH, c)
	}

	// X轴首尾日期
	lbl := color.RGBA{120, 120, 120, 255}
	drawText(img, left, H-14, bars[0].day[5:], lbl)
	last := bars[n-1].day[5:]
	drawText(img, W-right-len(last)*7, H-14, last, lbl)

	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil {
		return nil
	}
	return buf.Bytes()
}

// ---------- 缓存 ----------

type volPNGCache struct {
	png []byte
	at  time.Time
}

var (
	volMu    sync.Mutex
	volCache = make(map[string]volPNGCache)
)

func volChartPNG(symbol, name string) []byte {
	volMu.Lock()
	if v, ok := volCache[symbol]; ok && time.Since(v.at) < 5*time.Minute {
		volMu.Unlock()
		return v.png
	}
	volMu.Unlock()
	p := renderVolChartPNG(symbol, name)
	if p != nil {
		volMu.Lock()
		volCache[symbol] = volPNGCache{png: p, at: time.Now()}
		volMu.Unlock()
	}
	return p
}

// ---------- HTTP 端点(供钉钉公网URL拉取) ----------

func handleVolChart(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if !validStockSymbol(symbol) {
		http.Error(w, "invalid symbol", http.StatusBadRequest)
		return
	}
	name := r.URL.Query().Get("name")
	p := volChartPNG(symbol, name)
	if p == nil {
		http.Error(w, "no data", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "max-age=300")
	w.Write(p)
}

// ---------- 通知集成 ----------

// volChartFeishuElements 飞书卡片的成交量图元素(上传内嵌，失败降级链接/省略)
func volChartFeishuElements(symbol, name string) []CardElement {
	if !config.StockMonitor.VolChartEnabled {
		return nil
	}
	p := volChartPNG(symbol, name)
	if p == nil {
		return nil
	}
	creds := feishuAppCreds()
	for _, cr := range creds {
		token, err := feishuAppToken(cr[0], cr[1])
		if err != nil {
			continue
		}
		key, err := uploadFeishuImage(token, p)
		if err != nil {
			continue
		}
		return []CardElement{
			{Tag: "div", Text: &CardText{Tag: "lark_md", Content: "**成交量变化(近1月)**"}},
			{Tag: "img", ImgKey: key, Alt: &CardText{Tag: "plain_text", Content: symbol + " 成交量"}, Mode: "fit_horizontal"},
		}
	}
	// 无上传权限时降级为公网URL链接(若配置了 base)
	if u := volChartURL(symbol, name); u != "" {
		return []CardElement{{Tag: "div", Text: &CardText{Tag: "lark_md", Content: fmt.Sprintf("[📊 查看 %s 成交量变化图](%s)", symbol, u)}}}
	}
	return nil
}

// volChartURL 钉钉/降级用的公网图片URL(不带中文name，避免URL含未编码中文导致抓取失败)
func volChartURL(symbol, name string) string {
	_ = name
	base := strings.TrimRight(config.StockMonitor.ChartPublicBase, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/vol?symbol=%s", base, symbol)
}

// volChartDingtalk 钉钉markdown的成交量图
func volChartDingtalk(symbol, name string) string {
	if !config.StockMonitor.VolChartEnabled {
		return ""
	}
	u := volChartURL(symbol, name)
	if u == "" {
		return ""
	}
	return fmt.Sprintf("\n\n**成交量变化(近1月)**\n\n![%s 成交量](%s)\n", symbol, u)
}
