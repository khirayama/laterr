# laterr

[![CI](https://github.com/khirayama/laterr/actions/workflows/ci.yml/badge.svg)](https://github.com/khirayama/laterr/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/khirayama/laterr)](https://github.com/khirayama/laterr/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/khirayama/laterr)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**コマンドを「あとで」実行するための小さなCLI。**

[English](README.md) | 日本語

```console
$ laterr +10m -- npm install
✓ Scheduled job #42

  Command    npm install
  Run at     Today, 13:00 (in 10 minutes)
  Directory  ~/projects/my-app
  Env        47 variables captured
  If late    runs on wake or login, skipped after Tomorrow, 13:00
  Output     shown here while this terminal is open; always in `laterr log 42`

  Runs even after this terminal is closed. Stops if you log out mid-run.
$                                                  # そのまま作業を続ける...
laterr: job #42 (npm install) runs in 10 seconds (cancel: laterr rm 42)    # ...10分後
added 312 packages in 8s
laterr: job #42 succeeded (8.2s)
```

`laterr` は、コマンドを指定した時刻または指定した時間の経過後に **一度だけ**
実行します。cronではありません。定期実行の機能はありません。

プロンプトをすぐに返してくれて、ターミナルに依存しない
`sleep 600 && npm install` だと考えてください。ターミナルを開いたままにして
おけば、出力はその場に表示されます。閉じてもジョブは実行され、出力と終了
コードは `laterr log` と `laterr show` で確認できます。

## 特徴

- **短い構文**: `laterr +10m -- <command>`、`laterr @13:00 -- <command>`
- **出力はその場に**: 予約したターミナルに出力がそのまま表示され、実行の
  少し前には予告も出る
- **同じ実行コンテキスト**: 予約したときのディレクトリと環境変数で実行される
- **ジョブ管理**: 一覧・キャンセル・停止・詳細表示。stdout、stderr、終了
  コードを保存する
- **macOSではメニューバーにも表示**: 待機中・実行中のジョブがある間、
  アイコンに件数が表示され、そこからキャンセルや停止もできる
- **root不要、cron不要、`at` 不要**: ユーザー単位の小さな常駐サービスが実行
  を担い、初回利用時に自動でセットアップされる
- **macOS / Linux対応**。ランタイム依存のない単一の静的バイナリ

## 目次

- [なぜ `at` ではないのか](#なぜ-at-ではないのか)
- [インストール](#インストール)
- [クイックスタート](#クイックスタート)
- [時刻の構文](#時刻の構文)
- [コマンド](#コマンド)
- [こんなときどうなる?](#こんなときどうなる)
- [環境変数](#環境変数)
- [プラットフォーム別の注意](#プラットフォーム別の注意)
- [トラブルシューティング](#トラブルシューティング)
- [仕組み](#仕組み)
- [開発](#開発)
- [コントリビュート](#コントリビュート)
- [ライセンス](#ライセンス)

## なぜ `at` ではないのか

`at` も建前上は同じことができますが、次の違いがあります。

- macOSでは `atrun` がデフォルトで無効で、有効にするには `sudo` が必要です。
  `laterr` はroot不要で、初回利用時に自分でセットアップします。
- `at` は出力をローカルのメールボックスに送りますが、それが設定されている
  マシンはほとんどありません。`laterr` はジョブごとにログ(`laterr log <id>`)
  を残し、ステータス、開始・終了時刻、終了コードも記録します
  (`laterr show <id>`、`laterr ls --all`)。
- `at` はシェルスクリプトを標準入力から読みます。`laterr` はコマンドを引数
  として受け取り、直接実行するので、クォートは入力したとおりに働きます。

## インストール

[リリースページ](https://github.com/khirayama/laterr/releases) から、お使いの
プラットフォーム(macOSまたはLinux、arm64またはamd64)用のアーカイブを
ダウンロードし、バイナリを `PATH` の通った場所に置きます。

```sh
tar xzf laterr_*_darwin_arm64.tar.gz
install -m 755 laterr ~/.local/bin/laterr      # または /usr/local/bin
```

Go 1.26以降でビルドすることもできます。

```sh
go install github.com/khirayama/laterr/cmd/laterr@latest
```

セットアップの手順はありません。最初にジョブを予約したときに、`laterr` が
常駐サービスをインストールし、配置した場所を表示します。

バイナリはインストールした場所に置いたままにしてください。常駐サービスは
そのパスのバイナリを実行します。移動した場合は、次の
`laterr <time> -- ...` がサービスを修復します。

### アンインストール

```sh
laterr uninstall            # 常駐サービスを停止して削除する
laterr uninstall --purge    # ...加えて、ジョブ履歴とログも削除する
```

そのあとでバイナリを削除します。`--purge` を付けなければ、データベースとログ
は残ります。pendingまたはrunningのジョブが残っている場合、`uninstall` は
その件数を表示し、続行するか確認します。

## クイックスタート

```sh
laterr +10m -- npm install                 # 10分後
laterr @13:00 -- npm run build             # 次の13:00
laterr @tomorrow/09:00 -- ./backup.sh      # 明日の朝

laterr ls                                  # 待機中・実行中のジョブ
laterr log 1                               # ジョブ1の出力
laterr show 1                              # ステータス、時刻、終了コード
laterr run 2                               # ジョブ2を予定を待たず今すぐ実行する
laterr rm 2                                # ジョブ2をキャンセルする
laterr kill 3                              # 実行中のジョブ3を停止する
laterr prune                               # 終了から30日を超えたジョブを消す
```

`--` は必須です。それより後ろはすべて実行するコマンドで、手を加えずに
渡されます。

## 時刻の構文

### 相対指定: `+<duration>`

| 構文 | 意味 |
|---|---|
| `+30s` | 30秒後 |
| `+10m` | 10分後 |
| `+2h` | 2時間後 |
| `+1d` | 24時間後 |
| `+1h30m` | 1時間30分後 |

単位は `d`、`h`、`m`、`s` で、数値は整数です。大きい単位から順に、各単位
1回まで組み合わせられます。`+1h30m` は有効、`+30m1h` と `+1h1h` は
エラーです。合計はゼロより大きくなければなりません。遅延は予約時点の壁時計
に加算されるので、スリープしていた時間も経過したものとして数えます。

### 絶対指定: `@<time>`

| 構文 | 意味 |
|---|---|
| `@13:00` | 次に訪れる13:00(今日。すでに過ぎていれば明日) |
| `@13:00:30` | 同上、秒まで指定 |
| `@tomorrow/09:00` | 明日の09:00 |
| `@2026-10-12/13:30` | 指定日の13:30 |

- 時刻は24時間表記の `HH:MM` または `HH:MM:SS` です。時は1桁でも書けます
  (`@9:00`)。日付は `YYYY-MM-DD` です。
- 時刻は、予約した時点のローカルタイムゾーン(`TZ` が設定されていればそれ)
  で解釈されます。あとでシステムのタイムゾーンを変えてもジョブは動きません。
  同じ瞬間に実行され、表示だけが新しいタイムゾーンに合わせて変わります。
- 過去の日時を指定するとエラーになります。
- 夏時間の切り替えで存在しない時刻はエラーになります。2回訪れる時刻は、
  先に訪れるほうを指します。

## コマンド

| コマンド | 用途 |
|---|---|
| [`laterr [options] <time> -- <command> [args...]`](#laterr-options-time----command-args) | コマンドを予約する |
| [`laterr ls [--all] [--json]`](#laterr-ls---all---json) | ジョブを一覧する |
| [`laterr show <id>`](#laterr-show-id) | ジョブの詳細を表示する |
| [`laterr run <id>...`](#laterr-run-id) | pendingのジョブを今すぐ実行する |
| [`laterr rm <id>...`](#laterr-rm-id) | pendingのジョブをキャンセルする |
| [`laterr kill <id>`](#laterr-kill-id) | runningのジョブを停止する |
| [`laterr log <id> [--follow]`](#laterr-log-id---follow) | ジョブの出力を表示する |
| [`laterr prune [--older-than <duration> \| --all]`](#laterr-prune---older-than-duration----all) | 終了済みのジョブとログを削除する |
| [`laterr menubar [on\|off]`](#laterr-menubar-onoff) | macOSのメニューバーにジョブを表示する |
| [`laterr doctor`](#laterr-doctor) | セットアップが正しいか診断する |
| [`laterr uninstall [--purge]`](#laterr-uninstall---purge) | 常駐サービス(とデータ)を削除する |
| `laterr --help`、`laterr --version` | ヘルプ、バージョンを表示する |

どのコマンドも、成功時は `0`、失敗時は `1` で終了します。エラーメッセージは
標準エラー出力に出ます。

### `laterr [options] <time> -- <command> [args...]`

コマンドを予約します。

| オプション | 意味 |
|---|---|
| `--grace <duration>` | 予定時刻からこの時間を超えて遅れる場合は実行しない。デフォルトは `24h`。書式は相対指定から `+` を除いたもの(例: `10m`、`1h30m`)。 |
| `--no-env` | 最小限の環境変数だけをコマンドに渡す([環境変数](#環境変数)を参照)。 |
| `--bg` | このターミナルに出力を表示しない([ターミナルへの出力](#ターミナルへの出力)を参照)。 |
| `--remind <duration>` | 実行のどれだけ前に、このターミナルで予告するか。デフォルトは `10s`。 |
| `--no-remind` | 予告しない。 |

オプションは `--` より前に書きます。値を取るオプションは `--grace 10m` と
`--grace=10m` のどちらの形でも書けます。`--remind` は `--no-remind` や
`--bg` と併用できません。

コマンドは **シェルを介さず** 直接実行されます。実行ファイルは、予約時の
`PATH` から探します。パイプ、リダイレクト、`&&`、グロブは解釈されません。
必要なときはシェルを明示してください。

```sh
laterr +10m -- sh -c 'echo hello && npm install > install.log'
```

標準入力は `/dev/null` です。ターミナルを必要とするコマンドは動きません。

#### ターミナルへの出力

ジョブを実行するのは常に常駐サービスで、出力は常にログに保存されます。
それに加えて、予約したターミナルにも出力がそのまま表示されます。

```console
$ laterr +1h -- npm test
✓ Scheduled job #7
...
$                                             # プロンプトが戻る
laterr: job #7 (npm test) runs in 10 seconds (cancel: laterr rm 7)   # 59分50秒後
...テストの出力...
laterr: job #7 failed (exit code 1)           # または succeeded (12.4s)
```

- 実行の少し前に予告が表示されるので、入力の途中にいきなり出力が割り込む
  ことはありません。予告にはキャンセル用のコマンド(`laterr rm <id>`)が
  添えられるので、実行したくなくなった場合はその場で取り消せます。予告の
  タイミングは `--remind 1m` で変更でき、`--no-remind` で無効にできます。
  予約から実行までが予告時間より短い場合(`+3s` など)、予告は出ません。
- 最後の1行でジョブの結果を伝えます。何も出力しないコマンドでも、実行
  されたことが分かります。`missed` になったジョブも通知されます。
- ジョブの出力は標準出力に、予告と結果の行は標準エラー出力に出ます。
- **別のプログラムがターミナルを使っている間は、何も書き込みません。**
  ジョブの実行時にエディタやページャなどがフォアグラウンドにある場合、出力
  は待たされ、プロンプトに戻った時点で表示されます。この場合、予告は表示
  されません。
- **ターミナルを閉じても、ジョブには何の影響もありません。** ジョブは実行
  され、出力はあとから `laterr log <id>` で読めます。
- `--bg` を付けるとターミナルには何も表示されず、出力はログにだけ残ります。
- laterr自身の出力先がターミナルでない場合(パイプ、リダイレクト、
  スクリプトによる捕捉)は、`--bg` と同じ動作になります。スクリプトが
  待たされることはありません。
- 出力をターミナルに表示するジョブは、専用のターミナル上で実行されます。
  サイズは予約したターミナルと同じで、ウィンドウのサイズを変えると追従
  します。色やプログレスバー、同じ行を書き換える表示は、手で実行したときと
  同じように見えます。入力は空のままなので、ジョブから何かを尋ねることは
  できません。
- `--bg` を付けた場合や、laterrの出力先がターミナルでない場合は、ジョブは
  ログファイルへ直接書き込み、コマンドはプレーンな出力になります。多くの
  コマンドには色を強制するためのフラグがあります(`--color=always`、
  `FORCE_COLOR=1`)。

### `laterr ls [--all] [--json]`

pendingとrunningのジョブを一覧します。`--all` を付けると、終了済みのジョブ
(`succeeded`、`failed`、`interrupted`、`cancelled`、`missed`)も表示します。

```text
ID  WHEN            STATUS   COMMAND
1   Today 13:00     pending  npm install
2   Tomorrow 09:00  pending  npm test
3   Today 12:30     running  npm run build
```

`--json` はスクリプト向けにJSON配列を出力します。標準出力にそれ以外のもの
は混ざりません。

```json
[
  {
    "id": 1,
    "status": "pending",
    "command": ["npm", "install"],
    "cwd": "/Users/me/projects/my-app",
    "created_at": "2026-10-10T03:50:00.000Z",
    "scheduled_at": "2026-10-10T04:00:00.000Z",
    "grace_seconds": 86400,
    "started_at": null,
    "finished_at": null,
    "pid": null,
    "exit_code": null,
    "error_message": null,
    "log_path": "/Users/me/Library/Application Support/laterr/logs/1.log"
  }
]
```

タイムスタンプはUTCです。`pid` はジョブの実行中だけ値が入ります。環境変数
は決して含まれません。

### `laterr show <id>`

ジョブを1件表示します。ステータス、コマンド、ディレクトリ、作成日時と予定
日時、猶予時間、実行中のPID、開始・終了日時、所要時間、終了コード、失敗の
理由(あれば)、ログのパスが表示されます。

```text
Job #42
  Status     succeeded
  Command    npm install
  Directory  ~/projects/my-app
  Created    Today, 12:50:00
  Scheduled  Today, 13:00
  Grace      24h (not started after Tomorrow, 13:00)
  Started    Today, 13:00:00
  Finished   Today, 13:00:08
  Duration   8.2s
  Exit code  0
  Log        /Users/me/Library/Application Support/laterr/logs/42.log
```

### `laterr run <id>...`

pendingのジョブを、予定時刻を待たずに今すぐ実行します。それ以外は予約した
ときのままです。コマンド、ディレクトリ、環境変数は変わらず、出力も予約した
ターミナル(と `laterr log <id>`)に出ます。予定時刻は現在時刻に書き換わり
ます。コマンドはジョブの終了ではなく、開始を確認して戻ります。IDは複数指定
でき、扱いは `laterr rm` と同じです。実行できるのはpendingのジョブだけです。
実行済みやキャンセル済みのジョブは、予約し直してください。

### `laterr rm <id>...`

pendingのジョブをキャンセルします。ジョブは `cancelled` として履歴に残り、
実行されることはありません。IDは複数指定できます。IDとして不正なものが
1つでもあれば、何もキャンセルしません。そうでなければジョブごとに処理し、
1つでもキャンセルできなければ終了コードが非0になります。runningのジョブは
キャンセルできません。`laterr kill` を使ってください。

### `laterr kill <id>`

runningのジョブを停止します。コマンドと、それが起動したすべてのプロセスに
`SIGTERM` を送り、5秒後にまだ残っていれば `SIGKILL` を送ります。ジョブは
`failed` として記録され、理由は ``stopped with `laterr kill` `` になります。
コマンドはジョブの終了を確認してから戻ります。pendingのジョブは停止
できません。`laterr rm` を使ってください。

### `laterr log <id> [--follow]`

ジョブのstdoutとstderrを表示します。`--follow`(または `-f`)を付けると、
実行中のジョブが終わるまで表示し続けます。まだ実行されていないジョブや、
実行されることのないジョブについては、その旨を表示します。ログはデータ
ディレクトリ配下のプレーンなファイルで、自動では削除されません。
`laterr prune` を参照してください。

### `laterr prune [--older-than <duration> | --all]`

終了済みのジョブとそのログを削除します。デフォルトでは終了から30日を超えた
もの、`--older-than 7d` ではそれより古いもの、`--all` では終了済みのすべて
が対象です。pendingとrunningのジョブには触れません。ジョブIDは再利用
されません。

### `laterr menubar [on|off]`

macOS専用です。pendingまたはrunningのジョブが1件でもある間、メニューバーに
時計のアイコンが表示され、その横にジョブの件数が出ます。最後のジョブが
終わるとアイコンは消え、次にジョブを予約するとまた現れます。

クリックすると、ジョブが1件1行で一覧されます。

```text
#42   Today 13:00   npm install
#43   running   npm run build
────────────────────────────────
Hide Menu Bar Icon
```

- 各ジョブにはサブメニューがあります。pendingのジョブでは **Run Now** と
  **Cancel Job** (`laterr run <id>`、`laterr rm <id>` と同じ)、runningの
  ジョブでは **Stop Job** (`laterr kill <id>` と同じ)を選べます。
- **Hide Menu Bar Icon** は、`laterr menubar off` と同じく、アイコンを以後
  表示しないようにします。
- 件数はおよそ1秒ごとに更新されます。一覧はメニューを開いた時点で組み立て
  られ、開いている間は変わりません。

アイコンはデフォルトで有効です。

```sh
laterr menubar        # 有効か無効かを表示する
laterr menubar off    # 表示しない
laterr menubar on     # 再び表示する
```

アイコンはジョブの状況を見せるだけのものです。無効にしても、アイコン側で
何か問題が起きても、ジョブがいつどう実行されるかは変わりません。Linuxでは
このコマンドはエラーになります。

### `laterr doctor`

インストール状態を診断します。バイナリ、データディレクトリとその権限、
データベース、常駐サービス、スケジューラが応答するか、CLIと同じバージョン
か、タイムゾーン、そしてpendingのジョブの実行を妨げる要因(Linuxでの
ログアウト、macOSの保護フォルダ、存在しなくなった作業ディレクトリ)を確認
します。問題があれば非0で終了します。

### `laterr uninstall [--purge]`

[アンインストール](#アンインストール)を参照してください。

### ジョブのステータス

| ステータス | 意味 |
|---|---|
| `pending` | 予定時刻を待っている |
| `running` | 開始したが、まだ終わっていない |
| `succeeded` | 終了コード0で終了した |
| `failed` | 非0で終了した、killされた(`laterr kill` を含む)、または起動できなかった |
| `interrupted` | 実行中にスケジューラが停止した |
| `cancelled` | 開始前に `laterr rm` で取り消された |
| `missed` | 開始できないまま、予定時刻から猶予時間を超えて過ぎた |

## こんなときどうなる?

| | |
|---|---|
| **ターミナルを閉じた** | ジョブは実行されます。ジョブはシェルではなく常駐サービスに属しているためです。失われるのはその場への出力表示だけで、`laterr log <id>` で読めます。 |
| **ターミナルを開いたままにした** | 実行の10秒前に、キャンセル用のコマンドを添えた予告が表示され、続いて出力が表示されます。 |
| **予定時刻にコンピュータがスリープしていた** | 復帰から30秒以内にジョブが開始されます。 |
| **コンピュータの電源が切れていた、またはログアウトしていた** | 次にスケジューラが起動したとき(ログイン時)に開始されます。ただし、それが予定時刻から `--grace`(デフォルト24時間)を超えていた場合は、`missed` として記録され、実行されません。 |
| **ジョブの実行中にログアウトまたは再起動した** | ジョブはスケジューラと一緒に停止され、`interrupted` として記録されます。再実行は **されません**。 |
| **ジョブが失敗した** | 終了コードとともに `failed` として記録されます。リトライはしません。 |
| **複数のジョブが同時に期限を迎えた** | 予定時刻の順に開始され、並列に実行されます。長いジョブが他のジョブを待たせることはありません。 |

`laterr` は各ジョブを **最大1回** だけ試行します。ジョブが完了したかどうか
判断できない場合(クラッシュの後など)は、コマンドを二重に実行する危険を
冒さず、`interrupted` として記録します。

正確なタイミングが保証されるのは、ログイン中でマシンが起きている間だけ
です。ジョブの開始が遅れた場合、実際の開始時刻は `laterr show` で確認
できます。

## 環境変数

デフォルトでは、ジョブは予約時に存在した **すべての環境変数** のコピーを
受け取ります。`at` と同じ挙動です。同じ `PATH`、同じバージョンマネージャの
shim、同じ認証情報が引き継がれるので、「ターミナルでは動く」がそのまま
「予約しても動く」になります。

このコピーは、ジョブが実行されうる間、ディスクに保存されます。そのため、

- ジョブが終わった時点で消去されます。成功、失敗、キャンセル、missedの
  いずれでも同じです。保持しているのはpendingとrunningのジョブだけです。
- データディレクトリは本人だけが読めます(ディレクトリ `0700`、
  ファイル `0600`)。
  既存のディレクトリ、DB、ジョブログの権限も使用時に補正します。
  データ・ログディレクトリと書き込み対象のデータファイルにはシンボリック
  リンクを使えません。`LATERR_HOME` を設定する場合も、実体のディレクトリを
  指定してください。
- 変数の値を `laterr` が表示することはありません。`show`、`ls`、`--json`
  のいずれにも出ません。予約時に件数が表示されるだけです。
- `--no-env` を付けると、`PATH`、`HOME`、`USER`、`LOGNAME`、`SHELL`、
  `LANG`、`LC_*`、`TMPDIR` だけが保存されます。

  ```sh
  laterr --no-env +10m -- npm test
  ```

## プラットフォーム別の注意

| | macOS | Linux |
|---|---|---|
| 常駐サービス | launchd LaunchAgent | systemd user service |
| サービス定義 | `~/Library/LaunchAgents/com.laterr.scheduler.plist` | `$XDG_CONFIG_HOME/systemd/user/laterr.service`(デフォルト `~/.config/systemd/user/`) |
| データとログ | `~/Library/Application Support/laterr/` | `$XDG_DATA_HOME/laterr/`(デフォルト `~/.local/share/laterr/`) |
| スケジューラの出力 | データディレクトリの `scheduler.log` | `journalctl --user -u laterr.service` |
| メニューバーのアイコン | あり([`laterr menubar`](#laterr-menubar-onoff) を参照) | なし |

Linuxでは、ユーザーサービスが使えるsystemd(`systemctl --user`)が必要です。
Windowsには対応していません。

### macOS: 保護されたフォルダ

macOSは `~/Documents`、`~/Desktop`、`~/Downloads` と、クラウド同期される
フォルダ(iCloud Drive、`~/Library/CloudStorage`)を保護しています。常駐
サービスから起動されたコマンドがこれらに触れると、許可ダイアログが表示
されるか、アクセスが拒否されてジョブが失敗します。

`laterr` はこれを回避しようとはしません。該当するフォルダから予約したとき
に警告し、`laterr doctor` が影響を受けるジョブを一覧します。アクセスを許可
するには、次のいずれかを行ってください。

- ダイアログが表示されたら **許可** をクリックする
- あらかじめ **システム設定 > プライバシーとセキュリティ > フルディスク
  アクセス** に `laterr` のバイナリを追加する
- プロジェクトをこれらのフォルダの外に置く(たとえば `~/projects`)

### Linux: ログアウト中の実行

デフォルトでは、最後のセッションが終了するとsystemdがユーザーサービスを
停止するため、ログアウト中はジョブが実行されません。`laterr` は予約時に
これを警告します。スケジューラを動かし続けるには、次を実行します。

```sh
loginctl enable-linger "$USER"
```

## トラブルシューティング

まず `laterr doctor` を実行してください。

**`Error: background scheduler is unavailable`**
サービスを起動できませんでした。メッセージにはlaunchdまたはsystemdが報告
した内容が含まれます。Linuxでは、そのセッションで
`systemctl --user status` が動くか確認してください。次に、スケジューラの
出力を見てください(上の表を参照)。

**出力がターミナルに表示されなかった**
出力が表示されるのは、予約したターミナルで、そのターミナルが開いている間
だけです。`--bg` を付けた場合や、laterrの出力をパイプやリダイレクトした
場合は表示されません。別のプログラムがフォアグラウンドにある間は、
プロンプトに戻るまで保留されます。出力は常に `laterr log <id>` にあります。

**出力に色が付かない**
`--bg` を付けた場合や、laterrの出力をパイプやリダイレクトした場合、ジョブの
出力先はログファイルなので、ターミナルかどうかを判定するコマンドは色を
無効にします。コマンド側のオプション(`--color=always` や `FORCE_COLOR=1`
など)で強制してください。

**ログがエスケープシーケンスだらけになる**
出力をターミナルに表示するジョブはターミナル上で実行されるため、ログには
色や行の書き換えが、コマンドが書いたまま残ります。ターミナルで
`laterr log <id>` を実行すれば問題なく再生されます。テキストとして読む
ためのログが必要なら、`--bg` を付けて予約してください。

**メニューバーにアイコンが表示されない(macOS)**
アイコンが表示されるのは、pendingまたはrunningのジョブがある間だけです。
コマンドや **Hide Menu Bar Icon** で無効にしていないかは `laterr menubar` で
確認でき、`laterr menubar on` で元に戻せます。アイコンを起動できなかった
場合、スケジューラは理由をログに残し、10分後に再試行します。
`scheduler.log` で `menu bar:` を探してください。`LATERR_HOME` を設定して
手動で動かしたスケジューラは、アイコンを表示しません。

**ジョブが "command not found" で失敗した**
コマンドは予約時の `PATH` から、シェルを介さずに探されます。エイリアスや
シェル関数は存在しません。実際の実行ファイルを指定するか、`sh -c '...'` /
`zsh -ic '...'` を使ってください。

**macOSでジョブがすぐに失敗した、または許可ダイアログが表示された**
[保護されたフォルダ](#macos-保護されたフォルダ)を参照してください。

**ジョブが `missed` になった**
予定時刻から猶予時間を超えるまで、スケジューラが動いていませんでした
(ログアウト、電源オフ)。`--grace` を大きくしてください。

**ジョブが `interrupted` になった**
実行中にスケジューラが停止しました。ログアウト、再起動、
`laterr uninstall`、クラッシュなどが原因です。リトライはされないので、
必要であれば予約し直してください。macOSでのクラッシュ後は、コマンド自体が
まだ動いていることがあります。`ps` で確認してください。

**アップグレード後、スケジューラのバージョンが違うと表示される**
古いスケジューラは、再起動されるまで動き続けます。ジョブの予約時と
`doctor` の両方で指摘されます。`doctor` が再起動のコマンドを表示しますが、
実行中のジョブが中断される点に注意してください。

**`the database was created by a newer version of laterr`**
新しいバージョンが書き込んだデータに対して、古い `laterr` バイナリを使って
います。新しいバイナリを使ってください。メッセージがスケジューラから出て
いる場合は、スケジューラを再起動してください。

## 仕組み

```text
 laterr <time> -- cmd ──► SQLite database ◄── laterr ls / show / rm / log ...
          │                     ▲
          │ wake                │ read due jobs, record results
          ▼                     │
     Unix socket ────────► scheduler (laterr daemon) ──► runs cmd ──► logs/<id>.log
                           launchd / systemd user service
```

- **CLI** は時刻の式を解決し、ジョブ(コマンド、ディレクトリ、環境変数、
  時刻)をSQLiteデータベースに記録して、Unixソケット経由でスケジューラに
  知らせます。
- **スケジューラ** は同じバイナリで、ユーザー単位のlaunchdまたはsystemd
  サービスが `laterr daemon` として実行します。次のジョブの期限まで待機
  しますが、一度に30秒を超えては待ちません。スリープからの復帰後や時計の
  変更後にジョブが遅れないようにするためです。ジョブを起動する *前に*
  `running` として記録することで、最大1回の実行を保証しています。
- 各ジョブは専用のプロセスグループで実行され、出力は専用のログファイルに
  追記されます。ジョブを停止すると、それが起動したものもすべて停止します。
- **follower** は、ターミナルに残される小さなプロセスです。ログを
  ターミナルに中継するだけで、ジョブの実行には関与しません。
- **メニューバーのアイコン**(macOS)も独立したプロセスで、表示するジョブが
  ある間、スケジューラが起動します。同じデータベースを読み、表示するものが
  なくなるか、スケジューラがいなくなると終了します。アイコン側の障害が
  スケジューラやジョブを巻き込むことはありません。

すべてのファイルは、本人だけが読める1つのデータディレクトリにあります。

| ファイル | 用途 |
|---|---|
| `laterr.db` | ジョブのデータベース(SQLite、WALモード) |
| `logs/<id>.log` | 各ジョブのstdoutとstderr |
| `laterr.sock` | CLIがスケジューラと通信するためのソケット |
| `scheduler.lock` | 実行中のスケジューラが保持するロック。スケジューラはデータディレクトリごとに1つ |
| `scheduler.log` | スケジューラ自身の出力(macOS) |
| `menubar.off` | メニューバーのアイコンを無効にした場合に存在する(macOS) |

仕様の全体と、こうした設計判断の理由は [CONCEPT.md](CONCEPT.md) に
まとめてあります。

## 開発

セキュリティパッチを適用したGo 1.26以降が必要です。開発時は `go.mod` の
設定によりGo 1.27.3以降が選択され、必要に応じてGoが自動ダウンロードします。
`laterr` はSQLiteも含めてpure Goで、CGOなしで
ビルドできます。メニューバーのアイコンも、cgoではなくObjective-Cランタイム
([purego](https://github.com/ebitengine/purego))経由でAppKitを呼び出して
います。

```sh
make check          # フォーマット、モジュール、vet、テスト、脆弱性検査
make fmt            # Goソースを整形
make build          # CGOなしで ./laterr をビルド
make race           # データ競合を検査(Cコンパイラが必要)
make cross-build    # リリース対象の4環境向けにビルド
```

`make vulncheck` はMakefileでバージョンを固定した
[govulncheck](https://go.dev/doc/security/vuln/)を、一時ディレクトリにインストール
して実行します。プラットフォーム固有のコードは
`GOOS=linux GOARCH=arm64 make vulncheck` または
`GOOS=darwin GOARCH=arm64 make vulncheck` で検査できます。CIは両方の検査を
push・Pull Request時と毎週実行し、Go 1.26と最新安定版でmacOS・Linuxの
テストを、Linuxでデータ競合の検査を行います。DependabotがGoモジュールと
GitHub Actionsの更新を毎週提案し、Actionsはコミットハッシュで固定します。

| パッケージ | 責務 |
|---|---|
| `cmd/laterr` | エントリポイントと、実際のスケジューラを動かす統合テスト |
| `internal/cli` | 引数の解析、各コマンド、出力の整形、follower |
| `internal/timespec` | 時刻と時間の構文 |
| `internal/store` | ジョブとそのステータス遷移(SQLite) |
| `internal/scheduler` | デーモン: タイミング、ジョブの実行、ソケットのプロトコル |
| `internal/menubar` | macOSのメニューバーのアイコン |
| `internal/service` | launchd / systemdとの統合 |
| `internal/paths` | ファイルの配置場所 |

`LATERR_HOME` を設定すると、`laterr` は別のデータディレクトリを使い、
サービスの管理を一切行わなくなります(メニューバーのアイコンも表示
しません)。統合テストはこの仕組みを使って、
実際のスケジューラに触れずにスケジューラを動かしています。

```sh
export LATERR_HOME="$(mktemp -d /tmp/laterr-dev.XXXXXX)"
./laterr daemon &          # 開発用バイナリのスケジューラを動かす
./laterr +5s -- echo hello
```

このパスは短くしてください。配下にUnixソケットを置くため、アドレスの長さが
約100バイトに制限されます。

`v0.1.0` のようなタグをpushすると、macOSとLinux(arm64 / amd64)向けの
リリースアーカイブがビルドされ、チェックサムとともに公開されます。

## コントリビュート

IssueやPull Requestを歓迎します。Pull Requestを出す前に、`make check` が
通ることを確認してください。CIではプラットフォームの互換性とデータ競合も
検査します。

`laterr` は意図的に小さく保っています。定期実行、リトライ、リモートホスト、
Windows対応はスコープ外です。[CONCEPT.md](CONCEPT.md) の Non-goals を参照
してください。

## ライセンス

[MIT](LICENSE)
