# SessionTradeExecuter

保存済みのルールと先物板情報から売買を管理する、単独のWebアプリです。一つの実行ファイルで日経225・mini・micro、TOPIX・miniの5商品を扱い、商品ごとにOFF・DryRun・ONを選択できます。

設定・ルール・注文・所有建玉をアプリ側で管理し、controllerのAPIへ接続します。初回配布は全商品OFFです。CLI引数は使用せず、実行ファイルの隣の `conf` から設定を読みます。

## 起動

1. Windowsは `dist/windows-amd64/SessionTradeExecuter/session-trade-executer.exe`、Linuxは `dist/linux-amd64/SessionTradeExecuter/session-trade-executer` を起動します。Linuxで必要な場合は実行権限を付けます。
2. 同じPCから <http://localhost:8090/> を開きます。別PCからは起動PCのIPアドレス又はホスト名と設定したポートを使います。
3. 全般設定でcontrollerの接続先を確認し、商品オプションで数量・時間帯ごとの手法を設定します。初回はOFFのまま設定を確認できます。
4. DryRunは仮想売買、ONは実注文です。モードは商品ごとに選びます。保存したモードは次回起動にも適用します。

ブラウザを閉じても運転は続きます。OFFは自分の未約定注文・予約の取消と照合を行い、既存玉を返済せず管理対象から外します。その後の玉は手動管理です。`Ctrl+C` によるアプリ終了は、稼働中の当運転の所有玉の返済を試みます。両操作の違いは [Web版の設定と操作](docs/Web版.md) を参照してください。

既存設定を引き継ぐ場合、保存値がliveの商品は起動時から実運転を開始します。全商品OFFは新規配布の既定値であり、毎回の起動で保存モードをOFFへ変える仕様ではありません。

## 設定とルール

| 保存先 | 用途 |
|---|---|
| `conf/settings.toml` | 配布設定。接続先・保存先・商品・共通時刻など |
| `conf/web-ui.toml` | 画面で保存した公開設定の優先値 |
| `conf/settings.toml.sample` | 配布時の最新ひな形。実行時には読まない |
| `conf/rules/*.toml` | 手法別の実条件式。既存の改変式を保持 |

相対パスは実行ファイルのフォルダ基準です。作業フォルダの別設定へ切り替わりません。商品オプションの変更はOFF・注文照合完了後だけ受け付け、稼働中はREST APIでも拒否します。ルール本文の全体再読込と待受IP・ポート変更は再起動で反映します。

日経3商品の標準は日中 `NK225_D164_15`、夜間の米国開始15分前まで `NK225_P164_06`、前後15分は静観、開始15分後から `NK225_U165_03` です。TOPIX2商品は日中 `TOPIX_T06`・夜間 `TOPIX_T04` です。手法IDと運転時間割は別に管理します。[新標準手法](docs/新標準手法.md)・[時間帯切替](docs/時間帯切替.md)・[ルールエンジン](docs/ルールエンジン.md)に条件を記載しています。

Qtyは今回の所有玉と未約定新規を合わせた片方向上限です。手動玉や前起動の管理外玉を含めた口座全体の上限ではありません。再起動は新規運転とし、旧未確定注文を照合した後、旧実建玉は自動返済せず手動管理へ移します。

## 監査と損益

SQLite保存とCSV・Viewer出力の競合停止を修正しています。読取り前の書込予約と、接続交換後の設定維持を行います。注文の再送は追加していません。[原因・修正・検証範囲](docs/SQLite競合修正.md)を参照してください。

注文・約定・所有建玉ID・判断根拠は、商品と実／仮想を分離したSQLiteへ保存します。送信前に発注意図を確定し、受付を約定とは扱わず、確認できた実約定だけを反映します。結果不明の注文を自動再送しません。

- `data/{dryrun|live}/{商品名小文字}/`：原板と運転・注文の正本。
- `log/{商品}/{セッション}.csv`：終了確定後の6列約定履歴。仮想は `_dryrun.csv`。
- `log/sessionPL.csv`：商品・実／仮想・場ごとの7列集計。利益・損失・収支は手数料前。
- `log/sessionviewer.resources.json` と商品・場別 `*.trades.jsonl`：確認済み約定と管理外移管の小さい閲覧用資料。
- `log/web/操作.jsonl`：画面の設定・モード変更。詳しい転送資料は明示操作で診断ZIPへ出力。

終了CSVを削除してもDBから勝手に再生成しません。毎ティックの全状態や手法別の約定コピーを増やさず、Viewer用JSONLは同じ所有玉の全返済・移管を残します。保存の定義と終了条件は [運転と保存](docs/運転と保存.md)・[セッション損益](docs/セッション損益.md)・[診断データの出力](docs/診断データの出力.md) を参照してください。

## SessionTradeViewerとの連携

表示には独立した [SessionTradeViewer](https://github.com/masayoshi4649/SessionTradeViewer) を利用できます。Viewerが参照できる場所へ原板と閲覧用資料を配置し、`sessionviewer.resources.json` を開きます。Executerの稼働にはViewerを必要としません。

[Viewer連携](docs/Viewer連携.md)に保存単位を、独立アプリの [データ仕様](https://github.com/masayoshi4649/SessionTradeViewer/blob/main/docs/データ仕様.md) と [画像API](https://github.com/masayoshi4649/SessionTradeViewer/blob/main/docs/画像API.md) に読取契約を記載しています。

## ソースから配布を作る

Go 1.24以降を使い、このリポジトリのルートで実行します。Windows/Linuxとも `CGO_ENABLED=0` で生成します。SQLiteは純Go実装で、MinGW・Cコンパイラ・外部SQLiteサーバー・Node.jsは不要です。

```text
go test ./...
go vet ./...
go run ./cmd/build-dist
```

出力は次の2種類です。HTML・CSS・JavaScriptは実行ファイルへ埋め込みます。

```text
dist/windows-amd64/SessionTradeExecuter/session-trade-executer.exe
dist/linux-amd64/SessionTradeExecuter/session-trade-executer
```

両OSのビルドが成功してから配布物を更新します。再配布は既存の実設定・画面保存設定・改変ルール・SQLite・ログを保持し、不足する実ルールだけを追加します。旧配置や未知のファイルを自動削除・移動・複製しません。所有台帳を持つ `data` や `log` をコピーして同じ商品を同時運転しないでください。

`go run .` は一時実行ファイルの隣に `conf` がないため通常起動には使いません。配布ビルダーを使うか、設定と同じフォルダへバイナリを作成してから引数なしで起動します。設定検査だけを行う場合は `[startup] action="check"` に変更して起動します。

APIと所有照合は [APIと注文照合](docs/APIと注文照合.md)、確認手順は [検証方法](docs/検証結果.md) を参照してください。
