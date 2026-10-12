# laterr — Specification v0.1.0

**A tiny CLI to run commands later.**

## 1. Overview

`laterr` は、任意のシェルコマンドを指定時刻または指定時間後に一度だけ実行するCLIツール。

cronのような定期実行ではなく、単発の遅延実行に特化する。

### Goals

- 短く直感的なコマンド構文
- ターミナルを閉じても予約ジョブが実行される
- ジョブの一覧・キャンセル・停止・実行結果を確認できる
- 予約したターミナルが開いていれば、実行時の出力をその場で見られる
- インストールと初期設定が簡単
- macOS / Linuxで動作する
- cron、at、root権限に依存しない

### Non-goals (MVP)

- 定期実行
- リモートホストでの実行
- GUI / TUI（macOSのメニューバー表示は例外。2.8参照）
- Windows対応
- 自然言語による日時指定
- OSの通知、リトライ、ジョブ間の依存関係（予約したターミナルへの予告は4.7参照）

## 2. CLI Interface

### 2.1 Schedule

```bash
# Relative time
laterr +10m -- npm install
laterr +2h -- npm run build
laterr +1h30m -- npm test
laterr +600s -- echo hello
laterr +1d -- ./backup.sh

# Absolute time
laterr @13:00 -- npm install
laterr @tomorrow/09:00 -- npm test
laterr @2026-10-12/13:30 -- npm run build

# Shell expressions
laterr +10m -- sh -c 'echo hello && npm install'
```

Syntax:

`laterr [options] <time> -- <command> [args...]`

`--` は必須。以降の引数はすべて実行対象コマンドとして扱う。

Options:

| Option | Meaning |
|---|---|
| `--grace <duration>` | 予定時刻からこの時間を超えて遅れた場合は実行しない（デフォルト `24h`、4.4参照） |
| `--no-env` | 環境変数を最小限だけ引き継ぐ（4.2参照） |
| `--bg` | 出力を予約したターミナルに表示しない（4.7参照） |
| `--remind <duration>` | 実行のどれだけ前にターミナルへ予告するか（デフォルト `10s`、4.7参照） |
| `--no-remind` | 予告しない |

`<duration>` は相対時刻と同じ書式から `+` を除いたもの（例: `10m`, `1h30m`）。

- オプションは `--` より前に置く。値を取るオプションは `--grace 10m` と `--grace=10m` のどちらの形でも受け付ける
- `--remind` と `--no-remind` の併用はエラー（`--bg` との併用は4.7参照）
- 時刻の前置記号を忘れた入力（`10m`, `13:00`）には、`did you mean +10m?` のように正しい書き方を示す

### 2.2 List

```bash
laterr ls
laterr ls --all
laterr ls --json
```

デフォルトはpending / runningジョブのみを表示。`--all` は終了済みのジョブ（succeeded / failed / interrupted / cancelled / missed）も表示する。

表示例:

```text
ID  WHEN            STATUS   COMMAND
1   Today 13:00     pending  npm install
2   Tomorrow 09:00  pending  npm test
3   Today 12:30     running  npm run build
```

- 並びはID順
- COMMANDは1行に収め、長い場合は60文字で切り詰める
- ジョブがない場合はその旨を1行で表示する
- `--json` はJSON配列を出力する（ジョブがなければ `[]`）。各要素のフィールドは `id`, `status`, `command`（argvの配列）, `cwd`, `created_at`, `scheduled_at`, `grace_seconds`, `started_at`, `finished_at`, `pid`, `exit_code`, `error_message`, `log_path`。日時はUTCのISO 8601（ミリ秒まで）、値がない項目は `null`。`pid` はrunningの間だけ値を持つ。環境変数は含めない

### 2.3 Inspect

```bash
laterr show 1
```

表示内容:

- ID
- コマンドと引数
- 作業ディレクトリ
- 作成日時
- 実行予定日時
- 猶予時間（grace）
- ステータス
- 実行中のPID（runningの場合）
- 実際の開始・終了日時（cancelled / missedでは、そうなった日時）
- 所要時間
- 終了コード
- エラーの理由（あれば）
- ログ保存先

### 2.3.1 Run now

```bash
laterr run 1
laterr run 1 2 3
```

- pendingのジョブのみ対象。予定時刻を現在時刻に書き換え、スケジューラに `wake` を送る。起動するのは常にスケジューラで、期限が来たジョブと同じ経路を通る（新しいステータスもIPCの操作も増やさない）
- コマンド、ディレクトリ、環境変数、出力先（予約したターミナルのfollower）は予約時のまま
- runningのジョブ、終了済み・キャンセル済みのジョブはエラーにする
- スケジューラが動いていなければ、登録時と同じ手順で起動してから書き換える
- コマンドはジョブがpendingでなくなったことを確認してから戻る（最大5秒待ち、それでも開始しなければエラーにする）。終了は待たない
- IDの複数指定の扱いは `rm` と同じ

### 2.4 Cancel

```bash
laterr rm 1
laterr rm 1 2 3
```

- pendingのジョブのみキャンセル可能
- runningのジョブはエラーにし、`laterr kill` を案内する
- IDは複数指定できる。IDの書式が1つでも不正なら何もキャンセルしない。それ以外はジョブごとに処理し、1つでも失敗すれば終了コードを非0にする
- キャンセル済みジョブの履歴は保持する
- 同じジョブが後から実行されないことを保証する

### 2.4.1 Stop

```bash
laterr kill 1
```

- runningのジョブのみ対象。pendingのジョブはエラーにし、`laterr rm` を案内する
- 停止するのは常駐サービス。ジョブとその子孫プロセス（プロセスグループ全体）にSIGTERMを送り、5秒後も残っていればSIGKILLを送る
- 停止したジョブはfailedとして記録し、理由（`stopped with laterr kill`）を残す。新しいステータスは増やさない
- コマンドはジョブの終了を確認してから戻る（最大8秒待ち、それでも終了しなければエラーにする）

### 2.5 Logs

```bash
laterr log 1
laterr log 1 --follow
```

- stdout / stderrを記録
- 終了コードを保存
- 実行中は `--follow`（短縮形 `-f`）で追跡可能
- 実行前のジョブ、および実行されなかったジョブ（cancelled / missed）では、その旨を表示する（終了コードは0）
- ログはユーザーのローカルディスクに保存する

### 2.5.1 Prune

```bash
laterr prune
laterr prune --older-than 7d
laterr prune --all
```

- 終了済みジョブ（succeeded / failed / interrupted / cancelled / missed）の履歴とログを削除する
- デフォルトは終了から30日を超えたもの。`--older-than <duration>` で変更し、`--all` で終了済みをすべて削除する
- pending / runningのジョブは削除しない
- 削除したIDは再利用しない
- 自動では実行しない

### 2.6 Diagnostics

```bash
laterr doctor
laterr --version
laterr --help
```

`--help` は `-h` / `help` でも表示できる。引数なしで起動した場合は、使い方を標準エラー出力に表示して非0で終了する。

`doctor` は以下を順に確認し、項目ごとに1行で結果を表示する。壊れているもの（`✗`）が1つでもあれば非0で終了する。警告（`!`）だけなら0で終了する。

- 対応プラットフォームか
- 実行バイナリのパス
- データ保存先の存在、権限（0700）、書き込み可否
- データベースを開けるか、権限（0600）、pending / runningの件数
- サービス定義の有無と、そこに書かれたバイナリが存在するか
- スケジューラがソケットに応答するか、CLIと同じバージョンか（異なる場合は再起動コマンドを案内する）
- タイムゾーン（`TZ` が設定されていればその値も）
- Linuxでlingeringが無効の場合の警告
- pendingジョブの作業ディレクトリが存在しない場合の警告
- macOSで、pendingジョブの作業ディレクトリがTCCの保護対象（`~/Documents`, `~/Desktop`, `~/Downloads`, iCloud Drive, `~/Library/CloudStorage`）にある場合の警告と、許可方法の案内

### 2.7 Uninstall

```bash
laterr uninstall
laterr uninstall --purge
```

- 常駐サービスを停止し、登録したサービス定義（LaunchAgentのplist / systemd user unit）を削除する
- デフォルトではデータベースとログを残す。`--purge` でそれらも削除する
- pending / runningジョブが残っている場合は、その件数を表示して確認を求める（`y` / `yes` 以外は中止し、非0で終了する）
- サービスの削除後、スケジューラが停止するまで最大15秒待つ。停止を確認できない場合、`--purge` はデータを削除せずエラーにする

### 2.8 Menu Bar

```bash
laterr menubar
laterr menubar off
laterr menubar on
```

macOSでは、pending / runningのジョブをメニューバーにも表示する。CLIの上に載せた補助的な表示であり、ジョブの実行には関与しない。

- pending / runningのジョブが1件以上ある間だけ、時計のアイコンとその件数を表示する。なくなればアイコンを消し、次に予約されたときにまた表示する
- メニューにはジョブを1件1行で並べる（ID、予定時刻またはrunning、40文字で切り詰めたコマンド）。並びは `ls` と同じID順
- 各ジョブのサブメニューから、pendingは `Run Now`（`laterr run` 相当）と `Cancel Job`（`laterr rm` 相当）、runningは `Stop Job`（`laterr kill` 相当）を実行できる。失敗しても表示する場所がないので、ジョブがメニューに残ることで分かるようにする
- メニュー末尾の `Hide Menu Bar Icon` は `laterr menubar off` と同じ
- 件数は約1秒ごとに更新する。メニューの中身は開いた時点で組み立て、開いている間は変えない（ポインタの下で項目が動かないようにするため）
- デフォルトは有効。`laterr menubar off` / `on` で切り替え、引数なしでは現在の状態を表示する
- 設定はデータ保存先の `menubar.off` の有無で持つ（あれば無効）。デフォルトの状態ではファイルを必要としない
- 切り替え時はスケジューラへ `wake` を送り、すぐに反映させる
- macOS以外では `laterr menubar` はエラーにする
- `LATERR_HOME` を設定したスケジューラ（テスト・開発用）はアイコンを表示しない

## 3. Time Syntax

### Relative

| Syntax | Meaning |
|---|---|
| `+30s` | 30秒後 |
| `+10m` | 10分後 |
| `+2h` | 2時間後 |
| `+1d` | 24時間後 |
| `+1h30m` | 1時間30分後 |

- `s`, `m`, `h`, `d` をサポートする
- 複数単位の連結を許可する
- 連結は大きい単位から順に、各単位1回まで（`+1h30m` は可、`+30m1h` や `+1h1h` はエラー）
- ゼロ以下、無効な単位、オーバーフローはエラー
- 登録時点の壁時計に加算した絶対時刻として保存する（スリープ中も時間は経過する扱い）

### Absolute

| Syntax | Meaning |
|---|---|
| `@13:00` | 次に訪れる13:00 |
| `@13:00:30` | 次に訪れる13:00:30 |
| `@tomorrow/09:00` | 明日09:00 |
| `@2026-10-12/13:30` | 指定日13:30 |

- システムのローカルタイムゾーンを使用
- `@HH:MM` は必ず未来の時刻に解決する
- 時刻部分は24時間表記の `HH:MM` または `HH:MM:SS`。秒の指定がなければ `:00`。時は1桁でもよい（`@9:00`）
- 日付部分は `tomorrow` または `YYYY-MM-DD`。存在しない日付はエラー
- 明示的な過去日時はエラー
- DSTによる存在しないローカル時刻はエラー
- DSTによる重複時刻は先に訪れる時刻を採用
- 内部ではUTCのタイムスタンプとして保存する
- 時刻の解釈は登録時にCLI側で行う（`TZ` 環境変数があればそれに従う）。スケジューラ側のタイムゾーン設定には依存しない
- 登録後にシステムのタイムゾーンが変わっても、実行される瞬間は変わらない（表示上の時刻だけが新しいタイムゾーンに合わせて変わる）
- タイムゾーンを明示する構文やオプションはMVPでは提供しない

パーサーは実行処理から独立させ、ユニットテスト可能にする。

## 4. Execution Semantics

### 4.1 Working Directory

予約登録時のカレントディレクトリを記録し、実行時に再現する。

```bash
cd ~/projects/my-app
laterr +10m -- npm install
```

この場合、10分後に `my-app` ディレクトリで `npm install` を実行する。

実行時にディレクトリが存在しなければfailedにする。

### 4.2 Environment Variables

- デフォルトでは登録時の環境変数をすべて保存し、実行時に再現する（`at` と同じ挙動。「ターミナルでは動くのに予約すると失敗する」を避けるため）
- `--no-env` を指定した場合は `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_*`, `TMPDIR` のみを保存する
- 保存した環境変数の値を `show` / `ls` / `--json` の出力に表示しない
- 保存した環境変数は、ジョブが終端ステータス（succeeded / failed / interrupted / cancelled / missed）になった時点で消去する。保持するのはpending / runningの間だけ
- 環境変数には秘密情報が含まれる可能性があるため、保存先の権限を制限する（Storage参照）

例:

```bash
laterr --no-env +10m -- npm test
```

### 4.3 Command Execution

- コマンドと引数を配列として保存する
- デフォルトでシェルを経由せず、直接プロセスを実行する
- PATHを使って実行ファイルを探索する
- パイプやリダイレクト等のシェル構文は暗黙的に解釈しない
- シェル構文が必要な場合は `sh -c` 等を明示する
- stdinは `/dev/null` に接続する
- 入力を必要とするインタラクティブなコマンドは対象外

### 4.4 Persistence

- ターミナルを閉じても予約は維持される
- OSのスリープ中は実行せず、復帰後に実行する
- 電源OFF中に予定時刻を過ぎたpendingジョブは、次にスケジューラが起動した際に実行する
- ただし、予定時刻から猶予時間（`--grace`、デフォルト24時間）を超えて遅れたジョブは実行せず、missedとして記録する
- 期限超過のジョブは予定時刻の古いものから順に起動する
- 完了済みジョブを再実行しない
- 実行失敗時の自動リトライは行わない

システム停止中やログアウト中の正確な時刻での実行は保証しない。遅延実行した場合は実際の開始時刻を記録する。

### 4.5 Process Failures

ジョブ実行直前にステータスをrunningとして永続化する。

起動中にスケジューラがクラッシュし、状態が不明になったジョブは、再起動後にinterruptedとして記録する。自動再実行はしない。

スケジューラの停止・再起動（ログアウト、サービス再起動、`uninstall` 等）時、実行中のジョブはスケジューラと一緒に終了し、interruptedになる。ジョブをスケジューラから切り離して生存させることはしない。

各ジョブは自身をリーダーとするプロセスグループで実行する。停止（スケジューラの終了、`laterr kill`）はグループ全体にシグナルを送り、`sh -c` やビルドツールが起動した子プロセスも止める。その代償として、スケジューラが強制終了（SIGKILL、クラッシュ）した場合、macOSではジョブのプロセスが残ることがある（systemdはcgroupで回収する）。この場合も記録はinterruptedとし、再実行はしない。

実行中のスケジューラを不用意に再起動しない。CLIからの応答確認に失敗しても、サービス定義が変わっていなければ「起動」だけを行い、「再起動」はしない。

厳密なexactly-once実行は保証できないため、MVPは**重複実行を避けるat-most-onceの試行**を基本方針とする。

### 4.6 Concurrency

- ジョブは互いに独立して並列に実行する。長時間のジョブが後続のジョブを待たせない
- 同時に期限を迎えたジョブは予定時刻の古い順に起動する（完了は待たない）
- 同時実行数の上限はMVPでは設けない

### 4.7 Terminal Output

ジョブは常に常駐サービスが実行し、出力は常にログへ保存する。そのうえで、デフォルトでは予約したターミナルにも出力を表示する。

```bash
laterr +1h -- npm test            # 出力はこのターミナルにも表示される
laterr --bg +1h -- npm test       # 表示しない。laterr log <id> で確認する
laterr --remind 1m +1h -- npm test
```

- 予約コマンドはすぐに戻る。予約後、そのターミナルに紐づく中継プロセス（follower）が残り、予告と出力の表示を行う
- followerは表示するだけで、ジョブの実行には関与しない。実行するのは常に常駐サービスである
- ターミナルを閉じてもジョブは実行される。その場合、出力はログにのみ残り、followerは終了する
- 実行の少し前（デフォルト10秒前）に、ターミナルへ1行で予告する。予告にはキャンセル用のコマンド（`laterr rm <id>`）を添え、読んでから取り消せるようにする。`--remind <duration>` で変更し、`--no-remind` で無効にする
- 予約から実行までの時間が予告時間以下の場合（例: `+3s`）は予告しない
- 出力の後に、結果を1行で表示する（成功時は所要時間、失敗時は終了コードまたは理由）。何も出力しないコマンドでも、実行されたことが分かるようにするため
- ジョブがmissedになった場合もその旨を1行で表示する。cancelledになった場合は何も表示せずに終了する
- followerがターミナルへ書き込むのは、ターミナルがシェルのプロンプトにある間だけとする。エディタやページャ等、別のプログラムがフォアグラウンドにある間は書き込まず、プロンプトに戻った時点でまとめて表示する（全画面プログラムの表示を壊さないため）。この間に予告の時刻が来た場合、予告は表示しない
- 「プロンプトにある」とは、ターミナルのフォアグラウンドプロセスグループが、予約コマンドを起動したシェル、またはセッションリーダーのものであること
- followerはシェルが描画済みのプロンプトの下に書き込むため、書き込んだ後にシェルへプロンプトを再描画させる。外部から再描画を依頼する手段はないので、ターミナルのサイズを一瞬だけ1行縮めて戻し、シェルのリサイズ時の再描画を利用する
- ターミナルが閉じられてもfollowerにシグナルは届かない（シェルのジョブではないため）。followerは制御端末の有無を定期的に確認し、なくなっていれば終了する
- followerの待機は1回あたり最大1秒とする。pendingのジョブは `laterr run` でいつでも開始されうるが、followerには通知されないので、自分で見に行く。スリープ復帰後に予告や出力が遅れないことも兼ねる
- 予告と結果の行は標準エラー出力、ジョブの出力は標準出力に出す
- `--bg` を指定した場合、followerを起動しない。`--bg` と `--remind` の併用はエラー
- 予約コマンドの標準出力がターミナルでない場合（パイプ、リダイレクト、スクリプト内での捕捉）は、`--bg` と同じ扱いにする。followerが出力先を掴み続けて呼び出し元を待たせないため
- followerを起動するジョブは、擬似端末（pty）上で実行する。色や進捗バー、同じ行を書き換える表示を、手で実行したときと同じにするため。ptyのサイズは予約時のターミナルのサイズとし、ジョブの出力はスケジューラがログファイルへ写す
- 実行中は、followerがターミナルのサイズを定期的に確認し、変わっていればスケジューラへ伝える。スケジューラはptyのサイズを変え、ジョブのプロセスグループへ `SIGWINCH` を送る（ptyはジョブの制御端末ではないため、カーネルは送らない）。予約から実行までの間に変わったサイズも、これで実行開始時に反映される
- サイズは2回続けて同じ値を観測してから伝える。ウィンドウをドラッグしている途中のサイズや、他のfollowerがプロンプト再描画のために一瞬だけ縮めたサイズを拾わないため
- followerがいなくなった後（ターミナルを閉じた後）は、ptyのサイズは最後の値のままとする
- ログにはジョブが書いたバイト列をそのまま残す（エスケープシーケンスを含む）。改行をCR LFに変換しないよう、ptyの `ONLCR` は無効にする
- ptyを使う場合もstdinは `/dev/null` のままとし、ptyを制御端末にはしない。入力待ちで止まらないようにするため
- followerを起動しないジョブ（`--bg`、標準出力がターミナルでない場合）の出力先はログファイルであり、TTYではない。色や進捗バーを端末判定で切り替えるコマンドは、非TTY向けの出力になる
- どのモードでも、ジョブは `ls` / `show` / `rm` / `log` の対象になる

## 5. Job Lifecycle

```text
                 ┌───────────┐
                 │  pending  │
                 └─────┬─────┘
                       │
          ┌────────────┼────────────┐
          ▼            ▼            ▼
       running     cancelled     missed
          │
    ┌─────┼─────────┐
    ▼     ▼         ▼
 succeeded failed  interrupted
```

各ジョブは最低限以下のデータを保持する。

```text
id
command (argv)
cwd
environment
created_at
scheduled_at
grace_seconds
started_at
finished_at
status
pid
exit_code
error_message
log_path
```

- IDは永続的な連番整数とする
- 作成日時、予定日時、開始日時、終了日時はUTC（Unixエポックからのミリ秒）で記録する
- `finished_at` は、実行されなかったジョブではcancelled / missedになった日時を表す
- `pid` は起動後に記録する。`exit_code` はプロセスが通常終了した場合のみ記録し、シグナルによる終了や起動失敗では空のまま `error_message` に理由を残す
- 一覧や詳細表示ではローカルタイムゾーンに変換する
- ジョブ情報はアプリ再起動後も保持する
- pending → running、pending → cancelled、pending → missedの状態変更はアトミックにする

## 6. Architecture

### Language

**Go**

理由:

- 単一の実行バイナリを配布しやすい
- 起動が軽量
- プロセス制御やOS連携に適している
- macOS / Linuxのクロスコンパイルが容易

クロスコンパイルの容易さを保つため、CGOに依存しない（`CGO_ENABLED=0` でビルドできること）。

### Components

1. CLI — ユーザー入力とジョブ管理
2. Scheduler — 実行予定時刻の管理
3. Executor — プロセス実行とログ収集
4. Storage — 永続化
5. Platform Service — バックグラウンド実行

パッケージとの対応:

| パッケージ | 役割 |
|---|---|
| `cmd/laterr` | エントリポイント。実際のスケジューラを動かす統合テストもここに置く |
| `internal/cli` | 引数の解析、各サブコマンド、出力の整形、follower |
| `internal/timespec` | 時刻・時間の構文のパーサー（他のパッケージに依存しない） |
| `internal/store` | ジョブとステータス遷移の永続化（SQLite） |
| `internal/scheduler` | 常駐プロセス（待機、起動、停止）、Executor、ソケットのプロトコルとそのクライアント |
| `internal/menubar` | macOSのメニューバーのアイコン（設定の読み書きとAppKitの呼び出し） |
| `internal/service` | launchd / systemdへの登録と解除 |
| `internal/paths` | データ保存先とその配下のファイルの位置 |

CLI、スケジューラ、follower、メニューバーのアイコンはすべて同じ実行バイナリで、サブコマンドで役割が決まる。スケジューラは `laterr daemon`、followerは `laterr __follow`、メニューバーのアイコンは `laterr __menubar` として起動する（いずれもヘルプには載せない）。

### Scheduling Backend

ユーザースコープの常駐サービスを採用する。

- macOS: launchd LaunchAgent
- Linux: systemd user service
- root権限不要
- cron / atを使用しない
- 初回スケジュール登録時に必要なサービスを自動セットアップし、配置したファイルのパスと `laterr uninstall` で撤去できることを表示する
- pendingジョブがないときは、通知が来るまで無期限にブロックする（CPUを消費しない）
- pendingジョブがあるときは「次の予定時刻までの残り時間」と「30秒」の短い方だけ待機し、起床のたびに壁時計と予定時刻を比較して期限到来を判定する
- CLIとスケジューラの通信はデータ保存先配下のUnixドメインソケット（権限0600）で行う。1接続につきJSONのリクエスト1行とレスポンス1行をやり取りして切断する。操作は `ping`（生存確認）、`wake`（スケジュールの再確認）、`kill`（実行中ジョブの停止）の3つで、レスポンスには常にスケジューラのバージョンとPIDを含める。タイムアウトは2秒
- スケジューラはデータ保存先ごとに1つだけ動く。起動時にロックファイルを排他ロックし、取れなければ起動しない
- 登録時、CLIはまずスケジューラの応答を確認し、応答がなければサービスをインストールして起動し、応答するまで最大10秒待つ。ジョブの記録はその後に行い、スケジューラを用意できなかった場合にジョブを残さない。記録後の通知（`wake`）に失敗した場合は、そのジョブをキャンセルしてエラーにする
- サービスはOS側で常時再起動させる（launchdは `KeepAlive`、systemdは `Restart=always`）
- 新規登録・キャンセル時はソケット経由でスケジューラへ通知し、待機時刻を再計算する
- `doctor` はソケットへの応答でスケジューラの生存を確認する
- 実行中ジョブの停止（`laterr kill`）もソケット経由でスケジューラに依頼する
- スケジューラのバージョンがCLIと異なる場合（更新後に古いスケジューラが動き続けている場合）、登録時に警告する。自動では再起動しない（実行中のジョブを中断するため）

待機を単一の長いタイマーにしない理由: Goのタイマーはモノトニック時計基準で、OSのスリープ中は進まない。スリープ復帰後やシステム時刻の変更後も、最大30秒以内に期限到来を検知できるようにする。

OSサービスが再起動した場合は永続化されたジョブからスケジュールを復元する。

macOSでは、常駐サービスから起動したプロセスがTCCの保護対象ディレクトリ（`~/Documents`, `~/Desktop`, `~/Downloads` 等）にアクセスすると、許可ダイアログが表示されるか拒否されて失敗する。自動的に回避はせず、`doctor` とREADMEで案内する。

Linuxでは、ユーザーの完全ログアウト後も動作させるにはlingering設定が必要な場合がある。自動的に有効化せず、`doctor` で検出・案内する。

環境変数 `LATERR_HOME` を設定すると、データ保存先をそのディレクトリに切り替え、サービスのインストール・削除を一切行わない。スケジューラは `laterr daemon` を手動で起動する。テストと開発のための仕組みで、統合テストはこれを使って実環境に触れずにスケジューラを動かす。

### Menu Bar

メニューバーのアイコン（2.8参照）は、スケジューラとは別のプロセス（`laterr __menubar`）にする。UI側の障害が、スケジューラやそれが実行しているジョブを巻き込まないようにするため。

- スケジューラが子プロセスとして起動する。スケジュールが変わりうるたび（起床のたび）に、アイコンが有効で、pending / runningのジョブがあり、まだ起動していなければ起動する
- アイコンのプロセスはデータベースを直接読む。今すぐ実行とキャンセルはデータベースの更新と `wake`、停止はソケットの `kill` で行い、CLIと同じ経路を使う
- 表示するジョブがなくなったとき、無効にされたとき、起動したスケジューラがいなくなったとき（親プロセスが変わったとき）は、アイコンのプロセスが自分で終了する。スケジューラは停止時にもSIGTERMを送る
- 起動に失敗するか異常終了した場合は理由をスケジューラのログに残し、10分間は再試行しない。SSH越しに手動で起動したスケジューラなど、メニューバーに届かない環境では、繰り返しても改善しないため
- Dockには表示しない（アクセサリアプリとして動かす）
- AppKitはcgoではなく、Objective-Cランタイム経由（`github.com/ebitengine/purego`）で呼び出す。CGOに依存しない方針を保つため
- macOS以外のビルドにはAppKitを呼ぶコードを含めない

### Storage

SQLiteを使用する。

- ドライバはpure Goの `modernc.org/sqlite` を使用する（CGO不要）
- WALモードと `busy_timeout` を設定する
- トランザクションによって状態変更の競合を防ぐ
- 複数のCLIプロセスから同時にアクセス可能
- データベースとログはOS標準のユーザーデータ領域に保存
- 保存ディレクトリは0700、データベースとログは0600
- ログはジョブ単位で保存し、自動削除しない。整理は `laterr prune` で行う（2.5.1参照）
- スキーマのバージョンを `PRAGMA user_version` に記録する。古いデータベースは開いた時点で移行し、自分より新しいバージョンのデータベースは開かずにエラーにする
- `secure_delete` を有効にし、消去した環境変数がファイル内に残らないようにする

macOSは `~/Library/Application Support/laterr/`、LinuxはXDG Base Directoryに準拠する（`$XDG_DATA_HOME/laterr/`、未設定なら `~/.local/share/laterr/`）。サービス定義はmacOSが `~/Library/LaunchAgents/com.laterr.scheduler.plist`、Linuxが `$XDG_CONFIG_HOME/systemd/user/laterr.service`（未設定なら `~/.config/` 配下）。

データ保存先の中身:

| ファイル | 用途 |
|---|---|
| `laterr.db` | ジョブのデータベース |
| `logs/<id>.log` | ジョブごとのstdout / stderr |
| `laterr.sock` | CLIとスケジューラの通信用ソケット |
| `scheduler.lock` | スケジューラの多重起動を防ぐロック |
| `scheduler.log` | スケジューラ自身の出力（macOSのみ。Linuxではjournalに出る） |
| `menubar.off` | メニューバーのアイコンを無効にした場合に存在する（macOSのみ） |

Unixソケットのアドレス長には上限（約100バイト）があるため、データ保存先のパスが長すぎる場合はエラーにする。

常駐サービスから呼び出すlaterr実行ファイルは、Homebrew等で更新しても参照切れになりにくい安定したパスを使用する。具体的には、`PATH` 上の同名のシンボリックリンクが現在の実行ファイルを指していれば、そのリンクのパスを採用する。サービス定義の内容が変わった場合（バイナリを移動した場合など）は、次の登録時に書き直して反映する。

## 7. CLI UX

### Scheduling Success

```text
✓ Scheduled job #42

  Command    npm install
  Run at     Today, 13:00 (in 16 minutes)
  Directory  ~/projects/my-app
  Env        47 variables captured
  If late    runs on wake or login, skipped after Tomorrow, 13:00
  Output     shown here while this terminal is open; always in `laterr log 42`

  Runs even after this terminal is closed. Stops if you log out mid-run.
```

登録時の出力は、ユーザーが仕様を読まなくても実行時の挙動をおおまかに把握できることを目的とする。

- `Env`: 引き継ぐ環境変数の件数を表示する（値や名前は表示しない）。`--no-env` の場合は `minimal (PATH, HOME, ...)` と表示する
- `If late`: スリープや電源OFFで遅れた場合の扱いと、猶予時間から計算した実行期限を表示する
- `Output`: 出力の行き先を表示する。`--bg` の場合、または標準出力がターミナルでない場合は `` `laterr log 42` `` とだけ表示する
- 末尾の1行: ターミナルを閉じても実行されること、実行中にログアウトすると中断されることを伝える
- これらは毎回表示する。常時表示する説明は上記の範囲にとどめ、これ以上増やさない

該当する場合のみ、以下を追加で表示する。

初回登録時（常駐サービスをセットアップした場合）:

```text
  Installed background service:
    ~/Library/LaunchAgents/com.laterr.scheduler.plist
  Remove it anytime with `laterr uninstall`.
```

作業ディレクトリがmacOSのTCC保護対象にある場合:

```text
! ~/Documents is protected by macOS. This job may fail or show a
  permission dialog when it runs. See `laterr doctor`.
```

Linuxでlingeringが無効の場合:

```text
! This job will not run while you are logged out.
  See `laterr doctor`.
```

- `!` で始まる警告は標準エラー出力に出す
- 警告があっても登録は成功とし、終了コードは0とする
- 同じ警告も登録のたびに表示する（抑制状態を持たない）

### Reminder and Result

予約したターミナルが開いている場合、実行前の予告、ジョブの出力、結果がその場に表示される（4.7参照）。

```text
laterr: job #42 (npm install) runs in 10 seconds (cancel: laterr rm 42)
...npm install の出力...
laterr: job #42 succeeded (8.2s)
```

失敗時など:

```text
laterr: job #42 failed (exit code 1)
laterr: job #42 failed: stopped with `laterr kill`
laterr: job #42 failed: command not found: npm
laterr: job #42 interrupted: the scheduler stopped while this job was running
laterr: job #42 (npm install) was missed and did not run
```

### Completed Job

```text
$ laterr show 42
Job #42
  Status     succeeded
  Command    npm install
  Directory  ~/projects/my-app
  Created    Today, 12:44:00
  Scheduled  Today, 13:00
  Grace      24h (not started after Tomorrow, 13:00)
  Started    Today, 13:00:00
  Finished   Today, 13:00:08
  Duration   8.2s
  Exit code  0
  Log        /Users/me/Library/Application Support/laterr/logs/42.log
```

該当しない項目（開始前の `Started`、シグナルで終了した場合の `Exit code` 等）は行ごと省く。runningの間は `PID`、理由がある場合は `Error` の行が加わる。

### Errors

エラーは具体的に説明する。

```text
Error: invalid time expression: +10x
Error: scheduled time is in the past
Error: job #42 does not exist
Error: job #42 is already running (stop it with `laterr kill 42`)
Error: job #42 is not running (it has succeeded)
Error: unknown command: 10m (did you mean +10m?)
Error: background scheduler is unavailable
Error: invalid duration for --grace: 10x
Error: invalid duration for --remind: 10x
```

エラーは `Error: ` を付けて標準エラー出力に出す。使い方の誤りの場合は、続けて `` Run `laterr --help` for usage. `` と案内する。

CLIは成功時0、失敗時非0（1）の終了コードを返す。

`--json` の出力は機械処理可能であること。JSONモードでは装飾や進捗メッセージを標準出力に混ぜない。

## 8. Testing / Acceptance Criteria

以下をすべて満たすこと。

- [ ] `laterr +10s -- ...` で10秒後（スリープなしの場合、誤差2秒以内）に実行される
- [ ] `laterr @HH:MM -- ...` が次の正しい時刻に解決される
- [ ] 日付またぎ、月末、DSTの境界を適切に処理する
- [ ] 登録したディレクトリで実行される
- [ ] 引数内のスペースや特殊文字が正しく保持される
- [ ] ターミナル終了後も実行される
- [ ] スケジューラ再起動後もpendingジョブが復元される
- [ ] スリープや停止中に期限を過ぎたジョブが復帰後30秒以内に処理される
- [ ] 猶予時間を超えて遅れたジョブは実行されず、missedになる
- [ ] 長時間実行中のジョブがあっても、他のジョブが予定時刻に実行される
- [ ] 登録時の環境変数が実行時に再現され、`--no-env` では最小限のみになる
- [ ] 環境変数の値が `show` / `ls` / `--json` に出力されない
- [ ] 登録時の出力に、環境変数の件数、遅延時の扱いと実行期限、ターミナル終了・ログアウト時の挙動が表示される
- [ ] 初回登録時にサービス定義のパスと撤去方法が表示される
- [ ] TCC保護対象ディレクトリやlingering無効の場合に、登録時に警告が標準エラー出力へ出る
- [ ] `run` したpendingジョブは予定時刻を待たずに開始され、二重には実行されない
- [ ] `rm` したpendingジョブは実行されない
- [ ] `kill` したrunningジョブは、その子プロセスも含めて停止し、failedになる
- [ ] 終了したジョブの環境変数がデータベースに残らない
- [ ] `prune` が終了済みジョブとそのログだけを削除し、pending / runningを残す
- [ ] 旧バージョンのデータベースを開くと移行され、新しいバージョンのデータベースはエラーになる
- [ ] 別のプログラムがフォアグラウンドにある間は、followerがターミナルへ書き込まない
- [ ] 非ゼロ終了コードがfailedとして記録される
- [ ] stdoutとstderrがログに残る
- [ ] 同時に複数のジョブを登録してもIDと状態が競合しない
- [ ] 実行中のクラッシュ後に自動重複実行しない
- [ ] スケジューラ再起動時に実行中だったジョブがinterruptedになる
- [ ] `laterr uninstall` でサービス定義が削除され、`--purge` なしではデータが残る
- [ ] ターミナルから予約した場合、コマンドはすぐに戻り、実行時の出力がそのターミナルに表示される
- [ ] 実行の10秒前（`--remind` で変更可）に、キャンセル用のコマンドを添えた予告が表示され、`--no-remind` では表示されない
- [ ] ターミナルを閉じてもジョブは実行され、出力がログに残り、followerが残留しない
- [ ] `--bg`、または標準出力がターミナルでない場合は、followerが起動せず予約コマンドの呼び出し元を待たせない
- [ ] ジョブの終了時、ターミナルに結果（成功時は所要時間、失敗時は終了コード）が表示される
- [ ] macOSで、pending / runningのジョブがある間だけメニューバーにアイコンと件数が表示され、メニューからキャンセル・停止できる
- [ ] `laterr menubar off` の後はアイコンが表示されず、`on` で再び表示される
- [ ] メニューバーのアイコンのプロセスが異常終了しても、スケジューラとジョブに影響しない
- [ ] `CGO_ENABLED=0` でmacOS / Linux両方でビルド・実行できる

日時パーサー、ステータス遷移、ストレージ処理はユニットテストを用意する。

可能な範囲でmacOS / LinuxのCIとスケジューラ統合テストも用意する。

## 9. Repository / Delivery

以下を含める。

```text
README.md        (英語)
README.ja.md     (日本語)
CONCEPT.md       (この仕様書)
LICENSE (MIT)
go.mod
cmd/laterr/
internal/
.github/workflows/
```

READMEには以下を記載する。

- コンセプト
- `at` との違い（macOSでは `atrun` がデフォルト無効で有効化にsudoが必要なこと、ログ・履歴・終了コードを確認できること）
- インストール方法とアンインストール方法
- クイックスタート
- 時刻指定の構文
- コマンドリファレンス
- OS別の動作条件
- ターミナル終了・ログアウト・スリープ時の挙動
- 環境変数の扱いと `--no-env`
- macOSのTCC（保護対象ディレクトリ）に関する注意
- macOSのメニューバー表示と、その無効化の方法
- トラブルシューティング

GitHub ReleasesでmacOS（arm64/amd64）とLinux（arm64/amd64）のバイナリを配布できる構成にする。

## 10. Implementation Priorities

以下の順序で実装する。

1. CLI構文と日時パーサー
2. SQLiteによるジョブ管理
3. コマンド実行・ログ保存
4. バックグラウンドスケジューラ
5. launchd / systemdとの統合
6. CLIの一覧・詳細・キャンセル・ログ
7. `doctor` / `uninstall`
8. テスト、CI、README

**最優先は、シンプルなCLI体験と予約実行の信頼性。**

仕様外の機能を勝手に追加せず、MVPを完成させること。内部アーキテクチャは保守性を優先し、不要な抽象化を避けること。
