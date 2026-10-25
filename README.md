# Remo Panel

Nature Remo をブラウザのボタンから操作する、ローカル常駐型の小さなサーバ。
mac と Windows で同じコードが動き、同一LANのスマホからも使えます。

```
ブラウザ (localhost:8080)
   ↓
remo-panel（各マシンに常駐・約10MB）
   ├→ http://Remo-xxxx.local/messages   赤外線を直接送信（高速・API制限なし）
   └→ https://api.nature.global/1/...   エアコン・照明・TV・家電一覧
```

- 家電一覧は API から自動取得してボタンを生成するので、設定作業はトークンの登録だけ
- アクセストークンはブラウザに渡らず、`~/.remo-panel/config.json`（権限 600）に保存
- Cloud API のレート制限（5分あたり30リクエスト）を踏まえ、一覧は60秒キャッシュ
  （開きっぱなしのタブの自動更新は5分まで同じキャッシュで応答し、複数の端末から同時に
  来たリクエストも上流へは1回にまとめる）
- 制限や通信断で一覧が取れないときは、直前の情報を表示して操作を継続

---

## セットアップ

### 1. アクセストークンを発行

<https://home.nature.global/> にログインし、アクセストークンを発行してコピーしておきます。

### 2. インストール

**macOS**

```bash
chmod +x install/install-mac.sh
./install/install-mac.sh
```

`~/bin/remo-panel` に配置し、ログイン時に自動起動する launchd エージェントを登録します。

**Windows**

```powershell
powershell -ExecutionPolicy Bypass -File .\install\install-windows.ps1
```

`%LOCALAPPDATA%\RemoPanel` に配置し、ログオン時に起動するタスクを登録します。
管理者権限は不要です。

> どちらのスクリプトも、OS がダウンロードファイルに付ける隔離属性
> （macOS の `com.apple.quarantine` / Windows の «ブロック»）を自動で外します。
> これを外さないと Gatekeeper や SmartScreen が実行を止めます。

### 3. トークンを登録

<http://localhost:8080/> を開き、右上の「設定」からトークンを貼り付けて保存。
家電のボタンが自動で並びます。

---

## ローカル送信（高速パス）

Cloud API 経由だと往復で数百ミリ秒かかりますが、Remo 本体が LAN に公開している
Local API に直接投げると体感ほぼ即時で、レート制限も消費しません。

1. 「設定」→「Remo のホスト名 / IP アドレス」に `Remo-XXXXXX.local` または IP を入力
2. 「接続確認」で疎通を確認
3. 物理リモコンを Remo に向けてボタンを押し、「信号を取得」→ 名前を付けて保存

保存したボタンは「ローカル送信」カードに並びます。

**注意点**

- Local API は生の赤外線パターンを扱う仕組みで、Nature アプリに登録済みの家電とは
  別系統です。エアコンの温度・モード保持はクラウド側の機能なので、
  **赤外線 = ローカル / エアコン = クラウド** の併用が現実的です。
- Remo nano は Local API 非対応です。
- `.local` の名前解決（mDNS）は Windows だと不安定なことがあります。
  解決できない場合は IP 直指定にし、ルータ側で DHCP 予約して固定してください。

---

## スマホからも使う

既定では `127.0.0.1` で待ち受けるため、そのPCからしか繋がりません。
同一LANの端末から使うには「設定」→「待ち受けアドレス」を `0.0.0.0` にします。

**その場合はアクセスキーを必ず設定してください。** 設定しないと、同じ Wi-Fi に
いる人は誰でもあなたの家電を操作できます。キーを設定すると
`http://192.168.x.y:8080/?k=キー` でのみアクセスでき、以降はクッキーで保持されます。

待ち受けアドレスの変更は再起動後に反映されます（mac: `launchctl kickstart -k gui/$(id -u)/local.remo-panel`、
Windows: タスクスケジューラで RemoPanel を再実行）。

---

## リソース消費

常駐と聞くと電池が心配になりますが、アイドル時の HTTP サーバは
`accept()` でブロックしているだけで、リクエストが来るまで一切起床しません。

実測（Linux、アイドル30秒）:

```
RSS                 9.5 MB
CPU時間の増加        0 ticks
コンテキストスイッチ  計 8 回
```

電池を食うのは常駐そのものではなく定期的な起床なので、このアプリは
ポーリングを持ちません。ブラウザ側も自動更新は2分間隔に抑えてあり、
その更新にはサーバが5分以内のキャッシュで応答するので、開きっぱなしにしていても
API を叩くのは数分に1回です。

---

## 設定ファイル

`~/.remo-panel/config.json`（Windows は `%USERPROFILE%\.remo-panel\config.json`）

```json
{
  "token": "...",
  "bind": "127.0.0.1",
  "port": 8080,
  "accessKey": "",
  "remoLocalHost": "Remo-XXXXXX.local",
  "localSignals": []
}
```

2台で同じ状態にしたいときは、このファイルを共有（git private repo / iCloud / Dropbox）
すれば、学習済みの赤外線ボタンごと持ち回せます。トークンを含むので公開リポジトリには置かないでください。

## コマンドラインオプション

```
-config <path>   設定ファイルのパス（既定 ~/.remo-panel/config.json）
-port <n>        待ち受けポート（設定ファイルより優先）
-bind <addr>     待ち受けアドレス（設定ファイルより優先）
-log <path>      ログの出力先ファイル（既定は標準エラー出力）
```

## ソースからビルド

Go 1.22 以降が必要です。依存パッケージはありません（標準ライブラリのみ）。

```bash
go build -o remo-panel .          # 自分の環境向け
GOOS=windows GOARCH=amd64 go build -o remo-panel.exe .   # Mac から Windows 用
```

`dist/` の各バイナリもこの方法で生成しています。

## ブラウザをアプリ風に開く

```bash
# macOS
open -na "Google Chrome" --args --app=http://localhost:8080
```

アドレスバーのないウィンドウで開きます。

## 開発用スタブ

実機もアカウントも使わずに動作確認できます。

```bash
go run testharness/stub.go -cloud 127.0.0.1:9001 -local 127.0.0.1:9002
REMO_PANEL_CLOUD_BASE=http://127.0.0.1:9001 go run . -port 8099
```

スタブ側の config には `"token": "TEST-TOKEN"`、
`"remoLocalHost": "127.0.0.1:9002"` を設定してください。

## 参考

- Cloud API: <https://swagger.nature.global>
- Local API: <https://local-swagger.nature.global>
- 開発者向けトップ: <https://developer.nature.global/>
