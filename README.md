# jiejiao-monitor

考勤状态监控、分级提醒、股票监控和 iPhone 蜂窝网络请求分析工具。

项目由两个部分组成：

- `jj/`：Go 编写的常驻监控服务，轮询签到接口，在未打卡或登录态失效时通过飞书、钉钉发送提醒。
- `iphone-capture/`：通过 USB 读取 iPhone 状态、抓取 4G/5G 数据包、解析明文 HTTP、提取 TLS SNI，并从当前 App 请求安全刷新考勤 token。

仅可分析自己拥有或已获得明确授权的设备和流量。

## 目录

```text
.
├── iphone-capture/
│   ├── capture-pcap.sh           # 抓取 iPhone 蜂窝接口 pdp_ip
│   ├── analyze-pcap.sh           # 提取 HTTP、DNS、TLS SNI
│   ├── sync-attendance-auth.sh   # 更新 jj/.env 中的 token/device-sn
│   └── start-mitm.sh             # mitmproxy WireGuard 解析服务
└── jj/
    ├── main.go                   # 服务入口与考勤逻辑
    ├── config.json               # 非敏感配置，密钥使用 ${VAR}
    ├── .env.example              # 密钥模板
    └── deploy/                   # 构建与安装脚本
```

## iPhone 4G/5G 抓包

环境要求：macOS、USB 连接并信任的 iPhone、[`uv`](https://docs.astral.sh/uv/)。脚本会通过 `uv` 获取 `pymobiledevice3`，无需安装完整 Xcode。

先关闭 iPhone Wi-Fi，再执行：

```bash
./iphone-capture/capture-pcap.sh
```

默认监听 iOS pcapd 上报的蜂窝接口 `pdp_ip`，文件写入 `iphone-capture/captures/`。按 `Ctrl-C` 停止。

解析最新抓包：

```bash
./iphone-capture/analyze-pcap.sh
```

同目录会生成权限为 `0600` 的 `.requests.json`，包含：

- 明文 HTTP 的方法、完整 URL、请求头和 TCP 重组后的 POST 正文；
- DNS 查询域名；
- HTTPS TLS ClientHello 中可见的 SNI 域名；
- 发起连接的 iOS 进程名。

Authorization、Cookie 等敏感请求头默认显示为 `<redacted>`。

### 明文查看完整 HTTP 请求

需要核对原始 Authorization 或全部请求头时，用 Wireshark 打开 `.pcapng`：

```text
显示过滤器：http.request && http.host == "yhq.sifa365.cn"
```

选中目标请求后依次使用：

```text
Analyze → Follow → TCP Stream
```

即可查看完整请求行、请求头和正文。抓包包含 token 与个人信息，不要上传、提交 Git 或发送给第三方。

HTTP 是明文，可以直接恢复内容；HTTPS 旁路抓包只能看到 IP、端口、进程、DNS/SNI。需要解析 HTTPS 正文时，使用：

```bash
./iphone-capture/start-mitm.sh
```

并按 [iPhone 抓包详细说明](iphone-capture/README.md) 配置 WireGuard 和 iOS CA 完全信任。证书固定的 App 仍可能拒绝中间人证书。

## 从 iPhone 刷新考勤 token

确保最新抓包中已经出现 `/api/mob/signIn/onlineSigns/listByDay`，然后运行：

```bash
./iphone-capture/sync-attendance-auth.sh
```

脚本从最新请求提取 Authorization 和 `device-sn`，不在终端回显完整值，并以 `0600` 权限原子更新 `jj/.env`。

## 运行监控服务

```bash
cd jj
cp .env.example .env
# 编辑 .env，填写通知渠道和 API 密钥
go test ./...
go run .
```

默认配置监听 `:8082`。查询接口需要 `X-API-Key` 请求头或 `key` 参数：

```bash
curl -H "X-API-Key: $JJ_API_KEY" http://127.0.0.1:8082/api/check
curl http://127.0.0.1:8082/health
```

真实凭证只放在 `jj/.env`，不要写入 `config.json`。

## 构建与部署

构建 Linux/macOS 的 amd64/arm64 二进制：

```bash
cd jj
bash deploy/build.sh
```

在目标机器运行安装脚本：

```bash
bash deploy/install.sh
```

安装脚本会选择当前平台对应的二进制，安装 systemd/launchd 服务并启用自动重启。已有 `config.json` 不会被覆盖；升级接口版本或请求头时应单独同步配置与 `.env`。详细步骤见 [部署说明](jj/deploy/README.md)。

生产发布建议采用 `.new` 文件、SHA-256 校验、旧版本备份和原子替换，再执行健康检查：

```bash
systemctl restart jj
curl -fsS http://127.0.0.1:8082/health
```

## 验证

```bash
cd jj
go test ./...
go vet ./...
```

Python 与 Shell 工具可执行：

```bash
python3 -m py_compile iphone-capture/*.py
bash -n iphone-capture/*.sh
```

## 安全说明

- `.env`、pcapng、解析报告、数据库、日志和编译产物均被忽略，不应进入 Git。
- 当前考勤上游使用明文 HTTP，Authorization 和个人数据可能被链路中的第三方读取或篡改；条件允许时应由上游迁移到 HTTPS。
- token 失效时，服务会跳过未打卡判定并发送登录态失效告警，避免误报。
