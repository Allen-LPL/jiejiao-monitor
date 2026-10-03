# jj 服务部署与迁移

打卡提醒（飞书分级加急 + 电话）+ 雪球股票监控（阈值告警 + 定期盈亏播报）+ 多点故障转移。

## 目录

- `deploy/build.sh` —— 交叉编译全平台二进制（linux/darwin × amd64/arm64）
- `deploy/install.sh` —— 一键安装/升级/卸载（自动识别 systemd 或 launchd）
- `config.json` —— 全部配置（通知渠道、打卡时段、升级策略、股票、集群）

## 快速迁移到一台新机器

```bash
# 1. 在开发机编译全平台二进制
bash deploy/build.sh

# 2. 打包源目录（含二进制 + config + deploy 脚本）
tar czf jj-bundle.tar.gz jj-linux-* jj-darwin-* config.json deploy stocks.db 2>/dev/null

# 3. 传到新机器并安装
scp jj-bundle.tar.gz newhost:/root/
ssh newhost 'mkdir -p /root/jj-src && tar xzf /root/jj-bundle.tar.gz -C /root/jj-src && cd /root/jj-src && bash deploy/install.sh'
```

安装脚本会：拷贝对应平台二进制到 `/root/jj/jj-bin`、写入 systemd/launchd 服务、开机自启、崩溃自动重启。
已存在的 `config.json` 不会被覆盖。

## 多点多活 / 故障转移

每个节点在 `config.json` 的 `cluster` 段配置：

```json
"cluster": {
  "enabled": true,
  "node_name": "ys-company-01",
  "priority": 1,                       // 数字越小优先级越高，1=主节点
  "egress_check_url": "https://open.feishu.cn",
  "check_timeout_ms": 3000,
  "peers": [
    {"name": "mac-ssh-001", "priority": 2, "health_url": "http://100.64.0.9:8082/health"}
  ]
}
```

**工作原理**：发送任何告警前，节点先询问所有"更高优先级"对等节点的 `/health`。
只要有一个更高优先级节点在线且能对外发送，本节点就抑制，交给它发；否则本节点接管。

- 正常：只有主节点(priority 1)发送，无重复。
- 主节点宕机 / 进程挂掉：备用节点 `/health` 探测失败 → 备用接管。
- 主节点在线但对外发送能力丧失（如无法访问飞书）：主节点 `/health` 上报 `egress_ok=false` → 备用接管。

**主节点配置**：priority=1，peers 里列出所有备用节点。
**备用节点配置**：priority=2/3...，peers 里至少列出主节点（health_url 指向主节点的 Tailscale IP:8082）。

> 后续加机器：新节点 priority 递增，并把它加进其它节点的 peers；主节点也把新备用节点加进自己的 peers（非必需，但便于统一）。

## 健康检查

```bash
curl http://127.0.0.1:8082/health
# {"node":"ys-company-01","priority":1,"healthy":true,"egress_ok":true,"ts":...}
```

## 常用运维

```bash
# Linux
systemctl status jj ; journalctl -u jj -f ; tail -f /root/jj/jj.log
# macOS
launchctl list | grep jj ; tail -f /root/jj/jj.log
# 卸载
bash deploy/install.sh uninstall
```

## 安全升级已有节点

`install.sh` 为避免覆盖生产差异，会保留已有 `config.json`。当升级同时修改了接口版本或请求头时，应显式同步配置与 `.env`。推荐发布顺序：

1. 上传为 `.new` 文件；
2. 校验二进制 SHA-256、校验 JSON、确认 `.env` 权限为 `0600`；
3. 将现有二进制、配置和 `.env` 备份到带时间戳的 release 目录；
4. 原子替换文件并 `systemctl restart jj`；
5. 请求 `/health` 并查看启动日志。

不要把 `.env`、pcapng、接口响应、数据库或日志加入发布包或 Git。
