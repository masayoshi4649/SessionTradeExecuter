# SessionTradeViewerで売買経路を確認する

表示アプリは独立した[SessionTradeViewer](https://github.com/masayoshi4649/SessionTradeViewer)です。表示側の設定で原板と資料の保存先を指定します。JSONLと一覧の読取契約は独立アプリの[データ仕様](https://github.com/masayoshi4649/SessionTradeViewer/blob/main/docs/データ仕様.md)です。

通常の売買・終了CSVに加え、小さい閲覧用ファイルを自動生成します。追加のTOML設定は不要です。

```text
log/sessionviewer.resources.json
log/NK225mini/2026-10-29_Day.trades.jsonl
log/NK225mini/2026-10-29_Day_dryrun.trades.jsonl
```

SessionTradeViewerで`log/sessionviewer.resources.json`を開くと、商品・日付_セッション・手法IDを選択できます。両アプリを更新してください。既存の6列売買CSV・`sessionPL.csv`の形式と終了時出力は維持します。

AIからも、Viewerの`/api/resources`→`/api/chart`→`png_url`・`data_url`で画像と同じ区間の数値を取得できます。詳しくは独立アプリの[画像API仕様](https://github.com/masayoshi4649/SessionTradeViewer/blob/main/docs/画像API.md)を参照してください。

## 保存するもの

- 商品・場・実／仮想ごとに約定JSONLを1個だけ保存します。確定約定・管理外移管が増えた時に更新し、毎ティックの価格・保有・損益は出力しません。
- JSONLには約定価格・枚数・所有建玉ID・当時の手法・手数料を記録します。複数玉の返済は対象IDごとに分け、費用を重複させず配賦します。予約・未確定注文は約定に含めません。
- 手法別のデータはコピーしません。同じJSONLから、新規時の手法に属する玉と、その後の全返済・移管を取り出します。返済を別手法が判断しても履歴から落としません。「全手法（合計）」でも確認できます。
- 原板はViewer側の既存`data_root`を使用します。SQLite・原板・重いtraceを閲覧用に複製しません。入力のSQLite行番号を原板ファイルの行番号へ転用しません。

通常は約5秒周期で確認し、変更時だけ書き出します。ファイル障害は画面のレポートエラーへ表示して再試行し、売買を再送しません。更新前の全過去場を自動で一括生成する機能ではありません。

## OFF・再起動・金額

OFFや新規運転で管理外にした残玉は`Detach`として記録します。これは返済ではなく、利益・損失・手数料を発生させません。移管後の玉や手動決済は図へ含めません。損失が解消した意味ではありません。

確定損益は確認済み実約定から再計算し、終了CSVと同じ手数料前を標準にします。手数料もJSONLに保存し、Viewerの`pnl_basis=net`なら控除できます。手数料はアプリの計算値です。

Viewerの含みはラージ気配による参考評価、SessionTradeExecuterのカードは発注商品の気配による評価です。両者の含みが一致するとは限りません。現実の注文・建玉の正本は引き続きSessionTradeExecuter側のSQLiteです。

## 別PCへ移す時

一覧JSONと対象の`商品/*.trades.jsonl`だけを、フォルダ構成を保ってコピーしてください。Viewerの「サーバー上の一覧ファイル」へコピー先の一覧パスを指定すると、そのフォルダ基準で読みます。Viewerの原板ルートは別途アクセスできる必要があります。

同じPCでファイル選択・ドロップする時は、一覧内の生成元`base_dir`を使います。別PCへコピー後はサーバー上のパスで開いてください。既存の一覧は従来のパス基準を維持します。

出力済みJSONLや一覧から削除した過去資源を、再起動や遅着約定を理由に無断で復元しません。不足する対象IDなどで正しく変換できない旧記録はエラーとし、FIFOや架空決済で補いません。
