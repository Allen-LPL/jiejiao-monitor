#!/usr/bin/env bash
# jj 打卡提醒与股票监控服务 —— 一键安装/迁移脚本
# 用法：
#   ./install.sh                      # 安装/升级（自动识别平台，装 systemd 或 launchd）
#   NODE_NAME=mac-ssh-001 PRIORITY=2 ./install.sh   # 指定节点名与优先级（备用节点）
#   ./install.sh uninstall            # 卸载
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-/root/jj}"
SERVICE_NAME="jj"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OS="$(uname -s)"
ARCH="$(uname -m)"

log() { printf '\033[0;32m[install]\033[0m %s\n' "$*"; }
err() { printf '\033[0;31m[error]\033[0m %s\n' "$*" >&2; }

# ---- 选择二进制 ----
pick_binary() {
  case "$OS-$ARCH" in
    Linux-x86_64)   echo "jj-linux-amd64" ;;
    Linux-aarch64)  echo "jj-linux-arm64" ;;
    Darwin-arm64)   echo "jj-darwin-arm64" ;;
    Darwin-x86_64)  echo "jj-darwin-amd64" ;;
    *) err "不支持的平台: $OS-$ARCH"; exit 1 ;;
  esac
}

uninstall() {
  if [ "$OS" = "Linux" ]; then
    systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    systemctl disable "$SERVICE_NAME" 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload
  else
    launchctl unload "$HOME/Library/LaunchAgents/com.jj.${SERVICE_NAME}.plist" 2>/dev/null || true
    rm -f "$HOME/Library/LaunchAgents/com.jj.${SERVICE_NAME}.plist"
  fi
  log "已卸载 $SERVICE_NAME（保留 $INSTALL_DIR 数据）"
}

if [ "${1:-}" = "uninstall" ]; then uninstall; exit 0; fi

BIN="$(pick_binary)"
if [ ! -f "$SCRIPT_DIR/../$BIN" ] && [ ! -f "$SCRIPT_DIR/$BIN" ]; then
  err "找不到二进制 $BIN，请先在源码目录执行 deploy/build.sh 生成全平台二进制"
  exit 1
fi
SRC_BIN="$SCRIPT_DIR/../$BIN"; [ -f "$SRC_BIN" ] || SRC_BIN="$SCRIPT_DIR/$BIN"

# ---- 部署文件 ----
mkdir -p "$INSTALL_DIR"
cp "$SRC_BIN" "$INSTALL_DIR/jj-bin.new"
chmod +x "$INSTALL_DIR/jj-bin.new"
mv "$INSTALL_DIR/jj-bin.new" "$INSTALL_DIR/jj-bin"

# 配置：不存在才拷贝，避免覆盖线上配置
if [ ! -f "$INSTALL_DIR/config.json" ]; then
  cp "$SCRIPT_DIR/../config.json" "$INSTALL_DIR/config.json"
  log "已写入默认 config.json（请按节点修改 cluster.node_name / cluster.priority）"
else
  log "保留已有 config.json"
fi
sync

# ---- 安装服务 ----
if [ "$OS" = "Linux" ]; then
  cat > "/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=jj 打卡提醒与股票监控服务
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/jj-bin
Restart=always
RestartSec=5
StandardOutput=append:$INSTALL_DIR/jj.log
StandardError=append:$INSTALL_DIR/jj.log

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "$SERVICE_NAME"
  systemctl restart "$SERVICE_NAME"
  sleep 2
  systemctl is-active "$SERVICE_NAME" && log "systemd 服务已启动并设为开机自启"
else
  PLIST="$HOME/Library/LaunchAgents/com.jj.${SERVICE_NAME}.plist"
  mkdir -p "$HOME/Library/LaunchAgents"
  cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.jj.${SERVICE_NAME}</string>
  <key>ProgramArguments</key><array><string>$INSTALL_DIR/jj-bin</string></array>
  <key>WorkingDirectory</key><string>$INSTALL_DIR</string>
  <key>KeepAlive</key><true/>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>$INSTALL_DIR/jj.log</string>
  <key>StandardErrorPath</key><string>$INSTALL_DIR/jj.log</string>
</dict></plist>
EOF
  launchctl unload "$PLIST" 2>/dev/null || true
  launchctl load "$PLIST"
  sleep 2
  launchctl list | grep -q "com.jj.${SERVICE_NAME}" && log "launchd 服务已启动并设为开机自启"
fi

log "安装完成：$INSTALL_DIR    健康检查：curl http://127.0.0.1:8082/health"
