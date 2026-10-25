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
TARGET="gui/$(id -u)/$LABEL"

# bootout は非同期で、戻った時点ではまだジョブが残っていることがあります。
# そのまま bootstrap すると失敗するので、消えるまで（最大5秒）待ちます。
unload() {
  launchctl bootout "$TARGET" 2>/dev/null || true
  for _ in $(seq 25); do
    launchctl print "$TARGET" >/dev/null 2>&1 || return 0
    sleep 0.2
  done
}

if [ "${1:-}" = "uninstall" ]; then
  unload
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
# 稼働中のバイナリを上書きすると署名の検証で新しいプロセスが強制終了される
# ことがあるため、別名でコピーしてから置き換えます。
cp "$SRC" "$BIN.new"
chmod +x "$BIN.new"
mv -f "$BIN.new" "$BIN"

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
unload
# 待っても残っていた場合に備えて、bootstrap も数回試します。
for i in 1 2 3; do
  launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null && break
  [ "$i" = 3 ] && { echo "起動に失敗しました。ログを確認してください: $LOG" >&2; exit 1; }
  sleep 1
done

sleep 1
if launchctl print "$TARGET" >/dev/null 2>&1; then
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
