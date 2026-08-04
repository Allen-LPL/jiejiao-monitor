# jj 服务 · 架构与运维说明

打卡提醒 + 股票监控 + 多点故障转移的 Go 服务。本文汇总已实现的全部逻辑、部署方式、配置项、数据源分工与踩坑记录。

---

## 1. 系统概览

| 模块 | 作用 | 关键文件 |
|---|---|---|
| 打卡提醒 | 按时段检测未打卡，分级升级到应用内加急/电话加急 | `main.go`、`feishu_app.go` |
| 股票监控 | 雪球行情轮询，阈值/快速变动告警、定期盈亏播报、K线图、近1月回撤 | `socks.go`、`kline.go`、`metrics.go` |
| 多点多活 | 主备节点按优先级接管发送，避免重复与漏发 | `cluster.go` |
| 通知渠道 | 飞书自定义机器人、飞书自建应用(加急/图片)、钉钉 | `main.go`、`feishu_app.go` |
| 部署迁移 | 全平台交叉编译 + 一键安装(systemd/launchd) | `deploy/` |

进程启动流程(`main()`)：加载 `config.json` → 初始化通知管理器 → 初始化集群/故障转移(注册 `/health`) → 启动股票监控 → 启动 HTTP 服务(默认 `:8082`) → 每分钟 tick 检查打卡。

---

## 2. 打卡提醒

### 时段与检测
- `time_slots` 定义打卡时段（如 08:45–09:15、14:45–15:15、20:45–21:15）。
- 每分钟检查，命中时段且分钟为偶数时请求考勤 API；未打卡则触发提醒，每 2 分钟一轮。

### 分级升级策略（`escalation`，按距时段开始的分钟数）
| 距开始 | 动作 |
|---|---|
| 0–10 min | 普通消息（全部渠道，每 2 分钟一轮） |
| ≥10 min (`app_urgent_after_min`) | 飞书**应用内加急**（强弹窗，发本人） |
| ≥15 min (`phone_after_min`) | **电话加急**，每 2 分钟一通，最多 `phone_max_per_slot` 通 |
| ≥25 min (`backup_phone_after_min`) | 额外给**备用号**拨一通（每时段每天一次，不占本人配额） |

### 飞书电话加急（`feishu_app` 通知器，`feishu_app.go`）
- webhook 机器人**无法**打电话，必须用**企业自建应用**（`app_id`/`app_secret`）。
- 流程：发文本消息拿 `message_id` → `PATCH /im/v1/messages/:id/urgent_phone`。
- 加急电话只能打给**已绑定飞书账号且在应用可用范围内**的用户；未注册/未加入团队的号打不了。
- **多应用轮换额度**：`apps[]` 配多个自建应用，电话按 `phone_mobile` 分工——
  - 应用2(cli_aac0e91aaf78dce1) → 拨本人 15157105503（走应用2的 50 次/月额度）
  - 应用1(cli_aac0e9d8bc78dcdb) → 拨 15713722367（走应用1额度）
  - 备用兜底 `backup_mobiles` 按优先级：15713722367 → 13148309696，任一应用解析成功即拨。
- open_id 解析：优先用配置里预存的 `open_id`/`phone_open_id`，否则按 `mobile` 实时查（需应用 `contact:user.id:readonly` 权限）。

### 飞书应用所需权限（自建应用发版后生效）
`im:message`、`im:message.urgent:app`、`im:message.urgent:phone`、`contact:user.id:readonly`、`im:resource`(或 `im:resource:upload`，图片上传)。
> 权限加在**任一** `apps[]` 里的应用即可——图片上传会遍历所有应用，用有权限的那个。

---

## 3. 股票监控

### 数据源分工（关键：不同来源对 Go 的可达性不同）
| 用途 | 数据源 | 说明 |
|---|---|---|
| 实时行情 / 批量报价 | 雪球 `xueqiu.com` / `stock.xueqiu.com` | Go 可直连，无需 cookie |
| 调仓历史 | 雪球 cubes/rebalancing | 同上 |
| K线**图片** | 东财 `webquotepic.eastmoney.com` | Go 可直连，**必须带浏览器 User-Agent** |
| 近1月最高**数据** | 新浪 `money.finance.sina.com.cn` getKLineData | Go 可直连，免鉴权，结构化 JSON |

> ⚠️ **东财数据接口 `push2his.eastmoney.com` 对 Go 会直接 RST**（TLS 指纹识别，curl 能过 Go 过不了），因此近1月最高改用**新浪**，不要用东财数据接口。雪球 K线接口 `chart/kline.json` 需要登录 cookie，也不用。

### 告警类型
- **阈值告警**：`thresholds` 多级（warning 3% / alert 5% / urgent 8%），取绝对涨跌幅命中的最高级别，按 `level` 配额限流（warning 每时段1次、alert/urgent 每时段3次）。
- **快速变动**：相邻两次轮询价格变动 ≥5% 触发。
- **定期盈亏播报**：`report_interval_minutes`（默认30分钟），仅 A 股交易时段（9:30–11:30 / 13:00–15:00）发送。每只票报现价、今日涨跌、近1月回撤；有持仓的报市值+盈亏%+盈亏额，卡片底部报总盈亏。

### K线图（`kline.go`，`kline_enabled`）
- 每只票附**两张图**：分时（东财 `imageType=r`）+ 日K（`imageType=KXL`），可用 `kline_charts` 自定义。
- **钉钉**：markdown 直接内嵌图片 URL。
- **飞书**：卡片图片需 `img_key`——下载图片经自建应用上传换取（遍历 `apps[]` 用有 `im:resource` 权限的），`img_key` 同租户所有机器人卡片通用。无权限时降级为"📈 查看K线图"链接。
- img_key 按 `symbol|label` 缓存 3 分钟，避免重复上传。

### 成交量变化块状图（`volchart.go`，`vol_chart_enabled`）
- **自渲染**：从新浪日K取近1月每日成交量，用 Go stdlib `image/png` + `golang.org/x/image/font/basicfont` 画柱状图（A股**红涨绿跌**，5分钟缓存）。
- 图内标题用 **ASCII**（basicfont 不含中文，中文说明放飞书卡片标题"成交量变化(近1月)"里）。
- **飞书**：生成PNG → 经自建应用上传换 img_key → 内嵌。
- **钉钉**：需公网图片URL → jj 暴露 HTTP 端点 `GET /chart/vol?symbol=X`，经 frp+阿里云nginx 对外为 `chart_public_base`（`https://yx.ysfaedu.com/jjchart/vol?symbol=X`）。
- 依赖：因 `golang.org/x/image` 要求 go ≥ 1.25，go.mod 已升至 1.25。

### 近1月最高回撤（`metrics.go`）
- 取新浪最近40个交易日日K，按自然日期过滤出**近31天**，取最高价。
- 展示：`📉 近1月最高 ¥209.55(07-01) · 当前距最高 -16.60%`。
- 加入：飞书/钉钉的阈值与快速卡片、定期盈亏播报每行。10 分钟缓存。

### 持仓盈亏（`positions`）
- 每项 `{symbol, cost, shares}`，`shares=0` 表示仅观察（不算持仓盈亏）。
- 清仓后把 shares/cost 置 0（如 SH688693 已于 92.21 全仓卖出，已实现亏损 −7558.20 元，改为观察）。

---

## 4. 多点多活 / 故障转移（`cluster.go`）

**机制**：每个节点有 `priority`（数字越小优先级越高）。发任何告警前，先查所有**更高优先级**节点的 `/health`；只要有一个在线且能对外发送（`egress_ok`），本节点就**抑制**，否则**接管**。正常只有主节点发送，无重复。

**覆盖的故障**：
- 主节点进程挂/断电 → 备用探测失败 → 接管。
- 主节点在线但发不出去（如访问不了飞书）→ 其 `/health` 报 `egress_ok=false` → 备用接管。

**对外能力自检**：每 30 秒 GET `egress_check_url`（默认 open.feishu.cn），**连续 2 次失败**才降级（去抖，避免单次超时误判），超时默认 8 秒。

**当前拓扑**：
| 节点 | 角色 | 地址 | 端口 |
|---|---|---|---|
| ys-company-01 | 主(priority 1) | Tailscale 100.64.0.3 | :8082 |
| mac-001-ssh | 备(priority 2) | Tailscale 100.64.0.2 | :18082（8082被youxi占用故改端口） |

主节点 `peers` 指向备用 `http://100.64.0.2:18082/health`；备用 `peers` 指向主 `http://100.64.0.3:8082/health`。加机器只需 priority 递增并互相加入 peers，无需改代码。

**健康检查**：`curl http://127.0.0.1:8082/health` → `{"node":..,"priority":..,"healthy":true,"egress_ok":true}`。

> 已实测：停主节点→备用几秒内接管；恢复主节点→备用自动回切抑制。

---

## 5. 部署与迁移（`deploy/`）

- `deploy/build.sh`：交叉编译 linux/darwin × amd64/arm64 四个二进制。
- `deploy/install.sh`：一键安装/升级/卸载，自动识别 Linux(systemd) 或 macOS(launchd)，开机自启 + 崩溃重启，不覆盖已有 `config.json`。
- 迁移新机器：`build.sh` → 打包(二进制+config+deploy) → scp → `install.sh`。

**服务管理**：
```bash
# Linux 主节点
systemctl status jj ; journalctl -u jj -f ; tail -f /root/jj/jj.log
# macOS 备用节点
launchctl list | grep jj ; tail -f /Users/allen/jj/jj.log
```

**部署纪律**（吃过断电文件损坏的亏）：上传后 `sync` + 校验 md5，二进制先传 `.new` 再 mv 覆盖。

---

## 6. config.json 配置速查

```
attendance_api / auth_token   考勤接口与令牌
http_port                     HTTP端口(主:8082 备:18082)
notifiers[]                   通知渠道：
  type=feishu                 自定义机器人(webhook+secret)
  type=feishu_app             自建应用(加急电话/图片)：apps[]、mobile、phone_max_per_slot、backup_mobiles
  type=dingtalk               钉钉机器人(webhook+secret)
time_slots[]                  打卡时段
escalation                    app_urgent_after_min/phone_after_min/backup_phone_after_min
cluster                       enabled/node_name/priority/peers[]/egress_check_url/check_timeout_ms
stock_monitor
  batch_quote_url             雪球批量行情(symbol列表)
  thresholds[]                告警阈值
  report_interval_minutes     盈亏播报间隔
  positions[]                 持仓 {symbol,cost,shares}
  kline_enabled / kline_charts K线图开关与图列表
```

---

## 7. 基础设施

### 域名 / 站点
- **yx.ysfaedu.com → 阿里云 47.97.76.109**（方案B）：静态 youxi 站点部署在阿里云 `/www/youxi`，nginx 80→301→443，Let's Encrypt 证书(certbot 自动续期)。
- youxi 前端用项目原配镜像 `pxb7/backend_workspace(node v14)` 执行 `npm run build:test`（`.env.test` 全部指向 **ppaa66.com**，是自有域名），产物部署到阿里云。旧版备份 `/www/youxi.bak.2022`。
- **home.ysfaedu.com**：ddns-go 动态跟踪家庭公网 IP（原 yx 记录已让出）。ddns-go 装在 ys-company-01（systemd，管理页 `http://100.64.0.3:9876`），阿里云 DNS(RAM)。

### 网络要点（重要）
- 家庭电信线**能接受 80 入站**（apikf.com 可证），但**运营商按域名做 ICP 备案过滤**：备案接入在本电信线的域名(apikf.com)放行，备案在阿里云接入的(ysfaedu.com)被 RST。故 ysfaedu 子域要走阿里云(备案接入在阿里云)提供服务——这是选方案B的根本原因。
- 内网穿透 frp：ys-company-01 作为 frpc（systemd 自启），阿里云 47.97.76.109 作为 frps，转发考勤/上传等内网服务。Emergency-AI-Examiner 上传链路 `APP → 阿里云:17000(frps) → frpc → examiner_frontend(nginx:80) → examiner_api`。
- **股票图表公网化**（钉钉成交量图用）：frpc 代理 `ys-company-01-jj-chart`（jj :8082 → 阿里云 :17082），阿里云 nginx 在 yx.ysfaedu.com 加 `location /jjchart/ → 127.0.0.1:17082/chart/`，并用 iptables 把 17082 限制为本机(仅 nginx 可达，不对公网暴露 jj 的 /api)。
- Tailscale/headscale：headscale 自建 DERP 在阿里云(hs.ysfaedu.com:443 + STUN UDP 3478)，绕开 GFW 对官方 DERP 的干扰；开放 3478 后 Mac↔ys-company-01 直连。

### 待办安全项（尚未处理）
1. 公网**裸奔的数据库端口** 5432/5433/6379（examiner_db/accuprice_pg/redis）建议在路由器取消转发或防火墙封禁。
2. 阿里云用的是**主账号 AK**（权限过大），建议换成只授 `AliyunDNSFullAccess` 的 RAM 子账号，主账号 AK 作废。

---

## 8. 数据源可达性踩坑记录

| 现象 | 原因 | 处理 |
|---|---|---|
| 东财 push2his 数据接口 Go 直接 EOF/RST | TLS 指纹识别，拒非浏览器客户端 | 改用新浪 getKLineData |
| 东财 webquotepic 图片 Go 下载失败 | 缺 User-Agent | 请求加浏览器 UA |
| 雪球 chart/kline 返回400 | 需登录 cookie | 不用，改新浪 |
| 阿里云→家庭IP 全端口超时 | 阿里云出站对国内住宅IP受限 | 判断家宽可达性不能用阿里云做探针 |
| 本机测家庭IP 4ms RST | 本机 Clash 代理 fake-ip(198.18.x) 劫持 | 测试用 `--noproxy` 或换干净视角 |
| 飞书图片上传 99991672 | 应用无 im:resource 权限 | 权限加在任一 app，代码遍历上传 |
