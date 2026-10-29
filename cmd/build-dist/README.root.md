# SessionTradeExecuter 配布一覧

[SessionTradeExecuterフォルダ](./SessionTradeExecuter/README.md)を使用します。実行ファイル一つで全5商品のOFF／DryRun／ONと設定を画面で変更できます。旧単品版は配布しません。

Windowsは`SessionTradeExecuter/session-trade-executer.exe`、Linuxは`SessionTradeExecuter/session-trade-executer`を引数なしで起動します。ポート・枚数・昼夜手法は`SessionTradeExecuter/conf/settings.toml`、手法ごとの条件式は`SessionTradeExecuter/conf/rules/*.toml`です。

初期設定は全商品OFFで、全インターフェースから接続を受け付けます。同じPCは **http://localhost:8090/**、別PCは`http://起動PCのIPアドレス又はホスト名:8090/`で開けます。日経3商品は昼NK225_D164_15、夜NK225_P164_06・米国開始前後の静観・NK225_U165_03です。TOPIX2商品は昼TOPIX_T06・夜TOPIX_T04を維持します。

通常の再ビルドは既存の実設定・ルール・`data`・`log`を保持します。`settings.toml.sample`は更新しますが、ルールは実ファイルだけを初回配置し、ルールのsampleは作りません。変更は再起動時に適用します。

画面の設定はconf/web-ui.tomlへ保存します。OFFは建玉を決済せず管理対象から外す操作です。詳しくはWeb内の説明書を参照してください。
