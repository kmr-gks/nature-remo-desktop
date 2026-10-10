#!/bin/bash
# Remo Panel — macOS セットアップ
#
#   ./install-mac.sh
#
# バイナリを ~/bin に置き、ログイン時に自動起動する launchd エージェントを
# 登録します。アンインストールは ./install-mac.sh uninstall
set -euo pipefail

LABEL="local.remo-panel"
PLIST="$HOME/Library/LaunchAgents/$LABEL.plist"
BIN_DIR="$HOME/bin"
BIN="$BIN_DIR/remo-panel"
LOG="$HOME/Library/Logs/remo-panel.log"
PORT="${PORT:-8080}"

if [ "${1:-}" = "uninstall" ]; then
  launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
  rm -f "$PLIST"
  echo "自動起動を解除しました。バイナリ ($BIN) と設定 (~/.remo-panel) は残しています。"
  exit 0
fi

# --- 1. アーキテクチャに合うバイナリを選ぶ -----------------------------------
cd "$(dirname "$0")/.."
case "$(uname -m)" in
  arm64) SRC="dist/remo-panel-mac-arm64" ;;
  x86_64) SRC="dist/remo-panel-mac-intel" ;;
  *) echo "未対応のアーキテクチャ: $(uname -m)" >&2; exit 1 ;;
esac
[ -f "$SRC" ] || { echo "$SRC が見つかりません" >&2; exit 1; }

mkdir -p "$BIN_DIR"
cp "$SRC" "$BIN"
chmod +x "$BIN"

# ダウンロードしたファイルには隔離属性が付くため、外しておかないと
# Gatekeeper が「開発元を確認できないため開けません」と言って実行を止めます。
xattr -d com.apple.quarantine "$BIN" 2>/dev/null || true

# --- 2. launchd エージェントを書く -------------------------------------------
mkdir -p "$(dirname "$PLIST")" "$(dirname "$LOG")"
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>              <string>$LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN</string>
    <string>-port</string>
    <string>$PORT</string>
  </array>
  <key>RunAtLoad</key>          <true/>
  <key>KeepAlive</key>          <true/>
  <key>ProcessType</key>        <string>Background</string>
  <key>StandardOutPath</key>    <string>$LOG</string>
  <key>StandardErrorPath</key>  <string>$LOG</string>
</dict>
</plist>
EOF

# --- 3. 読み込む（既存があれば入れ替え）--------------------------------------
launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"

sleep 1
if launchctl print "gui/$(id -u)/$LABEL" >/dev/null 2>&1; then
  echo
  echo "✓ 起動しました: http://localhost:$PORT/"
  echo "  ログ:   $LOG"
  echo "  設定:   ~/.remo-panel/config.json"
  echo
  echo "ブラウザで開いて、設定からアクセストークンを登録してください。"
  echo "停止・自動起動の解除: $0 uninstall"
else
  echo "起動に失敗しました。ログを確認してください: $LOG" >&2
  exit 1
fi
