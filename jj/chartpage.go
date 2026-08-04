package main

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
)

// A股代码格式：两位交易所前缀(SH/SZ/BJ) + 6位数字。严格校验，
// 拒绝任何其它字符，杜绝 symbol 反射进 HTML/URL 造成 XSS/注入。
var stockSymbolRe = regexp.MustCompile(`^(SH|SZ|BJ)[0-9]{6}$`)

func validStockSymbol(s string) bool {
	return stockSymbolRe.MatchString(s)
}

// ==================== 图表趋势页(点击盈亏播报标题打开) ====================
// 展示：分时(趋势)、日K(日线,含5日均线)、周K(周线)、成交量(近1月)。
// 东财图片为 HTTPS(避免页面混合内容被拦)；成交量走本服务自渲染。

func emKlinePic(symbol, imageType, typ string) string {
	return fmt.Sprintf("https://webquotepic.eastmoney.com/GetPic.aspx?nid=%s&imageType=%s&type=%s&unitWidth=-6&ef=&formula=RSI&AT=1",
		emSecID(symbol), imageType, typ)
}

func handleChartPage(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if !validStockSymbol(symbol) {
		http.Error(w, "invalid symbol", http.StatusBadRequest)
		return
	}
	name := r.URL.Query().Get("name")
	title := symbol
	if name != "" {
		title = html.EscapeString(name) + " " + symbol
	}

	fenshi := emKlinePic(symbol, "r", "")
	dayK := emKlinePic(symbol, "KXL", "")
	weekK := emKlinePic(symbol, "KXL", "w")
	volURL := "vol?symbol=" + symbol // 相对本路径 → /jjchart/vol

	page := `<!doctype html><html lang="zh-CN"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + title + ` 行情图</title>
<style>
body{margin:0;background:#f5f6f8;font-family:-apple-system,Segoe UI,Roboto,sans-serif;color:#222}
.wrap{max-width:820px;margin:0 auto;padding:14px}
h1{font-size:18px;margin:8px 4px}
.card{background:#fff;border-radius:10px;box-shadow:0 1px 4px rgba(0,0,0,.08);margin:12px 0;overflow:hidden}
.card h2{font-size:15px;margin:0;padding:10px 14px;border-bottom:1px solid #eee;color:#333}
.card img{display:block;width:100%;height:auto}
.tip{color:#999;font-size:12px;padding:6px 14px}
</style></head><body><div class="wrap">
<h1>` + title + `</h1>
<div class="card"><h2>分时(趋势)</h2><img loading="lazy" src="` + fenshi + `" alt="分时"></div>
<div class="card"><h2>日K线(含5日均线)</h2><img loading="lazy" src="` + dayK + `" alt="日K"></div>
<div class="card"><h2>周K线</h2><img loading="lazy" src="` + weekK + `" alt="周K"></div>
<div class="card"><h2>成交量变化(近1月)</h2><img loading="lazy" src="` + volURL + `" alt="成交量"></div>
<div class="tip">数据来源：东方财富(K线) / 新浪(成交量)。图片实时拉取。</div>
</div></body></html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "max-age=60")
	w.Write([]byte(page))
}

// chartPageURL 生成图表页公网URL(盈亏播报标题链接用)，未配置公网基址返回""
func chartPageURL(symbol string) string {
	base := strings.TrimRight(config.StockMonitor.ChartPublicBase, "/")
	if base == "" {
		return ""
	}
	return fmt.Sprintf("%s/page?symbol=%s", base, symbol)
}
