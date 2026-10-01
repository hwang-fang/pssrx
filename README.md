# pssrx

測定局の受信データから SSR の質問予定表を作り（interrogator）、機体の応答信号を
質問に対応づけて位置を推定する（pssr）。

```sh
# 2 段をメモリで直列に流す（本番の形）
go run ./cmd/pssrx run \
  -config testdata/kx90.yaml -ssr KX90S -station KX90 \
  -data-root samples -intg-root /tmp/intg -out-dir /tmp/fixes \
  -from 2026-06-10T00:00 -to 2026-06-10T00:10 -stats
```

位置の出力は `-out-dir`（スコークごとの CSV。同じスコークでも 600 s 以上
離れた点は別のファイル。`{最初の点の時刻 UTC}_{スコーク}.csv`）と `-out`
（全点を 1 つの CSV に。解析用）で、併用できる。時刻は UTC。
`-include-dropped` で、抑圧で落としたプロットと位置の解けなかったプロットも
`drop` 列に理由を入れて含める（デバッグ用）。

このアプリは実時間で行える処理（位置の算出と、連続性による航跡片への
グルーピング）に限る。点には航跡片 ID と航跡片の中での順番だけを付け、
確定・像の判定・便・平滑化は後続に任せる（PSSR.md 7 節）。

`interrogator` と `pssr` のサブコマンドは段を単独で走らせる。`pssr` は intg
ファイルからの再処理で、`run` と同じ入力ならバイト単位で同じ位置を出す
（`run` は intg をファイル形式と同じに量子化して次の段へ渡す）。

| 文書 | 内容 |
| --- | --- |
| [CONFIG.md](CONFIG.md) | 設定ファイルの書き方（SSR・測定局のマスタ、解析の定数） |
| [PSSR.md](PSSR.md) | 応答信号による位置推定の原理・手順・実装 |
| [NUMERICS.md](NUMERICS.md) | 出力の再現性を守る演算規約と、その実測 |

## interrogator

qpkx（測定局が受信した質問データ）から SSR のドウェル——ビームが測定局を
向いていた区間——を検出し、ドウェルとドウェルの間の質問時刻を内挿して
質問予定表 intg を出力する。

処理はおおまかに 4 段:

1. 振幅ゲートで弱い受信を落とし、時間差でセグメントに切る
2. 動的計画法で、質問間隔と質問種別のパターンに合う連鎖を 1 本選び出す
   （1 つの qpkx には複数の SSR の質問が混在するので、ここで選り分ける）
3. 連鎖の振幅列に放物線を当て、頂点をビーム中心通過時刻とする
4. 隣り合うドウェルの間を内挿し、質問 1 発ごとの時刻と方位を書き出す

## 構成

```
cmd/pssrx          CLI。サブコマンド interrogator（qpkx -> intg）、pssr（intg + apkx -> 位置）、run（2 段を直列に）
cmd/intgdiff       2 つの intg ディレクトリをレコード単位で突き合わせる

internal/pipeline  ブロック単位のループ。段を繋ぎ、設定を段の入力に直す（設定 -> Stage -> 解析）

internal/interrogator  質問信号解析の段（連鎖検出 DP・放物線フィット・ドウェル検出・内挿）
internal/pssr      応答信号解析の段。役割ごとのサブパッケージ（PSSR.md）
  plot             単一測定点の応答を質問予定と対応づけ、プロットにする（Synchronizer, Pair, Suppress）
  bistatic         単一測定点の双基地幾何で座標を解く（Geometry, Locate）
  tracking         座標の列を航跡片にまとめる（Track）。座標の出所を知らない
  sink             位置を書く（SquawkSink: スコークごとの CSV、CSVSink: 1 つの CSV）
  simtest          既知の質問予定・機体から応答を合成する（テスト用）

internal/config    SSR・測定局のマスタ YAML の読み込みと検証、緯度経度からの基線（距離・方位）
internal/ssr       SSR そのものの性質。質問パターン（PRI 列と質問種別列、最小周期へ簡約）、物理定数
internal/record    段のあいだを流れるレコード型（受信した質問・応答・質問予定）、ブロック、JST の時刻規約
internal/archive   分ファイル（qpkx / apkx / intg）の配置と形式。分単位の読み書きと、分ファイルからのブロック生成
internal/geodesy   WGS84 緯度経度と ENU の変換、JPGEO2024 ジオイド
internal/numeric   出力値を一意に決める演算規約（偶数丸め・床除算・pairwise 総和・LU）

testdata/golden    ゴールデン（入力 qpkx・正解 intg・解析パラメータ）
```

`internal/` は 3 層に分かれる。`pipeline` が段を順に呼び、段（`interrogator/`、
`pssr/` の各パッケージ）は処理本体を持ち、残りは段が共有する基盤。依存は
`pipeline -> 段 -> 基盤` の一方向で、段どうしは import せず（`pssr` の中は
`plot ← bistatic → tracking ← sink`）、基盤は段を import しない。設定の書式を段の入力に直すのは `pipeline` の仕事で、段は
設定の書式を知らない。段が import する基盤は `record`（レコード型）だけで、
ファイル形式（`archive`）は知らない。

`pipeline` の入力は `Source`（`iter.Seq2[record.Block, error]`）で、出力は
`IntgSink` と `sink.Sink`。`record.Block` は 1 回ぶんの投入（質問受信・応答・
質問予定）で、ブロックの区分はデータの出どころが決める。ファイルからは
`archive.FileSource` が 1 分 1 ファイルをそのまま 1 ブロックにする。実時間化
では受信側が 1 秒ごとにブロックを作って渡せばよく、段の呼び出し順は
`pipeline.run` の 1 箇所だけにある。解析はブロックの幅に依存せず、
`internal/pipeline` のテストが 1 分のブロックを 10 秒・1 秒・100 ms に切り
直しても同じ出力になることを要求する。

apkx はファイル上の時刻が F2 で、F1 に直すと 20.3 µs 早くなるため、
ブロックの応答は分の区切りより 20.3 µs 早い側にずれる。時刻で切り直さず
ファイル区分のまま渡し、pssr 段の `Synchronizer` が実際の時刻で対応づけて
吸収する。

依存は Pure Go のみ（`github.com/goccy/go-yaml` の 1 つ）。cgo は使わない。

## 使い方

```sh
go run ./cmd/pssrx interrogator \
  -config testdata/kx90.yaml \
  -station KX90 -ssr KX90S \
  -qpkx-root samples \
  -intg-root /tmp/out \
  -from 2026-06-10T00:00 \
  -to   2026-06-11T00:00 \
  -stats
```

時刻は JST 固定。`.qpkx` / `.intg` のファイル名が JST 前提で組まれているため、
オフセット付きの指定は受け付けない。

主なフラグ:

| フラグ | 既定 | 説明 |
| --- | --- | --- |
| `-append` | `false` | 既存 intg を切り詰めず追記する（別々に解析した期間を 1 つの出力へ継ぎ足す用） |
| `-stats` | `false` | 棄却理由別の件数と処理時間を出力する |
| `-v` | `false` | 棄却の詳細を DEBUG ログに出す |

### ディレクトリレイアウト

```
qpkx: {root}/{YYYYMM}/{station}/{YYYYMMDD}/qpkx/{YYYYMMDDHHMM}{station}.qpkx
apkx: {root}/{YYYYMM}/{station}/{YYYYMMDD}/apkx/{YYYYMMDDHHMM}{station}.apkx
intg: {root}/{YYYYMM}/{ssrid}/{YYYYMMDD}/{YYYYMMDDHHMM}{ssrid}.intg
```

### 設定ファイル

書き方は [CONFIG.md](CONFIG.md) を参照。設定は SSR と測定局のマスタ
（`ssrs` / `stations`）と、解析の定数の上書き（`analysis`、省略可）からなる。
どの局とどの SSR を組み合わせるかは設定には書かず、`-station` / `-ssr` /
`-reply-stations` で実行時に指定する。

実装上の要点:

- 位置は緯度経度で書き、SSR を原点にした ENU に変換して距離（斜距離）と
  方位（真北基準）を出す（`config.Baseline`）。標高はジオイド（JPGEO2024）で
  楕円体高に直す。
- 質問パターンは `mode_pattern`（種別の繰り返し）と `interval_pattern_ns`
  （間隔の繰り返し）の対で受け、最小公倍数の長さに展開して最小周期へ簡約する
  （`ssr.PatternFromStagger`）。
- 段の手続きの定数（`interrogator.Config` と `pssr` の各パッケージの `Config`）
  は `DefaultConfig` が既定値を持ち、`analysis` 節が項目ごとに上書きする。`config` は YAML 用の
  鏡像構造体（`config.InterrogatorAnalysis` / `config.PSSRAnalysis`）を持ち、
  `pipeline` が既定値に重ねて各段の `Validate` を通す。段の `Config` に直接
  `yaml` タグを付けないのは、段が設定の書式を知らずに済むことと、Go の
  フィールド名を変えてもファイルの形式が変わらないため。鏡像が段の `Config`
  を漏れなく写していることは `pipeline` のテストが反射で確かめる。

## pssr

intg（質問予定表）と apkx（測定局が受信した Mode A/C 応答）から、機体ごと・
ドウェルごとのプロットを作り、単局で位置を推定して CSV に書く。

```sh
go run ./cmd/pssrx pssr \
  -config testdata/kx90.yaml \
  -ssr KX90S -station KX90 \
  -intg-root /tmp/out -data-root samples \
  -from 2026-06-10T00:00 -to 2026-06-10T00:10 \
  -stats -out-dir /tmp/fixes
```

`-from` / `-to` は入力ファイルに合わせて JST で指定し、出力の時刻は UTC。

`-station` は intg を作った質問解析局、`-reply-stations` は応答を受信した局
（省略時は質問解析局と同じ局の単局計算）。intg には局の情報が残らないので
別々に指定する。局の時計は GPS で同期している前提。

処理は `Synchronizer`（質問予定と応答の時刻同期）→ `Pair`（応答を質問に対応づけて
列にし、プロットにする）→ `Suppress`（サイドローブと反射の幽霊を落とす）→
`Locate`（双基地距離・ビーム方位・気圧高度から位置を解く）→ `Track`（連続性で
航跡片にまとめる）→ `Sink` の順。どの段も実時間で行え、点には航跡片 ID と
航跡片の中での順番だけを付けて、確定・像の判定・便・平滑化は後続に任せる。
原理・手順・実装と、採らなかった案は [PSSR.md](PSSR.md) を参照。

応答符号のビット配置は仕様書が無く、実データから決めた（`internal/pssr/plot/decode.go`）。

apkx は 8 バイト固定長（分先頭からの経過 [100 ns]、12 ビット応答符号、
波高値）。時刻は応答の末尾 F2 パルスのもので、読み込み時に F1–F2 間隔
20.3 µs を引いて先頭 F1 の時刻に直す。応答符号はスコーク（Mode A）か高度符号
（Mode C）で、どちらかは対応づいた質問の種別で決まる。

## 検証

```sh
go test ./...                 # 単体・ゴールデン
go run ./cmd/intgdiff A B     # 2 つの intg ディレクトリを突き合わせる
```

検証は固定データに対する一致で行う。

1. `internal/numeric` と `internal/ssr`（質問パターン）のテストは、`testdata/` に固定した
   参照ベクタに対してビット単位の一致を要求する。演算規約が 1 ulp でも
   変われば落ちる。
2. `internal/pipeline` のゴールデンテストは、`testdata/golden/` に固定した
   既知の正しい `.intg` とバイト単位の一致を要求する。入力の qpkx も
   一緒に置いてあるので、3 分ぶん 2 ケースだけで解析全体を通せる。
3. PSSR は `rounding` ケースに apkx を添え、intg + apkx から出した位置
   `fixes.csv` とのバイト一致、メモリ直列（`run`）とファイル再処理（`pssr`）
   の一致、`run` が書く intg とゴールデンの一致を確認する。`fixes.csv` は
   現行実装の出力を固定したもので、仕様を意図して変えるときだけ
   `go test ./internal/pipeline -update-pssr-golden` で更新し、差分を記録する。段ごとの規則は `internal/pssr` の各パッケージの単体テストが合成データで守る。
4. 設定から解析パラメータと幾何を導く層（`config.Baseline`、
   `pipeline.InterrogatorParams`、`pipeline.PSSRParams`、`NewInterrogatorStage`）は
   単体テストで検証する。
5. 投入の刻みに依存しないことを、ゴールデン入力の 1 分ブロックを 10 秒・
   1 秒・100 ms に切り直して流し、出力が一致することで確認する（interrogator
   段の intg と 2 段直列の位置）。実時間化の前提。

ゴールデンは解析本体だけを通す。解析パラメータ（走査周期・PRI 列・
質問種別・距離・方位）は `testdata/golden/golden.yaml` にリテラルで固定し、
`archive.FileSource` のブロックと一緒に `pipeline.RunInterrogator` へ直接渡す。設定ファイルの書式や緯度経度からの幾何計算は
通さないので、それらの仕様を変えてもゴールデンは変えずに済む。

ゴールデンの 2 ケースはそれぞれ別の性質を守るために選んである。

| ケース | 期間 | 守っているもの |
| --- | --- | --- |
| `sorting` | 00:47–00:50 | 入力に時刻の逆行を含む。読み込み時の整列 |
| `rounding` | 00:00–00:03 | 内挿の丸めが 0.5 に当たる質問を含む。偶数丸め |

ゴールデンは既知の正しい出力で、これが唯一の正解になる。
更新してよいのは解析本体の仕様を意図して変えるときだけで、その際は
`cmd/intgdiff` で旧ゴールデンとの差分を確かめて記録する。実装の都合で出た
差分をゴールデンの更新で吸収してはならない。退行を検出できなくなる。

ゴールデンの解析パラメータ（走査周期・質問パターン・座標）は実データの
qpkx から推定した暫定値で、実際の局の値ではない。座標や走査周期が実物と
違っても検証は成立する。距離と方位はタイムスタンプの一定オフセットと
方位の定数回転にしか効かず、ドウェル検出そのものには関与しないため。

不一致が出たときは `cmd/intgdiff` でレコード単位の内訳を見る。どのファイルの
どのレコードが、方位角にして何 LSB ずれたかが出る。

## 常駐運転について

長時間動かしても保持量が増え続けないようにしてある。

- `archive.IntgDir` は書き込み済みファイルを SSR ごとの「到達済みの
  最新の分」1 個だけで判定する。書き込み先の分は進む一方なので、
  ファイル名を溜める必要が無い。
- `pipeline.Timing` は所要時間の標本を持たず、件数・合計・最小・最大だけを
  更新する。
- `interrogator.Analyzer` が持ち越すのは先送り生データ 1 セグメントぶんと
  最終ドウェル 1 本だけで、いずれも呼び出しごとに入れ替わる。
- `plot.Synchronizer` は保持幅の上限（`MaxRetentionNs`、2 分）を超えた古い
  データを捨てる。質問予定か応答の片方が止まっても、他方が溜まり続けない。

実時間で流したときの遅れは、段の仕組みから次のように決まる。

- 質問予定は、ドウェル A と次のドウェル B の間を内挿して出るので、A の
  質問の予定表は B を検出してから確定する。B の検出には B の後に間隙
  （走査周期 × `DwellGapPeriods`、0.8 s）が見えるまで待つので、遅れは
  走査周期 + 0.8 s + ブロック 1 つぶん（4.04 s の SSR で 5〜6 s）。
- 位置は、質問予定が出てから `TauMax`（2.7 ms）ぶん応答が揃った範囲を対応
  づけ、幽霊抑圧が同じ走査の相手（走査周期 × `SameScanFraction`）を待つ
  ので、そこからさらに 3 s ほど遅れる。

いずれも `MaxRetentionNs` より十分短く、`Synchronizer` が応答を待たせる間に
質問予定が追いつく。これは設計上の性質で、ブロックを細かくしても縮まない。

処理は 1 スレッドで実時間より 3 桁ほど速く、時間の大半は pssr 段の応答列の
集約にかかる。集約について検討し採らなかった最適化は [PSSR.md](PSSR.md) の
12 節を参照。

## データの欠落・異常時の挙動

実データで確かめた挙動（`internal/pipeline/gap_test.go` がゴールデンで固定している）。

| 状況 | 挙動 |
| --- | --- |
| qpkx の分ファイルが無い | その分は空のブロックになる。欠落前の最後のドウェル以降と欠落区間の質問予定は出ず（次のドウェルが無く内挿できない）、欠落後は最初のドウェルから再開する。前後の出力は欠落が無いときと**レコード単位で同じ**。欠落をまたぐドウェル対は `RotationMismatch` に 1 件数える |
| ドウェルを 1 本取り逃がす（連鎖長不足・振幅不足など） | 隣のドウェル対が走査 2 回分を内挿で埋める（`max_bridge_rotations`、既定 2）。レコード数は変わらず、その回転の位置は数 m ずれる |
| 欠落が `max_bridge_rotations` を超える | その区間だけ質問予定が抜け、前後は計算される |
| apkx の分ファイルが無い | その区間の位置が無いだけ。質問予定は影響を受けない |
| 質問予定の無い区間の応答 | `Synchronizer` が最大 `max_retention_ns`（2 分）保持したのち捨てる。`Pair` では直前の質問から `TauMax` 超で `Unpaired`。偽の位置は出ない |
| ファイルが壊れている（サイズが記録長の倍数でない） | **エラーで停止**する。バッチ処理では問題が見えるほうが安全。常駐運転を足すときに、ERROR を出して空として扱い処理を続ける仕組みを設ける |
| 走査周期の設定が実機と違う | 20% 以内なら影響なし（方位の内挿は走査周期に依存せず、回転数の判定の許容が 20%） |
| 質問間隔の設定が実機と違う | 内挿の 1 質問あたりの誤差項が吸収するので、1 走査でのずれが質問パターン周期の半分（例: 2.9 ms、約 700 ppm）までは正しい結果になる。それを超えるとドウェル間の段数を数え間違え、**検査に引っかからずに**ずれた質問予定を出す。ドウェル内の残差の傾き（設定と実機の差そのもの。正しい設定で 3〜31 ppm、0.5% の誤りで 5,000 ppm）で検出できるが、設定の誤りは運用で防ぐものとして検査は入れていない |

## 時刻の逆行について

実データの qpkx には**時刻が昇順になっていないファイルが相当数ある**。
逆行は 2 種類ある。

- ファイル先頭の数レコードが前の分に属する（56〜59 秒の逆行）
- ファイル途中で 1〜4 秒巻き戻る

解析はデータが時刻昇順であることを前提にしている。セグメント分割は
隣接レコードの時間差で切るし、連鎖検出の探索窓は二分探索で決めるので、
逆行があるとどちらも意味を失う。つまり**このデータでは前提が破れている**。

読み込み時（`archive.QpkxDir.ReadMinute`）に必ずタイムスタンプで整列して
これを正す。逆行はファイルの中で閉じているので、整列はファイル単位で足りる。同一時刻のレコードは実データに無いので、整列は安定でなくてよい。
整列の有無で解析結果そのものがどれだけ変わるかは
[NUMERICS.md](NUMERICS.md) の「入力の整列が結果に与える影響」を参照。
差は丸め誤差の水準ではない。

## 演算規約

Go の標準的な演算からずらしている箇所が 2 つある。

- ブラケット内挿の質問時刻は `math.Round` ではなく `math.RoundToEven`。
  0.5 ちょうどに当たる質問が実データに一定数あり、`math.Round` に変えると
  その方位角が 1 LSB ずれる。
- 方位角の `[0, 2pi)` への畳み込みは `math.Mod` ではなく `interrogator.wrapAngle`。
  `math.Mod` は負の値を負のまま返し、それを uint32 へ変換すると
  Go の仕様上「実装依存」の結果になる。

いずれも実測に基づく判断で、差が出なかった候補（床除算・pairwise 総和）は
採っていない。実測値と根拠は [NUMERICS.md](NUMERICS.md) を参照。
