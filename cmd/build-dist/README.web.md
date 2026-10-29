# SessionTradeExecuter Web版

このフォルダの実行ファイル一つで、全5商品をブラウザからOFF／DryRun／ONへ切り替えられます。

1. Windowsは`session-trade-executer.exe`、Linuxは`./session-trade-executer`を引数なしで起動します。
2. 同じPCは **http://localhost:8090/**、別PCは`http://起動PCのIPアドレス又はホスト名:8090/`を開きます。
3. 商品の⚙でQty・時間帯ごとの手法、全般設定で米国時間・接続先・ポート等を変更します。保存は`conf/web-ui.toml`へ反映します。

日経3商品の標準は日中`NK225_D164_15`、米国開始15分前まで`NK225_P164_06`、前後15分の静観、開始15分後から`NK225_U165_03`です。TOPIXは日中T06・夜間T04を維持します。 詳細は[新標準手法](docs/新標準手法.md)・[時間帯切替](docs/時間帯切替.md)を参照してください。

旧標準のS01・P05・静観・D07の4枠と新旧の標準式が一致する日経商品だけを新3手法へ移行します。独自時間枠・数量・モード・接続先・改変式は保持します。移行版`settings_version=2`は通常保存で記録し、その後の明示的な旧手法選択を再移行しません。読込だけで元ファイルは書き換えません。

売買管理の損益は当セッション分で、場替わりでリセットします。終了時には`log/{商品}/{セッション}.csv`（仮想は`_dryrun.csv`）へ売買履歴を、`log/sessionPL.csv`へ商品・モード別の損益を保存します。「セッション損益」タブはこの集計CSVだけを読み、削除した履歴は復元しません。終了レポートは手数料前です。[保存先と集計方法](docs/セッション損益.md)。更新時は実行ファイルと不足する`conf/rules/NK225_D164_15.toml`・`NK225_P164_06.toml`・`NK225_U165_03.toml`を配置してください。既存の実設定・改変ルールは保持してください。

初期設定は全商品OFFです。DryRunは仮想売買、ONは実トレードです。OFFでは自身の未約定注文・予約だけを取消・照合し、保有玉は決済せず管理対象から外します。再ONは新規運転です。手法・Qty変更はOFF・照合完了後だけ受け付けます。

保存したモードは次回起動にも使います。ブラウザを閉じても売買処理は継続します。待受IP・ポート変更だけはアプリ再起動が必要です。

商品ごとのSQLite台帳は`data/{モード}/{商品名小文字}/`へ保存し、注文・建玉IDを商品ごとに分離します。終了CSVに加え、Viewer用の`log/sessionviewer.resources.json`と商品・場・実／仮想ごとに1個の`*.trades.jsonl`を生成します。確認済み約定と管理外移管だけを保存し、手法別コピーや毎ティックの状態ログは作りません。[Viewer連携](docs/Viewer連携.md)を参照してください。旧モード階層内の詳細CSV・旧約定JSONL・`session_summary*.csv`と、`trace.jsonl`は追加・再作成しません。既存の旧ファイルは自動削除しません。Webの`log/web/操作.jsonl`と、明示操作による診断ZIPは維持します。旧単品版の台帳を自動で取り込む機能はありません。

手法の条件式は`conf/rules/`の実TOMLで変更します。ルールのsampleはありません。

| 手法 | 条件式ファイル |
|---|---|
| NK225_D164_15 | [NK225_D164_15.toml](conf/rules/NK225_D164_15.toml) |
| NK225_P164_06 | [NK225_P164_06.toml](conf/rules/NK225_P164_06.toml) |
| NK225_U165_03 | [NK225_U165_03.toml](conf/rules/NK225_U165_03.toml) |
| NK225_S01 | [NK225_S01.toml](conf/rules/NK225_S01.toml) |
| NK225_D07 | [NK225_D07.toml](conf/rules/NK225_D07.toml) |
| NK225_P05 | [NK225_P05.toml](conf/rules/NK225_P05.toml) |
| NK225_TG04 | [NK225_TG04.toml](conf/rules/NK225_TG04.toml) |
| NK225_PG04 | [NK225_PG04.toml](conf/rules/NK225_PG04.toml) |
| TOPIX_T06 | [TOPIX_T06.toml](conf/rules/TOPIX_T06.toml) |
| NK225_T06 | [NK225_T06.toml](conf/rules/NK225_T06.toml) |
| TOPIX_T04 | [TOPIX_T04.toml](conf/rules/TOPIX_T04.toml) |
| NK225_U06 | [NK225_U06.toml](conf/rules/NK225_U06.toml) |

通常の再配布では既存の実ルールを上書きしません。新名が未作成で旧名の実ファイルがある場合は、その内容を新名へ引き継ぎます。どちらもない手法だけ標準式から作成します。式を変更するときは[ルールエンジンの説明](docs/ルールエンジン.md)を参照してください。変更は再起動時に適用し、不正な式では起動しません。

詳しくは[Web版の設定と操作](docs/Web版.md)を参照してください。旧単品版は廃止しました。
