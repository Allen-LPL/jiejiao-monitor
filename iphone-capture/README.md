# iPhone 蜂窝网络抓取

当前方案提供两层能力：

1. **USB 原始抓包**：无需 Xcode，可看到 iPhone 上每个进程的接口、目标 IP、端口和协议，并保存为 pcapng。HTTPS 正文保持加密。
2. **WireGuard + mitmproxy 解析**：iPhone 主动连接这台 Mac 上的 VPN 后，可解析 HTTP/HTTPS 方法、URL、头和正文，并在本机 Web UI 中查看。

所有操作仅适用于你拥有或已获授权测试的设备和流量。

## 查看设备状态

```bash
python3 iphone-capture/status.py
python3 iphone-capture/status.py --json
```

## 直接抓 4G/5G 原始数据包

先在 iPhone 关闭 Wi-Fi，保持 USB 连接并信任此电脑，然后运行：

```bash
./iphone-capture/capture-pcap.sh
```

默认只抓 iOS pcapd 上报的蜂窝接口 `pdp_ip`。如需先确认所有接口：

```bash
IPHONE_INTERFACE=all ./iphone-capture/capture-pcap.sh
IPHONE_PROCESS=目标进程名 ./iphone-capture/capture-pcap.sh
```

按 `Ctrl-C` 停止。文件保存在 `iphone-capture/captures/`，可用 Wireshark 打开。

提取明文 HTTP 请求、DNS 查询和 HTTPS TLS SNI（不传文件时自动选择最新捕获）：

```bash
./iphone-capture/analyze-pcap.sh
```

报告保存为同名的 `.requests.json` 文件，请求中的 Authorization、Cookie 等敏感头默认脱敏。

如需明文核对完整请求（包括 Authorization），用 Wireshark 打开 pcapng，显示过滤器使用：

```text
http.request && http.host == "yhq.sifa365.cn"
```

选中请求后执行 `Analyze → Follow → TCP Stream`，即可查看完整请求行、头和正文。原始捕获含 token 与个人信息，不能上传或提交 Git。

将最新 `listByDay` 请求中的 Authorization 和 `device-sn` 安全同步到 `jj/.env`：

```bash
./iphone-capture/sync-attendance-auth.sh
```

脚本使用最新抓包，也可以把指定 pcapng 路径作为第一个参数。凭证不会输出到终端，`.env` 以 `0600` 权限原子更新。

## 解析 HTTP/HTTPS

### 1. 建立公网回程

iPhone 使用 4G/5G 时无法访问 Mac 的局域网地址 `192.168.11.4`。必须满足下面任一条件：

- 在当前 H3C 路由器上，把公网 `UDP 51820` 转发到 `192.168.11.4:51820`；公网 IP 变化时建议使用 DDNS。
- 把 mitmproxy 部署到有公网 UDP 的 VPS。若运营商使用 CGNAT，这是更可靠的方式。

本机检测不到 NAT-PMP 或 UPnP，无法自动创建端口映射，需要登录路由器手工配置。不要把 Web UI 的 `TCP 8081` 暴露到公网。

### 2. 启动解析服务

```bash
./iphone-capture/start-mitm.sh
```

服务监听 `UDP 51820`，Web UI 只监听 `127.0.0.1:8081` 并自动打开带临时令牌的本地页面。解析结果写入：

- `captures/http.jsonl`：请求/响应 JSONL；Authorization、Cookie、Set-Cookie、X-API-Key 默认脱敏。
- `captures/flows-YYYYMMDD.mitm`：mitmproxy 原始流文件。

### 3. 生成并导入 iPhone 配置

把参数换成蜂窝网络实际可访问的公网地址或域名：

```bash
python3 iphone-capture/export-wireguard-config.py vpn.example.com:51820
```

脚本会生成 `iphone-wireguard.conf` 和二维码 PNG。配置包含私钥，不能分享。安装 iOS WireGuard 客户端，扫描二维码并启用隧道。

### 4. 安装并完全信任 mitmproxy CA

VPN 已连接后，在 iPhone Safari 打开 `http://mitm.it` 安装证书。随后前往：

`设置 → 通用 → 关于本机 → 证书信任设置 → 对 mitmproxy 开启完全信任`

先用 Safari 请求普通 HTTP 和 HTTPS 页面确认链路，再打开目标 App。

## 已知边界

- 启用了证书固定（certificate pinning）的 App 会拒绝 mitmproxy 证书，只能看到连接元数据，不能直接解密正文。
- QUIC/HTTP3 基于 UDP；必要时 App 会回退到 TCP/TLS，部分 App 不会回退。
- mitmproxy 当前对透明代理 IPv6 的支持有限，生成配置默认只路由 IPv4；蜂窝 IPv6 流量可能不会进入隧道。
- USB pcap 是旁路观察，不会自动解密 TLS；WireGuard 是解析内容所需的主动代理链路。
